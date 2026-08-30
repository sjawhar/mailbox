// Package e2e drives the real mailbox binary in a real terminal (tmux PTY).
package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

type capturedSend struct {
	Auth     string
	Raw      []byte
	ThreadID string
}

type fakeGmail struct {
	mu          sync.Mutex
	readAuths   []string
	writeAuths  []string
	sendAuths   []string
	sent        []capturedSend
	threads     map[string]string
	messages    map[string]string
	rawMessages map[string][]byte
	server      *httptest.Server
	token       *httptest.Server
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	t1 := fakeMessage("t1", "PTY smoke", "A <a@example.test>", "B <b@example.test>", "C <c@example.test>", "A <a@example.test>")
	t2 := fakeMessage("t2", "Second PTY smoke", "A <a@example.test>", "B <b@example.test>", "", "")
	t3 := fakeMessage("t3", "self-only", "Self <work@example.test>", "Self <work@example.test>", "Self <work@example.test>", "")
	g := &fakeGmail{
		threads: map[string]string{
			"t1": fakeThread("t1", t1),
			"t2": fakeThread("t2", t2),
			"t3": fakeThread("t3", t3),
		},
		messages: map[string]string{
			"m-t1": t1,
			"m-t2": t2,
			"m-t3": t3,
		},
		rawMessages: map[string][]byte{
			"m-t1": []byte("From: A <a@example.test>\r\nTo: B <b@example.test>\r\nSubject: PTY smoke\r\n\r\noriginal"),
			"m-t2": []byte("From: A <a@example.test>\r\nTo: B <b@example.test>\r\nSubject: Second PTY smoke\r\n\r\noriginal"),
			"m-t3": []byte("From: Self <work@example.test>\r\nTo: Self <work@example.test>\r\nSubject: self-only\r\n\r\noriginal"),
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gmail/v1/users/me/threads", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		fmt.Fprint(w, `{"threads":[{"id":"t1","snippet":"hello"},{"id":"t2","snippet":"second"}]}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		fmt.Fprint(w, `{"labels":[]}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		fmt.Fprint(w, `{"emailAddress":"work@example.test"}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/messages/send", func(w http.ResponseWriter, request *http.Request) {
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
		fmt.Fprint(w, `{"id":"sent-e2e-1","threadId":"t1"}`)
	})
	mux.HandleFunc("/gmail/v1/users/me/messages/", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		id := strings.TrimPrefix(request.URL.Path, "/gmail/v1/users/me/messages/")
		if request.URL.Query().Get("format") == "raw" {
			raw, ok := g.rawMessages[id]
			if !ok {
				http.NotFound(w, request)
				return
			}
			fmt.Fprintf(w, `{"id":%q,"threadId":%q,"raw":%q}`, id, strings.TrimPrefix(id, "m-"), base64.RawURLEncoding.EncodeToString(raw))
			return
		}
		message, ok := g.messages[id]
		if !ok {
			http.NotFound(w, request)
			return
		}
		fmt.Fprint(w, message)
	})
	mux.HandleFunc("/gmail/v1/users/me/threads/", func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/modify") {
			g.recordWriteAuth(request)
			fmt.Fprint(w, `{}`)
			return
		}
		g.recordReadAuth(request)
		threadID := strings.TrimPrefix(request.URL.Path, "/gmail/v1/users/me/threads/")
		thread, ok := g.threads[threadID]
		if !ok {
			http.NotFound(w, request)
			return
		}
		fmt.Fprint(w, thread)
	})
	mux.HandleFunc("/batch/gmail/v1", func(w http.ResponseWriter, request *http.Request) {
		g.recordReadAuth(request)
		const boundary = "e2e-boundary"
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+boundary)
		for index, threadID := range []string{"t1", "t2"} {
			fmt.Fprintf(w, "--%s\r\nContent-Type: application/http\r\nContent-ID: <response-item%d>\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n%s\r\n", boundary, index, g.threads[threadID])
		}
		fmt.Fprintf(w, "--%s--\r\n", boundary)
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	g.token = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"pty-mut-tok","expires_in":3600}`)
	}))
	t.Cleanup(g.token.Close)
	return g
}

func fakeThread(id, message string) string {
	return fmt.Sprintf(`{"id":%q,"messages":[%s]}`, id, message)
}

func fakeMessage(threadID, subject, from, to, carbonCopy, replyTo string) string {
	body := base64.RawURLEncoding.EncodeToString([]byte("<p>hi</p>"))
	headers := []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{
		{Name: "From", Value: from},
		{Name: "To", Value: to},
		{Name: "Subject", Value: subject},
		{Name: "Message-ID", Value: "<m-" + threadID + "@example.test>"},
	}
	if carbonCopy != "" {
		headers = append(headers, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "Cc", Value: carbonCopy})
	}
	if replyTo != "" {
		headers = append(headers, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "Reply-To", Value: replyTo})
	}
	headerJSON, err := json.Marshal(headers)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"id":%q,"threadId":%q,"internalDate":"1788000000000","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"text/html","headers":%s,"body":{"data":%q}}}`, "m-"+threadID, threadID, headerJSON, body)
}

