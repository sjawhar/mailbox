package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupMutationEnv strips every ambient credential, installs a recording stub
// `secrets` on PATH, points Gmail at the test server, and returns the stub's
// argv record file.
//
// EXEC SAFETY: mutation subcommands never call EnsureEnv, so nothing in this
// suite can syscall.Exec the test process — and that is itself under test: a
// regression routing a mutation subcommand back through the read-path
// re-exec would replace the test binary with the stub and fail unmissably.
func setupMutationEnv(t *testing.T, g *gmailTestServer, env map[string]string) string {
	t.Helper()
	stubs := t.TempDir()
	argvFile := filepath.Join(stubs, "secrets-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvFile + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubs, "secrets"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubs+":/usr/bin:/bin")
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_DMI_SYS_VENDOR", filepath.Join(t.TempDir(), "absent"))
	for _, name := range []string{"MAILBOX_TOKEN", "MAILBOX_SECRETS_REEXEC", "GWS_ACCOUNT",
		"GWS_WORK_READ_OAUTH", "GWS_PERSONAL_READ_OAUTH",
		"GWS_WORK_MODIFY_OAUTH", "GWS_PERSONAL_MODIFY_OAUTH",
		"GWS_WORK_MAIL_OAUTH", "GWS_PERSONAL_MAIL_OAUTH"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	return argvFile
}

func runMutationCLI(t *testing.T, g *gmailTestServer, env map[string]string, args ...string) (int, string, string, string) {
	t.Helper()
	argvFile := setupMutationEnv(t, g, env)
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String(), argvFile
}

func recordedSecretsArgv(t *testing.T, argvFile string) string {
	t.Helper()
	data, err := os.ReadFile(argvFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// F9: with no resolvable mutation credential the envelope is emitted and the
// process exits 1 — nothing is exec'd, nothing is spawned (a reintroduced
// read re-exec would replace this test process with the stub).
func TestMutationEnvelopeWithoutCredential(t *testing.T) {
	g := newGmailTestServer(t)
	code, stdout, stderr, argvFile := runMutationCLI(t, g, nil, "archive", "t1", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q, want 1", code, stderr)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Key     string `json:"key"`
			Command string `json:"command"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "needs_mutation_credential" ||
		envelope.Error.Key != "GWS_WORK_MODIFY_OAUTH" ||
		envelope.Error.Command != "secrets GWS_WORK_MODIFY_OAUTH -- mailbox archive t1 --json" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if got := recordedSecretsArgv(t, argvFile); got != "" {
		t.Fatalf("secrets invoked from the CLI surface: %q", got)
	}
}

func TestMutationEnvelopeStderrWithoutJSON(t *testing.T) {
	g := newGmailTestServer(t)
	code, stdout, stderr, _ := runMutationCLI(t, g, nil, "--account", "personal", "trash", "t1")
	if code != 1 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q, want 1 with empty stdout", code, stdout)
	}
	want := "mutation credentials for personal are human-tier; run: secrets GWS_PERSONAL_MODIFY_OAUTH -- mailbox --account personal trash t1"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

// The C ruling (F10): every mutation subcommand, with a stub secrets on PATH,
// never produces a secrets invocation naming a modify key — in both the
// no-credential (envelope) and credential-in-env (act) setups.
func TestCLINeverNamesModifyKeysToSecrets(t *testing.T) {
	commands := [][]string{
		{"archive", "t1"},
		{"trash", "t1"},
		{"mark", "read", "t1"},
		{"label", "add", "Newsletters", "t1"},
	}
	for _, argv := range commands {
		t.Run("no credential "+argv[0], func(t *testing.T) {
			g := newGmailTestServer(t)
			code, _, _, argvFile := runMutationCLI(t, g, nil, argv...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if got := recordedSecretsArgv(t, argvFile); got != "" {
				t.Fatalf("secrets invoked: %q", got)
			}
		})
		t.Run("credential in env "+argv[0], func(t *testing.T) {
			g := newGmailTestServer(t)
			g.mutationToken = "mut-tok"
			argvFile := setupMutationEnv(t, g, map[string]string{
				"GWS_WORK_MODIFY_OAUTH": `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`,
				"MAILBOX_TOKEN_URL":     g.tokenURL(t, "mut-tok"),
			})
			var stdout, stderr bytes.Buffer
			code := Run(argv, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
			}
			if got := recordedSecretsArgv(t, argvFile); got != "" {
				t.Fatalf("secrets invoked from the CLI surface: %q", got)
			}
		})
	}
}

// Spec §6, the whole sentence as a test: with the credential in env — even
// with a COLD read cache — the command refreshes in-process, acts, and
// exits 0. Nothing cached, nothing spawned. Reads inside the mutation
// subcommand ride the same gmail.modify token.
func TestColdCacheMutationRefreshesActsAndExits(t *testing.T) {
	g := newGmailTestServer(t)
	g.mutationToken = "mut-tok"
	argvFile := setupMutationEnv(t, g, map[string]string{
		"GWS_WORK_MODIFY_OAUTH": `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`,
		"MAILBOX_TOKEN_URL":     g.tokenURL(t, "mut-tok"),
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"archive", "t1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "archived 1 thread(s)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	entries, err := os.ReadDir(os.Getenv("MAILBOX_CACHE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache entries = %v, want none (nothing cached)", entries)
	}
	if got := recordedSecretsArgv(t, argvFile); got != "" {
		t.Fatalf("secrets spawned during an env-credential mutation: %q", got)
	}
}

// TestMutationIncidentalReadScopeHintUsesMutationCredential catches raw
// thread and label resolution scope errors rendered as read-token failures.
func TestMutationIncidentalReadScopeHintUsesMutationCredential(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{
			name: "raw thread with pinned token",
			env:  map[string]string{"MAILBOX_TOKEN": "test-token"},
			args: []string{"archive", "t1"},
			want: "MAILBOX_TOKEN lacks the gmail.modify scope",
		},
		{
			name: "label name with environment token",
			env: map[string]string{
				"GWS_WORK_MODIFY_OAUTH": `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`,
			},
			args: []string{"label", "add", "Newsletters", "t1"},
			want: "GWS_WORK_MODIFY_OAUTH lacks the gmail.modify scope",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			g.readForbidden = true
			env := testCase.env
			if env["GWS_WORK_MODIFY_OAUTH"] != "" {
				env["MAILBOX_TOKEN_URL"] = g.tokenURL(t, "mut-tok")
				g.mutationToken = "mut-tok"
			}

			code, _, stderr, _ := runMutationCLI(t, g, env, testCase.args...)
			if code != 1 {
				t.Fatalf("exit = %d, stderr = %q, want 1", code, stderr)
			}
			if !strings.Contains(stderr, testCase.want) {
				t.Fatalf("stderr = %q, want mutation hint %q", stderr, testCase.want)
			}
			if strings.Contains(stderr, "GWS_WORK_READ_OAUTH") {
				t.Fatalf("stderr = %q, must not name the read credential", stderr)
			}
		})
	}
}
