// Package e2e drives the real mailbox binary in a real terminal (tmux PTY).
package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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

type fakeGmail struct {
	mu         sync.Mutex
	readAuths  []string
	writeAuths []string
	threads    map[string]string
	server     *httptest.Server
	token      *httptest.Server
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	g := &fakeGmail{
		threads: map[string]string{
			"t1": fakeThread("t1", "PTY smoke"),
			"t2": fakeThread("t2", "Second PTY smoke"),
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

func fakeThread(id, subject string) string {
	body := base64.RawURLEncoding.EncodeToString([]byte("<p>hi</p>"))
	return fmt.Sprintf(`{"id":%q,"messages":[{"id":%q,"threadId":%q,"internalDate":"1788000000000","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"text/html","headers":[{"name":"From","value":"A <a@example.test>"},{"name":"To","value":"B <b@example.test>"},{"name":"Subject","value":%q}],"body":{"data":%q}}}]}`, id, "m-"+id, id, subject, body)
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
	if output, err := exec.Command(s.tmux, "send-keys", "-l", "-t", s.name, keys).CombinedOutput(); err != nil {
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

func (s *tmuxSession) findText(text string, timeout time.Duration) (string, bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	var pane string
	for time.Now().Before(deadline) {
		pane = s.Capture()
		if strings.Contains(pane, text) {
			return pane, true
		}
		time.Sleep(100 * time.Millisecond)
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
	return exec.Command(s.tmux, "has-session", "-t", s.name).Run() == nil
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

// fingerprintFor must track internal/auth/cache.go's unexported
// sourceFingerprint. Length-prefix framing avoids source-identity collisions
// involving embedded NUL bytes.
func fingerprintFor(account, class, kind string, identity ...string) string {
	parts := append([]string{account, class, kind}, identity...)
	preimageLen := 8 * len(parts)
	for _, part := range parts {
		preimageLen += len(part)
	}
	preimage := make([]byte, 0, preimageLen)
	for _, part := range parts {
		preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(part)))
		preimage = append(preimage, part...)
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:])[:16]
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

func writeExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func seedCachedToken(t *testing.T, dir, account, class, kind string, identity []string, accessToken string) (string, []byte) {
	t.Helper()
	fingerprint := fingerprintFor(account, class, kind, identity...)
	path := filepath.Join(dir, account+"."+fingerprint+".token.json")
	data := []byte(fmt.Sprintf(`{"access_token":%q,"route":%q,"expiry":%q,"fingerprint":%q}`, accessToken, kind, time.Now().Add(30*time.Minute).Format(time.RFC3339), fingerprint))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
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
	approve := filepath.Join(stubs, "approve-write")
	writeExecutable(t, approve, fmt.Sprintf(`#!/bin/sh
printf '%%s\t' "$@" >> %q
printf '\n' >> %q
sleep 2
[ "$1" = "--" ] && shift
export PTY_MODIFY_OAUTH=%q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$@"
`, argvFile, argvFile, testAuthorizedUser))

	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["approve-write", "--", %q, "__mint", "--env", "PTY_MODIFY_OAUTH"]
write_interactive = true
write_label = "PTY approval"
credential_env_passthrough = ["STUB_TOKEN_URL"]
`, binary))
	cache := t.TempDir()
	cachePath, cacheData := seedCachedToken(t, cache, "work", "read", "env", []string{"PTY_READ_OAUTH"}, "pty-read-tok")
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"STUB_TOKEN_URL":         gmail.token.URL,
	}
	session := newTmuxSession(t, env, binary)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	assertNoSpawns(t, argvFile)

	session.SendKeys("e")
	attribution := "waiting for PTY approval; approve only this request — work write access via " + approve
	session.WaitFor(attribution, 5*time.Second)
	session.WaitFor("archive completed", 15*time.Second)

	lines := waitForFileLines(t, argvFile, time.Second)
	if len(lines) != 1 {
		t.Fatalf("credential command spawns = %q, want one", lines)
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
	seedCachedToken(t, cache, "work", "read", "env", []string{"PTY_READ_OAUTH"}, "pty-read-tok")
	session := newTmuxSession(t, map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
	}, binary)
	t.Cleanup(func() {
		stopCredentialSession(session, approve)
	})
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	return session, config, approve
}

func stopCredentialSession(session *tmuxSession, helper string) {
	if session.Alive() {
		_ = exec.Command(session.tmux, "send-keys", "-l", "-t", session.name, "q").Run()
		_ = exec.Command(session.tmux, "send-keys", "-l", "-t", session.name, "q").Run()
		deadline := time.Now().Add(time.Second)
		for session.Alive() && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if session.Alive() {
			_ = exec.Command(session.tmux, "kill-session", "-t", session.name).Run()
		}
	}
	killCredentialProcessGroup(session.t, helper)
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
	seedCachedToken(t, cache, "work", "read", "env", []string{"PTY_READ_OAUTH"}, "pty-read-tok")

	code, stdout, stderr := runBinary(t, map[string]string{
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"PTY_MODIFY_OAUTH":       testAuthorizedUser,
	}, "status", "--json")
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
	seedCachedToken(t, cache, "work", "read", "env", []string{"PTY_READ_OAUTH"}, "pty-read-tok")

	code, stdout, stderr := runBinary(t, map[string]string{
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
	}, "archive", "t1", "--json")
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
	seedCachedToken(t, cache, "work", "read", "env", []string{"PTY_READ_OAUTH"}, "pty-read-tok")
	session := newTmuxSession(t, map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"MAILBOX_TOKEN_URL":      "http://127.0.0.1:1/decoy",
		"STUB_TOKEN_URL":         gmail.token.URL,
	}, binary)
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
	readApprove := filepath.Join(stubs, "approve-read")
	writeApprove := filepath.Join(stubs, "approve-write")
	writeExecutable(t, readApprove, fmt.Sprintf(`#!/bin/sh
printf '%%s\t' "$@" >> %q
printf '\n' >> %q
sleep 10
[ "$1" = "--" ] && shift
export PTY_READ_OAUTH=%q
export MAILBOX_TOKEN_URL="$STUB_TOKEN_URL"
exec "$@"
`, readArgv, readArgv, testAuthorizedUser))
	writeExecutable(t, writeApprove, fmt.Sprintf("#!/bin/sh\nprintf 'spawned\\n' >> %q\nexit 99\n", writeArgv))
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_cmd = ["approve-read", "--", %q, "__mint", "--env", "PTY_READ_OAUTH"]
read_interactive = true
write_credential_cmd = ["approve-write"]
credential_env_passthrough = ["STUB_TOKEN_URL"]
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
	pane, found := session.findText("work read access via", 5*time.Second)
	if !found || !strings.Contains(pane, filepath.Base(readApprove)) {
		t.Fatalf("interactive-read attribution missing; helper spawns=%q; Gmail authorizations=%q; pane:\n%s", fileLines(t, readArgv), gmail.recordedReadAuths(), pane)
	}
	assertNoSpawns(t, writeArgv)
	session.WaitFor("PTY smoke", 15*time.Second)
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
