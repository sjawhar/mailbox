package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
