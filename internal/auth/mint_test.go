package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSecretsMint writes a stub `secrets` that records its argv and prints
// stdoutScript's output INSTEAD of exec'ing the real child — the hostile
// "shim chatter" adversary of F11.
func stubSecretsMint(t *testing.T, dir, stdoutScript string) (argvFile string) {
	t.Helper()
	argvFile = filepath.Join(dir, "secrets-argv")
	writeStub(t, dir, "secrets",
		`printf '%s\n' "$0" "$@" > `+argvFile+`
`+stdoutScript)
	return argvFile
}

// mintProbeMain impersonates the real `mailbox __mint` child when ExecMinter
// re-execs this test binary (dispatched from TestMain): it records the
// environment THIS child actually inherited — the F3 contract is about the
// exec'd child, not the secrets stub — then runs the real child logic so the
// parent parses real output.
func mintProbeMain() {
	if err := os.WriteFile(os.Getenv("PROBE_MINT_ENV_FILE"), []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(os.Args) != 4 || os.Args[2] != "--account" {
		fmt.Fprintf(os.Stderr, "mint probe: unexpected argv %q\n", os.Args)
		os.Exit(1)
	}
	account, err := ResolveAccount(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := RunMintChild(context.Background(), account, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func execMinterEnv(t *testing.T, stubs string) {
	t.Helper()
	clearCredentialEnv(t)
	t.Setenv("PATH", stubs+":/usr/bin:/bin")
}

func TestExecMinterEnvFastPathNeverSpawns(t *testing.T) {
	execMinterEnv(t, t.TempDir()) // PATH has no secrets binary: a spawn would fail loudly
	t.Setenv("GWS_WORK_MODIFY_OAUTH", oauthJSON())
	t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, http.StatusOK, `{"access_token":"env-tok","expires_in":3600}`))

	minter := &ExecMinter{}
	token, err := minter.Mint(context.Background(), AccountWork)
	if err != nil || token.AccessToken != "env-tok" || token.Route != RouteMutationEnv {
		t.Fatalf("Mint = %+v, %v, want env fast-path token", token, err)
	}
}

// mintChildBannedNames is the exhaustive list of credential-class variables
// that must never reach the __mint child (F3). Each is seeded as a decoy so
// its absence in the child is meaningful. Of the credential class, exactly
// two variables are allowed in the child: the secrets-injected modify key
// and SECRETSD_SESSION_TOKEN_FILE.
var mintChildBannedNames = []string{
	"GWS_WORK_READ_OAUTH",
	"GWS_PERSONAL_READ_OAUTH",
	"GWS_WORK_MAIL_OAUTH",
	"GWS_PERSONAL_MAIL_OAUTH",
	"GWS_WORK_MODIFY_OAUTH", // the NON-injected account's modify key (mint targets personal)
	"MAILBOX_TOKEN",
	"MAILBOX_SECRETS_REEXEC",
}

// The full spawn chain with a REAL exec'd child: the stub secrets injects the
// key and execs `<self> __mint --account personal` exactly like the real
// secrets does; mintProbeMain (via TestMain) records the environment the
// child actually inherited, then runs the real child logic against the stub
// token endpoint, so the parent parses REAL child output (F3 both directions,
// argv shape, and the exec chain itself).
func TestExecMinterSpawnShapeAndRealChildEnv(t *testing.T) {
	stubs := t.TempDir()
	argvFile := filepath.Join(stubs, "secrets-argv")
	writeStub(t, stubs, "secrets",
		`printf '%s\n' "$0" "$@" > `+argvFile+`
key="$1"; shift; [ "$1" = "--" ] && shift
export "$key=$STUB_SECRET_VALUE"
export "MAILBOX_TOKEN_URL=$STUB_MINT_TOKEN_URL"
exec "$@"`)
	execMinterEnv(t, stubs)
	envFile := filepath.Join(t.TempDir(), "mint-child-env")
	t.Setenv("PROBE_MINT_ENV_FILE", envFile)
	t.Setenv("STUB_SECRET_VALUE", oauthJSON())
	t.Setenv("MAILBOX_TOKEN_URL", "http://parent-decoy.invalid")
	mintTokenURL := tokenServer(t, http.StatusOK, `{"access_token":"minted-tok","expires_in":3600}`)
	t.Setenv("STUB_MINT_TOKEN_URL", mintTokenURL)
	t.Setenv("SECRETSD_SESSION_TOKEN_FILE", "/run/user/1000/secretsd/session")
	for _, name := range mintChildBannedNames {
		t.Setenv(name, "decoy-should-not-leak")
	}

	minter := &ExecMinter{}
	token, err := minter.Mint(context.Background(), AccountPersonal)
	if err != nil || token.AccessToken != "minted-tok" || token.Route != RouteMint {
		t.Fatalf("Mint = %+v, %v", token, err)
	}

	// argv shape: secrets <KEY> -- <abs-self> __mint --account <a>
	argvData, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(argvData)), "\n")
	want := []string{filepath.Join(stubs, "secrets"), "GWS_PERSONAL_MODIFY_OAUTH", "--", self, "__mint", "--account", "personal"}
	if len(argv) != len(want) {
		t.Fatalf("secrets argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("secrets argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}

	// The environment the real exec'd child inherited (F3, both directions).
	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("mint child never ran: %v", err)
	}
	childEnv := "\n" + string(envData) + "\n"
	if !strings.Contains(childEnv, "\nSECRETSD_SESSION_TOKEN_FILE=/run/user/1000/secretsd/session\n") {
		t.Fatal("mint child env lacks SECRETSD_SESSION_TOKEN_FILE (F3 re-injection)")
	}
	if !strings.Contains(childEnv, "\nGWS_PERSONAL_MODIFY_OAUTH="+oauthJSON()+"\n") {
		t.Fatal("mint child env lacks the secrets-injected modify key: exec chain broken")
	}
	if strings.Contains(childEnv, "\nMAILBOX_TOKEN_URL=http://parent-decoy.invalid\n") {
		t.Fatal("mint child env inherited the parent MAILBOX_TOKEN_URL")
	}
	if !strings.Contains(childEnv, "\nMAILBOX_TOKEN_URL="+mintTokenURL+"\n") {
		t.Fatal("mint child env lacks the stub-injected MAILBOX_TOKEN_URL")
	}
	for _, name := range mintChildBannedNames {
		if strings.Contains(childEnv, "\n"+name+"=") {
			t.Fatalf("mint child env leaked %s: %q", name, childEnv)
		}
	}
	// Sweep: of the credential class, ONLY the injected key may remain — any
	// other GWS_*_OAUTH variable reaching the child is a scrub failure, even
	// one this list has never heard of.
	for _, line := range strings.Split(strings.TrimSpace(string(envData)), "\n") {
		name, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if strings.HasPrefix(name, "GWS_") && strings.HasSuffix(name, "_OAUTH") && name != "GWS_PERSONAL_MODIFY_OAUTH" {
			t.Fatalf("unexpected OAuth credential in mint child env: %q", line)
		}
	}
}

