package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGmail struct {
	mu                sync.Mutex
	readAuths         []string
	writeAuths        []string
	sendAuths         []string
	sent              []capturedSend
	batchRequests     []string
	modified          []string
	trashed           []string
	calls             []recordedCall
	unknown           []recordedCall
	listPages         [][]string
	listDelay         time.Duration
	threads           map[string]string
	messages          map[string]string
	rawMessages       map[string][]byte
	drafts            map[string]*fixtureDraft
	draftOrder        []string
	draftDeleteStatus int
	attachments       map[string][]byte
	sendGarbage       bool
	sendStatus        int
	server            *httptest.Server
	token             *httptest.Server
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	t1 := fakeMessage("t1", "PTY smoke", "A <a@example.test>", "B <b@example.test>", "C <c@example.test>", "A <a@example.test>")
	t2 := fakeMessage("t2", "Second PTY smoke", "A <a@example.test>", "B <b@example.test>", "", "")
	t3 := fakeMessage("t3", "self-only", "Self <work@example.test>", "Self <work@example.test>", "Self <work@example.test>", "")
	githubHeaders := map[string]string{
		"From":    "GitHub <notifications@github.com>",
		"List-ID": "<ci.github.example>",
		"To":      "Work <work@example.test>",
	}
	github := fakeMessageWithHeaders("t-gh", "GitHub notification", githubHeaders)
	githubTwo := fakeMessageWithHeaders("t-gh-2", "GitHub notification two", githubHeaders)
	githubThree := fakeMessageWithHeaders("t-gh-3", "GitHub notification three", githubHeaders)
	attachment := fakeAttachmentMessage()
	g := &fakeGmail{
		threads: map[string]string{
			"t1":     fakeThread("t1", t1),
			"t2":     fakeThread("t2", t2),
			"t3":     fakeThread("t3", t3),
			"t-gh":   fakeThread("t-gh", github),
			"t-gh-2": fakeThread("t-gh-2", githubTwo),
			"t-gh-3": fakeThread("t-gh-3", githubThree),
			"t-att":  fakeThread("t-att", attachment),
		},
		messages: map[string]string{
			"m-t1":     t1,
			"m-t2":     t2,
			"m-t3":     t3,
			"m-t-gh":   github,
			"m-t-gh-2": githubTwo,
			"m-t-gh-3": githubThree,
			"m-att":    attachment,
		},
		rawMessages: map[string][]byte{
			"m-t1": []byte("From: A <a@example.test>\r\nTo: B <b@example.test>\r\nSubject: PTY smoke\r\n\r\noriginal"),
			"m-t2": []byte("From: A <a@example.test>\r\nTo: B <b@example.test>\r\nSubject: Second PTY smoke\r\n\r\noriginal"),
			"m-t3": []byte("From: Self <work@example.test>\r\nTo: Self <work@example.test>\r\nSubject: self-only\r\n\r\noriginal"),
		},
		drafts:      make(map[string]*fixtureDraft),
		attachments: make(map[string][]byte),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gmail/v1/users/me/threads", g.serveThreadList)
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		fmt.Fprint(w, `{"labels":[]}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		fmt.Fprint(w, `{"emailAddress":"work@example.test"}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/messages/send", g.serveMessageSend)
	mux.HandleFunc("/gmail/v1/users/me/messages/", g.serveMessage)
	mux.HandleFunc("/gmail/v1/users/me/drafts", g.serveDrafts)
	mux.HandleFunc("/gmail/v1/users/me/drafts/send", g.serveDraftsSend)
	mux.HandleFunc("/gmail/v1/users/me/drafts/", g.serveDraft)
	mux.HandleFunc("/gmail/v1/users/me/threads/", g.serveThread)
	mux.HandleFunc("/batch/gmail/v1", g.serveBatch)
	mux.HandleFunc("/", g.serveUnknown)
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		g.record(recordedCall{Method: request.Method, Path: request.URL.Path, Query: request.URL.RawQuery, Bearer: request.Header.Get("Authorization")})
		mux.ServeHTTP(w, request)
	}))
	t.Cleanup(g.server.Close)
	g.token = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"pty-mut-tok","expires_in":3600}`)
	}))
	t.Cleanup(g.token.Close)
	return g
}

func (g *fakeGmail) serveMessageSend(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(w, "invalid send request", http.StatusBadRequest)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
	if err != nil {
		http.Error(w, "invalid raw message", http.StatusBadRequest)
		return
	}
	g.recordSendAuth(request)
	g.recordSend(request, raw, body.ThreadID)

	g.mu.Lock()
	garbage := g.sendGarbage
	status := g.sendStatus
	g.sendGarbage = false
	g.sendStatus = 0
	g.mu.Unlock()
	if garbage {
		fmt.Fprint(w, "not-json")
		return
	}
	if status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"armed send status"}}`, status)
		return
	}
	fmt.Fprint(w, `{"id":"sent-e2e-1","threadId":"t1"}`)
}

func (g *fakeGmail) serveMessage(w http.ResponseWriter, request *http.Request) {
	g.recordReadAuth(request)
	const prefix = "/gmail/v1/users/me/messages/"
	path := strings.TrimPrefix(request.URL.Path, prefix)
	if messageID, attachmentID, found := strings.Cut(path, "/attachments/"); found {
		g.serveAttachment(w, request, messageID, attachmentID)
		return
	}
	if request.URL.Query().Get("format") == "raw" {
		raw, ok := g.rawMessages[path]
		if !ok {
			http.NotFound(w, request)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"threadId":%q,"raw":%q}`, path, strings.TrimPrefix(path, "m-"), base64.RawURLEncoding.EncodeToString(raw))
		return
	}
	message, ok := g.messages[path]
	if !ok {
		http.NotFound(w, request)
		return
	}
	if request.URL.Query().Get("format") == "metadata" {
		message = fixtureMetadataMessage(message, request.URL.Query()["metadataHeaders"])
	}
	fmt.Fprint(w, message)
}

func (g *fakeGmail) serveUnknown(w http.ResponseWriter, request *http.Request) {
	call := recordedCall{Method: request.Method, Path: request.URL.Path, Query: request.URL.RawQuery, Bearer: request.Header.Get("Authorization")}
	g.mu.Lock()
	g.unknown = append(g.unknown, call)
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, `{"error":{"code":500,"message":"unknown fixture endpoint"}}`)
}