func (g *fakeGmail) recordReadAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readAuths = append(g.readAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordWriteAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writeAuths = append(g.writeAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordSendAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sendAuths = append(g.sendAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordSend(request *http.Request, raw []byte, threadID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, capturedSend{Auth: request.Header.Get("Authorization"), Raw: append([]byte(nil), raw...), ThreadID: threadID})
}

func (g *fakeGmail) recordedReadAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.readAuths...)
}

func (g *fakeGmail) recordedWriteAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writeAuths...)
}

func (g *fakeGmail) recordedSendAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.sendAuths...)
}

func (g *fakeGmail) recordedSends() []capturedSend {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]capturedSend(nil), g.sent...)
}

type tmuxSession struct {
	t      *testing.T
	tmux   string
	socket string
	name   string
}

// cmd runs tmux against a test-owned absolute socket path, keeping e2e
// sessions off whatever server (and TMUX_TMPDIR) the developer's own
// terminal happens to use — and letting env-scrubbed credential stubs
// address the same server unambiguously.
func (s *tmuxSession) cmd(args ...string) *exec.Cmd {
	return exec.Command(s.tmux, append([]string{"-S", s.socket}, args...)...)
}

func newTmuxSession(t *testing.T, env map[string]string, args ...string) *tmuxSession {
	t.Helper()
	session := &tmuxSession{t: t, tmux: findTmux(t), socket: filepath.Join(t.TempDir(), "tmux.sock"), name: fmt.Sprintf("mailbox-e2e-%d", time.Now().UnixNano())}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	words := []string{"env", "-i"}
	for _, key := range keys {
		words = append(words, key+"="+env[key])
	}
	// Session plumbing for credential stubs that capture their own pane (via
	// credential_env_passthrough): the pane command runs under `env -i`, so
	// tmux's own TMUX/TMUX_PANE never survive — name the target explicitly.
	words = append(words,
		"PTY_TMUX_BIN="+session.tmux,
		"PTY_TMUX_SOCKET="+session.socket,
		"PTY_TMUX_SESSION="+session.name,
	)
	words = append(words, args...)
	quoted := make([]string, len(words))
	for index, word := range words {
		quoted[index] = shellQuote(word)
	}
	run := session.cmd("new-session", "-d", "-s", session.name, "-x", "160", "-y", "45", strings.Join(quoted, " "))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = session.cmd("kill-session", "-t", session.name).Run() })
	return session
}

func shellQuote(word string) string { return "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'" }

func (s *tmuxSession) SendKeys(keys string) {
	s.t.Helper()
	if output, err := s.cmd("send-keys", "-l", "-t", s.name, keys).CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send-keys: %v: %s", err, output)
	}
}

func (s *tmuxSession) SendEnter() {
	s.t.Helper()
	if output, err := s.cmd("send-keys", "-t", s.name, "Enter").CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send Enter: %v: %s", err, output)
	}
}

