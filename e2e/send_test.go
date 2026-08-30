package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

func TestCLISendDryRunDoesNotTouchSendCustody(t *testing.T) {
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	spawnFile := filepath.Join(stubs, "send-spawns")
	writeExecutable(t, filepath.Join(stubs, "send-helper"), `#!/bin/sh
printf 'spawn\n' >> "$SEND_SPAWN_FILE"
printf '%s\n' "$SEND_CANARY"
`)
	config := writeE2EConfig(t, stubs, `default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
send_credential_cmd = ["send-helper"]
send_interactive = false
credential_env_passthrough = ["SEND_CANARY", "SEND_SPAWN_FILE"]
`)
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"SEND_CANARY":            sendCanary(),
		"SEND_SPAWN_FILE":        spawnFile,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	before := snapshotFiles(t, cache)

	code, stdout, stderr := runBinary(t, env, "send", "--reply", "t1", "--body", "hi")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	payload := decodeTOON(t, stdout)
	if got := toonBool(t, payload, "sendable"); !got {
		t.Fatalf("dry-run sendable = false, want true: %q", stdout)
	}
	if got := toonString(t, payload, "message"); got == "" {
		t.Fatalf("dry-run message = empty, want a pinning target: %q", stdout)
	}
	assertNoSpawns(t, spawnFile)
	if sends := gmail.recordedSends(); len(sends) != 0 {
		t.Fatalf("dry-run sent = %#v, want none", sends)
	}
	assertFileSnapshotEqual(t, before, snapshotFiles(t, cache))
}

const goldenReplyMIME = "To: \"A\" <a@example.test>\r\n" +
	"Cc: \"B\" <b@example.test>, \"C\" <c@example.test>\r\n" +
	"Subject: Re: PTY smoke\r\n" +
	"In-Reply-To: <m-t1@example.test>\r\n" +
	"References: <m-t1@example.test>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"aGk=\r\n"

func TestCLISendCapturesGoldenMIMEAndKeepsCanaryOffDisk(t *testing.T) {
	fixture := newSendFixture(t, false)

	code, stdout, stderr := runBinary(t, fixture.env, "send", "--reply", "t1", "--message", "m-t1", "--body", "hi", "--send", "--json")
	if code != 0 {
		t.Fatalf("send exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Sent struct {
			ID string `json:"id"`
		} `json:"sent"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode send JSON: %v: %q", err, stdout)
	}
	if payload.Sent.ID != "sent-e2e-1" {
		t.Fatalf("sent id = %q, want sent-e2e-1", payload.Sent.ID)
	}
	if lines := fileLines(t, fixture.spawnFile); len(lines) != 1 || lines[0] != "spawn" {
		t.Fatalf("send helper spawns = %q, want [spawn]", lines)
	}
	sends := fixture.gmail.recordedSends()
	if len(sends) != 1 {
		t.Fatalf("captured sends = %#v, want one", sends)
	}
	captured := sends[0]
	if captured.Auth != "Bearer "+sendCanary() {
		t.Fatalf("send authorization = %q, want canary bearer", captured.Auth)
	}
	if auths := fixture.gmail.recordedSendAuths(); len(auths) != 1 || auths[0] != captured.Auth {
		t.Fatalf("recorded send authorizations = %q, want [%q]", auths, captured.Auth)
	}
	if captured.ThreadID != "t1" {
		t.Fatalf("send threadId = %q, want t1", captured.ThreadID)
	}
	if !bytes.Equal(captured.Raw, []byte(goldenReplyMIME)) {
		t.Fatalf("captured MIME:\n got: %q\nwant: %q", captured.Raw, goldenReplyMIME)
	}
	assertNoCanaryOnDisk(t, sendCanary(), fixture.stubs, fixture.cache, filepath.Dir(buildMailbox(t)))
}

func TestCLISendRejectsSelfOnlyReplyWithoutTouchingSendCustody(t *testing.T) {
	fixture := newSendFixture(t, false)

	code, stdout, stderr := runBinary(t, fixture.env, "send", "--reply", "t3", "--body", "x", "--send", "--message", "m-t3", "--json")
	if code != 1 {
		t.Fatalf("R2 send exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Error struct {
			Rule string `json:"rule"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode R2 envelope: %v: %q", err, stdout)
	}
	if payload.Error.Rule != "R2" {
		t.Fatalf("R2 envelope rule = %q, want R2", payload.Error.Rule)
	}
	assertNoSpawns(t, fixture.spawnFile)
	if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
		t.Fatalf("R2 captured sends = %#v, want none", sends)
	}
}

