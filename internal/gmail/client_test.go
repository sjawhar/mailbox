package gmail

import (
	"bytes"
	"context"
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
	"strings"
	"sync"
	"testing"
)

type fakeCreds struct {
	mu               sync.Mutex
	tokens           []string
	i                int
	accessTokenCalls int
	accessTokenErrAt int
	accessTokenErr   error
	invalidated      int
}

func (f *fakeCreds) AccessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accessTokenCalls++
	if f.accessTokenErrAt == f.accessTokenCalls {
		return "", f.accessTokenErr
	}
	tok := f.tokens[f.i]
	if f.i < len(f.tokens)-1 {
		f.i++
	}
	return tok, nil
}

func (f *fakeCreds) Invalidate(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated++
	return nil
}

var _ Credentials = (*fakeCreds)(nil)

func newTestClient(t *testing.T, handler http.HandlerFunc, tokens ...string) (*Client, *fakeCreds) {
	t.Helper()

	creds := &fakeCreds{tokens: tokens}
	client := newTestClientWithConfig(t, handler, ClientConfig{
		Read:    creds,
		Write:   creds,
		Account: "test",
	})
	return client, creds
}

func newTestClientWithConfig(t *testing.T, handler http.HandlerFunc, config ClientConfig) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := NewClient(config)
	client.BaseURL = server.URL
	client.HTTP = server.Client()
	return client
}
func TestNewClientCopiesOptionalCredentials(t *testing.T) {
	read := &fakeCreds{tokens: []string{"read-token"}}
	write := &fakeCreds{tokens: []string{"write-token"}}
	send := &fakeCreds{tokens: []string{"send-token"}}

	readOnly := NewClient(ClientConfig{Read: read, Account: "work"})
	if readOnly.read != read || readOnly.write != nil || readOnly.send != nil || readOnly.account != "work" {
		t.Fatalf("read-only client = %+v, want read credentials and account only", readOnly)
	}

	readWrite := NewClient(ClientConfig{Read: read, Write: write, Account: "work"})
	if readWrite.read != read || readWrite.write != write || readWrite.send != nil || readWrite.account != "work" {
		t.Fatalf("read-write client = %+v, want read and write credentials only", readWrite)
	}

	readWriteSend := NewClient(ClientConfig{Read: read, Write: write, Send: send, Account: "work"})
	if readWriteSend.read != read || readWriteSend.write != write || readWriteSend.send != send || readWriteSend.account != "work" {
		t.Fatalf("send-enabled client = %+v, want all credentials and account", readWriteSend)
	}
}

func TestNewClientRejectsInvalidConfiguration(t *testing.T) {
	read := &fakeCreds{tokens: []string{"read-token"}}
	write := &fakeCreds{tokens: []string{"write-token"}}

	for _, config := range []ClientConfig{
		{Account: "work"},
		{Write: write, Account: "work"},
		{Read: read},
		{Read: read, Write: write},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewClient(%+v) did not panic", config)
				}
			}()
			NewClient(config)
		}()
	}
}

func requireRequest(t *testing.T, r *http.Request, method, path, token string) {
	t.Helper()
	if r.Method != method {
		t.Fatalf("method = %q, want %q", r.Method, method)
	}
	if r.URL.Path != path {
		t.Fatalf("path = %q, want %q", r.URL.Path, path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer "+token)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}

func googleError(status int, reason, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"errors":  []map[string]string{{"reason": reason}},
		},
	}
}

func TestListThreads(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/threads", "token")
		query := r.URL.Query()
		if got := query.Get("maxResults"); got != "25" {
			t.Fatalf("maxResults = %q, want 25", got)
		}
		if got := query["labelIds"]; len(got) != 1 || got[0] != "INBOX" {
			t.Fatalf("labelIds = %v, want [INBOX]", got)
		}
		if got := query.Get("q"); got != "is:unread" {
			t.Fatalf("q = %q, want is:unread", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"threads":       []map[string]string{{"id": "t1", "snippet": "s"}},
			"nextPageToken": "npt",
		})
	}, "token")

	threads, err := client.ListThreads(context.Background(), ListOptions{
		Query:      "is:unread",
		LabelIDs:   []string{"INBOX"},
		MaxResults: 25,
	})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if threads.NextPageToken != "npt" || len(threads.Threads) != 1 || threads.Threads[0].ID != "t1" || threads.Threads[0].Snippet != "s" {
		t.Fatalf("ListThreads = %#v, want decoded response", threads)
	}
}

