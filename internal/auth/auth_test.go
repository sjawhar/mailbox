package auth

import (
	"context"
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
	source := NewSource(cfg, acct)
	acquirer := &staticAcquirer{token: Token{AccessToken: "fresh-token", Route: RouteEnv, Expiry: time.Now().Add(time.Hour)}}
	if got, err := source.ReadCredentials(acquirer).AccessToken(context.Background()); err != nil || got != "caller-token" {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	if err := source.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || acquirer.calls != 0 {
		t.Fatalf("cache, acquisitions = %v, %d; want none, zero", entries, acquirer.calls)
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