func (s *tmuxSession) SendCtrl(key string) {
	s.t.Helper()
	if output, err := s.cmd("send-keys", "-t", s.name, "C-"+key).CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send Ctrl-%s: %v: %s", key, err, output)
	}
}

func (s *tmuxSession) Capture() string {
	s.t.Helper()
	output, err := s.cmd("capture-pane", "-p", "-t", s.name).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux capture-pane: %v: %s", err, output)
	}
	return string(output)
}

func (s *tmuxSession) findText(text string, timeout time.Duration) (string, bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var pane string
	for time.Now().Before(deadline) {
		pane = s.Capture()
		if strings.Contains(pane, text) {
			return pane, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.Capture(), false
}

func (s *tmuxSession) WaitFor(text string, timeout time.Duration) string {
	s.t.Helper()
	pane, found := s.findText(text, timeout)
	if !found {
		s.t.Fatalf("timed out waiting for %q; last pane:\n%s", text, pane)
	}
	return pane
}

func (s *tmuxSession) Alive() bool {
	s.t.Helper()
	return s.cmd("has-session", "-t", s.name).Run() == nil
}

func (s *tmuxSession) WaitForExit(timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.Alive() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("tmux session %s did not exit within %s", s.name, timeout)
}

// writeE2EConfig writes a trust-check-passing config into dir and returns its path.
func writeE2EConfig(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// shortTempDir creates an isolated path that keeps terminal status assertions
// visible within the fixed PTY width.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove temporary directory %s: %v", dir, err)
		}
	})
	return dir
}

// runBinary executes the built mailbox on the batch surface with only env.
func runBinary(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(buildMailbox(t), args...)
	command.Env = environment(env)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("run mailbox %q: %v", args, err)
	return -1, "", ""
}

func environment(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values
}

func withEnvironment(env map[string]string, additions map[string]string) map[string]string {
	result := make(map[string]string, len(env)+len(additions))
	for key, value := range env {
		result[key] = value
	}
	for key, value := range additions {
		result[key] = value
	}
	return result
}

