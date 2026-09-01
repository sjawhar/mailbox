package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func fixtureMetadataMessage(message string, metadataHeaders []string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(message), &decoded); err != nil {
		panic(fmt.Sprintf("decode fake Gmail message: %v", err))
	}
	payload, ok := decoded["payload"].(map[string]any)
	if !ok {
		panic("fake Gmail message has no payload")
	}
	headers, ok := payload["headers"].([]any)
	if !ok {
		panic("fake Gmail message payload has no headers")
	}
	if len(metadataHeaders) > 0 {
		wanted := make(map[string]struct{}, len(metadataHeaders))
		for _, name := range metadataHeaders {
			wanted[strings.ToLower(name)] = struct{}{}
		}
		filtered := make([]any, 0, len(headers))
		for _, header := range headers {
			value, ok := header.(map[string]any)
			if !ok {
				panic("fake Gmail message has an invalid header")
			}
			name, ok := value["name"].(string)
			if !ok {
				panic("fake Gmail message header has no name")
			}
			if _, ok := wanted[strings.ToLower(name)]; ok {
				filtered = append(filtered, header)
			}
		}
		headers = filtered
	}
	decoded["payload"] = map[string]any{"headers": headers}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		panic(fmt.Sprintf("encode fake Gmail metadata message: %v", err))
	}
	return string(encoded)
}

func fixtureMetadataThread(thread string, metadataHeaders []string) string {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(thread), &decoded); err != nil {
		panic(fmt.Sprintf("decode fake Gmail thread: %v", err))
	}
	messages, ok := decoded["messages"]
	if !ok {
		panic("fake Gmail thread has no messages")
	}
	var metadata []json.RawMessage
	if err := json.Unmarshal(messages, &metadata); err != nil {
		panic(fmt.Sprintf("decode fake Gmail thread messages: %v", err))
	}
	for index, message := range metadata {
		metadata[index] = json.RawMessage([]byte(fixtureMetadataMessage(string(message), metadataHeaders)))
	}
	encodedMessages, err := json.Marshal(metadata)
	if err != nil {
		panic(fmt.Sprintf("encode fake Gmail thread messages: %v", err))
	}
	decoded["messages"] = encodedMessages
	encoded, err := json.Marshal(decoded)
	if err != nil {
		panic(fmt.Sprintf("encode fake Gmail metadata thread: %v", err))
	}
	return string(encoded)
}

func (g *fakeGmail) serveAttachment(w http.ResponseWriter, request *http.Request, messageID, attachmentID string) {
	if messageID != "m-att" && !strings.HasPrefix(messageID, "m-d-e2e-") {
		http.NotFound(w, request)
		return
	}
	contents, ok := g.attachmentContents(attachmentID)
	if !ok {
		http.NotFound(w, request)
		return
	}
	fmt.Fprintf(w, `{"data":%q}`, base64.RawURLEncoding.EncodeToString(contents))
}

func fixtureAttachmentBytes(id string) []byte {
	return []byte("fixture-bytes-" + id)
}

func (g *fakeGmail) attachmentContents(id string) ([]byte, bool) {
	g.mu.Lock()
	contents, ok := g.attachments[id]
	g.mu.Unlock()
	if ok {
		return append([]byte(nil), contents...), true
	}
	switch id {
	case "a-evil", "a-ok":
		return fixtureAttachmentBytes(id), true
	default:
		return nil, false
	}
}

