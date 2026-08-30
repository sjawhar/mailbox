package auth

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeConfig writes a trust-check-passing config file and points
// MAILBOX_CONFIG at it.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", path)
	return path
}

// stubCmds creates executable stubs and prepends their dir to PATH so
// LookPath resolution at load succeeds without any real binary.
func stubCmds(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return dir
}

const validTwoAccounts = `
default_account = "work"
[accounts.work]
read_credential_cmd  = ["my-token-helper", "--scopes", "gmail.readonly"]
write_credential_cmd = ["my-approver", "--", "mailbox-mint"]
write_label          = "Acme approval"
credential_env_passthrough = ["ACME_SESSION_FILE"]
[accounts.personal]
read_credential_env = "ACME_OAUTH_JSON"
`

func TestLoadConfigParseContract(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want string // substring the error must contain (offending key path)
	}{
		{"unknown top-level key", "scrub_environment = []\n[accounts.a]\nread_credential_env = \"V\"\n", "scrub_environment"},
		{"unknown account key", "[accounts.a]\nread_credential_env = \"V\"\nwrite_interactve = true\n", "accounts.a.write_interactve"},
		{"both env and cmd", "[accounts.a]\nread_credential_env = \"V\"\nread_credential_cmd = [\"my-token-helper\"]\n", "accounts.a.read"},
		{"missing read source", "[accounts.a]\nwrite_credential_env = \"V\"\n", "accounts.a.read"},
		{"invalid account name", "[accounts.\"a/b\"]\nread_credential_env = \"V\"\n", "a/b"},
		{"case-insensitive collision", "default_account = \"Work\"\n[accounts.Work]\nread_credential_env = \"V1\"\n[accounts.work]\nread_credential_env = \"V2\"\n", "work"},
		{"empty argv", "[accounts.a]\nread_credential_cmd = []\n", "accounts.a.read_credential_cmd"},
		{"empty argv0", "[accounts.a]\nread_credential_cmd = [\"\"]\n", "accounts.a.read_credential_cmd"},
		{"unresolvable argv0", "[accounts.a]\nread_credential_cmd = [\"no-such-helper-xyz\"]\n", "no-such-helper-xyz"},
		{"invalid env var name", "[accounts.a]\nread_credential_env = \"1BAD\"\n", "accounts.a.read_credential_env"},
		{"env var with equals", "[accounts.a]\nread_credential_env = \"FOO=BAR\"\n", "accounts.a.read_credential_env"},
		{"duplicate credential var", "default_account = \"a\"\n[accounts.a]\nread_credential_env = \"SHARED\"\n[accounts.b]\nwrite_credential_env = \"SHARED\"\nread_credential_env = \"OTHER\"\n", "SHARED"},
		{"scrub_env empty entry", "scrub_env = [\"\"]\n[accounts.a]\nread_credential_env = \"V\"\n", "scrub_env"},
		{"scrub_env with equals", "scrub_env = [\"A=B\"]\n[accounts.a]\nread_credential_env = \"V\"\n", "scrub_env"},
		{"malformed pattern", "scrub_env_patterns = [\"[\"]\n[accounts.a]\nread_credential_env = \"V\"\n", "scrub_env_patterns"},
		{"default_account unknown", "default_account = \"zzz\"\n[accounts.a]\nread_credential_env = \"V\"\n", "zzz"},
		{"default_account missing with two accounts", "[accounts.a]\nread_credential_env = \"V1\"\n[accounts.b]\nread_credential_env = \"V2\"\n", "default_account"},
		{"interactive without cmd", "[accounts.a]\nread_credential_env = \"V\"\nwrite_credential_env = \"W\"\nwrite_interactive = true\n", "accounts.a.write_interactive"},
		{"label without write source", "[accounts.a]\nread_credential_env = \"V\"\nwrite_label = \"x\"\n", "accounts.a.write_label"},
		{"non-positive timeout", "credential_timeout_secs = 0\n[accounts.a]\nread_credential_env = \"V\"\n", "credential_timeout_secs"},
		{"passthrough names deny-set var", "[accounts.a]\nread_credential_env = \"V\"\ncredential_env_passthrough = [\"MAILBOX_TOKEN\"]\n", "accounts.a.credential_env_passthrough"},
		{"passthrough names a declared credential var", "default_account = \"a\"\n[accounts.a]\nread_credential_env = \"V\"\ncredential_env_passthrough = [\"W\"]\n[accounts.b]\nread_credential_env = \"W\"\n", "accounts.a.credential_env_passthrough"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubCmds(t, "my-token-helper", "my-approver")
			path := writeConfig(t, tc.toml)
			_, err := LoadConfig()
			if err == nil {
				t.Fatalf("LoadConfig() succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), path) && !strings.Contains(err.Error(), filepath.Base(path)) {
				t.Errorf("error = %q, want the config path named", err)
			}
		})
	}
}