// seedReadCache runs the shipped binary against the configured non-interactive
// read source, then returns the cache entry that the binary itself created.
func seedReadCache(t *testing.T, env map[string]string) (string, []byte) {
	t.Helper()
	code, stdout, stderr := runBinary(t, env, "status", "--json")
	if code != 0 {
		t.Fatalf("seed read cache: status exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	cacheDir := env["MAILBOX_CACHE_DIR"]
	if cacheDir == "" {
		t.Fatal("seed read cache requires MAILBOX_CACHE_DIR")
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var cacheFiles []os.DirEntry
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".token.json") {
			cacheFiles = append(cacheFiles, entry)
		}
	}
	if len(cacheFiles) != 1 {
		t.Fatalf("black-box seed cache entries = %v, want exactly one token entry", entries)
	}
	path := filepath.Join(cacheDir, cacheFiles[0].Name())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

func writeExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func waitForFileLines(t *testing.T, path string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if lines := fileLines(t, path); len(lines) > 0 {
			return lines
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

func assertNoSpawns(t *testing.T, path string) {
	t.Helper()
	if lines := fileLines(t, path); len(lines) != 0 {
		t.Fatalf("unexpected credential spawns in %s: %q", path, lines)
	}
}

// assertSpawnPaneContains proves attribution-before-spawn without racing the
// render fence: the credential stub captures its own pane as the FIRST thing it
// does, so if the attribution line is in that capture, it was painted strictly
// before the helper ran. Polling the pane from the test races the ~50ms fence
// under load; this cannot.
func assertSpawnPaneContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spawn-time pane capture %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("attribution %q missing from spawn-time pane capture %s:\n%s", want, path, data)
	}
}

func (g *fakeGmail) waitForWriteAuths(t *testing.T, count int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		auths := g.recordedWriteAuths()
		if len(auths) >= count {
			return auths
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d write requests; got %v", count, g.recordedWriteAuths())
	return nil
}

const testAuthorizedUser = `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`

func TestTUIUnlockFlowInRealPTY(t *testing.T) {
	binary := buildMailbox(t)
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	argvFile := filepath.Join(stubs, "approve-argv")
	stdinFile := filepath.Join(stubs, "approve-stdin")
	spawnPaneFile := filepath.Join(stubs, "approve-pane")
	approve := filepath.Join(stubs, "approve-write")
	writeExecutable(t, approve, fmt.Sprintf(`#!/bin/sh
"$PTY_TMUX_BIN" -S "$PTY_TMUX_SOCKET" capture-pane -p -t "$PTY_TMUX_SESSION" > %q 2>&1
printf '%%s\t' "$@" >> %q
printf '\n' >> %q
if [ -t 0 ]; then
  printf 'tty\n' > %q
else
  printf 'not-tty\n' > %q
fi
sleep 2
[ "$1" = "--" ] && shift
export PTY_MODIFY_OAUTH=%q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$@"
`, spawnPaneFile, argvFile, argvFile, stdinFile, stdinFile, testAuthorizedUser))

	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["approve-write", "--", %q, "__mint", "--env", "PTY_MODIFY_OAUTH"]
write_interactive = true
write_label = "PTY approval"
credential_env_passthrough = ["STUB_TOKEN_URL", "PTY_TMUX_BIN", "PTY_TMUX_SOCKET", "PTY_TMUX_SESSION"]
`, binary))
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"STUB_TOKEN_URL":         gmail.token.URL,
	}
	cachePath, cacheData := seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	session := newTmuxSession(t, env, binary)
	cleanupCredentialHelper(t, session, approve)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	assertNoSpawns(t, argvFile)

	session.SendKeys("e")
	attribution := "waiting for PTY approval; approve only this request — work write access via " + approve
	session.WaitFor(attribution, 5*time.Second)
	session.WaitFor("archive completed", 15*time.Second)
	assertSpawnPaneContains(t, spawnPaneFile, attribution)

	lines := waitForFileLines(t, argvFile, time.Second)
	if len(lines) != 1 {
		t.Fatalf("credential command spawns = %q, want one", lines)
	}
	if got := fileLines(t, stdinFile); len(got) != 1 || got[0] != "tty" {
		t.Fatalf("credential command stdin = %q, want [tty]", got)
	}
	gotArgv := strings.Fields(lines[0])
	wantArgv := []string{"--", binary, "__mint", "--env", "PTY_MODIFY_OAUTH"}
	if strings.Join(gotArgv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("credential command argv = %q, want %q", gotArgv, wantArgv)
	}
	auths := gmail.waitForWriteAuths(t, 1, time.Second)
	if len(auths) != 1 || auths[0] != "Bearer pty-mut-tok" {
		t.Fatalf("first write authorization = %v, want [Bearer pty-mut-tok]", auths)
	}

	session.SendKeys("e")
	auths = gmail.waitForWriteAuths(t, 2, 5*time.Second)
	if len(auths) != 2 || auths[0] != "Bearer pty-mut-tok" || auths[1] != "Bearer pty-mut-tok" {
		t.Fatalf("write authorizations = %v, want two Bearer pty-mut-tok requests", auths)
	}
	if lines := fileLines(t, argvFile); len(lines) != 1 {
		t.Fatalf("second write re-spawned credential helper: %q", lines)
	}

	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(cachePath) {
		t.Fatalf("cache entries = %v, %v", entries, err)
	}
	data, err := os.ReadFile(cachePath)
	if err != nil || !bytes.Equal(data, cacheData) {
		t.Fatalf("read cache changed = %q, %v", data, err)
	}
}

func startSlowWriteUnlock(t *testing.T, binary string, timeoutSeconds int) (*tmuxSession, string, string) {
	t.Helper()
	gmail := newFakeGmail(t)
	stubs := shortTempDir(t)
	started := filepath.Join(stubs, "approve-started")
	approve := filepath.Join(stubs, "approve-write")
	writeExecutable(t, approve, fmt.Sprintf("#!/bin/sh\nprintf 'started\\n' > %q\nsleep 10\n", started))
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
credential_timeout_secs = %d
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["approve-write"]
write_interactive = true
write_label = "Q"
`, timeoutSeconds))
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	session := newTmuxSession(t, env, binary)
	cleanupCredentialHelper(t, session, approve)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	return session, config, approve
}

func stopCredentialSession(session *tmuxSession, helper string) {
	if session.Alive() {
		_ = session.cmd("send-keys", "-l", "-t", session.name, "q").Run()
		_ = session.cmd("send-keys", "-l", "-t", session.name, "q").Run()
		deadline := time.Now().Add(time.Second)
		for session.Alive() && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if session.Alive() {
			_ = session.cmd("kill-session", "-t", session.name).Run()
		}
	}
	killCredentialProcessGroup(session.t, helper)
}

func cleanupCredentialHelper(t *testing.T, session *tmuxSession, helper string) {
	t.Helper()
	t.Cleanup(func() {
		stopCredentialSession(session, helper)
	})
}

func killCredentialProcessGroup(t *testing.T, helper string) {
	t.Helper()
	output, err := exec.Command("pgrep", "-f", helper).CombinedOutput()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return
	}
	if err != nil {
		t.Errorf("pgrep -f %q: %v: %s", helper, err, output)
		return
	}
	for _, field := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Errorf("parse credential helper pid %q: %v", field, err)
			continue
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("kill credential helper process group %d: %v", pid, err)
		}
	}
}