func fakeAttachmentMessage() string {
	attachmentPart := func(partID, filename, attachmentID string) map[string]any {
		contents := fixtureAttachmentBytes(attachmentID)
		return map[string]any{
			"partId":   partID,
			"mimeType": "application/pdf",
			"filename": filename,
			"headers": fixtureHeaderList(map[string]string{
				"Content-Disposition": `attachment; filename="` + filename + `"`,
				"Content-Type":        "application/pdf",
			}),
			"body": map[string]any{
				"attachmentId": attachmentID,
				"size":         len(contents),
			},
		}
	}
	message := map[string]any{
		"id":           "m-att",
		"threadId":     "t-att",
		"internalDate": "1788000000000",
		"labelIds":     []string{"INBOX"},
		"payload": map[string]any{
			"partId":   "",
			"mimeType": "multipart/mixed",
			"headers": fixtureHeaderList(map[string]string{
				"From":       "A <a@example.test>",
				"Message-ID": "<m-att@example.test>",
				"Subject":    "Attachments",
				"To":         "B <b@example.test>",
			}),
			"parts": []any{
				attachmentPart("0", "../../evil\u202e.pdf", "a-evil"),
				attachmentPart("1", "report.pdf", "a-ok"),
			},
		},
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func (g *fakeGmail) serveThreadList(w http.ResponseWriter, request *http.Request) {
	g.recordReadAuth(request)
	g.mu.Lock()
	delay := g.listDelay
	pages := append([][]string(nil), g.listPages...)
	g.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if pages == nil {
		fmt.Fprint(w, `{"threads":[{"id":"t1","snippet":"hello"},{"id":"t2","snippet":"second"}]}`)
		return
	}

	page := 0
	if token := request.URL.Query().Get("pageToken"); token != "" {
		const prefix = "page-"
		if !strings.HasPrefix(token, prefix) {
			http.Error(w, "unknown page token", http.StatusBadRequest)
			return
		}
		parsed, err := strconv.Atoi(strings.TrimPrefix(token, prefix))
		if err != nil || parsed < 1 {
			http.Error(w, "unknown page token", http.StatusBadRequest)
			return
		}
		page = parsed
	}
	if page >= len(pages) {
		http.Error(w, "unknown page token", http.StatusBadRequest)
		return
	}

	type listedThread struct {
		ID      string `json:"id"`
		Snippet string `json:"snippet"`
	}
	response := struct {
		Threads       []listedThread `json:"threads"`
		NextPageToken string         `json:"nextPageToken,omitempty"`
	}{Threads: make([]listedThread, len(pages[page]))}
	for index, id := range pages[page] {
		response.Threads[index] = listedThread{ID: id, Snippet: "fixture " + id}
	}
	if page+1 < len(pages) {
		response.NextPageToken = fmt.Sprintf("page-%d", page+1)
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		panic(err)
	}
}

func (g *fakeGmail) threadResponse(id string, query url.Values) (string, bool) {
	thread, found := g.threads[id]
	if !found {
		return "", false
	}
	if query.Get("format") == "metadata" {
		thread = fixtureMetadataThread(thread, query["metadataHeaders"])
	}
	return thread, true
}

func (g *fakeGmail) serveThread(w http.ResponseWriter, request *http.Request) {
	const prefix = "/gmail/v1/users/me/threads/"
	threadPath := strings.TrimPrefix(request.URL.Path, prefix)
	switch {
	case strings.HasSuffix(threadPath, "/modify"):
		g.recordModified(request, strings.TrimSuffix(threadPath, "/modify"))
		fmt.Fprint(w, `{}`)
	case strings.HasSuffix(threadPath, "/trash"):
		g.recordTrashed(request, strings.TrimSuffix(threadPath, "/trash"))
		fmt.Fprint(w, `{}`)
	default:
		g.recordReadAuth(request)
		thread, found := g.threadResponse(threadPath, request.URL.Query())
		if !found {
			http.NotFound(w, request)
			return
		}
		fmt.Fprint(w, thread)
	}
}

func fakeThread(id, message string) string {
	return fmt.Sprintf(`{"id":%q,"messages":[%s]}`, id, message)
}

func fakeMessage(threadID, subject, from, to, carbonCopy, replyTo string) string {
	headers := map[string]string{
		"From": from,
		"To":   to,
	}
	if carbonCopy != "" {
		headers["Cc"] = carbonCopy
	}
	if replyTo != "" {
		headers["Reply-To"] = replyTo
	}
	return fakeMessageWithHeaders(threadID, subject, headers)
}

func fakeMessageWithHeaders(threadID, subject string, input map[string]string) string {
	values := make(map[string]string, len(input)+2)
	for name, value := range input {
		values[name] = value
	}
	values["Subject"] = subject
	values["Message-ID"] = "<m-" + threadID + "@example.test>"
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	headers := make([]struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}, len(names))
	for index, name := range names {
		headers[index] = struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: name, Value: values[name]}
	}
	headerJSON, err := json.Marshal(headers)
	if err != nil {
		panic(err)
	}
	body := base64.RawURLEncoding.EncodeToString([]byte("<p>hi</p>"))
	return fmt.Sprintf(`{"id":%q,"threadId":%q,"internalDate":"1788000000000","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"text/html","headers":%s,"body":{"data":%q}}}`, "m-"+threadID, threadID, headerJSON, body)
}

func (g *fakeGmail) setListPages(pages [][]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listPages = make([][]string, len(pages))
	for index, page := range pages {
		g.listPages[index] = append([]string(nil), page...)
	}
}

func (g *fakeGmail) setListDelay(delay time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listDelay = delay
}
