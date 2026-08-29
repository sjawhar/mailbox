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
credential_env_passthrough = ["ACME_SESSION_FILE", "ACME_REGION"]
[accounts.personal]
read_credential_env = "PERSONAL_READ_JSON"
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
		"EXTRA_SENSITIVE":          "decoy",
		"ACME_WORK_OAUTH":          "decoy",
		"ACME_SESSION_FILE":        "session",
		"ACME_REGION":              "kept",
		"MAILBOX_CREDENTIAL_DEPTH": "1",
		"MAILBOX_UNRELATED":        "kept",
	} {
		t.Setenv(name, value)
	}

	got := envNames(configScrubbedEnviron(cfg))
	for _, denied := range []string{
		"MAILBOX_TOKEN",
		"MAILBOX_TOKEN_URL",
		"MAILBOX_CONFIG",
		"WORK_READ_JSON",
		"PERSONAL_READ_JSON",
		"EXTRA_SENSITIVE",
		"ACME_WORK_OAUTH",
		"ACME_SESSION_FILE",
	} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("configScrubbedEnviron leaked %s", denied)
		}
	}
	if got["MAILBOX_UNRELATED"] != "kept" || got["MAILBOX_CREDENTIAL_DEPTH"] != "1" || got["ACME_REGION"] != "kept" {
		t.Fatalf("kept vars wrong (a passthrough-only name must survive ordinary scrubbing): %v", got)
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
		"ACME_SESSION_FILE":  "session-path",
		"ACME_REGION":        "eu-1",
		"PERSONAL_READ_JSON": "cross-account-decoy",
		"WORK_READ_JSON":     "own-credential-decoy",
		"MAILBOX_TOKEN":      "decoy",
	} {
		t.Setenv(name, value)
	}

	env := CredentialChildEnviron(cfg, work)
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
	for _, denied := range []string{"WORK_READ_JSON", "PERSONAL_READ_JSON", "MAILBOX_TOKEN", "MAILBOX_TOKEN_URL"} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("credential child leaked %s", denied)
		}
	}
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "1" {
		t.Fatalf("depth = %q, want 1 (unset parent increments to 1)", got["MAILBOX_CREDENTIAL_DEPTH"])
	}

	t.Setenv("MAILBOX_CREDENTIAL_DEPTH", "1")
	got = envNames(CredentialChildEnviron(cfg, work))
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "2" {
		t.Fatalf("depth = %q, want incremented 2", got["MAILBOX_CREDENTIAL_DEPTH"])
	}
}

// This fails if a malformed inherited recursion depth leaks into a child
// instead of being treated as the initial invocation.
func TestCredentialChildEnvironTreatsMalformedDepthAsZero(t *testing.T) {
	cfg := envTestConfig(t)
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	t.Setenv("MAILBOX_CREDENTIAL_DEPTH", "not-a-number")

	got := envNames(CredentialChildEnviron(cfg, work))
	if got["MAILBOX_CREDENTIAL_DEPTH"] != "1" {
		t.Fatalf("depth = %q, want 1 for malformed parent depth", got["MAILBOX_CREDENTIAL_DEPTH"])
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

	got := envNames(configScrubbedEnviron(cfg))
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

	got := envNames(CredentialChildEnviron(cfg, work))
	for _, denied := range []string{"MAILBOX_TOKEN", "WORK_READ_JSON", "PERSONAL_READ_JSON"} {
		if _, leaked := got[denied]; leaked {
			t.Errorf("credential child leaked unsafe passthrough %s", denied)
		}
	}
}
