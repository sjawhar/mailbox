package cli

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/refs"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

type gmailTestServer struct {
	t                    *testing.T
	server               *httptest.Server
	listQuery            url.Values
	listCalls            int
	batchRequests        []string
	directRequests       []string
	labels               []map[string]any
	profile              map[string]any
	thread               map[string]any
	listPages            [][]string
	listPageStatus       map[int]int
	batchItemResponses   map[string][]scriptedResponse
	batchRequestStatus   []int
	batchCalls           int
	batchWriteCalls      int
	batchWriteIDs        [][]string
	listIDs              []string
	attachmentBytes      map[string][]byte
	metadata             map[string]map[string]any
	messages             map[string]map[string]any
	rawMessages          map[string][]byte
	sentBodies           []map[string]any
	sendBearers          []string
	draftCreates         []draftCreate
	drafts               map[string]map[string]any
	draftListIDs         []string
	draftListMax         string
	draftReadBearers     []string
	draftDeleteBearers   []string
	sendStatus           int
	sendPersistentStatus bool
	profileCalls         int
	attachmentRequestIDs []string
	draftDeleteStatus    int
	sendGarbage          bool
	readToken            string
	writeToken           string
	sendToken            string
	rawMessageID         string
	forbidden            bool
	readForbidden        bool
	readFailures         int
}
type draftCreate struct {
	Raw      string
	ThreadID string
	Bearer   string
}

type scriptedResponse struct {
	status     int
	retryAfter string
	reason     string
}

func newGmailTestServer(t *testing.T) *gmailTestServer {
	t.Helper()
	g := &gmailTestServer{
		t:               t,
		listIDs:         []string{"t1", "t2"},
		labels:          []map[string]any{{"id": "INBOX", "name": "INBOX"}, {"id": "Label_7", "name": "Newsletters"}},
		profile:         map[string]any{"emailAddress": "user@example.com"},
		thread:          testThread("t1", true, false),
		attachmentBytes: map[string][]byte{},
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.server.Close)
	return g
}

