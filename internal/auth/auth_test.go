package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProvisioningHint(t *testing.T) {
	cases := []struct {
		name    string
		account Account
		route   Route
		want    []string
	}{
		{name: "broker", account: AccountWork, route: RouteBroker, want: []string{"read-only", "GWS_WORK_MODIFY_OAUTH", "human tier", "README"}},
		{name: "env token", account: AccountWork, route: RouteEnvToken, want: []string{"gmail.modify", "MAILBOX_TOKEN", "README"}},
		{name: "personal oauth", account: AccountPersonal, route: RouteOAuthRefresh, want: []string{"gmail.modify", "GWS_PERSONAL_MODIFY_OAUTH", "README"}},
		{name: "mint", account: AccountWork, route: RouteMint, want: []string{"GWS_WORK_MODIFY_OAUTH", "gmail.modify", "ceremony"}},
		{name: "mutation env", account: AccountPersonal, route: RouteMutationEnv, want: []string{"GWS_PERSONAL_MODIFY_OAUTH", "gmail.modify", "ceremony"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProvisioningHint(tc.account, tc.route, "gmail.modify")
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("ProvisioningHint(%q, %q) = %q, want it to contain %q", tc.account, tc.route, got, want)
				}
			}
		})
	}
}

func TestFindSecretsRequiresPathExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := findSecrets()
	if err == nil {
		t.Fatal("findSecrets() succeeded without a PATH executable")
	}
	if got, want := err.Error(), "secrets executable not found on PATH"; got != want {
		t.Fatalf("findSecrets() error = %q, want %q", got, want)
	}
}

func TestSourceCacheLifecycle(t *testing.T) {
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("GWS_WORK_READ_OAUTH", "")
	t.Setenv("GWS_PERSONAL_READ_OAUTH", "")
	t.Setenv("MAILBOX_DMI_SYS_VENDOR", t.TempDir()+"/not-ec2")
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())

	if err := writeCache(AccountWork, cachedToken{
		AccessToken: "cached-token",
		Route:       RouteBroker,
		Expiry:      time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	source := NewSource(AccountWork)
	token, err := source.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token.Route != RouteCache || token.AccessToken != "cached-token" {
		t.Fatalf("Resolve() = %#v, want cached token", token)
	}
	if source.Account() != AccountWork {
		t.Fatalf("Account() = %q, want %q", source.Account(), AccountWork)
	}
	if source.LastRoute() != RouteCache {
		t.Fatalf("LastRoute() = %q, want %q", source.LastRoute(), RouteCache)
	}
	state := source.CacheState()
	if !state.Exists || !state.Valid || state.Expiry != token.Expiry {
		t.Fatalf("CacheState() = %#v, want an existing valid cache", state)
	}

	if err := source.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := source.CacheState(); state.Exists || state.Valid {
		t.Fatalf("CacheState() after Invalidate = %#v, want no cache", state)
	}
	if _, err := source.AccessToken(context.Background()); !errors.As(err, new(*NeedsSecretsError)) {
		t.Fatalf("AccessToken() after Invalidate error = %v, want NeedsSecretsError", err)
	}
}

func TestInvalidateLeavesEnvTokenCacheUntouched(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "caller-token")
	if err := writeCache(AccountWork, cachedToken{
		AccessToken: "cached-token",
		Route:       RouteBroker,
		Expiry:      time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	path, err := cachePath(AccountWork)
	if err != nil {
		t.Fatal(err)
	}

	source := NewSource(AccountWork)
	if got, err := source.AccessToken(context.Background()); err != nil || got != "caller-token" {
		t.Fatalf("AccessToken() = %q, %v, want caller token", got, err)
	}
	if err := source.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("env-token Invalidate removed cache %s: %v", path, err)
	}
}