func TestLoadConfigValid(t *testing.T) {
	stubs := stubCmds(t, "my-token-helper", "my-approver")
	writeConfig(t, validTwoAccounts)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NoConfig() {
		t.Fatal("NoConfig() = true for a loaded file")
	}
	if got := cfg.AccountNames(); len(got) != 2 || got[0] != "work" || got[1] != "personal" {
		t.Fatalf("AccountNames() = %v, want [work personal] in declaration order", got)
	}
	work, _ := cfg.Account("work")
	if work.Read.Kind != SourceCmd || work.Read.Argv0 != filepath.Join(stubs, "my-token-helper") {
		t.Fatalf("work read = %+v, want resolved absolute argv0", work.Read)
	}
	if work.Read.Interactive || !work.Write.Interactive {
		t.Fatalf("interactive defaults: read=%v write=%v, want false/true", work.Read.Interactive, work.Write.Interactive)
	}
	if work.Write.Label != "Acme approval" || work.Write.ConfigKey != "accounts.work.write_credential_cmd" {
		t.Fatalf("write source = %+v", work.Write)
	}
	if cfg.CredentialTimeout != 120*time.Second {
		t.Fatalf("CredentialTimeout = %v, want 120s default", cfg.CredentialTimeout)
	}
	personal, _ := cfg.Account("personal")
	if personal.Read.Kind != SourceEnv || personal.Read.EnvVar != "ACME_OAUTH_JSON" || personal.Write != nil {
		t.Fatalf("personal = %+v", personal)
	}
}

func TestLoadConfigResolvesRelativeArgv0(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rel-helper"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	writeConfig(t, "[accounts.a]\nread_credential_cmd = [\"./rel-helper\"]\n")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Account("a")
	if !filepath.IsAbs(a.Read.Argv0) {
		t.Fatalf("Argv0 = %q, want absolute (LookPath returns relative for ./cmds and relative PATH entries)", a.Read.Argv0)
	}
}

func TestLoadConfigTrustChecks(t *testing.T) {
	write := func(t *testing.T, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		body := "[accounts.a]\nread_credential_env = \"V\"\n"
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // WriteFile masks by umask
			t.Fatal(err)
		}
		t.Setenv("MAILBOX_CONFIG", path)
		return path
	}
	t.Run("group-writable refused", func(t *testing.T) {
		write(t, 0o620)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("err = %v, want group/world-writable refusal", err)
		}
	})
	t.Run("world-writable refused", func(t *testing.T) {
		write(t, 0o602)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("err = %v, want refusal", err)
		}
	})
	t.Run("other-owner file refused", func(t *testing.T) {
		if os.Getuid() == 0 {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[accounts.a]\nread_credential_env = \"V\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, 1, -1); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MAILBOX_CONFIG", path)
		} else {
			link := filepath.Join(t.TempDir(), "config.toml")
			if err := os.Symlink("/etc/passwd", link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MAILBOX_CONFIG", link)
		}
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "owned") {
			t.Fatalf("err = %v, want ownership refusal", err)
		}
	})
	t.Run("oversize refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, make([]byte, 262145), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MAILBOX_CONFIG", path)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "262144") {
			t.Fatalf("err = %v, want size refusal", err)
		}
	})
	t.Run("non-regular refused", func(t *testing.T) {
		t.Setenv("MAILBOX_CONFIG", t.TempDir()) // a directory
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("err = %v, want non-regular refusal", err)
		}
	})
	t.Run("FIFO refused immediately, no block", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MAILBOX_CONFIG", path)
		done := make(chan error, 1)
		go func() { _, err := LoadConfig(); done <- err }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "regular") {
				t.Fatalf("err = %v, want non-regular refusal", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("LoadConfig blocked opening a FIFO (missing O_NONBLOCK)")
		}
	})
	t.Run("explicit MAILBOX_CONFIG to missing file is loud", func(t *testing.T) {
		t.Setenv("MAILBOX_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
		if _, err := LoadConfig(); err == nil {
			t.Fatal("want loud error for explicit missing config, got nil")
		}
	})
}

func TestLoadConfigSanitizesExplicitPathErrors(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	t.Setenv("MAILBOX_CONFIG", filepath.Join(t.TempDir(), "missing-"+payload+".toml"))
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig unexpectedly accepted missing config")
	}
	if strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), "clipboard") {
		t.Fatalf("config error leaked terminal control text: %q", err)
	}
}