func waitForProcessGone(t *testing.T, pattern string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := exec.Command("pgrep", "-f", pattern).CombinedOutput()
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return
		}
		if err != nil {
			t.Fatalf("pgrep -f %q: %v: %s", pattern, err, output)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("credential helper %q is still running", pattern)
}

func TestTUIQuitDeflectsThenTimeoutForceAbandons(t *testing.T) {
	binary := buildMailbox(t)
	t.Run("first quit deflects an active unlock", func(t *testing.T) {
		session, _, approve := startSlowWriteUnlock(t, binary, 30)
		session.SendKeys("e")
		waitForFileLines(t, filepath.Join(filepath.Dir(approve), "approve-started"), 5*time.Second)
		time.Sleep(200 * time.Millisecond)
		writeAttributionPane, writeAttributionVisible := session.findText("work write access via", 2*time.Second)
		if !writeAttributionVisible {
			t.Fatalf("write attribution missing before first quit; pane:\n%s", writeAttributionPane)
		}
		session.SendKeys("q")
		firstQuitPane, found := session.findText("waiting for unlock", 2*time.Second)
		if !found || !strings.Contains(firstQuitPane, "press again to abandon") {
			t.Fatalf("first quit did not render an abandon instruction; write attribution visible=%t; write line=%q; quit line=%q", writeAttributionVisible, lineContaining(writeAttributionPane, "work write access via"), lineContaining(firstQuitPane, "waiting for unlock"))
		}
		if !session.Alive() {
			t.Fatal("quit exited while credential unlock was still active")
		}
	})

	t.Run("timeout releases a deflected quit", func(t *testing.T) {
		session, _, approve := startSlowWriteUnlock(t, binary, 2)
		session.SendKeys("e")
		waitForFileLines(t, filepath.Join(filepath.Dir(approve), "approve-started"), 5*time.Second)
		time.Sleep(200 * time.Millisecond)
		session.SendKeys("q")
		pane := session.WaitFor("credential command timed out", 5*time.Second)
		if !strings.Contains(pane, "accounts.work.write_credential_cmd") {
			t.Fatalf("timeout status = %q, want sanitized config key", pane)
		}
		session.SendKeys("q")
		session.WaitForExit(2 * time.Second)
	})

	t.Run("second quit force-abandons the helper process group", func(t *testing.T) {
		session, _, approve := startSlowWriteUnlock(t, binary, 30)
		session.SendKeys("e")
		waitForFileLines(t, filepath.Join(filepath.Dir(approve), "approve-started"), 5*time.Second)
		started := time.Now()
		session.SendKeys("q")
		session.SendKeys("q")
		session.WaitForExit(2 * time.Second)
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("force-abandon took %s, want under 2s", elapsed)
		}
		waitForProcessGone(t, approve, 2*time.Second)
	})
}