func TestGetThreadFull(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/threads/t1", "token")
		if got := r.URL.Query().Get("format"); got != "full" {
			t.Fatalf("format = %q, want full", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id": "t1",
			"messages": []map[string]any{
				{"id": "m1", "threadId": "t1", "internalDate": "1"},
				{"id": "m2", "threadId": "t1", "internalDate": "2"},
			},
		})
	}, "token")

	thread, err := client.GetThread(context.Background(), "t1", "full")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if thread.ID != "t1" || len(thread.Messages) != 2 || thread.Messages[1].ID != "m2" {
		t.Fatalf("GetThread = %#v, want decoded full thread", thread)
	}
}

func TestGetProfile(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/profile", "token")
		writeJSON(t, w, http.StatusOK, map[string]any{
			"emailAddress":  "user@example.com",
			"messagesTotal": 3,
			"threadsTotal":  2,
		})
	}, "token")

	profile, err := client.GetProfile(context.Background())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.EmailAddress != "user@example.com" {
		t.Fatalf("EmailAddress = %q, want user@example.com", profile.EmailAddress)
	}
}

func TestUnauthorizedRetryOnce(t *testing.T) {
	var authorization []string
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization = append(authorization, r.Header.Get("Authorization"))
		if len(authorization) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"emailAddress": "user@example.com"})
	}, "old", "new")

	profile, err := client.GetProfile(context.Background())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.EmailAddress != "user@example.com" {
		t.Fatalf("EmailAddress = %q, want user@example.com", profile.EmailAddress)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
	if got, want := authorization, []string{"Bearer old", "Bearer new"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Authorization values = %v, want %v", got, want)
	}
}

func TestUnauthorizedTwiceIsLoud(t *testing.T) {
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/profile", "token")
		w.WriteHeader(http.StatusUnauthorized)
	}, "token")

	_, err := client.GetProfile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still unauthorized") {
		t.Fatalf("GetProfile error = %v, want still unauthorized", err)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
}

func TestInsufficientScope(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/profile", "token")
		writeJSON(t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions", "Request had insufficient authentication scopes."))
	}, "token")

	_, err := client.GetProfile(context.Background())
	if !IsInsufficientScope(err) {
		t.Fatalf("IsInsufficientScope(%v) = false, want true", err)
	}
}

func TestResolveThreadIDFallback(t *testing.T) {
	t.Run("message resolves to its thread", func(t *testing.T) {
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/gmail/v1/users/me/threads/x":
				requireRequest(t, r, http.MethodGet, r.URL.Path, "token")
				if got := r.URL.Query().Get("format"); got != "minimal" {
					t.Fatalf("thread format = %q, want minimal", got)
				}
				writeJSON(t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound", "Not Found"))
			case "/gmail/v1/users/me/messages/x":
				requireRequest(t, r, http.MethodGet, r.URL.Path, "token")
				if got := r.URL.Query().Get("format"); got != "minimal" {
					t.Fatalf("message format = %q, want minimal", got)
				}
				writeJSON(t, w, http.StatusOK, map[string]string{"id": "x", "threadId": "tX"})
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
		}, "token")

		threadID, err := client.ResolveThreadID(context.Background(), "x")
		if err != nil {
			t.Fatalf("ResolveThreadID: %v", err)
		}
		if threadID != "tX" {
			t.Fatalf("ResolveThreadID = %q, want tX", threadID)
		}
	})

	t.Run("neither thread nor message exists", func(t *testing.T) {
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			requireRequest(t, r, http.MethodGet, r.URL.Path, "token")
			writeJSON(t, w, http.StatusNotFound, googleError(http.StatusNotFound, "notFound", "Not Found"))
		}, "token")

		_, err := client.ResolveThreadID(context.Background(), "x")
		if err == nil || err.Error() != "no thread or message with id 'x' in account" {
			t.Fatalf("ResolveThreadID error = %v, want not-found error", err)
		}
	})
}

