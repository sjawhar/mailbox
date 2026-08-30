package auth

import (
	"strings"
	"testing"
)

func envTestConfig(t *testing.T) *Config {
	t.Helper()
	stubCmds(t, "my-approver")
	writeConfig(t, `
default_account = "work"
scrub_env = ["EXTRA_SENSITIVE", "ACME_SESSION_FILE"]
scrub_env_patterns = ["ACME_*_OAUTH"]
[accounts.work]
read_credential_env = "WORK_READ_JSON"
write_credential_cmd = ["my-approver"]
send_credential_cmd = ["my-approver"]
credential_env_passthrough = ["ACME_SESSION_FILE", "ACME_REGION"]
[accounts.personal]
read_credential_env = "PERSONAL_READ_JSON"
write_credential_env = "PERSONAL_WRITE_JSON"
send_credential_env = "ACME_SEND_OAUTH_JSON"
`)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func envNames(env []string) map[string]string {
	names := make(map[string]string, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		names[name] = value
	}
	return names
}

func countEnvName(env []string, want string) int {
	count := 0
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == want {
			count++
		}
	}
	return count
}

// This fails if config scrubbing omits any deny source or treats a passthrough
// declaration as a deny rule.
func TestScrubbedEnviron(t *testing.T) {
	cfg := envTestConfig(t)
	for name, value := range map[string]string{
		"MAILBOX_TOKEN":            "decoy",
		"MAILBOX_TOKEN_URL":        "http://evil.example/token",
		"MAILBOX_CONFIG":           "/tmp/decoy.toml",
		"WORK_READ_JSON":           "decoy",
		"PERSONAL_READ_JSON":       "decoy",
		"PERSONAL_WRITE_JSON":      "decoy",
		"ACME_SEND_OAUTH_JSON":     "decoy",
		"EXTRA_SENSITIVE":          "decoy",
		"ACME_WORK_OAUTH":          "decoy",
		"ACME_SESSION_FILE":        "session",
		"ACME_REGION":              "kept",
		"MAILBOX_CREDENTIAL_DEPTH": "1",
		"MAILBOX_UNRELATED":        "kept",
	} {
		t.Setenv(name, value)
	}

	got := envNames(ScrubbedEnviron(cfg))
	for _, denied := range []string{
		"MAILBOX_TOKEN",
		"MAILBOX_TOKEN_URL",
		"MAILBOX_CONFIG",
		"WORK_READ_JSON",
		"PERSONAL_READ_JSON",
		"PERSONAL_WRITE_JSON",
		"EXTRA_SENSITIVE",
		"ACME_WORK_OAUTH",
		"ACME_SEND_OAUTH_JSON",
		"ACME_SESSION_FILE",
	} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("ScrubbedEnviron leaked %s", denied)
		}
	}
	if got["MAILBOX_UNRELATED"] != "kept" || got["MAILBOX_CREDENTIAL_DEPTH"] != "1" || got["ACME_REGION"] != "kept" {
		t.Fatalf("kept vars wrong (a passthrough-only name must survive ordinary scrubbing): %v", got)
	}
}

// This fails if no-config handling loses the unconditional deny set or panics
// while inspecting configured rules that do not exist.
func TestScrubbedEnvironNilConfig(t *testing.T) {
	for name, value := range map[string]string{
		"MAILBOX_TOKEN":            "decoy",
		"MAILBOX_TOKEN_URL":        "http://evil.example/token",
		"MAILBOX_CONFIG":           "/tmp/decoy.toml",
		"MAILBOX_CREDENTIAL_DEPTH": "1",
		"MAILBOX_UNRELATED":        "kept",
	} {
		t.Setenv(name, value)
	}

	got := envNames(ScrubbedEnviron(nil))
	for _, denied := range []string{"MAILBOX_TOKEN", "MAILBOX_TOKEN_URL", "MAILBOX_CONFIG"} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("nil config leaked %s", denied)
		}
	}
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "1" || got["MAILBOX_UNRELATED"] != "kept" {
		t.Fatalf("nil config kept vars wrong: %v", got)
	}
}