func TestStatusSmokeAgainstFakeGmail(t *testing.T) {
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	approve := filepath.Join(stubs, "approve-write")
	writeExecutable(t, approve, "#!/bin/sh\nexit 99\n")
	config := writeE2EConfig(t, stubs, `default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["approve-write"]
write_label = "PTY approval"
`)
	cache := t.TempDir()
	env := map[string]string{
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"PTY_MODIFY_OAUTH":       testAuthorizedUser,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))

	code, stdout, stderr := runBinary(t, env, "status", "--json")
	if code != 0 {
		t.Fatalf("status exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var status struct {
		OK       bool `json:"ok"`
		Accounts []struct {
			Name string `json:"name"`
			Read struct {
				Kind string `json:"kind"`
			} `json:"read"`
			Write struct {
				Argv0 string `json:"argv0"`
			} `json:"write"`
			Profile struct {
				Email string `json:"email"`
			} `json:"profile"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode status JSON: %v: %q", err, stdout)
	}
	if !status.OK || len(status.Accounts) != 1 {
		t.Fatalf("status = %#v, want one successful account", status)
	}
	account := status.Accounts[0]
	if account.Name != "work" || account.Read.Kind != "env" || account.Write.Argv0 != approve || account.Profile.Email != "work@example.test" {
		t.Fatalf("status account = %#v", account)
	}
	if strings.Contains(stdout, testAuthorizedUser) || strings.Contains(stdout, "client_secret") {
		t.Fatalf("status leaked credential material: %q", stdout)
	}
}

func TestEnvelopeSmokeNoWriteCredential(t *testing.T) {
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	approve := filepath.Join(stubs, "unreachable-approve-write")
	writeExecutable(t, approve, "#!/bin/sh\nexit 99\n")
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = [%q]
`, approve))
	cache := t.TempDir()
	env := map[string]string{
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))

	code, stdout, stderr := runBinary(t, env, "archive", "t1", "--json")
	if code != 1 {
		t.Fatalf("archive exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			ConfigKey string `json:"config_key"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode archive envelope: %v: %q", err, stdout)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("archive stdout must contain exactly one envelope: %q (extra decode: %v)", stdout, err)
	}
	if envelope.Error.Code != "needs_write_credential" || envelope.Error.ConfigKey != "accounts.work.write_credential_cmd" {
		t.Fatalf("archive envelope = %#v", envelope)
	}
}