func TestGetAttachment(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/messages/m1/attachments/a1", "token")
		writeJSON(t, w, http.StatusOK, map[string]any{"size": 5, "data": "aGVsbG8"})
	}, "token")

	attachment, err := client.GetAttachment(context.Background(), "m1", "a1")
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if !bytes.Equal(attachment, []byte("hello")) {
		t.Fatalf("GetAttachment = %q, want hello", attachment)
	}
}

func TestListLabels(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/labels", "token")
		writeJSON(t, w, http.StatusOK, map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "Inbox", "type": "system"}}})
	}, "token")

	labels, err := client.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(labels) != 1 || labels[0].ID != "INBOX" {
		t.Fatalf("ListLabels = %#v, want INBOX", labels)
	}
}

func TestListThreadsEncodesRepeatedLabelIDs(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/threads", "token")
		if got, want := r.URL.Query()["labelIds"], []string{"INBOX", "STARRED"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("labelIds = %v, want %v", got, want)
		}
		writeJSON(t, w, http.StatusOK, ThreadList{})
	}, "token")

	_, err := client.ListThreads(context.Background(), ListOptions{LabelIDs: []string{"INBOX", "STARRED"}})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
}

func TestNewClientReadsBaseURLOverride(t *testing.T) {
	t.Setenv("MAILBOX_GMAIL_BASE_URL", "http://gmail.test/")
	client := NewClient(ClientConfig{
		Read:    &fakeCreds{tokens: []string{"token"}},
		Account: "test",
	})
	if client.BaseURL != "http://gmail.test/" {
		t.Fatalf("BaseURL = %q, want environment override", client.BaseURL)
	}
	if client.HTTP == nil {
		t.Fatal("HTTP = nil, want default client")
	}
}

func TestListThreadsUsesValuesVerbatim(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/threads", "token")
		if got := r.URL.Query(); got.Encode() != (url.Values{"q": []string{"from:a+b"}}).Encode() {
			t.Fatalf("query = %q, want q passed through", got.Encode())
		}
		writeJSON(t, w, http.StatusOK, ThreadList{})
	}, "token")

	_, err := client.ListThreads(context.Background(), ListOptions{Query: "from:a+b"})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
}

var errNeedsUnlock = errors.New("write token expired; a new unlock is required")

type fakeWriteCreds struct {
	mu          sync.Mutex
	token       string
	invalidated int
}

func (f *fakeWriteCreds) AccessToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.invalidated > 0 {
		return "", errNeedsUnlock
	}
	return f.token, nil
}

func (f *fakeWriteCreds) Invalidate(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated++
	return nil
}

func TestWriteCallsUseWriteCredentials(t *testing.T) {
	var reads, writes []string
	read := &fakeCreds{tokens: []string{"read-tok"}}
	write := &fakeWriteCreds{token: "write-tok"}
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, request *http.Request) {
		credential := request.Header.Get("Authorization")
		if strings.HasSuffix(request.URL.Path, "/modify") || strings.HasSuffix(request.URL.Path, "/trash") {
			writes = append(writes, credential)
			writeJSON(t, w, http.StatusOK, map[string]any{})
			return
		}
		reads = append(reads, credential)
		writeJSON(t, w, http.StatusOK, map[string]any{"id": "t1"})
	}, ClientConfig{Read: read, Write: write, Account: "work"})

	if _, err := client.GetThread(context.Background(), "t1", "minimal"); err != nil {
		t.Fatal(err)
	}
	if err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := client.TrashThreads(context.Background(), []string{"t1"}); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[0] != "Bearer read-tok" || len(writes) != 2 {
		t.Fatalf("read, write authorization = %v, %v", reads, writes)
	}
	for _, credential := range writes {
		if credential != "Bearer write-tok" {
			t.Fatalf("write Authorization = %q", credential)
		}
	}
}

