package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

type draftFixture struct {
	sendFixture
	writeHelper        string
	writeSpawnFile     string
	writeSpawnPaneFile string
}

func newDraftFixture(t *testing.T) *draftFixture {
	t.Helper()
	send := newSendFixture(t, true)
	writeHelper := filepath.Join(send.stubs, "write-helper")
	writeSpawnFile := filepath.Join(send.stubs, "write-spawns")
	writeSpawnPaneFile := filepath.Join(send.stubs, "write-spawn-pane")
	writeExecutable(t, writeHelper, `#!/bin/sh
if [ -n "$PTY_TMUX_BIN" ]; then
  "$PTY_TMUX_BIN" -S "$PTY_TMUX_SOCKET" capture-pane -p -t "$PTY_TMUX_SESSION" > "$WRITE_PANE_FILE" 2>&1
fi
printf 'spawn\n' >> "$WRITE_SPAWN_FILE"
sleep 2
printf '%s\n' "$WRITE_CANARY"
`)
	config := writeE2EConfig(t, send.stubs, `default_account = "work"
[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["write-helper"]
write_interactive = true
write_label = "write approval"
send_credential_cmd = ["send-helper"]
send_interactive = true
send_label = "hardware key touch"
credential_env_passthrough = ["SEND_SPAWN_FILE", "SEND_PANE_FILE", "WRITE_SPAWN_FILE", "WRITE_PANE_FILE", "PTY_TMUX_BIN", "PTY_TMUX_SOCKET", "PTY_TMUX_SESSION"]
write_credential_env_passthrough = ["WRITE_CANARY"]
send_credential_env_passthrough = ["SEND_CANARY"]
`)
	send.env["MAILBOX_CONFIG"] = config
	send.env["WRITE_CANARY"] = writeCanary()
	send.env["WRITE_SPAWN_FILE"] = writeSpawnFile
	send.env["WRITE_PANE_FILE"] = writeSpawnPaneFile
	return &draftFixture{
		sendFixture:        *send,
		writeHelper:        writeHelper,
		writeSpawnFile:     writeSpawnFile,
		writeSpawnPaneFile: writeSpawnPaneFile,
	}
}

func writeCanary() string {
	return strings.Join([]string{"canary", "write", "token", "value", "1234567890abcdef"}, "-")
}
