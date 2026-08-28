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
	if os.Getenv("MAILBOX_AUTH_PROBE") == "1" {
		probeMain()
		return
	}
	os.Exit(m.Run())
}

func probeMain() {
	account, err := ResolveAccount(os.Getenv("PROBE_ACCOUNT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := NewSource(account)
	if os.Getenv("PROBE_ENSURE_ENV") == "1" {
		if err := s.EnsureEnv(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	tok, err := s.Resolve(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("ROUTE=%s\nTOKEN=%s\nREEXEC=%s\n", tok.Route, tok.AccessToken, os.Getenv("MAILBOX_SECRETS_REEXEC"))
	os.Exit(0)
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

type probeEnv struct {
	stubs, cache string
	dmi          string
	leakFile     string
	extra        map[string]string
}

type probeResult struct {
	stdout, stderr string
	exit           int
}

func execProbe(t *testing.T, pe probeEnv) probeResult {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = []string{
		"MAILBOX_AUTH_PROBE=1",
		"PATH=" + pe.stubs + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"MAILBOX_CACHE_DIR=" + pe.cache,
		"MAILBOX_DMI_SYS_VENDOR=" + pe.dmi,
		"PROBE_LEAK_FILE=" + pe.leakFile,
	}
	for k, v := range pe.extra {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return probeResult{stdout: out.String(), stderr: errb.String(), exit: exit}
}

func newProbeEnv(t *testing.T) probeEnv {
	t.Helper()
	stubs, cache := t.TempDir(), t.TempDir()
	leak := filepath.Join(stubs, "leaks")
	dmi := filepath.Join(stubs, "sys_vendor")
	if err := os.WriteFile(dmi, []byte("Amazon EC2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub(t, stubs, "google-user-token",
		`printf 'LEAK=%s%s%s%s%s%s%s%s%s%s%s\n' "${MAILBOX_TOKEN:-}" "${MAILBOX_SECRETS_REEXEC:-}" "${GWS_WORK_MAIL_OAUTH:-}" "${GWS_PERSONAL_MAIL_OAUTH:-}" "${GWS_WORK_READ_OAUTH:-}" "${GWS_PERSONAL_READ_OAUTH:-}" "${GWS_WORK_MODIFY_OAUTH:-}" "${GWS_PERSONAL_MODIFY_OAUTH:-}" "${GWS_WORK_SEND_OAUTH:-}" "${GWS_PERSONAL_SEND_OAUTH:-}" "${SECRETSD_SESSION_TOKEN_FILE:-}" >> "${PROBE_LEAK_FILE:-/dev/null}"
echo "SHOULD-NOT-RUN" >&2; exit 99`)
	writeStub(t, stubs, "secrets",
		`printf 'LEAK=%s%s%s%s%s%s%s%s%s%s\n' "${MAILBOX_TOKEN:-}" "${GWS_WORK_MAIL_OAUTH:-}" "${GWS_PERSONAL_MAIL_OAUTH:-}" "${GWS_WORK_READ_OAUTH:-}" "${GWS_PERSONAL_READ_OAUTH:-}" "${GWS_WORK_MODIFY_OAUTH:-}" "${GWS_PERSONAL_MODIFY_OAUTH:-}" "${GWS_WORK_SEND_OAUTH:-}" "${GWS_PERSONAL_SEND_OAUTH:-}" "${SECRETSD_SESSION_TOKEN_FILE:-}" >> "${PROBE_LEAK_FILE:-/dev/null}"
key="$1"; shift; [ "$1" = "--" ] && shift
if [ -z "$STUB_SECRET_VALUE" ]; then echo "stub secrets: no value for $key" >&2; exit 1; fi
export "$key=$STUB_SECRET_VALUE"
exec "$@"`)
	return probeEnv{stubs: stubs, cache: cache, dmi: dmi, leakFile: leak, extra: map[string]string{}}
}

func tokenServer(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func successfulBroker(t *testing.T, pe probeEnv, token string) {
	t.Helper()
	writeStub(t, pe.stubs, "google-user-token", `printf 'LEAK=%s%s%s%s%s%s%s%s%s%s%s\n' "${MAILBOX_TOKEN:-}" "${MAILBOX_SECRETS_REEXEC:-}" "${GWS_WORK_MAIL_OAUTH:-}" "${GWS_PERSONAL_MAIL_OAUTH:-}" "${GWS_WORK_READ_OAUTH:-}" "${GWS_PERSONAL_READ_OAUTH:-}" "${GWS_WORK_MODIFY_OAUTH:-}" "${GWS_PERSONAL_MODIFY_OAUTH:-}" "${GWS_WORK_SEND_OAUTH:-}" "${GWS_PERSONAL_SEND_OAUTH:-}" "${SECRETSD_SESSION_TOKEN_FILE:-}" >> "${PROBE_LEAK_FILE:-/dev/null}"
printf '%s ' "$@" > "${PROBE_LEAK_FILE:-/dev/null}.argv"
printf '%s\n' "`+token+`"`)
}

func oauthJSON() string {
	return `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`
}

func cacheFile(pe probeEnv, account Account) string {
	return filepath.Join(pe.cache, string(account)+".token.json")
}

func writeCachedToken(t *testing.T, pe probeEnv, account Account, token string, expiry time.Time) {
	t.Helper()
	content := fmt.Sprintf(`{"access_token":%q,"route":"broker","expiry":%q}`, token, expiry.Format(time.RFC3339))
	if err := os.WriteFile(cacheFile(pe, account), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readLeaks(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

var oauthEnvironmentNames = []string{
	"GWS_WORK_MAIL_OAUTH",
	"GWS_PERSONAL_MAIL_OAUTH",
	"GWS_WORK_READ_OAUTH",
	"GWS_PERSONAL_READ_OAUTH",
	"GWS_WORK_MODIFY_OAUTH",
	"GWS_PERSONAL_MODIFY_OAUTH",
	"GWS_WORK_SEND_OAUTH",
	"GWS_PERSONAL_SEND_OAUTH",
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

func TestRouting(t *testing.T) {
	t.Run("env token wins", func(t *testing.T) {
		pe := newProbeEnv(t)
		pe.extra["MAILBOX_TOKEN"] = "tok-abc"
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteEnvToken, "tok-abc")
		entries, err := os.ReadDir(pe.cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("cache entries = %v, want none", entries)
		}
		if strings.Contains(got.stderr, "SHOULD-NOT-RUN") {
			t.Fatalf("broker ran: stderr = %q", got.stderr)
		}
	})

	t.Run("valid cache", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeCachedToken(t, pe, AccountWork, "cached-tok", time.Now().Add(30*time.Minute))
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteCache, "cached-tok")
		if strings.Contains(got.stderr, "SHOULD-NOT-RUN") {
			t.Fatalf("broker ran: stderr = %q", got.stderr)
		}
	})

	t.Run("expired cache re-mints", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeCachedToken(t, pe, AccountWork, "old-tok", time.Now().Add(-time.Minute))
		successfulBroker(t, pe, "broker-tok")
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteBroker, "broker-tok")
		info, err := os.Stat(cacheFile(pe, AccountWork))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache file mode = %o, want 600", info.Mode().Perm())
		}
		dir, err := os.Stat(pe.cache)
		if err != nil {
			t.Fatal(err)
		}
		if dir.Mode().Perm() != 0o700 {
			t.Fatalf("cache directory mode = %o, want 700", dir.Mode().Perm())
		}
		data, err := os.ReadFile(cacheFile(pe, AccountWork))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old-tok") || !strings.Contains(string(data), "broker-tok") {
			t.Fatalf("cache = %q, want rewritten broker token", data)
		}
	})

	t.Run("broker argv requests gmail.readonly (F8)", func(t *testing.T) {
		pe := newProbeEnv(t)
		successfulBroker(t, pe, "broker-tok")
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteBroker, "broker-tok")
		argv, err := os.ReadFile(pe.leakFile + ".argv")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(argv)) != "--scopes gmail.readonly" {
			t.Fatalf("broker argv = %q, want --scopes gmail.readonly", argv)
		}
	})

	t.Run("broker child env is scrubbed", func(t *testing.T) {
		pe := newProbeEnv(t)
		successfulBroker(t, pe, "broker-tok")
		for _, name := range oauthEnvironmentNames {
			pe.extra[name] = "decoy-should-not-leak"
		}
		pe.extra["MAILBOX_SECRETS_REEXEC"] = "decoy-should-not-leak"
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteBroker, "broker-tok")
		if leaks := readLeaks(t, pe.leakFile); len(leaks) != 1 || leaks[0] != "LEAK=" {
			t.Fatalf("leaks = %q, want [LEAK=]", leaks)
		}
	})

	t.Run("broker failure is loud with no fallback", func(t *testing.T) {
		pe := newProbeEnv(t)
		writeStub(t, pe.stubs, "google-user-token", `echo boom >&2; exit 3`)
		pe.extra["GWS_WORK_READ_OAUTH"] = oauthJSON()
		got := execProbe(t, pe)
		if got.exit == 0 {
			t.Fatalf("unexpected success: stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "boom") {
			t.Fatalf("stderr = %q, want broker error", got.stderr)
		}
		if strings.Contains(got.stdout, "ROUTE=") {
			t.Fatalf("stdout = %q, want no resolved route", got.stdout)
		}
	})

	t.Run("MAILBOX_BROKER overrides PATH", func(t *testing.T) {
		pe := newProbeEnv(t)
		brokers := t.TempDir()
		writeStub(t, brokers, "custom-mailbox-broker", `[ "$MAILBOX_BROKER" = "$0" ] || { echo "MAILBOX_BROKER not forwarded" >&2; exit 88; }
printf 'override-tok\n'`)
		pe.extra["MAILBOX_BROKER"] = filepath.Join(brokers, "custom-mailbox-broker")
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteBroker, "override-tok")
		if strings.Contains(got.stderr, "SHOULD-NOT-RUN") {
			t.Fatalf("PATH broker ran: stderr = %q", got.stderr)
		}
	})

	t.Run("work off EC2 uses oauth env", func(t *testing.T) {
		pe := newProbeEnv(t)
		if err := os.WriteFile(pe.dmi, []byte("LENOVO\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pe.extra["GWS_WORK_READ_OAUTH"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusOK, `{"access_token":"ref-tok","expires_in":3600}`)
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteOAuthRefresh, "ref-tok")
		info, err := os.Stat(cacheFile(pe, AccountWork))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache file mode = %o, want 600", info.Mode().Perm())
		}
	})

	t.Run("personal ignores broker even on EC2", func(t *testing.T) {
		pe := newProbeEnv(t)
		pe.extra["PROBE_ACCOUNT"] = string(AccountPersonal)
		pe.extra["GWS_PERSONAL_READ_OAUTH"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusOK, `{"access_token":"personal-tok","expires_in":3600}`)
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteOAuthRefresh, "personal-tok")
		if strings.Contains(got.stderr, "SHOULD-NOT-RUN") {
			t.Fatalf("broker ran: stderr = %q", got.stderr)
		}
	})

	t.Run("re-exec under secrets", func(t *testing.T) {
		pe := newProbeEnv(t)
		pe.extra["PROBE_ACCOUNT"] = string(AccountPersonal)
		pe.extra["PROBE_ENSURE_ENV"] = "1"
		pe.extra["STUB_SECRET_VALUE"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusOK, `{"access_token":"reexec-tok","expires_in":3600}`)
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteOAuthRefresh, "reexec-tok")
		if !strings.Contains(got.stdout, "REEXEC=1\n") {
			t.Fatalf("stdout = %q, want re-exec guard", got.stdout)
		}
	})

	t.Run("re-exec scrubs credentials", func(t *testing.T) {
		pe := newProbeEnv(t)
		pe.extra["PROBE_ACCOUNT"] = string(AccountPersonal)
		pe.extra["PROBE_ENSURE_ENV"] = "1"
		pe.extra["STUB_SECRET_VALUE"] = oauthJSON()
		for _, name := range oauthEnvironmentNames {
			if name != "GWS_PERSONAL_READ_OAUTH" {
				pe.extra[name] = "decoy-should-not-leak"
			}
		}
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusOK, `{"access_token":"reexec-tok","expires_in":3600}`)
		got := execProbe(t, pe)
		assertProbeSuccess(t, got, RouteOAuthRefresh, "reexec-tok")
		if leaks := readLeaks(t, pe.leakFile); len(leaks) != 1 || leaks[0] != "LEAK=" {
			t.Fatalf("leaks = %q, want [LEAK=]", leaks)
		}
	})

	t.Run("re-exec guard is loud", func(t *testing.T) {
		pe := newProbeEnv(t)
		pe.extra["PROBE_ACCOUNT"] = string(AccountPersonal)
		pe.extra["PROBE_ENSURE_ENV"] = "1"
		pe.extra["STUB_SECRET_VALUE"] = ""
		pe.extra["MAILBOX_SECRETS_REEXEC"] = "1"
		got := execProbe(t, pe)
		if got.exit == 0 {
			t.Fatalf("unexpected success: stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
		want := "GWS_PERSONAL_READ_OAUTH still unset after re-exec under secrets"
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("stderr = %q, want %q", got.stderr, want)
		}
	})

	t.Run("refresh rejection is loud", func(t *testing.T) {
		pe := newProbeEnv(t)
		if err := os.WriteFile(pe.dmi, []byte("LENOVO\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pe.extra["GWS_WORK_READ_OAUTH"] = oauthJSON()
		pe.extra["MAILBOX_TOKEN_URL"] = tokenServer(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
		got := execProbe(t, pe)
		if got.exit == 0 {
			t.Fatalf("unexpected success: stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "invalid_grant") || !strings.Contains(got.stderr, "GWS_WORK_READ_OAUTH") {
			t.Fatalf("stderr = %q, want invalid_grant and key", got.stderr)
		}
	})

	t.Run("malformed oauth JSON is loud", func(t *testing.T) {
		pe := newProbeEnv(t)
		if err := os.WriteFile(pe.dmi, []byte("LENOVO\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pe.extra["GWS_WORK_READ_OAUTH"] = `{"client_id":""}`
		got := execProbe(t, pe)
		if got.exit == 0 {
			t.Fatalf("unexpected success: stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "client_id") || !strings.Contains(got.stderr, "GWS_WORK_READ_OAUTH") {
			t.Fatalf("stderr = %q, want client_id and key", got.stderr)
		}
	})
}

func TestScrubbedEnvironDropsCredentials(t *testing.T) {
	credentialNames := append([]string{
		"MAILBOX_TOKEN",
		"MAILBOX_SECRETS_REEXEC",
		"SECRETSD_SESSION_TOKEN_FILE",
		"GWS_WORK_MODIFY_OAUTH",
		"GWS_FUTURE_SCOPE_OAUTH",
	}, oauthEnvironmentNames...)
	for _, name := range credentialNames {
		t.Setenv(name, "credential-decoy")
	}
	t.Setenv("MAILBOX_UNRELATED", "kept")

	environment := strings.Join(ScrubbedEnviron(), "\n")
	for _, name := range credentialNames {
		if strings.Contains(environment, name+"=") {
			t.Errorf("ScrubbedEnviron() leaked %s: %q", name, environment)
		}
	}
	if !strings.Contains(environment, "MAILBOX_UNRELATED=kept") {
		t.Fatalf("ScrubbedEnviron() = %q, want unrelated environment retained", environment)
	}
}