// This fails if a credential child misses a declared exemption, leaks another
// account's credential, does not increment depth, or duplicates an existing
// passthrough-only variable.
func TestCredentialChildEnvironPassthroughExactness(t *testing.T) {
	cfg := envTestConfig(t)
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	for name, value := range map[string]string{
		"ACME_SESSION_FILE":    "session-path",
		"ACME_REGION":          "eu-1",
		"PERSONAL_READ_JSON":   "cross-account-decoy",
		"WORK_READ_JSON":       "own-credential-decoy",
		"PERSONAL_WRITE_JSON":  "cross-account-write-decoy",
		"ACME_SEND_OAUTH_JSON": "cross-account-send-decoy",
		"MAILBOX_TOKEN":        "decoy",
		"MAILBOX_TOKEN_URL":    "http://evil.example/token",
		"MAILBOX_CONFIG":       "/tmp/decoy.toml",
		"EXTRA_SENSITIVE":      "unrelated-scrubbed-decoy",
		"ACME_WORK_OAUTH":      "unrelated-pattern-decoy",
	} {
		t.Setenv(name, value)
	}

	env := CredentialChildEnviron(cfg, work, ClassRead)
	got := envNames(env)
	if got["ACME_SESSION_FILE"] != "session-path" {
		t.Fatalf("passthrough var removed by scrub_env was not re-added: %v", got)
	}
	if got["ACME_REGION"] != "eu-1" {
		t.Fatalf("passthrough-only var missing from credential child: %v", got)
	}
	if count := countEnvName(env, "ACME_REGION"); count != 1 {
		t.Fatalf("ACME_REGION appears %d times, want exactly once: %v", count, env)
	}
	for _, denied := range []string{
		"WORK_READ_JSON",
		"PERSONAL_READ_JSON",
		"PERSONAL_WRITE_JSON",
		"MAILBOX_TOKEN",
		"ACME_SEND_OAUTH_JSON",
		"MAILBOX_TOKEN_URL",
		"MAILBOX_CONFIG",
		"EXTRA_SENSITIVE",
		"ACME_WORK_OAUTH",
	} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("credential child leaked %s", denied)
		}
	}
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "1" {
		t.Fatalf("depth = %q, want 1 (unset parent increments to 1)", got["MAILBOX_CREDENTIAL_DEPTH"])
	}

	t.Setenv("MAILBOX_CREDENTIAL_DEPTH", "1")
	got = envNames(CredentialChildEnviron(cfg, work, ClassRead))
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "2" {
		t.Fatalf("depth = %q, want incremented 2", got["MAILBOX_CREDENTIAL_DEPTH"])
	}
}

// This fails if child-depth normalization changes the shared parser's handling
// of non-positive, malformed, overflowing, or huge inherited sentinels.
func TestCredentialChildEnvironClampsInvalidDepth(t *testing.T) {
	cfg := envTestConfig(t)
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	for _, test := range []struct {
		name    string
		current string
		want    string
	}{
		{name: "negative", current: "-5", want: "1"},
		{name: "malformed", current: "abc", want: "1"},
		{name: "empty", current: "", want: "1"},
		{name: "overflowing", current: "9999999999999999999999999999999999999999", want: "1"},
		{name: "max-int64", current: "9223372036854775807", want: "2"},
		{name: "max-int64-minus-one", current: "9223372036854775806", want: "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MAILBOX_CREDENTIAL_DEPTH", test.current)

			got := envNames(CredentialChildEnviron(cfg, work, ClassRead))
			if got["MAILBOX_CREDENTIAL_DEPTH"] != test.want {
				t.Fatalf("depth = %q, want %s for parent depth %q", got["MAILBOX_CREDENTIAL_DEPTH"], test.want, test.current)
			}
		})
	}
}

// This fails if a configured exact or pattern scrub rule can remove the depth
// sentinel needed by credential command recursion detection.
func TestScrubbedEnvironKeepsDepthDespiteScrubRule(t *testing.T) {
	cfg := &Config{
		ScrubEnv:         []string{"MAILBOX_CREDENTIAL_DEPTH"},
		ScrubEnvPatterns: []string{"MAILBOX_*_DEPTH"},
	}
	t.Setenv("MAILBOX_CREDENTIAL_DEPTH", "1")

	got := envNames(ScrubbedEnviron(cfg))
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "1" {
		t.Fatalf("depth = %q, want retained depth sentinel", got["MAILBOX_CREDENTIAL_DEPTH"])
	}
}

