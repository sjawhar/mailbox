package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestCredentialCommandChildEnvironmentIsIsolated(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "child-env")
	writeStub(t, dir, "env-helper", `
printf '%s|%s|%s|%s|%s|%s|%s\n' "${PASSTHROUGH:-}" "${MAILBOX_TOKEN:-}" "${MAILBOX_TOKEN_URL:-}" "${MAILBOX_CONFIG:-}" "${PERSONAL_READ:-}" "${SCRUB_THIS:-}" "${PATTERN_OAUTH:-}" > "$ENV_RECORD"
printf '%s\n' 'isolated.command.token-value-1234567890'`)
	configPath := filepath.Join(dir, "config.toml")
	config := `default_account = "work"
scrub_env = ["SCRUB_THIS"]
scrub_env_patterns = ["PATTERN_*"]
[accounts.work]
read_credential_cmd = ["env-helper"]
credential_env_passthrough = ["PASSTHROUGH"]
[accounts.personal]
read_credential_env = "PERSONAL_READ"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("MAILBOX_CONFIG", configPath)
	t.Setenv("MAILBOX_TOKEN", "must-not-leak")
	t.Setenv("MAILBOX_TOKEN_URL", "http://must-not-leak")
	t.Setenv("PERSONAL_READ", "cross-account-credential")
	t.Setenv("SCRUB_THIS", "configured-scrub")
	t.Setenv("PATTERN_OAUTH", "pattern-scrub")
	t.Setenv("PASSTHROUGH", "kept")
	t.Setenv("ENV_RECORD", record)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	work, ok := cfg.Account("work")
	if !ok {
		t.Fatal("work account missing")
	}
	if _, err := runCredentialCmd(context.Background(), cfg, work, work.Read); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(data)), "kept||||||"; got != want {
		t.Fatalf("credential child environment = %q, want %q", got, want)
	}
}

func TestNonInteractiveCredentialCommandDoesNotInheritParentStdin(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "stdin-record")
	writeStub(t, dir, "stdin-helper", `
if IFS= read -r value; then
  printf 'read:%s\n' "$value" > "$STDIN_RECORD"
else
  printf 'eof\n' > "$STDIN_RECORD"
fi
printf '%s\n' 'stdin.command.token-value-1234567890'`)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = reader.Close()
		_ = writer.Close()
	})
	if _, err := writer.Write([]byte("producer input\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STDIN_RECORD", record)

	src := &CredentialSource{
		Class:     ClassRead,
		Kind:      SourceCmd,
		Argv:      []string{"stdin-helper"},
		Argv0:     filepath.Join(dir, "stdin-helper"),
		ConfigKey: "accounts.work.read_credential_cmd",
	}
	acct := &AccountConfig{Name: "work", Read: src, Passthrough: []string{"STDIN_RECORD"}}
	cfg := &Config{Path: filepath.Join(dir, "config.toml"), Accounts: []*AccountConfig{acct}, CredentialTimeout: defaultCredentialTimeout}
	if _, err := runCredentialCmd(context.Background(), cfg, acct, src); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(data)), "eof"; got != want {
		t.Fatalf("non-interactive credential command stdin result = %q, want %q", got, want)
	}
}

func TestCredentialCommandDepthSpawnGate(t *testing.T) {
	dir := t.TempDir()
	spawnFile := filepath.Join(dir, "spawned")
	writeStub(t, dir, "depth-helper", `printf spawned > "$DEPTH_SPAWN_FILE"; printf '%s\n' depth.command.token-value-1234567890`)
	acct := &AccountConfig{Name: "work"}
	src := &CredentialSource{Class: ClassRead, Kind: SourceCmd, Argv: []string{"depth-helper"}, Argv0: filepath.Join(dir, "depth-helper"), ConfigKey: "accounts.work.read_credential_cmd"}
	acct.Read = src
	cfg := &Config{Path: "/tmp/config.toml", Accounts: []*AccountConfig{acct}, CredentialTimeout: defaultCredentialTimeout}
	for _, test := range []struct {
		name      string
		depth     string
		wantSpawn bool
	}{
		{name: "negative", depth: "-5", wantSpawn: true},
		{name: "empty", depth: "", wantSpawn: true},
		{name: "malformed", depth: "abc"},
		{name: "maximum integer", depth: "9223372036854775807"},
		{name: "overflowing", depth: "9999999999999999999999999999999999999999"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(credentialDepthEnvironment, test.depth)
			t.Setenv("DEPTH_SPAWN_FILE", spawnFile)
			if err := os.Remove(spawnFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			_, err := runCredentialCmd(context.Background(), cfg, acct, src)
			if test.wantSpawn {
				if err != nil {
					t.Fatalf("runCredentialCmd error = %v", err)
				}
				if _, err := os.Stat(spawnFile); err != nil {
					t.Fatalf("credential command did not spawn: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "recursion") {
				t.Fatalf("runCredentialCmd error = %v, want loud recursion refusal", err)
			}
			if _, err := os.Stat(spawnFile); !os.IsNotExist(err) {
				t.Fatalf("credential command spawned despite depth refusal: %v", err)
			}
		})
	}
}

func TestCredentialCommandErrorFoldsDiagnosticToOneLine(t *testing.T) {
	src := &CredentialSource{ConfigKey: "accounts.work.read_credential_cmd", Argv0: "/tmp/helper"}
	err := credentialCommandError(src, diagnosticFrom("first helper line\nsecond helper line"), errors.New("helper failed"))
	if strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "first helper line second helper line") {
		t.Fatalf("credential command error = %q", err)
	}
}

func TestParseCredentialOutputRejectsTwoTrailingNewlines(t *testing.T) {
	if _, err := parseCredentialOutput([]byte("bare.command.token-value-1234567890\n\n")); err == nil {
		t.Fatal("credential output with two trailing newlines was accepted")
	}
}

func TestTailBufferKeepsFinalSanitizedDiagnosticWithinCaps(t *testing.T) {
	var tail tailBuffer
	tail.limit = mintStderrLimit
	final := "approval final note \x1b]52;c;clipboard\a"
	if _, err := tail.Write(append(bytes.Repeat([]byte("x"), mintStderrLimit+1024), []byte(final)...)); err != nil {
		t.Fatal(err)
	}
	if got := len(tail.String()); got != mintStderrLimit {
		t.Fatalf("tail length = %d, want %d", got, mintStderrLimit)
	}
	if !strings.Contains(tail.String(), final) {
		t.Fatalf("tail lost final diagnostic: %q", tail.String())
	}
	diagnostic := diagnosticFrom(tail.String())
	if len(diagnostic) > diagnosticLimit || strings.Contains(diagnostic, "\x1b") || !strings.Contains(diagnostic, "approval final note") {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
}

func TestParseMintOutputRejectsUnknownOrTrailingContent(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"access_token":"token","expiry":"2099-01-01T00:00:00Z","scope":"gmail.send","extra":true}`),
		[]byte(`{"access_token":"token","expiry":"2099-01-01T00:00:00Z","scope":"gmail.send"}{}`),
	}
	for _, output := range cases {
		if _, err := parseMintOutput(output); err == nil {
			t.Fatalf("parseMintOutput accepted %q", output)
		}
	}
}

