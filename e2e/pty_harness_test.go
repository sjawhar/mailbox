// Package e2e drives the real mailbox binary in a real terminal (tmux PTY).
package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

// WaitForStable waits until a complete terminal frame contains text.
func (s *tmuxSession) WaitForStable(text string, timeout time.Duration) string {
	return s.WaitForStableCondition("containing "+fmt.Sprintf("%q", text), timeout, func(pane string) bool {
		return strings.Contains(pane, text)
	})
}

// WaitForStableCondition waits for a complete terminal frame matching matches.
// Timeout failures retain the raw pane so PTY races can be diagnosed.
func (s *tmuxSession) WaitForStableCondition(description string, timeout time.Duration, matches func(string) bool) string {
	s.t.Helper()
	const stableFor = 300 * time.Millisecond
	deadline := time.Now().Add(timeout)
	var previous, pane string
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		pane = s.Capture()
		if pane != previous {
			previous = pane
			lastChange = time.Now()
		} else if matches(pane) && time.Since(lastChange) >= stableFor {
			return pane
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for a stable pane %s; last pane:\n%s", description, s.Capture())
	return ""
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

const testAuthorizedUser = `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`

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