// This fails if an invalid in-memory configuration can use passthrough to
// bypass the unconditional or cross-account credential deny rules.
func TestCredentialChildEnvironRefusesUnsafePassthrough(t *testing.T) {
	work := &AccountConfig{
		Name: "work",
		Read: &CredentialSource{Kind: SourceEnv, EnvVar: "WORK_READ_JSON"},
		Passthrough: []string{
			"MAILBOX_TOKEN",
			"WORK_READ_JSON",
			"PERSONAL_READ_JSON",
		},
	}
	cfg := &Config{Accounts: []*AccountConfig{
		work,
		{Name: "personal", Read: &CredentialSource{Kind: SourceEnv, EnvVar: "PERSONAL_READ_JSON"}},
	}}
	for name, value := range map[string]string{
		"MAILBOX_TOKEN":      "decoy",
		"WORK_READ_JSON":     "own-credential-decoy",
		"PERSONAL_READ_JSON": "cross-account-decoy",
	} {
		t.Setenv(name, value)
	}

	got := envNames(CredentialChildEnviron(cfg, work, ClassRead))
	for _, denied := range []string{"MAILBOX_TOKEN", "WORK_READ_JSON", "PERSONAL_READ_JSON"} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("credential child leaked unsafe passthrough %s", denied)
		}
	}
}

func TestCredentialChildScrubsOtherClassCredentials(t *testing.T) {
	cfg := envTestConfig(t)
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	for name, value := range map[string]string{
		"ACME_SEND_OAUTH_JSON": "canary-send",
		"WORK_READ_JSON":       "canary-read",
		"PERSONAL_READ_JSON":   "canary-personal-read",
		"PERSONAL_WRITE_JSON":  "canary-personal-write",
	} {
		t.Setenv(name, value)
	}

	for _, class := range []Class{ClassRead, ClassWrite, ClassSend} {
		got := envNames(CredentialChildEnviron(cfg, work, class))
		for _, denied := range []string{"ACME_SEND_OAUTH_JSON", "WORK_READ_JSON", "PERSONAL_READ_JSON", "PERSONAL_WRITE_JSON"} {
			if _, leaked := got[denied]; leaked {
				t.Errorf("%s child leaked %s", class, denied)
			}
		}
	}
}

// This fails if a class-private credential passthrough survives scrubbing for
// another credential child or the requested child does not restore its own.
func TestCredentialChildEnvironScopesClassPassthrough(t *testing.T) {
	writeConfig(t, `
[accounts.work]
read_credential_env = "WORK_READ_JSON"
write_credential_env = "WORK_WRITE_JSON"
send_credential_env = "WORK_SEND_JSON"
credential_env_passthrough = ["SHARED_CANARY"]
read_credential_env_passthrough = ["READ_CANARY"]
send_credential_env_passthrough = ["SEND_CANARY"]
`)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	for name, value := range map[string]string{
		"SHARED_CANARY": "shared",
		"READ_CANARY":   "read",
		"SEND_CANARY":   "send",
	} {
		t.Setenv(name, value)
	}

	for _, test := range []struct {
		class  Class
		readOK bool
		sendOK bool
	}{
		{class: ClassRead, readOK: true},
		{class: ClassWrite},
		{class: ClassSend, sendOK: true},
	} {
		t.Run(string(test.class), func(t *testing.T) {
			got := envNames(CredentialChildEnviron(cfg, work, test.class))
			if got["SHARED_CANARY"] != "shared" {
				t.Fatalf("%s child shared passthrough = %q, want shared", test.class, got["SHARED_CANARY"])
			}
			if _, exists := got["READ_CANARY"]; exists != test.readOK {
				t.Fatalf("%s child read passthrough present = %v, want %v: %v", test.class, exists, test.readOK, got)
			}
			if _, exists := got["SEND_CANARY"]; exists != test.sendOK {
				t.Fatalf("%s child send passthrough present = %v, want %v: %v", test.class, exists, test.sendOK, got)
			}
		})
	}
}
