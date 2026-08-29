package auth

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mintProbeMain() {
	if len(os.Args) != 4 || os.Args[2] != "--env" {
		os.Exit(64)
	}
	if path := os.Getenv("PROBE_MINT_ENV_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	if err := RunMintChild(context.Background(), os.Args[3], os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func TestRunMintChildContract(t *testing.T) {
	t.Run("rejects invalid --env names before looking up the environment", func(t *testing.T) {
		for _, value := range []string{"", "BAD-NAME", "A=B"} {
			var stdout bytes.Buffer
			if err := RunMintChild(context.Background(), value, &stdout); err == nil {
				t.Fatalf("RunMintChild accepted invalid environment name %q", value)
			}
		}
	})

	t.Run("refuses MAILBOX_TOKEN verbatim", func(t *testing.T) {
		t.Setenv("MAILBOX_TOKEN", "caller-token")
		var stdout bytes.Buffer
		err := RunMintChild(context.Background(), "MINT_OAUTH", &stdout)
		if err == nil || !strings.Contains(err.Error(), "MAILBOX_TOKEN must not be set in a __mint child") {
			t.Fatalf("RunMintChild error = %v, want MAILBOX_TOKEN refusal", err)
		}
	})

	t.Run("emits one strict object without loading config or writing the cache", func(t *testing.T) {
		cache := t.TempDir()
		t.Setenv("MAILBOX_CACHE_DIR", cache)
		t.Setenv("MINT_OAUTH", oauthJSON())
		t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, 200, `{"access_token":"minted-token","expires_in":3600}`))
		t.Setenv("MAILBOX_CONFIG", filepath.Join(t.TempDir(), "missing-config.toml"))
		var stdout bytes.Buffer
		if err := RunMintChild(context.Background(), "MINT_OAUTH", &stdout); err != nil {
			t.Fatal(err)
		}
		token, err := parseMintOutput(stdout.Bytes())
		if err != nil || token.AccessToken != "minted-token" {
			t.Fatalf("strict mint output = %+v, %v", token, err)
		}
		entries, err := os.ReadDir(cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("mint cache entries = %v, want none", entries)
		}
	})

	t.Run("rejects malformed JSON rather than emitting a partial object", func(t *testing.T) {
		t.Setenv("MINT_OAUTH", `{"client_id":""}`)
		var stdout bytes.Buffer
		if err := RunMintChild(context.Background(), "MINT_OAUTH", &stdout); err == nil || stdout.Len() != 0 {
			t.Fatalf("RunMintChild error, stdout = %v, %q; want loud failure with no output", err, stdout.String())
		}
	})
}

func TestMintProbeUsesEnvContract(t *testing.T) {
	cache := t.TempDir()
	envFile := filepath.Join(t.TempDir(), "mint-env")
	server := tokenServer(t, 200, `{"access_token":"minted-token","expires_in":3600}`)
	cmd := exec.Command(os.Args[0], "__mint", "--env", "MINT_OAUTH")
	cmd.Env = []string{
		"PROBE_MINT_ENV_FILE=" + envFile,
		"MINT_OAUTH=" + oauthJSON(),
		"MAILBOX_TOKEN_URL=" + server,
		"MAILBOX_CACHE_DIR=" + cache,
		"MAILBOX_CREDENTIAL_DEPTH=1",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("__mint child: %v: %s", err, output)
	}
	token, err := parseMintOutput(output)
	if err != nil || token.AccessToken != "minted-token" {
		t.Fatalf("child output = %+v, %v", token, err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "MAILBOX_CREDENTIAL_DEPTH=1") {
		t.Fatalf("child environment = %q, want depth sentinel", env)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("mint cache entries = %v, want none", entries)
	}
}

func TestParseMintOutputRejectsUnknownOrTrailingContent(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"access_token":"token","expiry":"2099-01-01T00:00:00Z","extra":true}`),
		[]byte(`{"access_token":"token","expiry":"2099-01-01T00:00:00Z"}{}`),
	}
	for _, output := range cases {
		if _, err := parseMintOutput(output); err == nil {
			t.Fatalf("parseMintOutput accepted %q", output)
		}
	}
}