func TestTokenURLDecoyNeverReachesChildren(t *testing.T) {
	binary := buildMailbox(t)
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	envFile := filepath.Join(stubs, "approve-environment")
	approve := filepath.Join(stubs, "approve-write")
	writeExecutable(t, approve, fmt.Sprintf(`#!/bin/sh
env > %q
sleep 2
[ "$1" = "--" ] && shift
export PTY_MODIFY_OAUTH=%q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$@"
`, envFile, testAuthorizedUser))
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["approve-write", "--", %q, "__mint", "--env", "PTY_MODIFY_OAUTH"]
write_label = "PTY approval"
credential_env_passthrough = ["STUB_TOKEN_URL"]
`, binary))
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"MAILBOX_TOKEN_URL":      "http://127.0.0.1:1/decoy",
		"STUB_TOKEN_URL":         gmail.token.URL,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	session := newTmuxSession(t, env, binary)
	cleanupCredentialHelper(t, session, approve)
	session.WaitFor("PTY smoke", 15*time.Second)
	session.SendKeys("e")
	session.WaitFor("waiting for PTY approval", 5*time.Second)
	session.WaitFor("archive completed", 15*time.Second)

	childEnv := waitForFileLines(t, envFile, time.Second)
	for _, line := range childEnv {
		if strings.HasPrefix(line, "MAILBOX_TOKEN_URL=") {
			t.Fatalf("credential child inherited MAILBOX_TOKEN_URL: %q", childEnv)
		}
	}
	if !containsLine(childEnv, "STUB_TOKEN_URL="+gmail.token.URL) {
		t.Fatalf("credential child lost declared passthrough: %q", childEnv)
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func lineContaining(pane, text string) string {
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

func TestTUIInteractiveReadUnlockInRealPTY(t *testing.T) {

	binary := buildMailbox(t)
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	readArgv := filepath.Join(stubs, "approve-read-argv")
	writeArgv := filepath.Join(stubs, "approve-write-argv")
	readPaneFile := filepath.Join(stubs, "approve-read-pane")
	readApprove := filepath.Join(stubs, "approve-read")
	writeApprove := filepath.Join(stubs, "approve-write")
	writeExecutable(t, readApprove, fmt.Sprintf(`#!/bin/sh
"$PTY_TMUX_BIN" -S "$PTY_TMUX_SOCKET" capture-pane -p -t "$PTY_TMUX_SESSION" > %q 2>&1
printf '%%s\t' "$@" >> %q
printf '\n' >> %q
sleep 10
[ "$1" = "--" ] && shift
export PTY_READ_OAUTH=%q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$@"
`, readPaneFile, readArgv, readArgv, testAuthorizedUser))
	writeExecutable(t, writeApprove, fmt.Sprintf("#!/bin/sh\nprintf 'spawned\\n' >> %q\nexit 99\n", writeArgv))
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_cmd = ["approve-read", "--", %q, "__mint", "--env", "PTY_READ_OAUTH"]
read_interactive = true
write_credential_cmd = ["approve-write"]
credential_env_passthrough = ["STUB_TOKEN_URL", "PTY_TMUX_BIN", "PTY_TMUX_SOCKET", "PTY_TMUX_SESSION"]
`, binary))
	cache := t.TempDir()
	session := newTmuxSession(t, map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"STUB_TOKEN_URL":         gmail.token.URL,
	}, binary)
	cleanupCredentialHelper(t, session, readApprove)
	pane, found := session.findText("work read access via", 5*time.Second)
	if !found || !strings.Contains(pane, filepath.Base(readApprove)) {
		t.Fatalf("interactive-read attribution missing; helper spawns=%q; Gmail authorizations=%q; pane:\n%s", fileLines(t, readArgv), gmail.recordedReadAuths(), pane)
	}
	assertNoSpawns(t, writeArgv)
	session.WaitFor("PTY smoke", 15*time.Second)
	assertSpawnPaneContains(t, readPaneFile, "work read access via")
	readAuths := gmail.recordedReadAuths()
	if len(readAuths) == 0 {
		t.Fatal("interactive read unlock reached the inbox without a Gmail request")
	}
	for _, authorization := range readAuths {
		if authorization != "Bearer pty-mut-tok" {
			t.Fatalf("interactive read Gmail authorization = %q, want Bearer pty-mut-tok", authorization)
		}
	}
	lines := waitForFileLines(t, readArgv, time.Second)
	if len(lines) != 1 {
		t.Fatalf("read helper spawns = %q, want one", lines)
	}
	gotArgv := strings.Fields(lines[0])
	wantArgv := []string{"--", binary, "__mint", "--env", "PTY_READ_OAUTH"}
	if strings.Join(gotArgv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("read helper argv = %q, want %q", gotArgv, wantArgv)
	}
	assertNoSpawns(t, writeArgv)
}