func TestCLISendBatchRefusalEnvelopeNeverSpawnsInteractiveHelper(t *testing.T) {
	fixture := newSendFixture(t, true)
	args := []string{"send", "--reply", "t1", "--body", "hi", "--send", "--message", "m-t1"}

	code, stdout, stderr := runBinary(t, fixture.env, append(args, "--json")...)
	if code != 1 {
		t.Fatalf("JSON batch refusal exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var jsonEnvelope struct {
		Error struct {
			Code      string `json:"code"`
			ConfigKey string `json:"config_key"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &jsonEnvelope); err != nil {
		t.Fatalf("decode JSON batch refusal: %v: %q", err, stdout)
	}
	if jsonEnvelope.Error.Code != "needs_send_credential" || jsonEnvelope.Error.ConfigKey != "accounts.work.send_credential_cmd" {
		t.Fatalf("JSON batch refusal = %#v", jsonEnvelope)
	}

	code, stdout, stderr = runBinary(t, fixture.env, args...)
	if code != 1 {
		t.Fatalf("TOON batch refusal exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	toonEnvelope := toonField(t, decodeTOON(t, stdout), "error")
	if code := toonString(t, toonEnvelope, "code"); code != "needs_send_credential" {
		t.Fatalf("TOON batch refusal code = %q", code)
	}
	if configKey := toonString(t, toonEnvelope, "config_key"); configKey != "accounts.work.send_credential_cmd" {
		t.Fatalf("TOON batch refusal config_key = %q", configKey)
	}
	assertNoSpawns(t, fixture.spawnFile)
	if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
		t.Fatalf("batch refusal captured sends = %#v, want none", sends)
	}
}

func TestTUIReplyFenceAndEscapeDoNotTransmitEarly(t *testing.T) {
	binary := buildMailbox(t)
	t.Run("send captures attribution before helper spawn", func(t *testing.T) {
		fixture := newSendFixture(t, true)
		session := startSendTUI(t, binary, fixture)
		session.SendEnter()
		session.WaitFor("r reply", 15*time.Second)
		session.SendKeys("r")
		session.WaitFor("to  a@example.test", 5*time.Second)
		session.SendKeys("hello from tui")
		session.SendCtrl("s")
		session.WaitFor("Re: PTY smoke", 5*time.Second)
		session.SendKeys("y")
		attribution := "waiting for hardware key touch; approve only this request — work send access via " + fixture.helper
		session.WaitFor(attribution, 5*time.Second)
		session.WaitFor("sent — thread updated", 15*time.Second)
		assertSpawnPaneContains(t, fixture.spawnPaneFile, attribution)
		sends := fixture.gmail.recordedSends()
		if len(sends) != 1 || sends[0].Auth != "Bearer "+sendCanary() {
			t.Fatalf("TUI captured sends = %#v, want one canary-authenticated send", sends)
		}
		session.WaitFor("PTY smoke", 5*time.Second)
	})

	t.Run("escape abandons reply without a helper spawn", func(t *testing.T) {
		fixture := newSendFixture(t, true)
		session := startSendTUI(t, binary, fixture)
		session.SendEnter()
		session.WaitFor("r reply", 15*time.Second)
		session.SendKeys("r")
		session.WaitFor("Body:", 5*time.Second)
		session.SendKeys("esc")
		session.WaitFor("r reply", 5*time.Second)
		assertNoSpawns(t, fixture.spawnFile)
		if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
			t.Fatalf("escape captured sends = %#v, want none", sends)
		}
	})
}

func TestFormatResolutionMatrixEndToEnd(t *testing.T) {
	fixture := newSendFixture(t, false)
	binary := buildMailbox(t)

	session := newTmuxSession(t, fixture.env, "sh", "-c", binary+" inbox && sleep 30")
	pane := session.WaitFor("1\tt1", 15*time.Second)
	if strings.Contains(pane, "account: work") {
		t.Fatalf("TTY inbox selected machine output:\n%s", pane)
	}

	code, stdout, stderr := runBinary(t, fixture.env, "inbox")
	if code != 0 {
		t.Fatalf("pipe inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertTOONInbox(t, stdout)

	ciEnv := withEnvironment(fixture.env, map[string]string{"CI": "1"})
	code, stdout, stderr = runBinary(t, ciEnv, "inbox")
	if code != 0 {
		t.Fatalf("CI inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertTOONInbox(t, stdout)

	code, stdout, stderr = runBinary(t, ciEnv, "inbox", "--json")
	if code != 0 {
		t.Fatalf("CI JSON inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	assertJSONInbox(t, stdout)

	code, stdout, stderr = runBinary(t, ciEnv, "inbox", "--text")
	if code != 0 {
		t.Fatalf("piped text inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "1\tt1") || strings.Contains(stdout, "account: work") {
		t.Fatalf("piped --text inbox = %q, want human rows", stdout)
	}
}

type sendFixture struct {
	gmail         *fakeGmail
	stubs         string
	cache         string
	env           map[string]string
	helper        string
	spawnFile     string
	spawnPaneFile string
}

func newSendFixture(t *testing.T, interactive bool) *sendFixture {
	t.Helper()
	gmail := newFakeGmail(t)
	stubs := shortTempDir(t)
	helper := filepath.Join(stubs, "send-helper")
	spawnFile := filepath.Join(stubs, "send-spawns")
	spawnPaneFile := filepath.Join(stubs, "send-spawn-pane")
	script := `#!/bin/sh
printf 'spawn\n' >> "$SEND_SPAWN_FILE"
printf '%s\n' "$SEND_CANARY"
`
	if interactive {
		script = `#!/bin/sh
"$PTY_TMUX_BIN" -S "$PTY_TMUX_SOCKET" capture-pane -p -t "$PTY_TMUX_SESSION" > "$SEND_PANE_FILE" 2>&1
printf 'spawn\n' >> "$SEND_SPAWN_FILE"
sleep 2
printf '%s\n' "$SEND_CANARY"
`
	}
	writeExecutable(t, helper, script)
	config := writeE2EConfig(t, stubs, fmt.Sprintf(`default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
send_credential_cmd = ["send-helper"]
send_interactive = %t
send_label = "hardware key touch"
credential_env_passthrough = ["SEND_CANARY", "SEND_SPAWN_FILE", "SEND_PANE_FILE", "PTY_TMUX_BIN", "PTY_TMUX_SOCKET", "PTY_TMUX_SESSION"]
`, interactive))
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"SEND_CANARY":            sendCanary(),
		"SEND_SPAWN_FILE":        spawnFile,
		"SEND_PANE_FILE":         spawnPaneFile,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	return &sendFixture{
		gmail:         gmail,
		stubs:         stubs,
		cache:         cache,
		env:           env,
		helper:        helper,
		spawnFile:     spawnFile,
		spawnPaneFile: spawnPaneFile,
	}
}

func startSendTUI(t *testing.T, binary string, fixture *sendFixture) *tmuxSession {
	t.Helper()
	session := newTmuxSession(t, fixture.env, binary)
	cleanupCredentialHelper(t, session, fixture.helper)
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	return session
}

func assertNoCanaryOnDisk(t *testing.T, canary string, roots ...string) {
	t.Helper()
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(canary)) {
				t.Fatalf("send canary persisted at %s", path)
			}
			return nil
		}); err != nil {
			t.Fatalf("scan %s for send canary: %v", root, err)
		}
	}
}

func assertTOONInbox(t *testing.T, stdout string) {
	t.Helper()
	inbox := decodeTOON(t, stdout)
	if account := toonString(t, inbox, "account"); account != "work" {
		t.Fatalf("TOON inbox account = %q, want work", account)
	}
	threads := toonField(t, inbox, "threads")
	if threads.Kind != toontest.Array || len(threads.Arr) != 2 {
		t.Fatalf("TOON inbox threads = %#v, want two rows", threads)
	}
	for index, want := range []string{"t1", "t2"} {
		if got := toonString(t, threads.Arr[index], "id"); got != want {
			t.Fatalf("TOON inbox thread %d = %q, want %q", index, got, want)
		}
	}
}

func assertJSONInbox(t *testing.T, stdout string) {
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
		t.Fatalf("JSON inbox = %#v, want work t1 t2", inbox)
	}
}

func sendCanary() string {
	return strings.Join([]string{"canary", "send", "token", "value", "1234567890abcdef"}, "-")
}

func snapshotFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	}); err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return files
}

func assertFileSnapshotEqual(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("file count = %d, want %d; got=%v want=%v", len(got), len(want), sortedSnapshotNames(got), sortedSnapshotNames(want))
	}
	for path, wantData := range want {
		gotData, ok := got[path]
		if !ok {
			t.Fatalf("missing file %q; got=%v", path, sortedSnapshotNames(got))
		}
		if !bytes.Equal(gotData, wantData) {
			t.Fatalf("file %q changed:\n got: %q\nwant: %q", path, gotData, wantData)
		}
	}
}

func sortedSnapshotNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decodeTOON(t *testing.T, document string) toontest.Value {
	t.Helper()
	value, err := toontest.Decode(document)
	if err != nil {
		t.Fatalf("decode TOON: %v\n%s", err, document)
	}
	return value
}

func toonField(t *testing.T, object toontest.Value, key string) toontest.Value {
	t.Helper()
	if object.Kind != toontest.Object {
		t.Fatalf("TOON value kind = %v, want object", object.Kind)
	}
	for _, field := range object.Obj {
		if field.Key == key {
			return field.Val
		}
	}
	t.Fatalf("TOON object lacks %q: %#v", key, object.Obj)
	return toontest.Value{}
}

func toonString(t *testing.T, object toontest.Value, key string) string {
	t.Helper()
	value := toonField(t, object, key)
	if value.Kind != toontest.String {
		t.Fatalf("TOON %q kind = %v, want string", key, value.Kind)
	}
	return value.Str
}

func toonBool(t *testing.T, object toontest.Value, key string) bool {
	t.Helper()
	value := toonField(t, object, key)
	if value.Kind != toontest.Bool {
		t.Fatalf("TOON %q kind = %v, want bool", key, value.Kind)
	}
	return value.Bool
}
