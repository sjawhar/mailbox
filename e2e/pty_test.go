// Package e2e drives the real mailbox binary in a real terminal (tmux PTY).
// Tests skip when tmux is unavailable, so CI stays green on bare runners.
package e2e

import (
	"encoding/base64"
	"errors"
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
	mise := filepath.Join(os.Getenv("HOME"), ".mise/installs/github-tmux-tmux-builds/3.7b/tmux")
	if _, err := os.Stat(mise); err == nil {
		return mise
	}
	t.Skip("tmux not available; PTY e2e skipped")
	return ""
}

// fakeGmail serves the minimal Gmail surface the TUI touches (threads list,
// metadata batch, labels, modify) plus an OAuth token endpoint, and records
// mutation Authorization headers.
type fakeGmail struct {
	mu          sync.Mutex
	mutations   []string
	server      *httptest.Server
	tokenServer *httptest.Server
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	g := &fakeGmail{}
	mux := http.NewServeMux()
	mux.HandleFunc("/gmail/v1/users/me/threads", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"threads":[{"id":"t1","snippet":"hello"}]}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"labels":[]}`)
	})
	body := base64.RawURLEncoding.EncodeToString([]byte("<p>hi</p>"))
	thread := fmt.Sprintf(`{"id":"t1","messages":[{"id":"m1","threadId":"t1","internalDate":"1788000000000","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"text/html","headers":[{"name":"From","value":"A <a@example.test>"},{"name":"To","value":"B <b@example.test>"},{"name":"Subject","value":"PTY smoke"}],"body":{"data":%q}}}]}`, body)
	mux.HandleFunc("/gmail/v1/users/me/threads/t1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, thread)
	})
	mux.HandleFunc("/gmail/v1/users/me/threads/t1/modify", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.mutations = append(g.mutations, r.Header.Get("Authorization"))
		g.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/batch/gmail/v1", func(w http.ResponseWriter, r *http.Request) {
		// The single-thread listing batches one metadata GET; answer it.
		boundary := "e2e-boundary"
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+boundary)
		fmt.Fprintf(w, "--%s\r\nContent-Type: application/http\r\nContent-ID: <response-item0>\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n%s\r\n--%s--\r\n", boundary, thread, boundary)
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	g.tokenServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"pty-mut-tok","expires_in":3600}`)
	}))
	t.Cleanup(g.tokenServer.Close)
	return g
}

func (g *fakeGmail) mutationAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.mutations...)
}

// tmuxSession runs the binary inside a detached 160x45 tmux pane with an
// explicit environment and offers SendKeys/WaitFor/Capture.
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
	words := make([]string, 0, len(env)+len(args)+2)
	words = append(words, "env", "-i")
	for _, key := range keys {
		words = append(words, key+"="+env[key])
	}
	words = append(words, args...)
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = shellQuote(word)
	}
	run := exec.Command(session.tmux, "new-session", "-d", "-s", session.name, "-x", "160", "-y", "45", strings.Join(quoted, " "))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command(session.tmux, "kill-session", "-t", session.name).Run() })
	return session
}

func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
}

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

func (s *tmuxSession) WaitFor(substr string, timeout time.Duration) string {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = s.Capture()
		if strings.Contains(last, substr) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %q; last pane:\n%s", substr, last)
	return ""
}

func secretsInvocations(t *testing.T, argvFile string) [][]string {
	t.Helper()
	data, err := os.ReadFile(argvFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	records := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(records) == 1 && records[0] == "" {
		return nil
	}
	invocations := make([][]string, len(records))
	for i, record := range records {
		invocations[i] = strings.Split(strings.TrimSuffix(record, "\t"), "\t")
	}
	return invocations
}

// The whole mint path in a real terminal, no human, no real secretsd: a stub
// `secrets` injects a decoy modify credential and execs the REAL __mint
// child, which refreshes against the fake token endpoint.
func TestTUIMintFlowInRealPTY(t *testing.T) {
	binary := buildMailbox(t)
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	argvFile := filepath.Join(stubs, "secrets-argv")
	oauth := `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\t' \"$@\" >> %s\nprintf '\\n' >> %s\nkey=\"$1\"; shift; [ \"$1\" = \"--\" ] && shift\nvalue='%s'\nexport \"$key=$value\"\nexec \"$@\"\n", argvFile, argvFile, oauth)
	if err := os.WriteFile(filepath.Join(stubs, "secrets"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cacheRoot := t.TempDir()
	cache := filepath.Join(cacheRoot, "cache with spaces")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	// Seed a valid READ token so startup resolves from cache in-process: the
	// token endpoint then serves ONLY the __mint child, and the read/mutation
	// bearer tokens stay distinguishable.
	readCache := fmt.Sprintf(`{"access_token":"pty-read-tok","route":"broker","expiry":%q}`,
		time.Now().Add(30*time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(cache, "work.token.json"), []byte(readCache), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_TOKEN_URL":      gmail.tokenServer.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"MAILBOX_DMI_SYS_VENDOR": "/nonexistent",
	}
	session := newTmuxSession(t, env, binary)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	if got := secretsInvocations(t, argvFile); len(got) != 0 {
		t.Fatalf("secrets invocations before the mutation keypress = %q, want none", got)
	}

	session.SendKeys("e")
	session.WaitFor("archive completed", 15*time.Second)

	got := secretsInvocations(t, argvFile)
	if len(got) != 1 {
		t.Fatalf("secrets invocations after the mutation keypress = %q, want exactly one", got)
	}
	argv := got[0]
	want := []string{"GWS_WORK_MODIFY_OAUTH", "--", binary, "__mint", "--account", "work"}
	if len(argv) != len(want) {
		t.Fatalf("secrets argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("secrets argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}

	session.SendKeys("e")
	time.Sleep(250 * time.Millisecond)
	if got := secretsInvocations(t, argvFile); len(got) != 1 {
		t.Fatalf("secrets invocations after the second mutation keypress = %q, want exactly one", got)
	}
	auths := gmail.mutationAuths()
	if len(auths) != 1 || auths[0] != "Bearer pty-mut-tok" {
		t.Fatalf("mutation Authorization = %v, want the minted token exactly once", auths)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "work.token.json" {
		t.Fatalf("cache entries = %v, want only the seeded read token", entries)
	}
	data, err := os.ReadFile(filepath.Join(cache, "work.token.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != readCache {
		t.Fatalf("cache file changed during the mutation flow: %q", data)
	}
}