func TestParseMintOutputRejectsMissingOrInvalidTokenFields(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"access_token":"","expiry":"2099-01-01T00:00:00Z"}`),
		[]byte(`{"access_token":"token","expiry":"2000-01-01T00:00:00Z"}`),
		[]byte(`{"access_token":"token","expiry":"not-a-time"}`),
		bytes.Repeat([]byte("x"), mintStdoutLimit+1),
	}
	for _, output := range cases {
		if _, err := parseMintOutput(output); err == nil {
			t.Fatalf("parseMintOutput accepted %q", output)
		}
	}
}

func TestRunMintChildPreservesOptionalScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  string
		wantScope string
	}{
		{
			name:      "scope present",
			response:  `{"access_token":"minted-token","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.send"}`,
			wantScope: "https://www.googleapis.com/auth/gmail.send",
		},
		{
			name:     "scope absent",
			response: `{"access_token":"minted-token","expires_in":3600}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MINT_OAUTH", oauthJSON())
			t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, 200, tc.response))
			var stdout bytes.Buffer
			if err := RunMintChild(context.Background(), "MINT_OAUTH", &stdout); err != nil {
				t.Fatal(err)
			}

			var output struct {
				AccessToken string `json:"access_token"`
				Expiry      string `json:"expiry"`
				Scope       string `json:"scope,omitempty"`
			}
			decoder := json.NewDecoder(&stdout)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&output); err != nil {
				t.Fatalf("decode strict mint output: %v", err)
			}
			var trailing json.RawMessage
			if err := decoder.Decode(&trailing); err != io.EOF {
				t.Fatalf("strict mint output has trailing content: %v", err)
			}
			if output.AccessToken != "minted-token" || output.Expiry == "" || output.Scope != tc.wantScope {
				t.Fatalf("mint output = %+v, want scope %q", output, tc.wantScope)
			}
			if tc.wantScope == "" && strings.Contains(stdout.String(), `"scope"`) {
				t.Fatalf("unscoped mint output included scope: %q", stdout.String())
			}
		})
	}
}

func TestParseMintOutputAcceptsScope(t *testing.T) {
	token, err := parseMintOutput([]byte(`{"access_token":"token","expiry":"2099-01-01T00:00:00Z","scope":"gmail.send"}`))
	if err != nil || token.Scope != "gmail.send" {
		t.Fatalf("parseMintOutput scope token = %+v, %v", token, err)
	}
}
