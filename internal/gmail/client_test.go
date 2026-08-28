package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	creds := &fakeCreds{tokens: tokens}
	client := NewClient(creds)
	client.BaseURL = server.URL
	client.HTTP = server.Client()
	return client, creds
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
	client := NewClient(&fakeCreds{tokens: []string{"token"}})
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

var errNeedsMint = errors.New("mutation token expired; a new mint is required")

// fakeMutCreds returns tokens[i] until Invalidate, after which AccessToken
// returns errNeedsMint — the non-minting mutation provider contract.
type fakeMutCreds struct {
	mu          sync.Mutex
	token       string
	invalidated int
}

func (f *fakeMutCreds) AccessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.invalidated > 0 {
		return "", errNeedsMint
	}
	return f.token, nil
}

func (f *fakeMutCreds) Invalidate(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated++
	return nil
}

func TestMutationCallsUseMutationCredentials(t *testing.T) {
	var reads, mutations []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/modify") || strings.HasSuffix(r.URL.Path, "/trash"):
			mutations = append(mutations, auth)
			writeJSON(t, w, http.StatusOK, map[string]any{})
		default:
			reads = append(reads, auth)
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "t1"})
		}
	}, "read-tok")
	client.Mutation = &fakeMutCreds{token: "mut-tok"}
	client.Account = "work"

	if _, err := client.GetThread(context.Background(), "t1", "minimal"); err != nil {
		t.Fatal(err)
	}
	if err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if err := client.TrashThreads(context.Background(), []string{"t1"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range reads {
		if got != "Bearer read-tok" {
			t.Fatalf("read Authorization = %q, want read token", got)
		}
	}
	if len(mutations) != 2 {
		t.Fatalf("mutation requests = %d, want 2", len(mutations))
	}
	for _, got := range mutations {
		if got != "Bearer mut-tok" {
			t.Fatalf("mutation Authorization = %q, want mutation token", got)
		}
	}
}

func TestMutationWithoutMutationCredentialsIsLoud(t *testing.T) {
	requests := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(t, w, http.StatusOK, map[string]any{})
	}, "read-tok")

	err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"})
	if err == nil || !strings.Contains(err.Error(), "mutation") {
		t.Fatalf("ModifyThreads without Mutation = %v, want loud construction error", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0 (no HTTP without mutation credentials)", requests)
	}
}

// F2 client half: a mutation 401 invalidates once and then surfaces the
// provider's error; the client never mints and never silently retries beyond
// the single credential-swap attempt the provider refuses.
func TestMutation401SurfacesProviderErrorSingleRequest(t *testing.T) {
	t.Run("single modify", func(t *testing.T) {
		requests := 0
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusUnauthorized)
		}, "read-tok")
		mut := &fakeMutCreds{token: "stale-tok"}
		client.Mutation = mut
		client.Account = "work"

		err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"})
		if !errors.Is(err, errNeedsMint) {
			t.Fatalf("error = %v, want the provider's sentinel surfaced", err)
		}
		if mut.invalidated != 1 {
			t.Fatalf("Invalidate calls = %d, want 1", mut.invalidated)
		}
		if requests != 1 {
			t.Fatalf("HTTP requests = %d, want 1 (no blind retry)", requests)
		}
	})
	t.Run("multi-chunk batch", func(t *testing.T) {
		batches := 0
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/batch/gmail/v1" {
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
			batches++
			if batches >= 2 { // chunk 1: 200 multipart; chunk 2: 401
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			respondBatchOK(t, w, r)
		}, "read-tok")
		mut := &fakeMutCreds{token: "stale-tok"}
		client.Mutation = mut
		client.Account = "work"

		ids := make([]string, 150) // two chunks of maxBatchParts=100
		for i := range ids {
			ids[i] = fmt.Sprintf("t%03d", i)
		}
		err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"})
		if !errors.Is(err, errNeedsMint) {
			t.Fatalf("error = %v, want provider sentinel", err)
		}
		if mut.invalidated != 1 {
			t.Fatalf("Invalidate calls = %d, want exactly 1 across chunks", mut.invalidated)
		}
		if batches != 2 {
			t.Fatalf("batch requests = %d, want 2 (chunk1 OK, chunk2 401, no re-auth retry)", batches)
		}
	})
}

// respondBatchOK answers a Gmail batch request with HTTP 200 parts for every
// embedded item, mirroring the wire format parseBatchResponse expects.
func respondBatchOK(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("batch content type: %v", err)
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	count := 0
	for {
		if _, err := reader.NextPart(); err != nil {
			break
		}
		count++
	}
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	for i := range count {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/http")
		header.Set("Content-ID", fmt.Sprintf("<response-item%d>", i))
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

func TestMutation403MapsToErrInsufficientScope(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, googleError(http.StatusForbidden, "insufficientPermissions", "Request had insufficient authentication scopes."))
	}, "read-tok")
	client.Mutation = &fakeMutCreds{token: "readonly-tok"}
	client.Account = "personal"

	err := client.TrashThreads(context.Background(), []string{"t1"})
	var scope *ErrInsufficientScope
	if !errors.As(err, &scope) {
		t.Fatalf("error = %v, want ErrInsufficientScope", err)
	}
	if scope.Account != "personal" || scope.Scope != "gmail.modify" {
		t.Fatalf("ErrInsufficientScope = %+v", scope)
	}
	if !IsInsufficientScope(err) {
		t.Fatal("IsInsufficientScope lost through the typed wrapper")
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
