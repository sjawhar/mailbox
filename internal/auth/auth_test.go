package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type staticAcquirer struct {
	token Token
	err   error
	calls int
}

func (a *staticAcquirer) Acquire(context.Context, *AccountConfig, Class) (Acquired, error) {
	a.calls++
	if a.err != nil {
		return Acquired{}, a.err
	}
	return Acquired{Token: a.token}, nil
}

func readTestConfig() (*Config, *AccountConfig) {
	acct := &AccountConfig{Name: "work", Read: &CredentialSource{Class: ClassRead, Kind: SourceEnv, EnvVar: "TEST_READ", ConfigKey: "accounts.work.read_credential_env"}}
	return &Config{Path: "/tmp/mailbox.toml", Accounts: []*AccountConfig{acct}, CredentialTimeout: defaultCredentialTimeout}, acct
}

func TestRefreshRefusesRedirectsFromLoopbackTokenEndpoint(t *testing.T) {
	redirectHits := 0
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		redirectHits++
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"exfiltrated","expires_in":3600}`))
	}))
	t.Cleanup(redirectTarget.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	t.Setenv("MAILBOX_TOKEN_URL", redirector.URL)

	_, _, err := refreshAccessToken(context.Background(), "accounts.work.read_credential_env", oauthJSON())
	if err == nil {
		t.Fatal("refresh followed a redirect from the loopback endpoint")
	}
	if redirectHits != 0 {
		t.Fatalf("redirect target received %d credential POSTs", redirectHits)
	}
}

func TestSourceUsesFingerprintBoundReadCacheAndInvalidatesWithoutAcquiring(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cache)
	cfg, acct := readTestConfig()
	fingerprint := sourceFingerprint(acct.Name, ClassRead, acct.Read)
	if err := writeCache(acct.Name, fingerprint, cachedToken{AccessToken: "cached-token", Route: RouteEnv, Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	source := NewSource(cfg, acct)
	acquirer := &staticAcquirer{token: Token{AccessToken: "fresh-token", Route: RouteEnv, Expiry: time.Now().Add(time.Hour)}}
	token, err := source.Resolve(context.Background(), acquirer)
	if err != nil || token.AccessToken != "cached-token" || token.Route != RouteCache || acquirer.calls != 0 {
		t.Fatalf("Resolve = %+v, %v; calls = %d", token, err, acquirer.calls)
	}
	if err := source.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := source.CacheState(); state.Exists || state.Valid {
		t.Fatalf("CacheState after Invalidate = %+v", state)
	}
	if _, err := source.ReadCredentials(acquirer).AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acquirer.calls != 1 {
		t.Fatalf("acquisitions after explicit second read = %d, want 1", acquirer.calls)
	}
}

func TestSourceCallerOverridePinsReadCacheAndAcquisition(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cache)
	t.Setenv("MAILBOX_TOKEN", "caller-token")
	cfg, acct := readTestConfig()
	fingerprint := sourceFingerprint(acct.Name, ClassRead, acct.Read)
	if err := writeCache(acct.Name, fingerprint, cachedToken{AccessToken: "cached-token", Route: RouteEnv, Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	path, err := cachePath(acct.Name, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := NewSource(cfg, acct)
	acquirer := &staticAcquirer{token: Token{AccessToken: "fresh-token", Route: RouteEnv, Expiry: time.Now().Add(time.Hour)}}
	if got, err := source.ReadCredentials(acquirer).AccessToken(context.Background()); err != nil || got != "caller-token" {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	if err := source.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || acquirer.calls != 0 {
		t.Fatalf("cache, acquisitions changed under caller override: %q, %d", after, acquirer.calls)
	}
}

func TestDiagnosticQueueRetainsOnlyNewestBoundedEntries(t *testing.T) {
	var diagnostics []string
	for _, diagnostic := range []string{"one", "two", "three", "four", "five"} {
		diagnostics = appendDiagnostic(diagnostics, diagnostic)
	}
	if got, want := strings.Join(diagnostics, ","), "two,three,four,five"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func TestScopeHintNamesOnlyConfigMetadata(t *testing.T) {
	acct := &AccountConfig{Name: "work", Write: &CredentialSource{Class: ClassWrite, Kind: SourceCmd, Argv0: "/opt/helper", Argv: []string{"helper", "--secret", "value"}, ConfigKey: "accounts.work.write_credential_cmd"}}
	hint := ScopeHint(acct, ClassWrite, RouteCmd, "gmail.modify")
	for _, want := range []string{"accounts.work.write_credential_cmd", "/opt/helper", "gmail.modify"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("ScopeHint = %q, want %q", hint, want)
		}
	}
	for _, forbidden := range []string{"--secret", "value"} {
		if strings.Contains(hint, forbidden) {
			t.Fatalf("ScopeHint leaked command argument %q in %q", forbidden, hint)
		}
	}
}
