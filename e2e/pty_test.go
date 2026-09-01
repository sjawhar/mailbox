package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	config := writeE2EConfig(t, stubs, `default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_env = "UNSET_WRITE_CREDENTIAL"
`)
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
	if envelope.Error.Code != "needs_write_credential" || envelope.Error.ConfigKey != "accounts.work.write_credential_env" {
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