func (g *gmailTestServer) tokenURL(t *testing.T, accessToken string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, accessToken)
	}))
	t.Cleanup(server.Close)
	return server.URL
}
func (g *gmailTestServer) handle(w http.ResponseWriter, r *http.Request) {
	g.t.Helper()
	// Write commands use the configured write credential for every request.
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		if (g.readToken == "" || got != "Bearer "+g.readToken) &&
			(g.writeToken == "" || got != "Bearer "+g.writeToken) &&
			(g.sendToken == "" || got != "Bearer "+g.sendToken) {
			g.t.Fatalf("Authorization = %q, want a configured fixture token", got)
		}
	}
	if r.URL.Path == "/batch/gmail/v1" {
		g.handleBatch(w, r)
		return
	}
	switch {
	case r.URL.Path == "/gmail/v1/users/me/threads" && r.Method == http.MethodGet:
		g.listCalls++
		g.listQuery = r.URL.Query()
		page := 0
		ids := g.listIDs
		if g.listPages != nil {
			if token := r.URL.Query().Get("pageToken"); token != "" {
				var err error
				page, err = strconv.Atoi(strings.TrimPrefix(token, "page-"))
				if err != nil || token != fmt.Sprintf("page-%d", page) {
					g.t.Fatalf("pageToken = %q, want page-N", token)
				}
			}
			if page >= len(g.listPages) {
				g.t.Fatalf("requested page %d, only %d fixture pages", page, len(g.listPages))
			}
			ids = g.listPages[page]
		}
		if status := g.listPageStatus[page]; status != 0 {
			w.Header().Set("Retry-After", "0")
			writeResponse(g.t, w, status, googleError(status, "rateLimitExceeded"))
			return
		}
		threads := make([]map[string]any, len(ids))
		for i, id := range ids {
			threads[i] = map[string]any{"id": id, "snippet": "snippet " + id}
		}
		response := map[string]any{"threads": threads}
		if g.listPages != nil && page+1 < len(g.listPages) {
			response["nextPageToken"] = fmt.Sprintf("page-%d", page+1)
		}
		writeResponse(g.t, w, http.StatusOK, response)
	case g.rawMessageID != "" && r.URL.Path == "/gmail/v1/users/me/messages/"+g.rawMessageID && r.Method == http.MethodGet:
		if r.URL.Query().Get("format") != "minimal" {
			g.t.Fatalf("raw message format = %q, want minimal", r.URL.Query().Get("format"))
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{"id": g.rawMessageID, "threadId": "t1"})
	case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/messages/") && r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/attachments/"):
		id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")
		message, ok := g.messages[id]
		if !ok {
			writeResponse(g.t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound"))
			return
		}
		switch r.URL.Query().Get("format") {
		case "raw":
			writeResponse(g.t, w, http.StatusOK, map[string]any{
				"id":       id,
				"threadId": message["threadId"],
				"raw":      base64.RawURLEncoding.EncodeToString(g.rawMessages[id]),
			})
		case "minimal":
			writeResponse(g.t, w, http.StatusOK, map[string]any{"id": id, "threadId": message["threadId"]})
		default:
			writeResponse(g.t, w, http.StatusOK, message)
		}
	case r.URL.Path == "/gmail/v1/users/me/drafts" && r.Method == http.MethodGet:
		g.draftListMax = r.URL.Query().Get("maxResults")
		drafts := make([]map[string]any, len(g.draftListIDs))
		g.draftReadBearers = append(g.draftReadBearers, r.Header.Get("Authorization"))
		for i, id := range g.draftListIDs {
			drafts[i] = map[string]any{"id": id}
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{"drafts": drafts})
	case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/drafts/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/drafts/")
		g.draftReadBearers = append(g.draftReadBearers, r.Header.Get("Authorization"))
		if format := r.URL.Query().Get("format"); format != "metadata" && format != "full" {
			g.t.Fatalf("draft format = %q, want metadata or full", format)
		}
		draft, ok := g.drafts[id]
		if !ok {
			writeResponse(g.t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound"))
			return
		}
		writeResponse(g.t, w, http.StatusOK, draft)
	case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/drafts/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/drafts/")
		g.draftDeleteBearers = append(g.draftDeleteBearers, r.Header.Get("Authorization"))
		if g.draftDeleteStatus != 0 && g.draftDeleteStatus != http.StatusOK {
			writeResponse(g.t, w, g.draftDeleteStatus, googleError(g.draftDeleteStatus, "deleteFailed"))
			return
		}
		delete(g.drafts, id)
		writeResponse(g.t, w, http.StatusOK, map[string]any{})
	case r.URL.Path == "/gmail/v1/users/me/drafts" && r.Method == http.MethodPost:
		var body struct {
			Message struct {
				Raw      string `json:"raw"`
				ThreadID string `json:"threadId"`
			} `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			g.t.Fatalf("decode draft body: %v", err)
		}
		g.draftCreates = append(g.draftCreates, draftCreate{
			Raw:      body.Message.Raw,
			ThreadID: body.Message.ThreadID,
			Bearer:   r.Header.Get("Authorization"),
		})
		writeResponse(g.t, w, http.StatusOK, map[string]any{
			"id":      "d-1",
			"message": map[string]any{"id": "m-d-1", "threadId": body.Message.ThreadID},
		})
	case r.URL.Path == "/gmail/v1/users/me/messages/send" && r.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			g.t.Fatalf("decode sent message body: %v", err)
		}
		g.sentBodies = append(g.sentBodies, body)
		g.sendBearers = append(g.sendBearers, r.Header.Get("Authorization"))
		if g.sendGarbage {
			g.sendGarbage = false
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "not-json")
			return
		}
		if g.sendStatus != 0 && g.sendStatus != http.StatusOK {
			status := g.sendStatus
			if !g.sendPersistentStatus {
				g.sendStatus = 0
			}
			writeResponse(g.t, w, status, googleError(status, "sendFailed"))
			return
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{"id": "sent-1", "threadId": "t1"})
	case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/threads/") && r.Method == http.MethodGet:
		if g.readForbidden {
			writeResponse(g.t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions"))
			return
		}
		if g.readFailures > 0 {
			g.readFailures--
			writeResponse(g.t, w, http.StatusUnauthorized, googleError(http.StatusUnauthorized, "authError"))
			return
		}
		if g.rawMessageID != "" && strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/threads/"+g.rawMessageID) {
			writeResponse(g.t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound"))
			return
		}
		if r.URL.Query().Get("format") == "minimal" {
			writeResponse(g.t, w, http.StatusOK, map[string]any{"id": "t1"})
			return
		}
		writeResponse(g.t, w, http.StatusOK, g.thread)
	case r.URL.Path == "/gmail/v1/users/me/labels":
		if g.readForbidden {
			writeResponse(g.t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions"))
			return
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{"labels": g.labels})
	case strings.Contains(r.URL.Path, "/attachments/"):
		id := filepath.Base(r.URL.Path)
		g.attachmentRequestIDs = append(g.attachmentRequestIDs, id)
		g.draftReadBearers = append(g.draftReadBearers, r.Header.Get("Authorization"))
		contents, ok := g.attachmentBytes[id]
		if !ok {
			writeResponse(g.t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound"))
			return
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{"data": base64.RawURLEncoding.EncodeToString(contents)})
	case r.URL.Path == "/gmail/v1/users/me/profile":
		g.profileCalls++
		writeResponse(g.t, w, http.StatusOK, g.profile)
	case strings.HasSuffix(r.URL.Path, "/modify") || strings.HasSuffix(r.URL.Path, "/trash"):
		body, err := io.ReadAll(r.Body)
		if err != nil {
			g.t.Fatalf("read direct request: %v", err)
		}
		g.directRequests = append(g.directRequests, r.Method+" "+r.URL.String()+"\n"+string(body))
		if g.rawMessageID != "" && strings.Contains(r.URL.Path, "/threads/"+g.rawMessageID+"/") {
			writeResponse(g.t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound"))
			return
		}
		if g.forbidden {
			writeResponse(g.t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions"))
			return
		}
		writeResponse(g.t, w, http.StatusOK, map[string]any{})
	default:
		g.t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
	}
}

func (g *gmailTestServer) handleBatch(w http.ResponseWriter, r *http.Request) {
	g.batchCalls++
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		g.t.Fatalf("batch content type = %q, parse error = %v", r.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	var requests []*http.Request
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			g.t.Fatalf("read batch part: %v", err)
		}
		req, err := http.ReadRequest(bufio.NewReader(part))
		if err != nil {
			g.t.Fatalf("read embedded request: %v", err)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			g.t.Fatalf("read batch body: %v", err)
		}
		g.batchRequests = append(g.batchRequests, req.Method+" "+req.URL.String()+"\n"+string(body))
		requests = append(requests, req)
	}

	for _, request := range requests {
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/gmail/v1/users/me/drafts/") && request.URL.Query().Get("format") == "metadata" {
			g.draftReadBearers = append(g.draftReadBearers, r.Header.Get("Authorization"))
			break
		}
	}

	writeIDs := make([]string, 0, len(requests))
	for _, request := range requests {
		if request.Method == http.MethodPost {
			writeIDs = append(writeIDs, batchThreadID(g.t, request.URL.Path))
		}
	}
	if len(writeIDs) > 0 {
		g.batchWriteCalls++
		g.batchWriteIDs = append(g.batchWriteIDs, append([]string(nil), writeIDs...))
		if len(g.batchRequestStatus) > 0 {
			status := g.batchRequestStatus[0]
			g.batchRequestStatus = g.batchRequestStatus[1:]
			if status != 0 {
				writeResponse(g.t, w, status, googleError(status, "authError"))
				return
			}
		}
	}

	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	for i, request := range requests {
		status, body := http.StatusOK, `{}`
		var responseHeaders string
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/gmail/v1/users/me/drafts/") && request.URL.Query().Get("format") == "metadata" {
			id := filepath.Base(request.URL.Path)
			draft, ok := g.drafts[id]
			if !ok {
				status = http.StatusNotFound
				body = string(mustJSON(g.t, googleError(http.StatusNotFound, "notFound")))
			} else {
				body = string(mustJSON(g.t, draft))
			}
		} else if request.Method == http.MethodGet && strings.Contains(request.URL.RawQuery, "format=metadata") {
			id := filepath.Base(request.URL.Path)
			metadata := metadataThread(id)
			if g.metadata != nil && g.metadata[id] != nil {
				metadata = g.metadata[id]
			}
			body = string(mustJSON(g.t, metadata))
		} else if request.Method == http.MethodPost {
			id := batchThreadID(g.t, request.URL.Path)
			if script := g.batchItemResponses[id]; len(script) > 0 {
				response := script[0]
				g.batchItemResponses[id] = script[1:]
				status = response.status
				if status == 0 {
					status = http.StatusOK
				}
				reason := response.reason
				if reason == "" {
					reason = "rateLimitExceeded"
				}
				if status != http.StatusOK {
					body = string(mustJSON(g.t, googleError(status, reason)))
				}
				if response.retryAfter != "" {
					responseHeaders = "Retry-After: " + response.retryAfter + "\r\n"
				}
			} else if g.forbidden {
				status = http.StatusForbidden
				body = string(mustJSON(g.t, googleError(http.StatusForbidden, "insufficientPermissions")))
			}
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/http")
		header.Set("Content-ID", fmt.Sprintf("<response-item%d>", i))
		part, err := writer.CreatePart(header)
		if err != nil {
			g.t.Fatalf("create response part: %v", err)
		}
		if _, err := fmt.Fprintf(part, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\n%s\r\n%s", status, http.StatusText(status), responseHeaders, body); err != nil {
			g.t.Fatalf("write response part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		g.t.Fatalf("close batch response: %v", err)
	}
}

func batchThreadID(t *testing.T, path string) string {
	t.Helper()
	const prefix = "/gmail/v1/users/me/threads/"
	id, found := strings.CutPrefix(path, prefix)
	if !found {
		t.Fatalf("batch mutation path = %q, want Gmail thread path", path)
	}
	id, _, found = strings.Cut(id, "/")
	if !found || id == "" {
		t.Fatalf("batch mutation path = %q, want thread id and action", path)
	}
	return id
}

func runCLI(t *testing.T, g *gmailTestServer, args ...string) (int, string, string) {
	t.Helper()
	return runCLIWithConfig(t, g, "[accounts.work]\nread_credential_env = \"CLI_READ\"\n", args...)
}

func runCLIWithConfig(t *testing.T, g *gmailTestServer, config string, args ...string) (int, string, string) {
	t.Helper()
	setCLIConfigContents(t, config)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_TOKEN", "test-token")
	if os.Getenv("MAILBOX_CACHE_DIR") == "" {
		t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	}
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func setCLIConfig(t *testing.T) {
	t.Helper()
	setCLIConfigContents(t, "[accounts.work]\nread_credential_env = \"CLI_READ\"\n")
}

func setCLIConfigContents(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", path)
}
func runJSON(t *testing.T, g *gmailTestServer, args ...string) (int, map[string]any, string) {
	t.Helper()
	return runJSONWithConfig(t, g, "[accounts.work]\nread_credential_env = \"CLI_READ\"\n", args...)
}

func runJSONWithConfig(t *testing.T, g *gmailTestServer, config string, args ...string) (int, map[string]any, string) {
	t.Helper()
	code, stdout, stderr := runCLIWithConfig(t, g, config, args...)
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON stdout %q: %v", stdout, err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatalf("JSON stdout purity: %v", err)
	}
	return code, value, stderr
}

func configureAttachmentMessage(g *gmailTestServer) {
	g.messages = map[string]map[string]any{
		"m-att": {
			"id":       "m-att",
			"threadId": "t-att",
			"payload": map[string]any{
				"parts": []map[string]any{
					{
						"filename": "../../evil\u202e.pdf",
						"mimeType": "application/pdf",
						"body": map[string]any{
							"attachmentId": "a-evil",
							"size":         22,
						},
					},
					{
						"filename": "report.pdf",
						"mimeType": "application/pdf",
						"body": map[string]any{
							"attachmentId": "a-ok",
							"size":         20,
						},
					},
				},
			},
		},
		"m-plain": {
			"id":       "m-plain",
			"threadId": "t-att",
			"payload":  map[string]any{},
		},
	}
	g.attachmentBytes = map[string][]byte{
		"a-evil": attachmentFixtureBytes("a-evil"),
		"a-ok":   attachmentFixtureBytes("a-ok"),
	}
}

func configureDraftListing(g *gmailTestServer) {
	g.draftListIDs = []string{"d-old", "d-new"}
	g.drafts = map[string]map[string]any{
		"d-old": {
			"id": "d-old",
			"message": map[string]any{
				"id":           "m-old",
				"threadId":     "t-old",
				"internalDate": "1000",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "To", "value": "A <a@example.test>"},
						{"name": "Subject", "value": "old"},
					},
				},
			},
		},
		"d-new": {
			"id": "d-new",
			"message": map[string]any{
				"id":           "m-new",
				"threadId":     "t-new",
				"internalDate": "2000",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "To", "value": "\x1b]0;pwn\x07\x1bP+q\x1b\\ \u202eevil\r\ninjected\tcol <e@example.test>"},
						{"name": "Subject", "value": "new"},
					},
				},
			},
		},
	}
}

func (g *gmailTestServer) draftMessage(id string) map[string]any {
	g.t.Helper()
	draft, ok := g.drafts[id]
	if !ok {
		g.t.Fatalf("fixture draft %q does not exist", id)
	}
	message, ok := draft["message"].(map[string]any)
	if !ok {
		g.t.Fatalf("fixture draft %q has no message", id)
	}
	return message
}

func (g *gmailTestServer) draftPayload(id string) map[string]any {
	g.t.Helper()
	payload, ok := g.draftMessage(id)["payload"].(map[string]any)
	if !ok {
		g.t.Fatalf("fixture draft %q has no payload", id)
	}
	return payload
}

func (g *gmailTestServer) rotateDraft(id string) {
	g.t.Helper()
	message := g.draftMessage(id)
	current, ok := message["id"].(string)
	if !ok {
		g.t.Fatalf("fixture draft %q message has no id", id)
	}
	message["id"] = current + "r"
}

func (g *gmailTestServer) armSendGarbage() {
	g.sendGarbage = true
}

func (g *gmailTestServer) sendCalls() int {
	return len(g.sentBodies)
}

func (g *gmailTestServer) draftDeletes() int {
	return len(g.draftDeleteBearers)
}

func (g *gmailTestServer) draftExists(id string) bool {
	_, ok := g.drafts[id]
	return ok
}

func (g *gmailTestServer) sendRequestBearers() []string {
	return g.sendBearers
}

func (g *gmailTestServer) draftDeleteRequestBearers() []string {
	return g.draftDeleteBearers
}

func (g *gmailTestServer) setDraftHeader(id, name, value string) {
	g.t.Helper()
	headers, ok := g.draftPayload(id)["headers"].([]map[string]any)
	if !ok {
		g.t.Fatalf("fixture draft %q has no headers", id)
	}
	for _, header := range headers {
		if strings.EqualFold(header["name"].(string), name) {
			header["value"] = value
			return
		}
	}
	g.draftPayload(id)["headers"] = append(headers, map[string]any{"name": name, "value": value})
}

func (g *gmailTestServer) setDraftSubject(id, value string) {
	g.setDraftHeader(id, "Subject", value)
}

func (g *gmailTestServer) setDraftAttachmentName(id, value string) {
	g.t.Helper()
	parts, ok := g.draftPayload(id)["parts"].([]map[string]any)
	if !ok || len(parts) < 2 {
		g.t.Fatalf("fixture draft %q has no carried attachment", id)
	}
	parts[1]["filename"] = value
}

func (g *gmailTestServer) setDraftBody(id, value string) {
	g.t.Helper()
	parts, ok := g.draftPayload(id)["parts"].([]map[string]any)
	if !ok || len(parts) == 0 {
		g.t.Fatalf("fixture draft %q has no text part", id)
	}
	body, ok := parts[0]["body"].(map[string]any)
	if !ok {
		g.t.Fatalf("fixture draft %q text part has no body", id)
	}
	body["data"] = base64.RawURLEncoding.EncodeToString([]byte(value))
}

func attachmentFixtureBytes(id string) []byte {
	return []byte("fixture-bytes-" + id)
}

func assertOneJSON(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("extra JSON document or trailing content: %v", err)
	}
	return nil
}

func assertEmptyJSONField(t *testing.T, document, field string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(document), &object); err != nil {
		t.Fatalf("decode JSON %q: %v", document, err)
	}
	assertEmptyRawJSONField(t, object, field)
}

func assertEmptyRawJSONField(t *testing.T, object map[string]json.RawMessage, field string) {
	t.Helper()
	if got := string(object[field]); got != "[]" {
		t.Fatalf("%s = %s, want []", field, got)
	}
}

func seedRefs(t *testing.T, ids ...string) {
	t.Helper()
	if err := refs.Write("work", ids); err != nil {
		t.Fatalf("seed refs: %v", err)
	}
}

func TestInboxJSON(t *testing.T) {
	g := newGmailTestServer(t)
	code, value, stderr := runJSON(t, g, "inbox", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("inbox = (%d, %q), want success", code, stderr)
	}
	if got := g.listQuery.Get("maxResults"); got != "25" {
		t.Fatalf("maxResults = %q, want 25", got)
	}
	if got := g.listQuery["labelIds"]; len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("labelIds = %v, want [INBOX]", got)
	}
	threads, ok := value["threads"].([]any)
	if !ok || len(threads) != 2 {
		t.Fatalf("threads = %#v, want two rows", value["threads"])
	}
	row := threads[0].(map[string]any)
	for _, key := range []string{"n", "id", "subject", "from", "date", "snippet", "unread", "labels"} {
		if _, ok := row[key]; !ok {
			t.Errorf("row missing %q: %#v", key, row)
		}
	}
	if id, err := refs.Resolve("work", "1"); err != nil || id != "t1" {
		t.Fatalf("first written ref = (%q, %v), want t1", id, err)
	}
	if id, err := refs.Resolve("work", "2"); err != nil || id != "t2" {
		t.Fatalf("second written ref = (%q, %v), want t2", id, err)
	}
}

func TestInboxFilterKeepsMatchesAndNumbersRefs(t *testing.T) {
	g := newGmailTestServer(t)
	matching := metadataThread("t1")
	matching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "GitHub <notifications@github.com>"
	nonMatching := metadataThread("t2")
	nonMatching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "Human <human@example.test>"
	g.metadata = map[string]map[string]any{"t1": matching, "t2": nonMatching}

	config := "[accounts.work]\nread_credential_env = \"CLI_READ\"\n\n[filters.github]\nfrom = \"notifications@github\\\\.com\"\n"
	code, value, stderr := runJSONWithConfig(t, g, config, "inbox", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("inbox --filter = (%d, %q), want success", code, stderr)
	}
	if got := value["filter"]; got != "github" {
		t.Fatalf("payload filter = %#v, want github", got)
	}
	threads, ok := value["threads"].([]any)
	if !ok || len(threads) != 1 {
		t.Fatalf("filtered threads = %#v, want one row", value["threads"])
	}
	row := threads[0].(map[string]any)
	if got := row["id"]; got != "t1" {
		t.Fatalf("filtered thread ID = %#v, want t1", got)
	}
	if got := row["n"]; got != float64(1) {
		t.Fatalf("filtered thread number = %#v, want 1", got)
	}
	if id, err := refs.Resolve("work", "1"); err != nil || id != "t1" {
		t.Fatalf("first filtered ref = (%q, %v), want t1", id, err)
	}
	if _, err := refs.Resolve("work", "2"); err == nil {
		t.Fatal("filtered-out thread remained addressable by a numbered reference")
	}
}

func TestInboxFilterUnknownNameIsHardError(t *testing.T) {
	g := newGmailTestServer(t)
	config := "[accounts.work]\nread_credential_env = \"CLI_READ\"\n\n[filters.github]\nfrom = \"notifications@github\\\\.com\"\n\n[filters.hiring]\nsubject = \"(?i)red.?team\"\n"
	code, _, stderr := runCLIWithConfig(t, g, config, "inbox", "--filter", "nope")
	if code != 1 || !strings.Contains(stderr, `unknown filter "nope"; defined filters: github, hiring`) {
		t.Fatalf("inbox unknown filter = (%d, %q), want hard error with defined names", code, stderr)
	}
	if g.listCalls != 0 {
		t.Fatalf("unknown filter listed threads %d time(s), want no API call", g.listCalls)
	}
}

func TestInboxFilterUnknownWithNoConfigNamesDefaultPath(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configHome)
	previous, wasSet := os.LookupEnv("MAILBOX_CONFIG")
	if err := os.Unsetenv("MAILBOX_CONFIG"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("MAILBOX_CONFIG", previous)
			return
		}
		_ = os.Unsetenv("MAILBOX_CONFIG")
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"inbox", "--filter", "nope"}, &stdout, &stderr)
	want := "no filters are defined (config: " + filepath.Join(configHome, "mailbox", "config.toml") + ")"
	if code != 1 || !strings.Contains(stderr.String(), want) {
		t.Fatalf("inbox unknown no-config filter = (%d, %q), want %q", code, stderr.String(), want)
	}
}

func TestFilterFlagRefusedOnUnsupportedCommands(t *testing.T) {
	for _, args := range [][]string{
		{"read", "--filter", "github", "1"},
		{"open", "--filter", "github", "1"},
		{"attachment", "--filter", "github", "1"},
		{"status", "--filter", "github"},
		{"send", "--filter", "github"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(args, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "--filter is not supported by "+args[0]) {
				t.Fatalf("Run(%q) = (%d, %q), want usage refusal", args, code, stderr.String())
			}
		})
	}
}

func TestInboxFilterTextFormatNamesFilter(t *testing.T) {
	g := newGmailTestServer(t)
	config := "[accounts.work]\nread_credential_env = \"CLI_READ\"\n\n[filters.github]\nfrom = \"notifications@github\\\\.com\"\n"
	code, stdout, stderr := runCLIWithConfig(t, g, config, "inbox", "--filter", "github", "--text")
	if code != 0 || stderr != "" {
		t.Fatalf("inbox --filter --text = (%d, %q), want success", code, stderr)
	}
	if first, _, _ := strings.Cut(stdout, "\n"); first != "filter: github" {
		t.Fatalf("first text line = %q, want filter name", first)
	}
}

func TestInboxExcludesThreadsWithoutInboxMessage(t *testing.T) {
	g := newGmailTestServer(t)
	g.listIDs = []string{"inbox", "sent"}
	sent := metadataThread("sent")
	sent["messages"].([]map[string]any)[0]["labelIds"] = []string{"SENT"}
	g.metadata = map[string]map[string]any{
		"inbox": metadataThread("inbox"),
		"sent":  sent,
	}

	code, value, stderr := runJSON(t, g, "inbox", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("inbox = (%d, %q), want success", code, stderr)
	}
	threads := value["threads"].([]any)
	if len(threads) != 1 {
		t.Fatalf("inbox threads = %#v, want only the thread with an INBOX message", threads)
	}
	if got := threads[0].(map[string]any)["id"]; got != "inbox" {
		t.Fatalf("remaining inbox thread ID = %q, want inbox", got)
	}
	if _, err := refs.Resolve("work", "2"); err == nil {
		t.Fatal("filtered-out thread remained addressable by a numbered reference")
	}
}

func TestInboxUsesMatchingListSnippetAfterFilter(t *testing.T) {
	g := newGmailTestServer(t)
	g.listIDs = []string{"first", "sent", "last"}
	first := metadataThread("first")
	first["snippet"] = ""
	sent := metadataThread("sent")
	sent["messages"].([]map[string]any)[0]["labelIds"] = []string{"SENT"}
	last := metadataThread("last")
	last["snippet"] = ""
	g.metadata = map[string]map[string]any{
		"first": first,
		"sent":  sent,
		"last":  last,
	}

	code, value, stderr := runJSON(t, g, "inbox", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("inbox = (%d, %q), want success", code, stderr)
	}
	threads := value["threads"].([]any)
	if len(threads) != 2 {
		t.Fatalf("inbox threads = %#v, want two INBOX rows", threads)
	}
	lastRow := threads[1].(map[string]any)
	if got, want := lastRow["id"], "last"; got != want {
		t.Fatalf("second row ID = %q, want %q", got, want)
	}
	if got, want := lastRow["snippet"], "snippet last"; got != want {
		t.Fatalf("second row snippet = %q, want matching listed snippet %q", got, want)
	}
}

func TestEmptyInboxJSONUsesArray(t *testing.T) {
	g := newGmailTestServer(t)
	g.listIDs = []string{}
	code, stdout, stderr := runCLI(t, g, "inbox", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty inbox = (%d, %q), want success", code, stderr)
	}
	assertEmptyJSONField(t, stdout, "threads")
}

func TestInboxUnreadMax(t *testing.T) {
	g := newGmailTestServer(t)
	code, _, _ := runJSON(t, g, "inbox", "--unread", "--max", "7", "--json")
	if code != 0 {
		t.Fatalf("inbox exit = %d, want 0", code)
	}
	if got := g.listQuery["labelIds"]; len(got) != 2 || got[0] != "INBOX" || got[1] != "UNREAD" {
		t.Fatalf("labelIds = %v, want INBOX and UNREAD", got)
	}
	if got := g.listQuery.Get("maxResults"); got != "7" {
		t.Fatalf("maxResults = %q, want 7", got)
	}
}

func TestInboxMaxRange(t *testing.T) {
	for _, max := range []string{"0", "501"} {
		t.Run(max, func(t *testing.T) {
			g := newGmailTestServer(t)
			code, _, stderr := runCLI(t, g, "inbox", "--max", max)
			if code != 2 || !strings.Contains(stderr, "1..500") {
				t.Fatalf("inbox max %s = (%d, %q), want usage naming 1..500", max, code, stderr)
			}
		})
	}
}

func TestSearchPassthrough(t *testing.T) {
	g := newGmailTestServer(t)
	code, _, _ := runJSON(t, g, "search", "from:alice", "is:unread", "--json")
	if code != 0 || g.listQuery.Get("q") != "from:alice is:unread" {
		t.Fatalf("search = (%d, q=%q), want verbatim query", code, g.listQuery.Get("q"))
	}
	if id, err := refs.Resolve("work", "1"); err != nil || id != "t1" {
		t.Fatalf("written search ref = (%q, %v), want t1", id, err)
	}
}

func TestSearchFilterKeepsMatches(t *testing.T) {
	g := newGmailTestServer(t)
	matching := metadataThread("t1")
	matching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "GitHub <notifications@github.com>"
	nonMatching := metadataThread("t2")
	nonMatching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "Human <human@example.test>"
	g.metadata = map[string]map[string]any{"t1": matching, "t2": nonMatching}

	config := "[accounts.work]\nread_credential_env = \"CLI_READ\"\n\n[filters.github]\nfrom = \"notifications@github\\\\.com\"\n"
	code, value, stderr := runJSONWithConfig(t, g, config, "search", "is:unread", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("search --filter = (%d, %q), want success", code, stderr)
	}
	if got := g.listQuery.Get("q"); got != "is:unread" {
		t.Fatalf("search query = %q, want is:unread", got)
	}
	if got := value["filter"]; got != "github" {
		t.Fatalf("payload filter = %#v, want github", got)
	}
	threads, ok := value["threads"].([]any)
	if !ok || len(threads) != 1 || threads[0].(map[string]any)["id"] != "t1" {
		t.Fatalf("filtered search threads = %#v, want only t1", value["threads"])
	}
}

func TestSearchDashTerms(t *testing.T) {
	g := newGmailTestServer(t)
	code, _, _ := runCLI(t, g, "search", "--", "-label:promotions", "from:x")
	if code != 0 || g.listQuery.Get("q") != "-label:promotions from:x" {
		t.Fatalf("search = (%d, q=%q), want dash terms positional after --", code, g.listQuery.Get("q"))
	}
}

func TestFlagAfterPositional(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		g := newGmailTestServer(t)
		t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
		seedRefs(t, "t1")
		code, value, _ := runJSON(t, g, "read", "1", "--full", "--json")
		if code != 0 || value["id"] != "t1" {
			t.Fatalf("read = (%d, %#v), want rendered thread", code, value)
		}
	})
	t.Run("search", func(t *testing.T) {
		g := newGmailTestServer(t)
		code, _, _ := runJSON(t, g, "search", "is:unread", "--max", "7", "--json")
		if code != 0 || g.listQuery.Get("maxResults") != "7" {
			t.Fatalf("search exit/query = %d/%q", code, g.listQuery.Get("maxResults"))
		}
	})
	t.Run("attachment", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureAttachmentMessage(g)
		dir := t.TempDir()
		code, _, _ := runJSON(t, g, "attachment", "m-att", "1", "-o", dir, "--json")
		if code != 0 {
			t.Fatalf("attachment exit = %d", code)
		}
		if _, err := os.Stat(filepath.Join(dir, "report.pdf")); err != nil {
			t.Fatalf("download file: %v", err)
		}
	})
}

func TestReadJSON(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, value, _ := runJSON(t, g, "read", "1", "--json")
	if code != 0 || value["account"] != "work" || value["id"] != "t1" {
		t.Fatalf("read = (%d, %#v), want account and thread", code, value)
	}
	message := value["messages"].([]any)[0].(map[string]any)
	attachment := message["attachments"].([]any)[0].(map[string]any)
	if len(attachment) != 4 || attachment["filename"] != "report.pdf" {
		t.Fatalf("attachment JSON = %#v, want public four-key shape", attachment)
	}
}

func TestReadJSONUsesEmptyMessageArrays(t *testing.T) {
	g := newGmailTestServer(t)
	g.thread = testThread("t1", false, false)
	code, stdout, stderr := runCLI(t, g, "read", "t1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("read = (%d, %q), want success", code, stderr)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode read JSON: %v", err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(document["messages"], &messages); err != nil || len(messages) != 1 {
		t.Fatalf("read messages = (%v, %v), want one message", messages, err)
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(messages[0], &message); err != nil {
		t.Fatalf("decode rendered message: %v", err)
	}
	assertEmptyRawJSONField(t, message, "links")
	assertEmptyRawJSONField(t, message, "attachments")
}

func TestReadJSONUsesEmptyThreadArrays(t *testing.T) {
	g := newGmailTestServer(t)
	g.thread = map[string]any{"id": "t1", "messages": []map[string]any{}}
	code, stdout, stderr := runCLI(t, g, "read", "t1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty thread read = (%d, %q), want success", code, stderr)
	}
	assertEmptyJSONField(t, stdout, "participants")
	assertEmptyJSONField(t, stdout, "messages")
}

func TestReadFullKeepsQuotes(t *testing.T) {
	g := newGmailTestServer(t)
	g.thread = testThread("t1", false, true)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, without, _ := runJSON(t, g, "read", "1", "--json")
	if code != 0 || strings.Contains(firstMarkdown(without), "quoted marker") {
		t.Fatalf("default markdown retained quote: %q", firstMarkdown(without))
	}
	code, with, _ := runJSON(t, g, "read", "1", "--full", "--json")
	if code != 0 || !strings.Contains(firstMarkdown(with), "quoted marker") {
		t.Fatalf("full markdown omitted quote: %q", firstMarkdown(with))
	}
}

func TestReadPipedMarkdown(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, stdout, _ := runCLI(t, g, "read", "1", "--text")
	if code != 0 || !strings.HasPrefix(stdout, "# ") {
		t.Fatalf("read stdout = %q, want raw markdown", stdout)
	}
	if !strings.Contains(stdout, "\n(newest first)\n") {
		t.Fatalf("read stdout = %q, want newest-first marker", stdout)
	}
}

func TestReadPipedDefaultsToTOON(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, stdout, _ := runCLI(t, g, "read", "1")
	if code != 0 || !strings.HasPrefix(stdout, "account: ") {
		t.Fatalf("read stdout = %q, want TOON account field", stdout)
	}
	if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("decode TOON stdout %q: %v", stdout, err)
	}
}

func TestStructuredSurfacesDefaultToTOON(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *gmailTestServer) []string
	}{
		{name: "inbox", prepare: func(_ *testing.T, _ *gmailTestServer) []string { return []string{"inbox"} }},
		{name: "search", prepare: func(_ *testing.T, _ *gmailTestServer) []string { return []string{"search", "from:alice"} }},
		{name: "read", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			seedRefs(t, "t1")
			return []string{"read", "1"}
		}},
		{name: "status", prepare: func(_ *testing.T, _ *gmailTestServer) []string { return []string{"status"} }},
		{name: "archive", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			seedRefs(t, "t1")
			return []string{"archive", "1"}
		}},
		{name: "trash", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			seedRefs(t, "t1")
			return []string{"trash", "1"}
		}},
		{name: "mark", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			seedRefs(t, "t1")
			return []string{"mark", "unread", "1"}
		}},
		{name: "label", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			seedRefs(t, "t1")
			return []string{"label", "add", "Newsletters", "1"}
		}},
		{name: "attachment list", prepare: func(_ *testing.T, g *gmailTestServer) []string {
			configureAttachmentMessage(g)
			return []string{"attachment", "m-att"}
		}},
		{name: "attachment save", prepare: func(t *testing.T, g *gmailTestServer) []string {
			configureAttachmentMessage(g)
			return []string{"attachment", "m-att", "1", "-o", t.TempDir()}
		}},
		{name: "open", prepare: func(t *testing.T, _ *gmailTestServer) []string {
			stub := t.TempDir()
			path := filepath.Join(stub, "xdg-open")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", stub+":"+os.Getenv("PATH"))
			seedRefs(t, "t1")
			return []string{"open", "1"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
			args := test.prepare(t, g)
			code, stdout, stderr := runCLI(t, g, args...)
			if code != 0 {
				t.Fatalf("%v = (%d, %q, %q), want success", args, code, stdout, stderr)
			}
			if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
				t.Fatalf("%v TOON decode %q: %v", args, stdout, err)
			}
		})
	}
}

func TestReadJSONKeepsRawMailText(t *testing.T) {
	g := newGmailTestServer(t)
	payload := "mail\x1b]52;c;clipboard\a"
	message := g.thread["messages"].([]map[string]any)[0]
	message["payload"].(map[string]any)["headers"].([]map[string]string)[2]["value"] = payload
	part := message["payload"].(map[string]any)["parts"].([]map[string]any)[0]
	part["mimeType"] = "text/plain"
	part["body"].(map[string]any)["data"] = base64.RawURLEncoding.EncodeToString([]byte(payload))
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")

	code, value, stderr := runJSON(t, g, "read", "1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("read JSON = (%d, %q), want success", code, stderr)
	}
	if got := value["subject"]; got != payload {
		t.Fatalf("read JSON subject = %q, want raw %q", got, payload)
	}
	messageValue := value["messages"].([]any)[0].(map[string]any)
	if markdown, ok := messageValue["markdown"].(string); !ok || !strings.Contains(markdown, payload) {
		t.Fatalf("read JSON markdown = %#v, want raw body payload", messageValue["markdown"])
	}
}

func TestOpenWritesTempAndSpawns(t *testing.T) {
	g := newGmailTestServer(t)
	g.thread = testCIDThread()
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	stub := t.TempDir()
	capture := filepath.Join(t.TempDir(), "opened")
	path := filepath.Join(stub, "xdg-open")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$1\" > '"+capture+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+":"+os.Getenv("PATH"))
	code, value, stderr := runJSON(t, g, "open", "1", "--json")
	if code != 0 || !strings.Contains(stderr, "handed to opener: ") {
		t.Fatalf("open = (%d, %q), want handoff diagnostic", code, stderr)
	}
	opened, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	html, err := os.ReadFile(strings.TrimSpace(string(opened)))
	if err != nil || !strings.Contains(string(html), "data:image/png;base64") {
		t.Fatalf("opened HTML = %q, read error = %v", html, err)
	}
	for _, forbidden := range []string{"steal()", "onclick=", "https://tracker.example"} {
		if strings.Contains(string(html), forbidden) {
			t.Errorf("opened HTML retained active content %q: %q", forbidden, html)
		}
	}
	if value["messageId"] != "m1" {
		t.Fatalf("open JSON = %#v, want message id", value)
	}
}

func TestRawMessageReferenceResolvesForModify(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "archive", args: []string{"archive", "m-raw", "--json"}},
		{name: "trash", args: []string{"trash", "m-raw", "--json"}},
		{name: "mark", args: []string{"mark", "unread", "m-raw", "--json"}},
		{name: "label", args: []string{"label", "add", "newsletters", "m-raw", "--json"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			g.rawMessageID = "m-raw"
			code, _, stderr := runCLI(t, g, testCase.args...)
			if code != 0 || strings.Contains(strings.Join(g.directRequests, "\n"), "/threads/m-raw/") {
				t.Fatalf("%s = (%d, %q, %v), want operation on parent thread", testCase.name, code, stderr, g.directRequests)
			}
		})
	}
}

func TestRawMessageReferenceResolvesForOpen(t *testing.T) {
	g := newGmailTestServer(t)
	g.rawMessageID = "m-raw"
	stub := t.TempDir()
	path := filepath.Join(stub, "xdg-open")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+":"+os.Getenv("PATH"))
	code, stdout, stderr := runCLI(t, g, "open", "m-raw", "--json")
	var value map[string]any
	if code == 0 {
		if err := json.Unmarshal([]byte(stdout), &value); err != nil {
			t.Fatalf("decode open JSON %q: %v", stdout, err)
		}
	}
	if code != 0 || !strings.Contains(stderr, "handed to opener: ") || value["threadId"] != "t1" {
		t.Fatalf("open raw message = (%d, %#v, %q), want parent thread and handoff diagnostic", code, value, stderr)
	}
}

func TestArchiveMultiple(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1", "t2")
	code, value, _ := runJSON(t, g, "archive", "1", "2", "--json")
	if code != 0 || value["action"] != "archive" || !strings.Contains(strings.Join(g.batchRequests, "\n"), `"removeLabelIds":["INBOX"]`) {
		t.Fatalf("archive = (%d, %#v, %v), want batch archive", code, value, g.batchRequests)
	}
}

func TestArchiveDuplicateExplicitIDsDeduplicatesGmailWrite(t *testing.T) {
	g := newGmailTestServer(t)
	code, value, stderr := runJSON(t, g, "archive", "t1", "t1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("archive duplicate ids = (%d, %#v, %q), want success", code, value, stderr)
	}
	ids, ok := value["threadIds"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "t1" || ids[1] != "t1" {
		t.Fatalf("reported thread ids = %#v, want [t1 t1]", value["threadIds"])
	}
	if len(g.directRequests) != 1 || !strings.Contains(g.directRequests[0], "POST /gmail/v1/users/me/threads/t1/modify") || len(g.batchWriteIDs) != 0 {
		t.Fatalf("Gmail write requests = direct %v, batch %v; want one direct t1 mutation", g.directRequests, g.batchWriteIDs)
	}
}

func TestTrashHumanOutput(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, stdout, _ := runCLI(t, g, "trash", "1", "--text")
	if code != 0 || stdout != "trashed 1 thread(s)\n" {
		t.Fatalf("trash = (%d, %q), want human trash line", code, stdout)
	}
}

func TestMarkUnread(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, value, _ := runJSON(t, g, "mark", "unread", "1", "--json")
	if code != 0 || value["action"] != "mark" || !strings.Contains(strings.Join(g.directRequests, "\n"), `"addLabelIds":["UNREAD"]`) {
		t.Fatalf("mark = (%d, %#v, %v), want unread modification", code, value, g.directRequests)
	}
}

func TestLabelAddByName(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, _, _ := runJSON(t, g, "label", "add", "newsletters", "1", "--json")
	if code != 0 || !strings.Contains(strings.Join(g.directRequests, "\n"), "Label_7") {
		t.Fatalf("label = (%d, %v), want resolved label", code, g.directRequests)
	}
}

func TestLabelUnknownIsLoud(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, _, stderr := runCLI(t, g, "label", "add", "nope", "1")
	if code != 1 || !strings.Contains(stderr, "Newsletters") {
		t.Fatalf("label unknown = (%d, %q), want available names", code, stderr)
	}
}

func TestStatusJSON(t *testing.T) {
	g := newGmailTestServer(t)
	code, value, _ := runJSON(t, g, "status", "--json")
	accounts, ok := value["accounts"].([]any)
	if code != 0 || !ok || len(accounts) != 1 || value["ok"] != true {
		t.Fatalf("status = (%d, %#v), want one successful account", code, value)
	}
	row := accounts[0].(map[string]any)
	if row["name"] != "work" || row["route"] != "env-token" || row["pinned"] != true {
		t.Fatalf("status account = %#v", row)
	}
}

func TestStatusHumanWritesAllStatusLinesToStdout(t *testing.T) {
	g := newGmailTestServer(t)
	code, stdout, stderr := runCLI(t, g, "status", "--text")
	if code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	for _, line := range []string{
		"config: ", "account: work (default)", "read: env", "write: not configured",
		"route: env-token", "cache: absent", "profile: user@example.com",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout %q does not contain %q", stdout, line)
		}
	}
	if stderr != "" {
		t.Fatalf("status stderr = %q, want empty", stderr)
	}
}

func TestStatusJSONWriteFailurePrintsDiagnostic(t *testing.T) {
	g := newGmailTestServer(t)
	setCLIConfig(t)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_TOKEN", "test-token")
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	var stderr bytes.Buffer
	code := Run([]string{"status", "--json"}, failingWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("status exit = %d, want 1", code)
	}
	for _, line := range []string{"mailbox: write JSON"} {
		if !strings.Contains(stderr.String(), line) {
			t.Errorf("stderr %q does not contain %q", stderr.String(), line)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("closed output")
}

func TestScopeHintOn403(t *testing.T) {
	g := newGmailTestServer(t)
	g.forbidden = true
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	seedRefs(t, "t1")
	code, _, stderr := runCLI(t, g, "archive", "1")
	if code != 1 || !strings.Contains(stderr, "provision:") || !strings.Contains(stderr, "MAILBOX_TOKEN") {
		t.Fatalf("scope error = (%d, %q), want hint", code, stderr)
	}
}

func TestConfiguredReadScopeHintNamesConfigKey(t *testing.T) {
	g := newGmailTestServer(t)
	g.readForbidden = true
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(config, []byte("[accounts.work]\nread_credential_env = \"CLI_SCOPE_OAUTH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", config)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("CLI_SCOPE_OAUTH", `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`)
	t.Setenv("MAILBOX_TOKEN_URL", g.tokenURL(t, "test-token"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"read", "t1"}, &stdout, &stderr); code != 1 {
		t.Fatalf("read exit = %d, stderr = %q", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "accounts.work.read_credential_env") || strings.Contains(got, "CLI_SCOPE_OAUTH") {
		t.Fatalf("stderr = %q, want only the config key path", got)
	}
}

func TestStaleRefIsLoud(t *testing.T) {
	g := newGmailTestServer(t)
	code, _, stderr := runCLI(t, g, "read", "3")
	if code != 1 || !strings.Contains(stderr, "no ref cache for account 'work'") {
		t.Fatalf("stale ref = (%d, %q), want ref error", code, stderr)
	}
}

func TestFlagBeforeSubcommand(t *testing.T) {
	g := newGmailTestServer(t)
	code, value, _ := runJSON(t, g, "--json", "inbox")
	if code != 0 || value["account"] != "work" {
		t.Fatalf("leading flag = (%d, %#v)", code, value)
	}
}

func TestFilterBeforeSubcommand(t *testing.T) {
	g := newGmailTestServer(t)
	matching := metadataThread("t1")
	matching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "GitHub <notifications@github.com>"
	nonMatching := metadataThread("t2")
	nonMatching["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)[0]["value"] = "Human <human@example.test>"
	g.metadata = map[string]map[string]any{"t1": matching, "t2": nonMatching}

	config := "[accounts.work]\nread_credential_env = \"CLI_READ\"\n\n[filters.github]\nfrom = \"notifications@github\\\\.com\"\n"
	code, value, stderr := runJSONWithConfig(t, g, config, "--filter", "github", "inbox", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("leading filter = (%d, %q), want success", code, stderr)
	}
	if got := value["filter"]; got != "github" {
		t.Fatalf("leading filter payload = %#v, want github", got)
	}
	threads, ok := value["threads"].([]any)
	if !ok || len(threads) != 1 || threads[0].(map[string]any)["id"] != "t1" {
		t.Fatalf("leading filter threads = %#v, want only t1", value["threads"])
	}
}

func TestJSONStdoutPurity(t *testing.T) {
	g := newGmailTestServer(t)
	code, stdout, _ := runCLI(t, g, "--json", "status")
	if code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
}

func firstMarkdown(value map[string]any) string {
	return value["messages"].([]any)[0].(map[string]any)["markdown"].(string)
}

func writeResponse(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return contents
}

func TestCLIStartupConfigErrorSanitizesTerminalText(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	t.Setenv("MAILBOX_CONFIG", filepath.Join(t.TempDir(), "missing-"+payload+".toml"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"inbox"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "\x1b") || strings.Contains(stderr.String(), "clipboard") {
		t.Fatalf("CLI startup error leaked terminal control text: %q", stderr.String())
	}
}

func TestCLICredentialCommandErrorSanitizesTerminalText(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	dir := t.TempDir()
	helper := filepath.Join(dir, "broken-"+payload)
	if err := os.WriteFile(helper, []byte("#!/definitely/missing-interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	tomlHelper := strings.Replace(helper, payload, `\u001b]52;c;clipboard\u0007`, 1)
	config := "default_account = \"work\"\n[accounts.work]\nread_credential_cmd = [\"" + tomlHelper + "\"]\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", configPath)
	t.Setenv("MAILBOX_TOKEN", "")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"inbox"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "\x1b") || strings.Contains(stderr.String(), "clipboard") {
		t.Fatalf("CLI command error leaked terminal control text: %q", stderr.String())
	}
}

func TestHelpListsEveryPublicCommand(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%q) exit = %d, stdout=%q, stderr=%q", args, code, stdout.String(), stderr.String())
		}
		for _, command := range []string{"inbox", "search", "read", "open", "archive", "trash", "mark", "label", "attachment", "status", "send"} {
			if !strings.Contains(stdout.String(), command) {
				t.Fatalf("help for %q omitted %q: %q", args, command, stdout.String())
			}
		}
		if !strings.Contains(stdout.String(), "XDG_CONFIG_HOME") {
			t.Fatalf("help for %q omitted config location: %q", args, stdout.String())
		}
		if !strings.Contains(stdout.String(), "--filter NAME") {
			t.Fatalf("help for %q omitted filter flag: %q", args, stdout.String())
		}
	}
}

func TestCommandHelpDocumentsReadSearchAndSend(t *testing.T) {
	setCLIConfig(t)

	readHelp := commandHelp(t, "read")
	for _, want := range []string{
		"usage: mailbox read [--full] [--text|--json] <thread>",
		"Messages print newest first.",
		"--full",
	} {
		if !strings.Contains(readHelp, want) {
			t.Fatalf("read help = %q, want %q", readHelp, want)
		}
	}

	inboxHelp := commandHelp(t, "inbox")
	for _, want := range []string{
		"usage: mailbox inbox [--unread] [--max N] [--filter NAME] [--text|--json]",
		"--filter restricts rows to a named config filter",
	} {
		if !strings.Contains(inboxHelp, want) {
			t.Fatalf("inbox help = %q, want %q", inboxHelp, want)
		}
	}

	searchHelp := commandHelp(t, "search")
	for _, want := range []string{
		"usage: mailbox search [--max N] [--filter NAME] [--text|--json] <query...>",
		"--filter restricts rows to a named config filter",
		"Gmail query operators pass through verbatim: from: to: cc: bcc: subject: label: is: has: in: filename: after: before: older_than: newer_than: deliveredto: list: (see Gmail search syntax).",
	} {
		if !strings.Contains(searchHelp, want) {
			t.Fatalf("search help = %q, want %q", searchHelp, want)
		}
	}

	sendHelp := commandHelp(t, "send")
	for _, want := range []string{
		"mailbox send --to a@x [--cc b@y] [--bcc c@z] --subject S --body TEXT      # compose",
		"mailbox send --reply=<thread-id>  --body TEXT [--message=<id>] [--to ...] # reply",
		"mailbox send --forward=<thread-id> --to a@x --body TEXT [--message=<id>]  # forward",
		"dry-run",
		"--message",
		"--send",
		"--draft <draft-id>",
		"draft_changed",
		"draft_send_unknown",
		"drafts.send",
		"R1",
		"R2",
		"R3",
		"R4",
		"R5",
		"R6",
	} {
		if !strings.Contains(sendHelp, want) {
			t.Fatalf("send help = %q, want %q", sendHelp, want)
		}
	}
}

func TestCommandHelpDoesNotRequireConfiguration(t *testing.T) {
	t.Setenv("MAILBOX_CONFIG", filepath.Join(t.TempDir(), "missing-config.toml"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"read", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("read --help exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Messages print newest first.") {
		t.Fatalf("read help = %q, want ordering documentation", stdout.String())
	}
}

func TestHelpDocumentsThreadIDSemantics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "ids: mailbox ids are THREAD ids everywhere; the exceptions are 'send --message' and 'attachment', which take message ids (message ids appear in 'read' output). All-digit arguments are refs into the last 'inbox'/'search' listing."
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("help = %q, want id semantics", stdout.String())
	}
}

func TestSkillGeneratorDocumentsDispatchAndHelp(t *testing.T) {
	output := filepath.Join(t.TempDir(), "SKILL.md")
	command := exec.Command("go", "run", "./cmd/skillgen", "-out", output)
	command.Dir = filepath.Join("..", "..")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate skill: %v\n%s", err, result)
	}
	document, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	skill := string(document)
	for _, command := range commandSpecs() {
		if !strings.Contains(skill, "`"+command.name+"`") {
			t.Fatalf("generated skill omitted command %q:\n%s", command.name, skill)
		}
	}
	for _, want := range []string{
		"---\nname: mailbox\ndescription: Gmail triage CLI — one-shot commands with TOON/JSON machine output\n---",
		"Messages print newest first.",
		"Gmail query operators pass through verbatim:",
		"mailbox ids are THREAD ids everywhere",
		"TOON is the default for agents and pipes.",
		"`--json` is the stable opt-in.",
		"`--text` forces human output.",
		"Every surface executes configured credential commands.",
		"`*_interactive` passes caller standard input only when it is a real terminal; otherwise helpers receive `/dev/null`.",
		"Start with the dry run, copy its `--message` value, then add `--send` to transmit that exact target.",
		"| R1 | empty_recipients |",
		"| R2 | self_only_recipients |",
		"| R3 | invalid_address |",
		"| R4 | header_injection |",
		"| R5 | empty_body |",
		"| R6 | needs_explicit_recipient |",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("generated skill omitted %q:\n%s", want, skill)
		}
	}
}

func commandHelp(t *testing.T, name string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{name, "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("%s --help exit = %d, stdout=%q, stderr=%q", name, code, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestInvalidGlobalFlagRemainsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--not-a-real-flag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("invalid flag exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCommandUsageErrorsPrintTheirOwnHelp(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		diagnostic string
		usage      string
		help       string
	}{
		{
			name:       "send positional",
			args:       []string{"send", "reply", "t123"},
			diagnostic: "send requires 0 argument(s)",
			usage:      "usage: mailbox send [--attach PATH]... [--save-draft|--send] [options]",
			help:       "Compose:",
		},
		{
			name:       "attachment selector count",
			args:       []string{"attachment", "m1", "report.pdf", "extra"},
			diagnostic: "attachment requires 1 to 2 argument(s)",
			usage:      "usage: mailbox attachment [-o PATH|-o -] [--text|--json] <message-id> [filename|index]",
			help:       "Listings use zero-based indexes",
		},
		{
			name:       "drafts positional",
			args:       []string{"drafts", "extra"},
			diagnostic: "drafts requires 0 argument(s)",
			usage:      "usage: mailbox drafts [--max N] [--text|--json]",
			help:       "Lists Gmail server-side drafts newest-first",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(c.args, &stdout, &stderr); code != 2 {
				t.Fatalf("Run(%v) = %d, want 2", c.args, code)
			}

			got := stderr.String()
			if !strings.Contains(got, "mailbox: "+c.diagnostic) {
				t.Fatalf("stderr = %q, want diagnostic %q", got, c.diagnostic)
			}
			if !strings.Contains(got, c.usage) {
				t.Fatalf("stderr = %q, want command usage %q", got, c.usage)
			}
			if !strings.Contains(got, c.help) {
				t.Fatalf("stderr = %q, want command help %q", got, c.help)
			}
			if strings.Contains(got, "global flags:") {
				t.Fatalf("stderr must not include global help: %q", got)
			}

			wantHelp := commandHelp(t, c.args[0])
			gotHelp := strings.TrimPrefix(got, "mailbox: "+c.diagnostic+"\n")
			if gotHelp != wantHelp {
				t.Fatalf("stderr help = %q, want %q", gotHelp, wantHelp)
			}
		})
	}
}

func TestMintUsageErrorPrintsHiddenCommandUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(__mint) = %d, want 2", code)
	}

	got := stderr.String()
	const wantDiagnostic = "mailbox: __mint requires --env VAR\n"
	const wantUsage = "usage: mailbox __mint --env VAR\n"
	if !strings.HasPrefix(got, wantDiagnostic+wantUsage) {
		t.Fatalf("stderr = %q, want diagnostic and command usage", got)
	}
	if strings.Contains(got, "global flags:") {
		t.Fatalf("stderr must not include global help: %q", got)
	}
}

func TestGlobalHelpOmitsMint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(--help) = %d, want 0", code)
	}
	if strings.Contains(stdout.String(), "__mint") {
		t.Fatalf("global help must omit __mint: %q", stdout.String())
	}
}

func TestCommandUsageErrorsKeepMachineEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--json", "send", "reply", "t123"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(--json send reply t123) = %d, want 2", code)
	}
	const wantEnvelope = "{\"error\":{\"code\":\"usage\",\"message\":\"send requires 0 argument(s)\"}}\n"
	if got := stdout.String(); got != wantEnvelope {
		t.Fatalf("JSON usage envelope = %q, want %q", got, wantEnvelope)
	}
	if !strings.Contains(stderr.String(), "usage: mailbox send [--attach PATH]... [--save-draft|--send] [options]") {
		t.Fatalf("stderr = %q, want send help", stderr.String())
	}
}

func TestUnknownCommandUsageErrorKeepsGlobalHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"definitely-not-a-command"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(unknown command) = %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, "mailbox: unknown command \"definitely-not-a-command\"") {
		t.Fatalf("stderr = %q, want unknown-command diagnostic", got)
	}
	if got := stderr.String(); !strings.Contains(got, "usage: mailbox [--account NAME] [--json] [--text] <command> [options]") || !strings.Contains(got, "global flags:") {
		t.Fatalf("stderr = %q, want global help", got)
	}
}

func TestUsageErrorsEmitMachineEnvelope(t *testing.T) {
	cases := []struct {
		args    []string
		message string
	}{
		{[]string{"archive"}, "archive requires"},
		{[]string{"send", "--reply", "t1", "--body", "x", "--send"}, "--send requires --message"},
		{[]string{"mark", "sideways", "t1"}, "mark mode must be read or unread"},
		{[]string{"definitely-not-a-command"}, "unknown command"},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(c.args, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("Run(%v) = %d, want 2", c.args, code)
		}
		doc, err := toontest.Decode(strings.TrimSuffix(stdout.String(), "\n"))
		if err != nil {
			t.Fatalf("Run(%v) stdout %q is not TOON: %v", c.args, stdout.String(), err)
		}
		errObj := toonField(t, doc, "error")
		if got := toonString(t, errObj, "code"); got != "usage" {
			t.Fatalf("error.code = %q, want usage", got)
		}
		if got := toonString(t, errObj, "message"); !strings.Contains(got, c.message) {
			t.Fatalf("error.message = %q, want containing %q", got, c.message)
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("stderr usage dump must remain: %q", stderr.String())
		}
	}
}

func TestUsageErrorJSONAndTextModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--json", "archive"}, &stdout, &stderr); code != 2 {
		t.Fatal("exit must stay 2")
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil || payload.Error.Code != "usage" {
		t.Fatalf("JSON usage envelope = %q (%v)", stdout.String(), err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--text", "archive"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("text mode must keep stdout empty, got %q", stdout.String())
	}
}

func TestUsageErrorHonorsFlagsParsedBeforeFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--json", "--definitely-unknown"}, &stdout, &stderr); code != 2 {
		t.Fatal("exit must stay 2")
	}
	if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), `"usage"`) {
		t.Fatalf("top-level --json parse failure must emit strict JSON, got %q", stdout.String())
	}
	stdout.Reset()
	if code := Run([]string{"--text", "--definitely-unknown"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("top-level --text parse failure must keep stdout empty, got %q", stdout.String())
	}
	stdout.Reset()
	if code := Run([]string{"archive", "--json", "--definitely-unknown"}, &stdout, &stderr); code != 2 || !json.Valid(stdout.Bytes()) {
		t.Fatalf("command --json parse failure must emit strict JSON, got %q", stdout.String())
	}
	stdout.Reset()
	if code := Run([]string{"archive", "--text", "--definitely-unknown"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("command --text parse failure must keep stdout empty, got %q", stdout.String())
	}
}

func googleError(status int, reason string) map[string]any {
	return map[string]any{"error": map[string]any{"code": status, "message": "scope denied", "errors": []map[string]string{{"reason": reason}}}}
}

func metadataThread(id string) map[string]any {
	thread := testThread(id, false, false)
	thread["messages"] = []map[string]any{thread["messages"].([]map[string]any)[0]}
	return thread
}

func testThread(id string, attachment, quote bool) map[string]any {
	html := "<p>Hello from mailbox</p>"
	if quote {
		html += `<div class="gmail_quote">quoted marker</div>`
	}
	parts := []map[string]any{{
		"partId":   "body",
		"mimeType": "text/html",
		"body":     map[string]any{"size": len(html), "data": base64.RawURLEncoding.EncodeToString([]byte(html))},
	}}
	if attachment {
		parts = append(parts, map[string]any{
			"partId":   "pdf",
			"mimeType": "application/pdf",
			"filename": "report.pdf",
			"body":     map[string]any{"attachmentId": "att-1", "size": 11},
		})
	}
	message := map[string]any{
		"id":           "m1",
		"threadId":     id,
		"labelIds":     []string{"INBOX", "UNREAD"},
		"internalDate": "1780000000000",
		"payload": map[string]any{
			"headers": []map[string]string{
				{"name": "From", "value": "Alice <alice@example.com>"},
				{"name": "To", "value": "User <user@example.com>"},
				{"name": "Subject", "value": "Mailbox test"},
				{"name": "Date", "value": "Wed, 27 Aug 2026 01:02:03 +0000"},
			},
			"parts": parts,
		},
	}
	return map[string]any{"id": id, "snippet": "snippet " + id, "messages": []map[string]any{message}}
}

func testCIDThread() map[string]any {
	html := `<html><body><script>steal()</script><img src="https://tracker.example/pixel" onclick="steal()"><img src="cid:logo"></body></html>`
	message := map[string]any{
		"id":           "m1",
		"threadId":     "t1",
		"internalDate": "1780000000000",
		"payload": map[string]any{
			"headers": []map[string]string{
				{"name": "From", "value": "Alice"},
				{"name": "Subject", "value": "CID"},
			},
			"parts": []map[string]any{
				{
					"partId":   "html",
					"mimeType": "text/html",
					"body":     map[string]any{"size": len(html), "data": base64.RawURLEncoding.EncodeToString([]byte(html))},
				},
				{
					"partId":   "image",
					"mimeType": "image/png",
					"filename": "logo.png",
					"headers":  []map[string]string{{"name": "Content-ID", "value": "<logo>"}},
					"body":     map[string]any{"size": 3, "data": base64.RawURLEncoding.EncodeToString([]byte("png"))},
				},
			},
		},
	}
	return map[string]any{"id": "t1", "messages": []map[string]any{message}}
}
