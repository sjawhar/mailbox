package cli

import (
	"bytes"
	"encoding/json"
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

type configRig struct {
	configPath string
	spawnLog   string
	cacheDir   string
}

// newConfigRig makes command execution visible. Its default commands fail, so
// tests must explicitly replace a helper before expecting a command to run.
func newConfigRig(t *testing.T, g *gmailTestServer, body string) *configRig {
	t.Helper()
	stubs := t.TempDir()
	rig := &configRig{
		spawnLog: filepath.Join(stubs, "spawn-log"),
		cacheDir: t.TempDir(),
	}
	for _, name := range []string{"record-read", "record-write"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"$0\" \"$*\" >> " + rig.spawnLog + "\nexit 7\n"
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", stubs+":/usr/bin:/bin")
	rig.configPath = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(rig.configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", rig.configPath)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", rig.cacheDir)
	for _, name := range []string{"MAILBOX_TOKEN", "MAILBOX_ACCOUNT"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (r *configRig) command(name string) string {
	return filepath.Join(filepath.Dir(r.spawnLog), name)
}

func (r *configRig) recordedSpawns(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.spawnLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (r *configRig) replaceCommand(t *testing.T, name, script string) {
	t.Helper()
	if err := os.WriteFile(r.command(name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
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
	code := Run([]string{"archive", "thread"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "accounts.work.write_credential_cmd") || !strings.Contains(stderr.String(), config) {
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

func TestCLINeverSpawnsConfiguredCredentialCmds(t *testing.T) {
	commands := [][]string{
		{"archive", "t1"}, {"trash", "t1"}, {"mark", "read", "t1"},
		{"label", "add", "Newsletters", "t1"},
		{"inbox"}, {"search", "q"}, {"read", "t1"}, {"status"},
	}
	for _, argv := range commands {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			g := newGmailTestServer(t)
			rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_cmd  = ["record-read"]
read_interactive     = true
write_credential_cmd = ["record-write"]
`)
			var stdout, stderr bytes.Buffer
			code := Run(argv, &stdout, &stderr)
			if argv[0] != "status" && code == 0 {
				t.Fatalf("exit = 0, want credential failure for %v", argv)
			}
			if argv[0] == "status" && code != 0 {
				t.Fatalf("status exit = %d, want diagnostic success: stderr = %q", code, stderr.String())
			}
			if got := rig.recordedSpawns(t); got != "" {
				t.Fatalf("CLI spawned a credential cmd: %q", got)
			}
		})
	}
}

func TestWriteEnvelopeShape(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env  = "TEST_READ_OAUTH"
write_credential_cmd = ["record-write"]
`)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"archive", "t1", "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Account   string `json:"account"`
			ConfigKey string `json:"config_key"`
			Config    string `json:"config"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "needs_write_credential" ||
		envelope.Error.Account != "work" ||
		envelope.Error.ConfigKey != "accounts.work.write_credential_cmd" ||
		envelope.Error.Config != rig.configPath {
		t.Fatalf("envelope = %+v", envelope)
	}
	for _, output := range []string{stdout.String(), stderr.String()} {
		if strings.Contains(output, "record-write\" ") || strings.Contains(output, "-- mailbox") {
			t.Fatalf("output leaks argv tail: %q", output)
		}
	}
}

func TestReadEnvelopeShape(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_cmd = ["record-read"]
read_interactive = true
`)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"inbox", "--json"}, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Account   string `json:"account"`
			ConfigKey string `json:"config_key"`
			Config    string `json:"config"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "needs_read_credential" ||
		envelope.Error.Account != "work" ||
		envelope.Error.ConfigKey != "accounts.work.read_credential_cmd" ||
		envelope.Error.Config != rig.configPath {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestNonInteractiveWriteCmdActsFromCLI(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env  = "TEST_READ_OAUTH"
write_credential_cmd = ["record-write"]
write_interactive    = false
`)
	const token = "cli-write-token-1234567890"
	rig.replaceCommand(t, "record-write", "#!/bin/sh\nprintf '%s\\n' "+token+"\n")
	g.writeToken = token
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"archive", "t1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("archive = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := strings.Count(strings.TrimSpace(rig.recordedSpawns(t)), "\n") + 1; got != 1 {
		t.Fatalf("credential command spawns = %d, want 1", got)
	}
	if len(g.directRequests) != 1 {
		t.Fatalf("write requests = %v", g.directRequests)
	}
}

func TestEnvWriteCredentialActsAndCachesNothing(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env  = "TEST_READ_OAUTH"
write_credential_env = "TEST_WRITE_TOKEN"
`)
	const token = "env-write-token-1234567890"
	t.Setenv("TEST_WRITE_TOKEN", token)
	g.writeToken = token
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"archive", "t1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("archive = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(rig.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("write credential populated cache: %v", entries)
	}
}

func TestUnknownAccountListsConfigured(t *testing.T) {
	g := newGmailTestServer(t)
	newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env = "TEST_WORK_OAUTH"
[accounts.personal]
read_credential_env = "TEST_PERSONAL_OAUTH"
`)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--account", "nope", "inbox"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "configured accounts: work, personal") {
		t.Fatalf("unknown account = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestAccountSelectionPrecedence(t *testing.T) {
	g := newGmailTestServer(t)
	newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env = "TEST_WORK_OAUTH"
[accounts.personal]
read_credential_env = "TEST_PERSONAL_OAUTH"
`)
	t.Setenv("MAILBOX_TOKEN", "test-token")

	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v = %d, stdout=%q, stderr=%q", args, code, stdout.String(), stderr.String())
		}
		var output struct {
			Account string `json:"account"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
		return output.Account
	}

	if got := run("inbox", "--json"); got != "work" {
		t.Fatalf("default account = %q, want work", got)
	}
	t.Setenv("MAILBOX_ACCOUNT", "personal")
	if got := run("inbox", "--json"); got != "personal" {
		t.Fatalf("environment account = %q, want personal", got)
	}
	if got := run("--account", "work", "inbox", "--json"); got != "work" {
		t.Fatalf("flag account = %q, want work", got)
	}
}

func TestNoConfigTokenOnlyMode(t *testing.T) {
	g := newGmailTestServer(t)
	clearTestEnv(t, "MAILBOX_CONFIG")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_ACCOUNT", "")
	t.Setenv("MAILBOX_TOKEN", "test-token")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"inbox", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("token-only inbox = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var output struct {
		Account string `json:"account"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Account != "default" {
		t.Fatalf("token-only output = %q, value=%+v, error=%v", stdout.String(), output, err)
	}

	stdout.Reset()
	stderr.Reset()
	t.Setenv("MAILBOX_TOKEN", "")
	if code := Run([]string{"inbox"}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "~/.config/mailbox/config.toml") ||
		!strings.Contains(stderr.String(), "README") {
		t.Fatalf("no-config inbox = (%d, %q, %q)", code, stdout.String(), stderr.String())
	}
}

func TestConfigErrorIsLoudBeforeAnyCommand(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_cmd = ["record-read"]
`)
	if err := os.Chmod(rig.configPath, 0o660); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"inbox"}, {"search", "q"}, {"read", "t1"}, {"open", "t1"},
		{"archive", "t1"}, {"trash", "t1"}, {"mark", "read", "t1"},
		{"label", "add", "Newsletters", "t1"}, {"attachment", "t1"}, {"status"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(argv, &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
			!strings.Contains(stderr.String(), "group- or world-writable") {
			t.Fatalf("%v = (%d, %q, %q), want config trust failure", argv, code, stdout.String(), stderr.String())
		}
	}
	t.Setenv("MINT_OAUTH", `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`)
	t.Setenv("MAILBOX_TOKEN_URL", g.tokenURL(t, "test-token"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"__mint", "--env", "MINT_OAUTH"}, &stdout, &stderr); code != 0 {
		t.Fatalf("__mint = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCredentialCmdStderrSanitizedOnCLI(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_cmd = ["record-read"]
read_interactive    = false
`)
	rig.replaceCommand(t, "record-read", "#!/bin/sh\nprintf '\\033]52;c;payload\\007\\033[2J' >&2\nexit 3\n")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"inbox"}, &stdout, &stderr); code != 1 {
		t.Fatalf("inbox exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "\x1b") {
		t.Fatalf("output contains terminal escape: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestScopeHintNamesWriteConfigKeyAndArgv0Only(t *testing.T) {
	g := newGmailTestServer(t)
	g.forbidden = true
	newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env  = "TEST_READ_TOKEN"
write_credential_env = "TEST_WRITE_TOKEN"
`)
	t.Setenv("TEST_READ_TOKEN", "read-token-1234567890")
	t.Setenv("TEST_WRITE_TOKEN", "write-token-1234567890")
	g.writeToken = "write-token-1234567890"
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"archive", "t1"}, &stdout, &stderr); code != 1 {
		t.Fatalf("archive exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if output := stderr.String(); !strings.Contains(output, "accounts.work.write_credential_env") ||
		strings.Contains(output, "accounts.work.read_credential_env") {
		t.Fatalf("scope hint = %q", output)
	}
}

func TestStatusReportsAllAccounts(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env = "TEST_WORK_OAUTH"
write_credential_cmd = ["record-write"]
write_label = "Acme approval"
[accounts.personal]
read_credential_cmd = ["record-read"]
read_interactive = true
`)
	t.Setenv("TEST_WORK_OAUTH", `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`)
	t.Setenv("MAILBOX_TOKEN_URL", g.tokenURL(t, "test-token"))

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var output struct {
		Config   string `json:"config"`
		Accounts []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
			Read    struct {
				Kind        string `json:"kind"`
				Argv0       string `json:"argv0"`
				Interactive bool   `json:"interactive"`
			} `json:"read"`
			Write struct {
				Kind        string `json:"kind"`
				Argv0       string `json:"argv0"`
				Interactive bool   `json:"interactive"`
				Label       string `json:"label"`
			} `json:"write"`
			Route string `json:"route"`
			Cache struct {
				Exists bool `json:"exists"`
				Valid  bool `json:"valid"`
			} `json:"cache"`
			Profile struct {
				Email string `json:"email"`
			} `json:"profile"`
			Pinned bool   `json:"pinned"`
			Error  string `json:"error"`
		} `json:"accounts"`
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode status %q: %v", stdout.String(), err)
	}
	if output.Config != rig.configPath || len(output.Accounts) != 2 || !output.Accounts[0].Default ||
		output.Accounts[0].Read.Kind != "env" || output.Accounts[0].Read.Interactive ||
		output.Accounts[0].Write.Kind != "cmd" || output.Accounts[0].Write.Argv0 != rig.command("record-write") ||
		!output.Accounts[0].Write.Interactive || output.Accounts[0].Write.Label != "Acme approval" ||
		output.Accounts[0].Route != "env" || !output.Accounts[0].Cache.Exists || !output.Accounts[0].Cache.Valid ||
		output.Accounts[0].Profile.Email != "user@example.com" ||
		output.Accounts[1].Read.Kind != "cmd" || output.Accounts[1].Read.Argv0 != rig.command("record-read") ||
		!output.Accounts[1].Read.Interactive || !strings.Contains(output.Accounts[1].Error, "interactive") || !output.OK {
		t.Fatalf("status output = %+v", output)
	}
	if got := rig.recordedSpawns(t); got != "" {
		t.Fatalf("status spawned an interactive helper: %q", got)
	}

	t.Setenv("MAILBOX_TOKEN", "test-token")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pinned status exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Accounts) != 2 || !output.Accounts[0].Pinned || !output.Accounts[1].Pinned {
		t.Fatalf("pinned rows = %+v", output.Accounts)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pinned text status exit = %d, stderr=%q", code, stderr.String())
	}
	if got := strings.Count(stdout.String(), "MAILBOX_TOKEN pins one identity for all accounts"); got != 1 {
		t.Fatalf("pinning note count = %d, output=%q", got, stdout.String())
	}
}

func TestReadDiagnosticEmittedOnCLISuccess(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_cmd = ["record-read"]
read_interactive    = false
`)
	rig.replaceCommand(t, "record-read", "#!/bin/sh\nprintf '%s\\n' read-token-1234567890\nprintf '%s\\n' 'grant expires soon' 'reapprove tomorrow' >&2\n")
	g.writeToken = "read-token-1234567890"
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"inbox", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("inbox exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	var document json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("stdout is not JSON: %q: %v", stdout.String(), err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
	if got, want := stderr.String(), "mailbox: credential helper: grant expires soon reapprove tomorrow\n"; got != want {
		t.Fatalf("credential diagnostic = %q, want %q", got, want)
	}
}

func TestStatusSanitizesConfigDerivedText(t *testing.T) {
	g := newGmailTestServer(t)
	newConfigRig(t, g, `
default_account = "work"
[accounts.work]
read_credential_env = "TEST_READ_TOKEN"
write_credential_cmd = ["record-write"]
write_label = "evil\u001b]52;c;x\u0007name"
`)
	t.Setenv("MAILBOX_TOKEN", "test-token")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("text status contains terminal escape: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("JSON status exit = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	var output struct {
		Accounts []struct {
			Write struct {
				Label string `json:"label"`
			} `json:"write"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Accounts) != 1 || output.Accounts[0].Write.Label != "evil\x1b]52;c;x\aname" {
		t.Fatalf("JSON status label = %+v", output)
	}
}

func clearTestEnv(t *testing.T, name string) {
	t.Helper()
	value, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}