func TestLoadConfigAbsentDefaultPathIsNoConfigMode(t *testing.T) {
	t.Setenv("MAILBOX_CONFIG", "")
	os.Unsetenv("MAILBOX_CONFIG")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty dir: no mailbox/config.toml
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NoConfig() || len(cfg.Accounts) != 1 || cfg.Accounts[0].Name != "default" || cfg.Accounts[0].Read != nil {
		t.Fatalf("cfg = %+v, want implicit sourceless default account", cfg)
	}
	wantPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mailbox", "config.toml")
	needs := credentialError(cfg, cfg.Accounts[0], ClassRead, nil, ReasonNoSource)
	if !strings.Contains(needs.Error(), wantPath) {
		t.Fatalf("no-config guidance = %q, want %q", needs, wantPath)
	}
}

func TestConfigResolveAccount(t *testing.T) {
	stubCmds(t, "my-token-helper", "my-approver")
	writeConfig(t, validTwoAccounts)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv("MAILBOX_ACCOUNT", "work")
		acct, err := cfg.ResolveAccount("personal")
		if err != nil || acct.Name != "personal" {
			t.Fatalf("= %v, %v; want personal", acct, err)
		}
	})
	t.Run("env second", func(t *testing.T) {
		t.Setenv("MAILBOX_ACCOUNT", "personal")
		acct, err := cfg.ResolveAccount("")
		if err != nil || acct.Name != "personal" {
			t.Fatalf("= %v, %v; want personal", acct, err)
		}
	})
	t.Run("default third", func(t *testing.T) {
		os.Unsetenv("MAILBOX_ACCOUNT")
		acct, err := cfg.ResolveAccount("")
		if err != nil || acct.Name != "work" {
			t.Fatalf("= %v, %v; want work (default_account)", acct, err)
		}
	})
	t.Run("unknown lists names", func(t *testing.T) {
		_, err := cfg.ResolveAccount("nope")
		if err == nil || !strings.Contains(err.Error(), "work, personal") {
			t.Fatalf("err = %v, want configured names listed", err)
		}
	})
	t.Run("injection-shaped value rejected before lookup", func(t *testing.T) {
		_, err := cfg.ResolveAccount("../../etc")
		if err == nil {
			t.Fatal("want rejection of invalid account name syntax")
		}
	})
}

func TestLoadConfigSendCredentialSource(t *testing.T) {
	stubCmds(t, "my-send-helper")
	writeConfig(t, `
[accounts.work]
read_credential_env = "WORK_READ_JSON"
send_credential_cmd = ["my-send-helper"]
send_interactive = true
send_label = "hardware key touch"
`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	acct, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	if acct.Send == nil || !acct.Send.Interactive || acct.Send.Label != "hardware key touch" || acct.Send.ConfigKey != "accounts.work.send_credential_cmd" {
		t.Fatalf("send source = %+v", acct.Send)
	}
}

func TestSendCredentialCommandIsInteractiveByDefault(t *testing.T) {
	stubCmds(t, "my-send-helper")
	writeConfig(t, `
[accounts.work]
read_credential_env = "WORK_READ_JSON"
send_credential_cmd = ["my-send-helper"]
`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := cfg.Account("work")
	if acct.Send == nil || !acct.Send.Interactive {
		t.Fatalf("send source = %+v, want an interactive command source", acct.Send)
	}
}

func TestSendLabelRequiresCredentialSource(t *testing.T) {
	writeConfig(t, `
[accounts.work]
read_credential_env = "WORK_READ_JSON"
send_label = "hardware key touch"
`)

	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "send_label requires a send credential source") {
		t.Fatalf("LoadConfig error = %v, want send label source requirement", err)
	}
}

func TestSendCredentialEnvAndCommandConflict(t *testing.T) {
	stubCmds(t, "my-send-helper")
	writeConfig(t, `
[accounts.work]
read_credential_env = "WORK_READ_JSON"
send_credential_env = "ACME_SEND_OAUTH_JSON"
send_credential_cmd = ["my-send-helper"]
`)

	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "accounts.work.send") {
		t.Fatalf("LoadConfig error = %v, want send source conflict", err)
	}
}

func TestDuplicateEnvAcrossClassesFailsCompile(t *testing.T) {
	writeConfig(t, `
default_account = "work"
[accounts.work]
read_credential_env = "SHARED_OAUTH_JSON"
send_credential_env = "SHARED_OAUTH_JSON"
`)

	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "declared by both") {
		t.Fatalf("shared env var across classes compiled: %v", err)
	}
}
