package e2e

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const editorScissorsLine = "# ------------------------ >8 ------------------------"

type editorFixture struct {
	gmail         *fakeGmail
	cache         string
	env           map[string]string
	probeFile     string
	sendSpawnFile string
	sendPaneFile  string
}

func newEditorFixture(t *testing.T, editorScript string) *editorFixture {
	t.Helper()
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	probeFile := filepath.Join(stubs, "editor-probe")
	sendSpawnFile := filepath.Join(stubs, "send-spawns")
	sendPaneFile := filepath.Join(stubs, "send-pane")
	editor := filepath.Join(stubs, "editor")
	writeExecutable(t, editor, editorScript)
	writeExecutable(t, filepath.Join(stubs, "send-helper"), `#!/bin/sh
if [ -n "$PTY_TMUX_BIN" ]; then
  "$PTY_TMUX_BIN" -S "$PTY_TMUX_SOCKET" capture-pane -p -t "$PTY_TMUX_SESSION" > "$SEND_PANE_FILE" 2>&1
fi
printf 'spawn\n' >> "$SEND_SPAWN_FILE"
printf '%s\n' "$SEND_CANARY"
`)
	config := writeE2EConfig(t, stubs, `default_account = "work"
scrub_env = ["EDITOR_CANARY"]

[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
send_credential_cmd = ["send-helper"]
send_interactive = false
credential_env_passthrough = ["SEND_SPAWN_FILE", "SEND_PANE_FILE", "PTY_TMUX_BIN", "PTY_TMUX_SOCKET", "PTY_TMUX_SESSION"]
send_credential_env_passthrough = ["SEND_CANARY"]
`)
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"EDITOR_CANARY":          "secret-sentinel",
		"EDITOR_PROBE":           probeFile,
		"VISUAL":                 editor,
		"SEND_CANARY":            "editor.send.token.value.1234567890",
		"SEND_SPAWN_FILE":        sendSpawnFile,
		"SEND_PANE_FILE":         sendPaneFile,
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	return &editorFixture{
		gmail:         gmail,
		cache:         cache,
		env:           env,
		probeFile:     probeFile,
		sendSpawnFile: sendSpawnFile,
		sendPaneFile:  sendPaneFile,
	}
}

func startEditorReply(t *testing.T, fixture *editorFixture) *tmuxSession {
	t.Helper()
	session := newTmuxSession(t, fixture.env, buildMailbox(t))
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	session.SendEnter()
	session.WaitFor("r reply", 15*time.Second)
	session.SendKeys("r")
	return session
}

func TestTUIEditorComposeSendsViaConfirm(t *testing.T) {
	fixture := newEditorFixture(t, `#!/bin/sh
printf 'env:%s\n' "${EDITOR_CANARY:-unset}" >> "$EDITOR_PROBE"
stat -c '%a' "$(dirname "$1")" >> "$EDITOR_PROBE"
stat -c '%a' "$1" >> "$EDITOR_PROBE"
printf 'edited body line\n' >> "$1"
exit 0
`)
	session := startEditorReply(t, fixture)

	session.WaitFor("Confirm send", 15*time.Second)
	session.SendKeys("y")
	session.WaitFor("sent — thread updated", 15*time.Second)
	sends := fixture.gmail.recordedSends()
	if len(sends) != 1 {
		t.Fatalf("captured sends = %#v, want one", sends)
	}
	if plain := editorPlainAlternative(t, sends[0].Raw); !strings.Contains(plain, "edited body line\n") {
		t.Fatalf("editor-composed plain body = %q, want edited body line", plain)
	}
	if got := waitForFileLines(t, fixture.probeFile, time.Second); !sameStrings(got, []string{"env:unset", "700", "600"}) {
		t.Fatalf("editor probe = %q, want scrubbed env and 0700/0600 custody", got)
	}
	assertSpawnPaneContains(t, fixture.sendPaneFile, "Confirm send")
	assertComposeDirectoryEmpty(t, fixture.cache)
}

func TestTUIEditorNonzeroExitAbandons(t *testing.T) {
	fixture := newEditorFixture(t, "#!/bin/sh\nexit 1\n")
	session := startEditorReply(t, fixture)

	session.WaitFor("abandoned", 15*time.Second)
	if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
		t.Fatalf("nonzero editor captured sends = %#v, want none", sends)
	}
	assertNoSpawns(t, fixture.sendSpawnFile)
	assertComposeDirectoryEmpty(t, fixture.cache)
}

func TestTUIEditorSentinelInBodyStaysBody(t *testing.T) {
	fixture := newEditorFixture(t, `#!/bin/sh
printf '%s\nafter\n' '# ------------------------ >8 ------------------------' >> "$1"
exit 0
`)
	session := startEditorReply(t, fixture)

	session.WaitFor("Confirm send", 15*time.Second)
	session.SendKeys("y")
	session.WaitFor("sent — thread updated", 15*time.Second)
	sends := fixture.gmail.recordedSends()
	if len(sends) != 1 {
		t.Fatalf("sentinel editor captured sends = %#v, want one", sends)
	}
	plain := editorPlainAlternative(t, sends[0].Raw)
	for _, want := range []string{editorScissorsLine, "after\n"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain editor body = %q, want %q to remain body content", plain, want)
		}
	}
	assertComposeDirectoryEmpty(t, fixture.cache)
}

func assertComposeDirectoryEmpty(t *testing.T, cache string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(cache, "compose"))
	if err != nil {
		t.Fatalf("read compose cache directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("compose cache entries = %v, want no draft directories", entries)
	}
}

func editorPlainAlternative(t *testing.T, raw []byte) string {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse sent message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q (%v), want multipart/alternative", message.Header.Get("Content-Type"), err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read alternative part: %v", err)
		}
		if part.Header.Get("Content-Type") != "text/plain; charset=UTF-8" {
			continue
		}
		encoded, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read plain alternative part: %v", err)
		}
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(encoded)))
		if err != nil {
			t.Fatalf("decode plain alternative part: %v", err)
		}
		return string(decoded)
	}
	t.Fatal("multipart/alternative has no text/plain leaf")
	return ""
}
