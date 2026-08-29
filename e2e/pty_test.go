// Package e2e drives the real mailbox binary in a real terminal (tmux PTY).
package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func buildMailbox(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mailbox-e2e-*")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "mailbox")
		cmd := exec.Command("go", "build", "-o", buildPath, "github.com/sjawhar/mailbox/cmd/mailbox")
		cmd.Dir = ".."
		if output, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v: %s", err, output)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

func findTmux(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("tmux"); err == nil {
		return path
	}
	path := filepath.Join(os.Getenv("HOME"), ".mise/installs/github-tmux-tmux-builds/3.7b/tmux")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	t.Skip("tmux not available; PTY e2e skipped")
	return ""
}

type fakeGmail struct {
	mu         sync.Mutex
	writeAuths []string
	server     *httptest.Server
	token      *httptest.Server
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	g := &fakeGmail{}
	mux := http.NewServeMux()
	mux.HandleFunc("/gmail/v1/users/me/threads", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"threads":[{"id":"t1","snippet":"hello"}]}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"labels":[]}`)
	})
	body := base64.RawURLEncoding.EncodeToString([]byte("<p>hi</p>"))
	thread := fmt.Sprintf(`{"id":"t1","messages":[{"id":"m1","threadId":"t1","internalDate":"1788000000000","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"text/html","headers":[{"name":"From","value":"A <a@example.test>"},{"name":"To","value":"B <b@example.test>"},{"name":"Subject","value":"PTY smoke"}],"body":{"data":%q}}}]}`, body)
	mux.HandleFunc("/gmail/v1/users/me/threads/", func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/modify") {
			g.mu.Lock()
			g.writeAuths = append(g.writeAuths, request.Header.Get("Authorization"))
			g.mu.Unlock()
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, thread)
	})
	mux.HandleFunc("/batch/gmail/v1", func(w http.ResponseWriter, _ *http.Request) {
		boundary := "e2e-boundary"
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+boundary)
		fmt.Fprintf(w, "--%s\r\nContent-Type: application/http\r\nContent-ID: <response-item0>\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n%s\r\n--%s--\r\n", boundary, thread, boundary)
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	g.token = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"pty-write-token","expires_in":3600}`)
	}))
	t.Cleanup(g.token.Close)
	return g
}

func (g *fakeGmail) recordedWriteAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writeAuths...)
}

type tmuxSession struct {
	t    *testing.T
	tmux string
	name string
}

func newTmuxSession(t *testing.T, env map[string]string, args ...string) *tmuxSession {
	t.Helper()
	session := &tmuxSession{t: t, tmux: findTmux(t), name: fmt.Sprintf("mailbox-e2e-%d", time.Now().UnixNano())}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	words := []string{"env", "-i"}
	for _, key := range keys {
		words = append(words, key+"="+env[key])
	}
	words = append(words, args...)
	quoted := make([]string, len(words))
	for index, word := range words {
		quoted[index] = shellQuote(word)
	}
	run := exec.Command(session.tmux, "new-session", "-d", "-s", session.name, "-x", "160", "-y", "45", strings.Join(quoted, " "))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(session.tmux, "kill-session", "-t", session.name).Run() })
	return session
}

func shellQuote(word string) string { return "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'" }

func (s *tmuxSession) SendKeys(keys string) {
	s.t.Helper()
	if output, err := exec.Command(s.tmux, "send-keys", "-t", s.name, keys).CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send-keys: %v: %s", err, output)
	}
}

func (s *tmuxSession) Capture() string {
	s.t.Helper()
	output, err := exec.Command(s.tmux, "capture-pane", "-p", "-t", s.name).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux capture-pane: %v: %s", err, output)
	}
	return string(output)
}

func (s *tmuxSession) WaitFor(text string, timeout time.Duration) string {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pane := s.Capture()
		if strings.Contains(pane, text) {
			return pane
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %q; last pane:\n%s", text, s.Capture())
	return ""
}

func sourceFingerprint(parts ...string) string {
	preimage := make([]byte, 0)
	for _, part := range parts {
		preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(part)))
		preimage = append(preimage, part...)
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:])[:16]
}

func TestTUIUnlockFlowInRealPTY(t *testing.T) {
	binary := buildMailbox(t)
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	argvFile := filepath.Join(stubs, "approve-argv")
	approve := filepath.Join(stubs, "approve-write")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\t' \"$@\" >> %s\nprintf '\\n' >> %s\n[ \"$1\" = \"--\" ] && shift\nexport MAILBOX_TOKEN_URL=\"$STUB_TOKEN_URL\"\nexec \"$@\"\n", argvFile, argvFile)
	if err := os.WriteFile(approve, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(stubs, "config.toml")
	contents := fmt.Sprintf("default_account = \"work\"\n[accounts.work]\nread_credential_env = \"PTY_READ_OAUTH\"\nwrite_credential_cmd = [\"approve-write\", \"--\", %q, \"__mint\", \"--env\", \"PTY_WRITE_OAUTH\"]\nwrite_interactive = true\ncredential_env_passthrough = [\"PTY_WRITE_OAUTH\", \"STUB_TOKEN_URL\"]\n", binary)
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	fingerprint := sourceFingerprint("work", "read", "env", "PTY_READ_OAUTH")
	cachePath := filepath.Join(cache, "work."+fingerprint+".token.json")
	cacheData := fmt.Sprintf(`{"access_token":"pty-read-token","route":"env","expiry":%q,"fingerprint":%q}`, time.Now().Add(30*time.Minute).Format(time.RFC3339), fingerprint)
	if err := os.WriteFile(cachePath, []byte(cacheData), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"PTY_READ_OAUTH":         "not-used-because-cache",
		"PTY_WRITE_OAUTH":        `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`,
		"STUB_TOKEN_URL":         gmail.token.URL,
	}
	session := newTmuxSession(t, env, binary)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	session.SendKeys("e")
	time.Sleep(500 * time.Millisecond)
	if data, err := os.ReadFile(argvFile); err != nil {
		t.Fatalf("credential command did not start: %v; pane:\n%s", err, session.Capture())
	} else if len(data) == 0 {
		t.Fatalf("credential command recorded no argv; pane:\n%s", session.Capture())
	}
	session.WaitFor("archive completed", 15*time.Second)

	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Fields(strings.TrimSpace(string(data)))
	want := []string{"--", binary, "__mint", "--env", "PTY_WRITE_OAUTH"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("credential command argv = %q, want %q", argv, want)
	}
	auths := gmail.recordedWriteAuths()
	if len(auths) != 1 || auths[0] != "Bearer pty-write-token" {
		t.Fatalf("write authorization = %v", auths)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) {
		t.Fatalf("cache entries = %v, %v", entries, err)
	}
	data, err = os.ReadFile(cachePath)
	if err != nil || string(data) != cacheData {
		t.Fatalf("read cache changed = %q, %v", data, err)
	}
}
