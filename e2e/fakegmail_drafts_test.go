package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type fixtureDraft struct {
	MessageID string
	ThreadID  string
	Subject   string
	Raw       []byte
}

type fixtureAttachment struct {
	Filename string
	MimeType string
	Contents []byte
}

func (g *fakeGmail) serveDrafts(w http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		g.recordReadAuth(request)
		g.serveDraftList(w, request)
	case http.MethodPost:
		g.recordWriteAuth(request)
		g.serveDraftCreate(w, request)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *fakeGmail) serveDraft(w http.ResponseWriter, request *http.Request) {
	const prefix = "/gmail/v1/users/me/drafts/"
	path := strings.TrimPrefix(request.URL.Path, prefix)
	if draftID, update := strings.CutSuffix(path, "/update"); update {
		if request.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g.rotateDraft(w, request, draftID)
		return
	}
	switch request.Method {
	case http.MethodGet:
		g.recordReadAuth(request)
		g.serveDraftGet(w, request, path)
	case http.MethodDelete:
		g.recordWriteAuth(request)
		g.serveDraftDelete(w, request, path)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *fakeGmail) serveDraftsSend(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, `{"error":{"code":500,"message":"drafts.send is prohibited by the fixture"}}`)
}

func (g *fakeGmail) serveDraftList(w http.ResponseWriter, request *http.Request) {
	max := len(g.draftOrder)
	if raw := request.URL.Query().Get("maxResults"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			http.Error(w, "invalid maxResults", http.StatusBadRequest)
			return
		}
		max = min(max, parsed)
	}

	g.mu.Lock()
	drafts := make([]map[string]any, 0, max)
	for index := len(g.draftOrder) - 1; index >= 0 && len(drafts) < max; index-- {
		id := g.draftOrder[index]
		draft, ok := g.drafts[id]
		if !ok {
			continue
		}
		drafts = append(drafts, map[string]any{
			"id": id,
			"message": map[string]any{
				"id":       draft.MessageID,
				"threadId": draft.ThreadID,
			},
		})
	}
	g.mu.Unlock()
	if err := json.NewEncoder(w).Encode(map[string]any{"drafts": drafts}); err != nil {
		panic(err)
	}
}

func (g *fakeGmail) serveDraftCreate(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Message struct {
			Raw      string `json:"raw"`
			ThreadID string `json:"threadId"`
		} `json:"message"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(w, "invalid draft request", http.StatusBadRequest)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(body.Message.Raw)
	if err != nil {
		http.Error(w, "invalid draft raw message", http.StatusBadRequest)
		return
	}
	headers, _, _, err := fixtureDraftParts(raw)
	if err != nil {
		http.Error(w, "invalid draft MIME", http.StatusBadRequest)
		return
	}

	g.mu.Lock()
	number := len(g.draftOrder) + 1
	id := fmt.Sprintf("d-e2e-%d", number)
	draft := &fixtureDraft{
		MessageID: fmt.Sprintf("m-d-e2e-%d", number),
		ThreadID:  body.Message.ThreadID,
		Subject:   headers["Subject"],
		Raw:       append([]byte(nil), raw...),
	}
	g.drafts[id] = draft
	g.draftOrder = append(g.draftOrder, id)
	response, err := g.draftResponseLocked(id, draft, true)
	g.mu.Unlock()
	if err != nil {
		http.Error(w, "invalid draft MIME", http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		panic(err)
	}
}

func (g *fakeGmail) draftResponse(id string, query url.Values) (map[string]any, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	draft, ok := g.drafts[id]
	if !ok {
		return nil, false, nil
	}
	response, err := g.draftResponseLocked(id, draft, query.Get("format") != "metadata")
	return response, true, err
}

func (g *fakeGmail) serveDraftGet(w http.ResponseWriter, request *http.Request, id string) {
	response, found, err := g.draftResponse(id, request.URL.Query())
	if !found {
		http.NotFound(w, request)
		return
	}
	if err != nil {
		http.Error(w, "invalid draft MIME", http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		panic(err)
	}
}

func (g *fakeGmail) serveDraftDelete(w http.ResponseWriter, request *http.Request, id string) {
	g.mu.Lock()
	status := g.draftDeleteStatus
	_, ok := g.drafts[id]
	if status == 0 && ok {
		delete(g.drafts, id)
	}
	g.mu.Unlock()
	if !ok {
		http.NotFound(w, request)
		return
	}
	if status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"armed draft delete status"}}`, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *fakeGmail) rotateDraft(w http.ResponseWriter, request *http.Request, id string) {
	g.mu.Lock()
	draft, ok := g.drafts[id]
	if ok {
		draft.MessageID += "r"
	}
	g.mu.Unlock()
	if !ok {
		http.NotFound(w, request)
		return
	}
	fmt.Fprint(w, `{}`)
}