func TestExecMinterStrictStdout(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{name: "unknown fields", script: `printf '{"access_token":"t","expiry":"2999-01-02T15:04:05Z","extra":"x"}\n'`},
		{name: "trailing bytes", script: `printf '{"access_token":"t","expiry":"2999-01-02T15:04:05Z"}\ngarbage\n'`},
		{name: "two objects", script: `printf '{"access_token":"t","expiry":"2999-01-02T15:04:05Z"}{"access_token":"u","expiry":"2999-01-02T15:04:05Z"}\n'`},
		{name: "oversize", script: `head -c 20000 /dev/zero | tr '\0' 'a'`},
		{name: "empty token", script: `printf '{"access_token":"","expiry":"2999-01-02T15:04:05Z"}\n'`},
		{name: "expired token", script: `printf '{"access_token":"t","expiry":"2001-01-02T15:04:05Z"}\n'`},
		{name: "bad expiry", script: `printf '{"access_token":"t","expiry":"tomorrow"}\n'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubs := t.TempDir()
			stubSecretsMint(t, stubs, tc.script)
			execMinterEnv(t, stubs)
			minter := &ExecMinter{}
			if _, err := minter.Mint(context.Background(), AccountWork); err == nil {
				t.Fatal("Mint accepted a corrupt __mint stdout (F11)")
			}
		})
	}
}

func TestExecMinterChildFailureCarriesStderr(t *testing.T) {
	stubs := t.TempDir()
	writeStub(t, stubs, "secrets", `echo "REQUEST denied by policy" >&2; exit 3`)
	execMinterEnv(t, stubs)

	var sink bytes.Buffer
	minter := &ExecMinter{Stderr: &sink}
	_, err := minter.Mint(context.Background(), AccountWork)
	if err == nil || !strings.Contains(err.Error(), "REQUEST denied by policy") {
		t.Fatalf("Mint error = %v, want embedded child stderr", err)
	}
	if !strings.Contains(sink.String(), "REQUEST denied by policy") {
		t.Fatalf("stderr sink = %q, want live child stderr (F12)", sink.String())
	}
}

func TestRunMintChildContract(t *testing.T) {
	t.Run("refreshes and prints one object", func(t *testing.T) {
		clearCredentialEnv(t)
		t.Setenv("GWS_WORK_MODIFY_OAUTH", oauthJSON())
		t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, http.StatusOK, `{"access_token":"child-tok","expires_in":3600}`))
		var stdout bytes.Buffer
		if err := RunMintChild(context.Background(), AccountWork, &stdout); err != nil {
			t.Fatal(err)
		}
		token, err := parseMintOutput(stdout.Bytes())
		if err != nil {
			t.Fatalf("child stdout violates the strict contract: %v (stdout=%q)", err, stdout.String())
		}
		if token.AccessToken != "child-tok" {
			t.Fatalf("access_token = %q", token.AccessToken)
		}
	})
	t.Run("absent env is loud with empty stdout", func(t *testing.T) {
		clearCredentialEnv(t)
		var stdout bytes.Buffer
		err := RunMintChild(context.Background(), AccountPersonal, &stdout)
		var needs *NeedsSecretsError
		if !errors.As(err, &needs) || needs.Key != "GWS_PERSONAL_MODIFY_OAUTH" {
			t.Fatalf("error = %v, want NeedsSecretsError for the modify key", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})
	t.Run("malformed env is loud with empty stdout", func(t *testing.T) {
		clearCredentialEnv(t)
		t.Setenv("GWS_WORK_MODIFY_OAUTH", `{"client_id":""}`)
		var stdout bytes.Buffer
		err := RunMintChild(context.Background(), AccountWork, &stdout)
		if err == nil || !strings.Contains(err.Error(), "GWS_WORK_MODIFY_OAUTH") {
			t.Fatalf("error = %v, want malformed-credential diagnostic naming the key", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})
	t.Run("MAILBOX_TOKEN present is an error not an override (F11)", func(t *testing.T) {
		clearCredentialEnv(t)
		t.Setenv("GWS_WORK_MODIFY_OAUTH", oauthJSON())
		t.Setenv("MAILBOX_TOKEN", "pinned")
		var stdout bytes.Buffer
		err := RunMintChild(context.Background(), AccountWork, &stdout)
		if err == nil || !strings.Contains(err.Error(), "MAILBOX_TOKEN") {
			t.Fatalf("error = %v, want MAILBOX_TOKEN rejection", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})
}