func TestCLIRoutesCmdSourceEndToEnd(t *testing.T) {
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	spawns := filepath.Join(stubs, "token-helper-spawns")
	writeExecutable(t, filepath.Join(stubs, "token-helper"), fmt.Sprintf(`#!/bin/sh
printf 'spawned\n' >> %q
printf '%%s\n' '{"access_token":"cmd-read-tok","expiry":"2099-01-01T00:00:00Z"}'
`, spawns))
	config := writeE2EConfig(t, stubs, `default_account = "work"
[accounts.work]
read_credential_cmd = ["token-helper"]
`)
	cache := t.TempDir()
	env := map[string]string{
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
	}

	code, stdout, stderr := runBinary(t, env, "inbox", "--json")
	if code != 0 {
		t.Fatalf("first inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertInboxThreads(t, stdout)
	if lines := fileLines(t, spawns); len(lines) != 1 {
		t.Fatalf("command credential spawns = %q, want one", lines)
	}
	for _, auth := range gmail.recordedReadAuths() {
		if auth != "Bearer cmd-read-tok" {
			t.Fatalf("command source Gmail authorization = %q, want Bearer cmd-read-tok", auth)
		}
	}

	code, stdout, stderr = runBinary(t, env, "inbox", "--json")
	if code != 0 {
		t.Fatalf("cached inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertInboxThreads(t, stdout)
	if lines := fileLines(t, spawns); len(lines) != 1 {
		t.Fatalf("cache hit re-ran command credential helper: %q", lines)
	}
	t.Run("agent pipe keeps inner mint JSON", func(t *testing.T) {
		gmail := newFakeGmail(t)
		stubs := t.TempDir()
		spawns := filepath.Join(stubs, "mint-helper-spawns")
		binary := buildMailbox(t)
		writeExecutable(t, filepath.Join(stubs, "mint-helper"), fmt.Sprintf(`#!/bin/sh
printf 'spawned\n' >> %q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$TEST_BINARY" __mint --env MINT_READ_OAUTH
`, spawns))
		config := writeE2EConfig(t, stubs, `default_account = "work"
[accounts.work]
read_credential_cmd = ["mint-helper"]
credential_env_passthrough = ["AGENT", "MINT_READ_OAUTH", "STUB_TOKEN_URL", "TEST_BINARY"]
`)
		cache := t.TempDir()
		env := map[string]string{
			"PATH":                   stubs + ":/usr/bin:/bin",
			"MAILBOX_CONFIG":         config,
			"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
			"MAILBOX_CACHE_DIR":      cache,
			"AGENT":                  "1",
			"MINT_READ_OAUTH":        testAuthorizedUser,
			"STUB_TOKEN_URL":         gmail.token.URL,
			"TEST_BINARY":            binary,
		}

		code, stdout, stderr := runBinary(t, env, "inbox")
		if code != 0 {
			t.Fatalf("agent inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		assertTOONInbox(t, stdout)
		if lines := fileLines(t, spawns); len(lines) != 1 || lines[0] != "spawned" {
			t.Fatalf("mint command spawns = %q, want [spawned]", lines)
		}
	})
}

func TestCLIRoutesEnvSourceEndToEnd(t *testing.T) {
	gmail := newFakeGmail(t)
	configDir := t.TempDir()
	config := writeE2EConfig(t, configDir, `default_account = "work"
[accounts.work]
read_credential_env = "SMOKE_READ_OAUTH"
`)
	cache := t.TempDir()

	code, stdout, stderr := runBinary(t, map[string]string{
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"MAILBOX_TOKEN_URL":      gmail.token.URL,
		"SMOKE_READ_OAUTH":       testAuthorizedUser,
	}, "inbox", "--json")
	if code != 0 {
		t.Fatalf("env inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertInboxThreads(t, stdout)
	for _, auth := range gmail.recordedReadAuths() {
		if auth != "Bearer pty-mut-tok" {
			t.Fatalf("env source Gmail authorization = %q, want Bearer pty-mut-tok", auth)
		}
	}
}

func assertInboxThreads(t *testing.T, stdout string) {
	t.Helper()
	var inbox struct {
		Account string `json:"account"`
		Threads []struct {
			ID string `json:"id"`
		} `json:"threads"`
	}
	if err := json.Unmarshal([]byte(stdout), &inbox); err != nil {
		t.Fatalf("decode inbox JSON: %v: %q", err, stdout)
	}
	if inbox.Account != "work" || len(inbox.Threads) != 2 || inbox.Threads[0].ID != "t1" || inbox.Threads[1].ID != "t2" {
		t.Fatalf("inbox = %#v, want work t1,t2", inbox)
	}
}