func (g *fakeGmail) draftResponseLocked(id string, draft *fixtureDraft, full bool) (map[string]any, error) {
	headers, text, attachments, err := fixtureDraftParts(draft.Raw)
	if err != nil {
		return nil, err
	}
	headers["Subject"] = draft.Subject
	headers["Message-ID"] = "<" + draft.MessageID + "@example.test>"
	parts := make([]any, 0, len(attachments)+1)
	textBody := map[string]any{"size": len([]byte(text))}
	if full {
		textBody["data"] = base64.RawURLEncoding.EncodeToString([]byte(text))
	}
	parts = append(parts, map[string]any{
		"partId":   "0",
		"mimeType": "text/plain",
		"filename": "",
		"body":     textBody,
	})
	for index, attachment := range attachments {
		attachmentID := fmt.Sprintf("%s-a-%d", id, index)
		g.attachments[attachmentID] = append([]byte(nil), attachment.Contents...)
		parts = append(parts, map[string]any{
			"partId":   strconv.Itoa(index + 1),
			"mimeType": attachment.MimeType,
			"filename": attachment.Filename,
			"headers": fixtureHeaderList(map[string]string{
				"Content-Type": attachment.MimeType,
			}),
			"body": map[string]any{
				"attachmentId": attachmentID,
				"size":         len(attachment.Contents),
			},
		})
	}
	return map[string]any{
		"id": id,
		"message": map[string]any{
			"id":           draft.MessageID,
			"threadId":     draft.ThreadID,
			"internalDate": fmt.Sprintf("%d", 1788000000000+int64(len(g.draftOrder))),
			"payload": map[string]any{
				"partId":   "",
				"mimeType": "multipart/mixed",
				"headers":  fixtureHeaderList(headers),
				"parts":    parts,
			},
		},
	}, nil
}

func fixtureDraftParts(raw []byte) (map[string]string, string, []fixtureAttachment, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, "", nil, err
	}
	headers := make(map[string]string)
	for _, name := range []string{"To", "Subject", "In-Reply-To", "References"} {
		if value := message.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	var text string
	var attachments []fixtureAttachment
	if err := walkFixtureMIME(message.Header, message.Body, &text, &attachments); err != nil {
		return nil, "", nil, err
	}
	return headers, text, attachments, nil
}

func walkFixtureMIME(header interface{ Get(string) string }, body io.Reader, text *string, attachments *[]fixtureAttachment) error {
	mediaType := "text/plain"
	params := map[string]string{}
	if contentType := header.Get("Content-Type"); contentType != "" {
		parsed, parsedParams, err := mime.ParseMediaType(contentType)
		if err != nil {
			return err
		}
		mediaType = parsed
		params = parsedParams
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return errors.New("multipart draft without a boundary")
		}
		reader := multipart.NewReader(body, boundary)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walkFixtureMIME(part.Header, part, text, attachments); err != nil {
				return err
			}
		}
	}

	contents, err := fixtureMIMEPartBytes(body, header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return err
	}
	disposition, dispositionParams, err := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err != nil && header.Get("Content-Disposition") != "" {
		return err
	}
	if filename := dispositionParams["filename"]; filename != "" || strings.EqualFold(disposition, "attachment") {
		*attachments = append(*attachments, fixtureAttachment{
			Filename: filename,
			MimeType: mediaType,
			Contents: contents,
		})
		return nil
	}
	if *text == "" && strings.EqualFold(mediaType, "text/plain") {
		*text = string(contents)
	}
	return nil
}

func fixtureMIMEPartBytes(body io.Reader, transferEncoding string) ([]byte, error) {
	if strings.EqualFold(strings.TrimSpace(transferEncoding), "base64") {
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, body))
	}
	return io.ReadAll(body)
}

func fixtureHeaderList(values map[string]string) []map[string]string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	headers := make([]map[string]string, 0, len(names))
	for _, name := range names {
		headers = append(headers, map[string]string{"name": name, "value": values[name]})
	}
	return headers
}
