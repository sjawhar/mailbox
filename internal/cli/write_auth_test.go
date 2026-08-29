package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCredentialConfig(t *testing.T, dir, writeSource string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	contents := "default_account = \"work\"\n[accounts.work]\nread_credential_env = \"CLI_READ\"\n" + writeSource
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBatchWriteRefusesInteractiveConfiguredCommand(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "approve")
	spawned := filepath.Join(dir, "spawned")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ntouch "+spawned+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeCredentialConfig(t, dir, "write_credential_cmd = [\"approve\"]\n")
	t.Setenv("MAILBOX_CONFIG", config)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := Run([]string{"archive", "thread"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "accounts.work.write_credential_cmd") || !strings.Contains(stderr.String(), config) {
		t.Fatalf("archive = %d, %q", code, stderr.String())
	}
	if _, err := os.Stat(spawned); !os.IsNotExist(err) {
		t.Fatalf("interactive credential command ran: %v", err)
	}
}

func TestBatchWriteNoSourceNamesWriteConfigKey(t *testing.T) {
	dir := t.TempDir()
	config := writeCredentialConfig(t, dir, "")
	t.Setenv("MAILBOX_CONFIG", config)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"archive", "thread", "--json"}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "accounts.work.write_credential_cmd") || !strings.Contains(stderr.String(), config) {
		t.Fatalf("archive = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestBatchWriteExecutesNonInteractiveConfiguredCommand(t *testing.T) {
	g := newGmailTestServer(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, "approve")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' cli-write-token-1234567890\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeCredentialConfig(t, dir, "write_credential_cmd = [\"approve\"]\nwrite_interactive = false\n")
	t.Setenv("MAILBOX_CONFIG", config)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	g.writeToken = "cli-write-token-1234567890"
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"archive", "t1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("archive = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(g.directRequests) != 1 {
		t.Fatalf("write requests = %v", g.directRequests)
	}
}
