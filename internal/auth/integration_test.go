// Package auth_test wires internal/auth and internal/gmail together: the
// one-unlock-per-batch contract must hold across the real seam.
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

type integCountingAcquirer struct {
	mu    sync.Mutex
	calls int
}

func (a *integCountingAcquirer) Acquire(context.Context, *auth.AccountConfig, auth.Class) (auth.Acquired, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return auth.Acquired{Token: auth.Token{AccessToken: fmt.Sprintf("unlock-%d", a.calls), Route: auth.RouteCmd, Expiry: time.Now().Add(time.Hour)}}, nil
}

func (a *integCountingAcquirer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func TestWrite401AcrossMultiChunkBatchUnlocksExactlyOnce(t *testing.T) {
	os.Unsetenv("MAILBOX_TOKEN")
	acct := &auth.AccountConfig{Name: "work", Read: &auth.CredentialSource{Class: auth.ClassRead, Kind: auth.SourceEnv, EnvVar: "TEST_READ"}, Write: &auth.CredentialSource{Class: auth.ClassWrite, Kind: auth.SourceEnv, EnvVar: "TEST_WRITE"}}
	cfg := &auth.Config{Accounts: []*auth.AccountConfig{acct}, CredentialTimeout: 120 * time.Second}

	var mu sync.Mutex
	batches := 0
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/batch/gmail/v1" {
			t.Errorf("unexpected path %q", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		batches++
		n := batches
		seenAuth = append(seenAuth, request.Header.Get("Authorization"))
		mu.Unlock()
		if n == 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		integRespondBatchParts(t, w, request)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", server.URL)

	source := auth.NewSource(cfg, acct)
	acquirer := &integCountingAcquirer{}
	if _, err := source.WriteToken(context.Background(), acquirer); err != nil {
		t.Fatal(err)
	}
	credentials := source.WriteCredentials()
	client := gmail.NewClient(gmail.ClientConfig{Read: credentials, Write: credentials, Account: acct.Name})

	ids := make([]string, 150)
	for index := range ids {
		ids[index] = fmt.Sprintf("%016x", index+1)
	}

	err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"})
	if !errors.Is(err, auth.ErrExpiredToken) {
		t.Fatalf("first attempt error = %v, want ErrExpiredToken", err)
	}
	if got := acquirer.count(); got != 1 {
		t.Fatalf("unlocks during failed attempt = %d, want 1", got)
	}

	source.InvalidateWrite()
	if _, err := source.WriteToken(context.Background(), acquirer); err != nil {
		t.Fatal(err)
	}
	if err := client.ModifyThreads(context.Background(), ids, nil, []string{"INBOX"}); err != nil {
		t.Fatalf("retry = %v, want success", err)
	}

	if got := acquirer.count(); got != 2 {
		t.Fatalf("total unlocks = %d, want 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if batches != 4 {
		t.Fatalf("batch requests = %d, want 4", batches)
	}
	if seenAuth[0] != "Bearer unlock-1" || seenAuth[1] != "Bearer unlock-1" || seenAuth[2] != "Bearer unlock-2" || seenAuth[3] != "Bearer unlock-2" {
		t.Fatalf("Authorization sequence = %v, want one credential per attempt", seenAuth)
	}
}

func integRespondBatchParts(t *testing.T, w http.ResponseWriter, request *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		t.Errorf("batch content type: %v", err)
		return
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
			t.Errorf("create part: %v", err)
			return
		}
		fmt.Fprint(part, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{}")
	}
	if err := writer.Close(); err != nil {
		t.Errorf("close batch response: %v", err)
	}
}
