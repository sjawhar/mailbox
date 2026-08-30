package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__mint" && os.Getenv("PROBE_MINT_ENV_FILE") != "" {
		mintProbeMain()
		return
	}
	if os.Getenv("MAILBOX_AUTH_PROBE") == "1" {
		probeMain()
		return
	}
	os.Exit(m.Run())
}

func probeMain() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	acct, err := cfg.ResolveAccount(os.Getenv("PROBE_ACCOUNT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	class := ClassRead
	switch os.Getenv("PROBE_CLASS") {
	case string(ClassWrite):
		class = ClassWrite
	case string(ClassSend):
		class = ClassSend
	}
	var acq Acquirer
	if os.Getenv("PROBE_SURFACE") == "tui" {
		acq = InteractiveExecAcquirer{Cfg: cfg}
	} else {
		acq = BatchAcquirer(cfg, acct, class)
	}
	source := NewSource(cfg, acct)
	if class == ClassWrite {
		token, err := source.WriteToken(context.Background(), acq)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("ROUTE=%s\nTOKEN=%s\nDIAG=%s\n", source.WriteRoute(), token, source.TakeDiagnostic(class))
		return
	}
	if class == ClassSend {
		token, err := source.SendToken(context.Background(), acq)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("ROUTE=%s\nTOKEN=%s\nDIAG=%s\n", source.SendRoute(), token.AccessToken, source.TakeDiagnostic(class))
		return
	}
	token, err := source.Resolve(context.Background(), acq)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("ROUTE=%s\nTOKEN=%s\nDIAG=%s\n", token.Route, token.AccessToken, source.TakeDiagnostic(class))
}

type probeEnv struct {
	stubs    string
	cache    string
	config   string
	leakFile string
	extra    map[string]string
}

type probeResult struct {
	stdout string
	stderr string
	exit   int
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newProbeEnv(t *testing.T) probeEnv {
	t.Helper()
	stubs, cache := t.TempDir(), t.TempDir()
	leak := filepath.Join(stubs, "leaks")
	writeStub(t, stubs, "token-helper", `
printf 'spawn\n' >> "${PROBE_SPAWN_FILE:-/dev/null}"
printf '%s\n' "$*" > "${PROBE_ARGV_FILE:-/dev/null}"
case "${STUB_MODE:-json}" in
json) printf '%s\n' '{"access_token":"command-json-token","expiry":"2099-01-01T00:00:00Z"}' ;;
bare) printf '%s\n' 'bare.command.token-value-1234567890' ;;
bad) printf '%s\n' 'short' ;;
chatter) printf 'chatter\nnoise\n' ;;
malformed) printf '%s\n' '{"access_token": broken' ;;
diag) printf '%s\n' 'diagnostic.command.token-1234567890'; printf 'grant expires in 7d\033]52;c;steal\a\n' >&2 ;;
# 96KiB of TOP-LEVEL builtin writes > capture cap (16KiB) + kernel pipe
# buffer (64KiB). The cap-tripped copier closes its read end, so the shell
# either blocks on the full pipe or dies of SIGPIPE mid-flood — it can never
# reach the completion write. (A smaller flood fits the pipe buffer and can
# finish before the trip; a pipeline moves the SIGPIPE to a child.)
oversize) i=0; while [ "$i" -lt 96 ]; do printf '%01024d' 0; i=$((i + 1)); done; printf completed > "${PROBE_COMPLETED_FILE:-/dev/null}" ;;
sleep) sleep 30 ;;
descendant) (sleep 30 >&1 &) ;;
*) echo "unknown stub mode" >&2; exit 64 ;;
esac`)
	writeStub(t, stubs, "approve-write", `
printf 'spawn\n' >> "${PROBE_SPAWN_FILE:-/dev/null}"
printf '%s\n' 'write.command.token-value-1234567890'`)
	return probeEnv{
		stubs:    stubs,
		cache:    cache,
		config:   filepath.Join(stubs, "config.toml"),
		leakFile: leak,
		extra: map[string]string{
			"PROBE_SPAWN_FILE":     filepath.Join(stubs, "spawns"),
			"PROBE_ARGV_FILE":      filepath.Join(stubs, "argv"),
			"PROBE_COMPLETED_FILE": filepath.Join(stubs, "completed"),
		},
	}
}

func writeProbeConfig(t *testing.T, pe probeEnv, body string) {
	t.Helper()
	if err := os.WriteFile(pe.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	pe.extra["MAILBOX_CONFIG"] = pe.config
}

func readCommandConfig(readSource, writeSource string, timeout int) string {
	var timeoutLine string
	if timeout != 0 {
		timeoutLine = fmt.Sprintf("credential_timeout_secs = %d\n", timeout)
	}
	return "default_account = \"work\"\n" + timeoutLine + "[accounts.work]\n" + readSource + writeSource
}

func execProbe(t *testing.T, pe probeEnv) probeResult {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = []string{
		"MAILBOX_AUTH_PROBE=1",
		"PATH=" + pe.stubs + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"MAILBOX_CACHE_DIR=" + pe.cache,
		"PROBE_LEAK_FILE=" + pe.leakFile,
	}
	for key, value := range pe.extra {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if status, ok := err.(*exec.ExitError); ok {
		exit = status.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return probeResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

func tokenServer(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := request.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func assertProbeSuccess(t *testing.T, got probeResult, route Route, token string) {
	t.Helper()
	if got.exit != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, "ROUTE="+string(route)+"\n") {
		t.Fatalf("stdout = %q, want route %q", got.stdout, route)
	}
	if !strings.Contains(got.stdout, "TOKEN="+token+"\n") {
		t.Fatalf("stdout = %q, want token %q", got.stdout, token)
	}
}

func readSpawns(t *testing.T, pe probeEnv) []string {
	t.Helper()
	data, err := os.ReadFile(pe.extra["PROBE_SPAWN_FILE"])
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func oauthJSON() string {
	return `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`
}

func TestRouting(t *testing.T) {
	readCmd := "read_credential_cmd = [\"token-helper\", \"--read\"]\n"
	writeCmd := "write_credential_cmd = [\"approve-write\", \"--write\"]\n"

	t.Run("env token wins on both surfaces without a cache write or command spawn", func(t *testing.T) {
		for _, surface := range []string{"batch", "tui"} {
			t.Run(surface, func(t *testing.T) {
				pe := newProbeEnv(t)
				writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
				pe.extra["MAILBOX_TOKEN"] = "caller-token"
				pe.extra["PROBE_SURFACE"] = surface
				assertProbeSuccess(t, execProbe(t, pe), RouteEnvToken, "caller-token")
				if spawns := readSpawns(t, pe); len(spawns) != 0 {
					t.Fatalf("credential command spawns = %v, want none", spawns)
				}
				entries, err := os.ReadDir(pe.cache)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("cache entries = %v, want none", entries)
				}
			})
		}
	})

	t.Run("command JSON stdout caches a fingerprinted read token", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		assertProbeSuccess(t, execProbe(t, pe), RouteCmd, "command-json-token")
		entries, err := os.ReadDir(pe.cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "work.") || !strings.HasSuffix(entries[0].Name(), ".token.json") {
			t.Fatalf("cache entries = %v, want one fingerprinted token", entries)
		}
		info, err := entries[0].Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache file mode = %o, want 600", info.Mode().Perm())
		}
		cacheInfo, err := os.Stat(pe.cache)
		if err != nil {
			t.Fatal(err)
		}
		if cacheInfo.Mode().Perm() != 0o700 {
			t.Fatalf("cache directory mode = %o, want 700", cacheInfo.Mode().Perm())
		}
	})

	t.Run("expired fingerprinted cache reacquires and rewrites", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		source := &CredentialSource{
			Class: ClassRead,
			Kind:  SourceCmd,
			Argv:  []string{"token-helper", "--read"},
			Argv0: filepath.Join(pe.stubs, "token-helper"),
		}
		fingerprint := sourceFingerprint("work", ClassRead, source)
		path := filepath.Join(pe.cache, "work."+fingerprint+".token.json")
		stale := fmt.Sprintf(`{"access_token":"expired-token","route":"cmd","expiry":%q,"fingerprint":%q}`, time.Now().Add(-time.Minute).Format(time.RFC3339), fingerprint)
		if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
		assertProbeSuccess(t, execProbe(t, pe), RouteCmd, "command-json-token")
		if spawns := readSpawns(t, pe); len(spawns) != 1 {
			t.Fatalf("credential command spawns = %v, want one reacquisition", spawns)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "expired-token") || !strings.Contains(string(data), "command-json-token") {
			t.Fatalf("cache after reacquisition = %q", data)
		}
	})

	t.Run("command bare token is accepted but never cached", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		pe.extra["STUB_MODE"] = "bare"
		assertProbeSuccess(t, execProbe(t, pe), RouteCmd, "bare.command.token-value-1234567890")
		entries, err := os.ReadDir(pe.cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("cache entries = %v, want none for bare token", entries)
		}
	})

	for name, fixture := range map[string]string{
		"command bare token bad charset is rejected":            "bad|bare token",
		"command JSON-leading malformed output is a hard error": "malformed|decode __mint stdout",
		"command chatter is rejected rather than laundered":     "chatter|bare token",
		"command oversized stdout is rejected":                  "oversize|output exceeded",
	} {
		name, fixture := name, fixture
		t.Run(name, func(t *testing.T) {
			mode, want, _ := strings.Cut(fixture, "|")
			pe := newProbeEnv(t)
			writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
			pe.extra["STUB_MODE"] = mode
			got := execProbe(t, pe)
			if got.exit == 0 || !strings.Contains(got.stderr, want) {
				t.Fatalf("exit, stderr = %d, %q; want failure containing %q", got.exit, got.stderr, want)
			}
		})
	}

	t.Run("command capture cap stops helper before it completes", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		pe.extra["STUB_MODE"] = "oversize"
		got := execProbe(t, pe)
		if got.exit == 0 || !strings.Contains(got.stderr, "output exceeded") {
			t.Fatalf("exit, stderr = %d, %q; want capture-cap failure", got.exit, got.stderr)
		}
		if _, err := os.Stat(pe.extra["PROBE_COMPLETED_FILE"]); !os.IsNotExist(err) {
			t.Fatalf("helper ran after capture cap: %v", err)
		}
	})

	t.Run("interactive command is structurally refused in batch and allowed in TUI", func(t *testing.T) {
		config := readCommandConfig(readCmd+"read_interactive = true\n", "", 0)
		batch := newProbeEnv(t)
		writeProbeConfig(t, batch, config)
		got := execProbe(t, batch)
		if got.exit == 0 || !strings.Contains(got.stderr, "accounts.work.read_credential_cmd") || !strings.Contains(got.stderr, batch.config) {
			t.Fatalf("batch result = %+v, want interactive credential refusal naming config key and path", got)
		}
		if spawns := readSpawns(t, batch); len(spawns) != 0 {
			t.Fatalf("batch command spawns = %v, want none", spawns)
		}

		tui := newProbeEnv(t)
		writeProbeConfig(t, tui, config)
		tui.extra["PROBE_SURFACE"] = "tui"
		assertProbeSuccess(t, execProbe(t, tui), RouteCmd, "command-json-token")
		if spawns := readSpawns(t, tui); len(spawns) != 1 {
			t.Fatalf("TUI command spawns = %v, want one", spawns)
		}
	})

	t.Run("environment authorized user refresh rejects non-loopback token URL at use", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig("read_credential_env = \"PROBE_OAUTH\"\n", "", 0))
		pe.extra["PROBE_OAUTH"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = "http://169.254.169.254/token"
		got := execProbe(t, pe)
		if got.exit == 0 || !strings.Contains(got.stderr, "loopback") {
			t.Fatalf("exit, stderr = %d, %q; want loopback rejection", got.exit, got.stderr)
		}
	})

	t.Run("environment authorized user refresh accepts loopback token URL", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig("read_credential_env = \"PROBE_OAUTH\"\n", "", 0))
		pe.extra["PROBE_OAUTH"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusOK, `{"access_token":"refreshed-token","expires_in":3600}`)
		assertProbeSuccess(t, execProbe(t, pe), RouteEnv, "refreshed-token")
	})

	t.Run("environment refresh failures name the config key rather than its secret variable", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig("read_credential_env = \"SUPER_SECRET_VAR_NAME\"\n", "", 0))
		pe.extra["SUPER_SECRET_VAR_NAME"] = `{"client_id":""}`
		got := execProbe(t, pe)
		if got.exit == 0 || !strings.Contains(got.stderr, "accounts.work.read_credential_env") || strings.Contains(got.stderr, "SUPER_SECRET_VAR_NAME") {
			t.Fatalf("exit, stderr = %d, %q; want config key and no environment variable", got.exit, got.stderr)
		}
	})

	t.Run("depth sentinel rejects recursive credential command without spawning", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		pe.extra["MAILBOX_CREDENTIAL_DEPTH"] = "1"
		got := execProbe(t, pe)
		if got.exit == 0 || !strings.Contains(got.stderr, "recursion") {
			t.Fatalf("exit, stderr = %d, %q; want recursion refusal", got.exit, got.stderr)
		}
		if spawns := readSpawns(t, pe); len(spawns) != 0 {
			t.Fatalf("credential command spawns = %v, want none", spawns)
		}
	})

	t.Run("command timeout kills its process group", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 1))
		pe.extra["STUB_MODE"] = "sleep"
		started := time.Now()
		got := execProbe(t, pe)
		if got.exit == 0 || !strings.Contains(got.stderr, "timed out") || time.Since(started) > 8*time.Second {
			t.Fatalf("exit, stderr, duration = %d, %q, %s; want bounded timeout", got.exit, got.stderr, time.Since(started))
		}
	})

	t.Run("descendant holding stdout cannot block command completion", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 1))
		pe.extra["STUB_MODE"] = "descendant"
		started := time.Now()
		got := execProbe(t, pe)
		if got.exit == 0 || time.Since(started) > 8*time.Second {
			t.Fatalf("exit, stderr, duration = %d, %q, %s; want bounded empty-output failure", got.exit, got.stderr, time.Since(started))
		}
	})

	t.Run("write source acquires on batch without touching populated read cache", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, writeCmd+"write_interactive = false\n", 0))
		readSource := &CredentialSource{
			Class: ClassRead,
			Kind:  SourceCmd,
			Argv:  []string{"token-helper", "--read"},
			Argv0: filepath.Join(pe.stubs, "token-helper"),
		}
		fingerprint := sourceFingerprint("work", ClassRead, readSource)
		path := filepath.Join(pe.cache, "work."+fingerprint+".token.json")
		before := []byte(fmt.Sprintf(`{"access_token":"read-cache-token","route":"cmd","expiry":%q,"fingerprint":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339), fingerprint))
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		pe.extra["PROBE_CLASS"] = "write"
		assertProbeSuccess(t, execProbe(t, pe), RouteCmd, "write.command.token-value-1234567890")
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("write acquisition changed read cache: before=%q after=%q", before, after)
		}
	})

	t.Run("write interactive command is refused in batch and produces a completion diagnostic in TUI", func(t *testing.T) {
		batch := newProbeEnv(t)
		writeProbeConfig(t, batch, readCommandConfig(readCmd, writeCmd, 0))
		batch.extra["PROBE_CLASS"] = "write"
		got := execProbe(t, batch)
		if got.exit == 0 || !strings.Contains(got.stderr, "accounts.work.write_credential_cmd") {
			t.Fatalf("batch result = %+v, want write config-key refusal", got)
		}

		tui := newProbeEnv(t)
		writeProbeConfig(t, tui, readCommandConfig(readCmd, writeCmd, 0))
		tui.extra["PROBE_CLASS"] = "write"
		tui.extra["PROBE_SURFACE"] = "tui"
		assertProbeSuccess(t, execProbe(t, tui), RouteCmd, "write.command.token-value-1234567890")
	})

	t.Run("successful command diagnostic is sanitized before a surface drains it", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeProbeConfig(t, pe, readCommandConfig(readCmd, "", 0))
		pe.extra["STUB_MODE"] = "diag"
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteCmd, "diagnostic.command.token-1234567890")
		if !strings.Contains(got.stdout, "DIAG=grant expires in 7d") || strings.Contains(got.stdout, "\x1b") {
			t.Fatalf("stdout = %q, want sanitized diagnostic", got.stdout)
		}
	})

	t.Run("no-config mode works only with MAILBOX_TOKEN", func(t *testing.T) {
		withToken := newProbeEnv(t)
		withToken.extra["MAILBOX_TOKEN"] = "caller-token"
		assertProbeSuccess(t, execProbe(t, withToken), RouteEnvToken, "caller-token")

		withoutToken := newProbeEnv(t)
		got := execProbe(t, withoutToken)
		if got.exit == 0 || !strings.Contains(got.stderr, "Configuration") {
			t.Fatalf("exit, stderr = %d, %q; want no-config guidance", got.exit, got.stderr)
		}
	})
}

func TestCredentialChildrenCannotMintOtherClassCredentials(t *testing.T) {
	tests := []struct {
		name      string
		class     Class
		helper    string
		config    string
		canaryEnv string
		targetEnv string
	}{
		{
			name:   "read helper cannot mint send credential",
			class:  ClassRead,
			helper: "mint-send-canary",
			config: `default_account = "work"
[accounts.work]
read_credential_cmd = ["mint-send-canary"]
send_credential_env = "CANARY_SEND_OAUTH"
credential_env_passthrough = ["TEST_BINARY", "PROBE_MINT_ENV_FILE"]
`,
			canaryEnv: "CANARY_SEND_OAUTH",
			targetEnv: "CANARY_SEND_OAUTH",
		},
		{
			name:   "send helper cannot mint read credential",
			class:  ClassSend,
			helper: "mint-read-canary",
			config: `default_account = "work"
[accounts.work]
read_credential_env = "WORK_READ_JSON"
send_credential_cmd = ["mint-read-canary"]
send_interactive = false
credential_env_passthrough = ["TEST_BINARY", "PROBE_MINT_ENV_FILE"]
`,
			canaryEnv: "WORK_READ_JSON",
			targetEnv: "WORK_READ_JSON",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pe := newProbeEnv(t)
			envFile := filepath.Join(pe.stubs, "mint-child-env")
			writeStub(t, pe.stubs, tc.helper, `exec "$TEST_BINARY" __mint --env `+tc.targetEnv)
			writeProbeConfig(t, pe, tc.config)
			pe.extra["TEST_BINARY"] = os.Args[0]
			pe.extra["PROBE_MINT_ENV_FILE"] = envFile
			pe.extra[tc.canaryEnv] = "not-an-authorized-user-json"
			if tc.class == ClassSend {
				pe.extra["PROBE_CLASS"] = string(ClassSend)
			}

			got := execProbe(t, pe)
			if got.exit == 0 || !strings.Contains(got.stderr, tc.targetEnv+" is unset") {
				t.Fatalf("mint-probe result = %+v, want unset credential refusal", got)
			}
			if strings.Contains(got.stdout, "TOKEN=") {
				t.Fatalf("mint-probe stdout = %q, want zero minted tokens", got.stdout)
			}
			childEnv, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(childEnv), tc.targetEnv+"=") {
				t.Fatalf("credential child leaked %s into __mint: %q", tc.targetEnv, childEnv)
			}
		})
	}
}
