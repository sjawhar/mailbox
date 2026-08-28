// Package auth_test wires internal/auth and internal/gmail together: the
// one-mint-per-batch contract must hold across the real seam, not just fakes.
package auth_test

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
)

type integCountingMinter struct {
	mu    sync.Mutex
	mints int
}

func (m *integCountingMinter) Mint(ctx context.Context, account auth.Account) (auth.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mints++
	return auth.Token{
		AccessToken: fmt.Sprintf("mint-%d", m.mints),
		Route:       auth.RouteMint,
		Expiry:      time.Now().Add(time.Hour),
	}, nil
}

func (m *integCountingMinter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mints
}

// Spec §4/§8 end-to-end: a mutation 401 across a multi-chunk batch produces
// exactly one re-mint and one retry. Real Source, real MutationCredentials,
// real client batch path; only the Minter and the Gmail server are doubles.
func TestMutation401AcrossMultiChunkBatchMintsExactlyOnce(t *testing.T) {
	for _, name := range []string{"MAILBOX_TOKEN", "GWS_WORK_MODIFY_OAUTH"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	var mu sync.Mutex
	batches := 0
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/batch/gmail/v1" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		batches++
		n := batches
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		if n == 2 { // second chunk of the first attempt: token has "expired"
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		integRespondBatchParts(t, w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", server.URL)

	source := auth.NewSource(auth.AccountWork)
	minter := &integCountingMinter{}
	if _, err := source.MutationToken(context.Background(), minter); err != nil {
		t.Fatal(err) // the initial mint (the TUI's first-keypress mint)
	}
	creds := source.MutationCredentials()
	client := gmail.NewClient(gmail.ClientConfig{
		Read:     creds,
		Mutation: creds,
		Account:  string(auth.AccountWork),
	})

	ids := make([]string, 150) // two chunks of 100-part batches
	for i := range ids {
		ids[i] = fmt.Sprintf("%016x", i+1)
	}

	err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"})
	if !errors.Is(err, auth.ErrExpiredToken) {
		t.Fatalf("first attempt error = %v, want ErrExpiredToken", err)
	}
	if got := minter.count(); got != 1 {
		t.Fatalf("mints during the failed attempt = %d, want 1 (the client never mints)", got)
	}

	// The surface's policy (§5): exactly one re-mint, exactly one retry.
	source.InvalidateMutation()
	if _, err := source.MutationToken(context.Background(), minter); err != nil {
		t.Fatal(err)
	}
	if err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"}); err != nil {
		t.Fatalf("retry = %v, want success", err)
	}

	if got := minter.count(); got != 2 {
		t.Fatalf("total mints = %d, want 2 (initial + exactly one re-mint)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if batches != 4 {
		t.Fatalf("batch requests = %d, want 4 (2 per attempt, exactly one retry)", batches)
	}
	if seenAuth[0] != "Bearer mint-1" || seenAuth[1] != "Bearer mint-1" ||
		seenAuth[2] != "Bearer mint-2" || seenAuth[3] != "Bearer mint-2" {
		t.Fatalf("Authorization sequence = %v, want mint-1 ×2 then mint-2 ×2", seenAuth)
	}
}

// integRespondBatchParts answers a Gmail batch request with an HTTP 200 part
// per embedded item, in the wire format parseBatchResponse expects.
func integRespondBatchParts(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Errorf("batch content type: %v", err)
		return
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
			t.Errorf("create part: %v", err)
			return
		}
		fmt.Fprint(part, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{}")
	}
	if err := writer.Close(); err != nil {
		t.Errorf("close batch response: %v", err)
	}
}