func TestWrite401SurfacesProviderErrorSingleRequest(t *testing.T) {
	write := &fakeWriteCreds{token: "stale-tok"}
	requests := 0
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-tok"}}, Write: write, Account: "work"})
	err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"})
	if !errors.Is(err, errNeedsUnlock) || write.invalidated != 1 || requests != 1 {
		t.Fatalf("error, invalidations, requests = %v, %d, %d", err, write.invalidated, requests)
	}
}

func TestWrite401AcrossMultiChunkBatchInvalidatesOnce(t *testing.T) {
	batches := 0
	write := &fakeWriteCreds{token: "stale-tok"}
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/batch/gmail/v1" {
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		batches++
		if batches == 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		respondBatchOK(t, w, request)
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-tok"}}, Write: write, Account: "work"})
	ids := make([]string, 150)
	for index := range ids {
		ids[index] = fmt.Sprintf("t%03d", index)
	}
	err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"})
	if !errors.Is(err, errNeedsUnlock) || write.invalidated != 1 || batches != 2 {
		t.Fatalf("error, invalidations, batches = %v, %d, %d", err, write.invalidated, batches)
	}
}

func respondBatchOK(t *testing.T, w http.ResponseWriter, request *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(request.Body, params["boundary"])
	count := 0
	for {
		if _, err := reader.NextPart(); err != nil {
			break
		}
		count++
	}
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	for index := range count {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/http")
		header.Set("Content-ID", fmt.Sprintf("<response-item%d>", index))
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(part, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{}")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWrite403MapsToErrInsufficientScope(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions", "Request had insufficient authentication scopes."))
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-tok"}}, Write: &fakeWriteCreds{token: "readonly-tok"}, Account: "personal"})
	err := client.TrashThreads(context.Background(), []string{"t1"})
	var scope *ErrInsufficientScope
	if !errors.As(err, &scope) || scope.Account != "personal" || scope.Scope != "gmail.modify" || !IsInsufficientScope(err) {
		t.Fatalf("ErrInsufficientScope = %+v, error = %v", scope, err)
	}
}

func TestRead403MapsToErrInsufficientScope(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions", "scopes"))
	}, "read-tok")
	_, err := client.GetProfile(context.Background())
	var scope *ErrInsufficientScope
	if !errors.As(err, &scope) {
		t.Fatalf("read 403 = %v, want ErrInsufficientScope", err)
	}
	if scope.Scope != "gmail.readonly" {
		t.Fatalf("scope = %q, want gmail.readonly", scope.Scope)
	}
}
func TestSendMessagePostsMIMEWithSendCredentials(t *testing.T) {
	raw := []byte("Subject: test\r\n\r\nBody")
	read := &fakeCreds{tokens: []string{"read-token"}}
	send := &fakeCreds{tokens: []string{"send-token"}}
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, request *http.Request) {
		requireRequest(t, request, http.MethodPost, "/gmail/v1/users/me/messages/send", "send-token")
		if got := request.URL.Query(); len(got) != 0 {
			t.Fatalf("query = %q, want none", got.Encode())
		}

		var payload struct {
			Raw      string `json:"raw"`
			ThreadID string `json:"threadId"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if want := base64.RawURLEncoding.EncodeToString(raw); payload.Raw != want {
			t.Fatalf("raw = %q, want %q", payload.Raw, want)
		}
		if payload.ThreadID != "t1" {
			t.Fatalf("threadId = %q, want t1", payload.ThreadID)
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"id": "sent1", "threadId": "t1"})
	}, ClientConfig{Read: read, Send: send, Account: "work"})

	sent, err := client.SendMessage(context.Background(), raw, "t1")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent.ID != "sent1" || sent.ThreadID != "t1" {
		t.Fatalf("sent message = %+v, want id sent1 in thread t1", sent)
	}
}

func TestSendMessageOmitsEmptyThreadID(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, request *http.Request) {
		requireRequest(t, request, http.MethodPost, "/gmail/v1/users/me/messages/send", "send-token")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if bytes.Contains(body, []byte(`"threadId"`)) {
			t.Fatalf("request body = %s, want threadId omitted", body)
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"id": "sent1"})
	}, ClientConfig{
		Read:    &fakeCreds{tokens: []string{"read-token"}},
		Send:    &fakeCreds{tokens: []string{"send-token"}},
		Account: "work",
	})

	sent, err := client.SendMessage(context.Background(), []byte("MIME"), "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent.ID != "sent1" || sent.ThreadID != "" {
		t.Fatalf("sent message = %+v, want id sent1 without thread", sent)
	}
}

func TestSendMessageMapsInsufficientScope(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, request *http.Request) {
		requireRequest(t, request, http.MethodPost, "/gmail/v1/users/me/messages/send", "send-token")
		writeJSON(t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions", "scope"))
	}, ClientConfig{
		Read:    &fakeCreds{tokens: []string{"read-token"}},
		Send:    &fakeCreds{tokens: []string{"send-token"}},
		Account: "work",
	})

	_, err := client.SendMessage(context.Background(), []byte("MIME"), "")
	var scope *ErrInsufficientScope
	if !errors.As(err, &scope) || scope.Scope != "gmail.send" {
		t.Fatalf("SendMessage error = %v, scope = %+v, want gmail.send scope error", err, scope)
	}
}

func TestSendMessageRequiresSendCredentials(t *testing.T) {
	client := NewClient(ClientConfig{
		Read:    &fakeCreds{tokens: []string{"read-token"}},
		Account: "work",
	})

	_, err := client.SendMessage(context.Background(), []byte("MIME"), "")
	if err == nil || err.Error() != "gmail: client has no send credentials" {
		t.Fatalf("SendMessage error = %v, want missing send credentials error", err)
	}
}

func TestGetMessage(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		requireRequest(t, request, http.MethodGet, "/gmail/v1/users/me/messages/m1", "read-token")
		if got := request.URL.Query().Get("format"); got != "metadata" {
			t.Fatalf("format = %q, want metadata", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":       "m1",
			"threadId": "t1",
			"payload": map[string]any{
				"headers": []map[string]string{{"name": "Subject", "value": "Test"}},
			},
		})
	}, "read-token")

	message, err := client.GetMessage(context.Background(), "m1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if message.ID != "m1" || message.ThreadID != "t1" || message.Header("Subject") != "Test" {
		t.Fatalf("message = %+v, want m1 in t1 with metadata headers", message)
	}
}

func TestGetMessageRawAndRawBytes(t *testing.T) {
	raw := []byte("Subject: test\r\n\r\nBody")
	client, _ := newTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		requireRequest(t, request, http.MethodGet, "/gmail/v1/users/me/messages/m1", "read-token")
		if got := request.URL.Query().Get("format"); got != "raw" {
			t.Fatalf("format = %q, want raw", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]string{
			"id":  "m1",
			"raw": base64.RawURLEncoding.EncodeToString(raw),
		})
	}, "read-token")

	message, err := client.GetMessageRaw(context.Background(), "m1")
	if err != nil {
		t.Fatalf("GetMessageRaw: %v", err)
	}
	got, err := message.RawBytes()
	if err != nil {
		t.Fatalf("RawBytes unpadded: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("RawBytes unpadded = %q, want %q", got, raw)
	}

	message.Raw = base64.URLEncoding.EncodeToString(raw)
	got, err = message.RawBytes()
	if err != nil {
		t.Fatalf("RawBytes padded: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("RawBytes padded = %q, want %q", got, raw)
	}
}
