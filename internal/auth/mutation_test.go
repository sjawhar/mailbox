package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeMinter struct {
	mu    sync.Mutex
	mints int
	delay time.Duration
	token Token
	err   error
}

func (f *fakeMinter) Mint(ctx context.Context, account Account) (Token, error) {
	f.mu.Lock()
	f.mints++
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.token, f.err
}

func (f *fakeMinter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints
}

func validToken(route Route) Token {
	return Token{AccessToken: "mut-tok", Route: route, Expiry: time.Now().Add(time.Hour)}
}

func hashDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(digest, "%s\x00%s\x00", name, data)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"MAILBOX_TOKEN", "MAILBOX_SECRETS_REEXEC", "SECRETSD_SESSION_TOKEN_FILE"}, oauthEnvironmentNames...) {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func TestMutationTokenEnvTokenOverride(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("MAILBOX_TOKEN", "pinned-tok")
	source := NewSource(AccountWork)
	minter := &fakeMinter{token: validToken(RouteMint)}

	token, err := source.MutationToken(context.Background(), minter)
	if err != nil || token != "pinned-tok" {
		t.Fatalf("MutationToken = %q, %v, want pinned MAILBOX_TOKEN", token, err)
	}
	if minter.count() != 0 {
		t.Fatalf("mints = %d, want 0 (route-1 override)", minter.count())
	}
	if source.MutationRoute() != RouteEnvToken {
		t.Fatalf("MutationRoute = %q, want %q", source.MutationRoute(), RouteEnvToken)
	}
}

func TestMutationTokenMintsOnceThenCaches(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountWork)
	minter := &fakeMinter{token: validToken(RouteMint)}

	for range 3 {
		token, err := source.MutationToken(context.Background(), minter)
		if err != nil || token != "mut-tok" {
			t.Fatalf("MutationToken = %q, %v", token, err)
		}
	}
	if minter.count() != 1 {
		t.Fatalf("mints = %d, want 1 (memory slot reused)", minter.count())
	}
}

func TestMutationTokenSingleFlight(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountPersonal)
	minter := &fakeMinter{token: validToken(RouteMint), delay: 100 * time.Millisecond}

	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if token, err := source.MutationToken(context.Background(), minter); err != nil || token != "mut-tok" {
				t.Errorf("MutationToken = %q, %v", token, err)
			}
		}()
	}
	group.Wait()
	if minter.count() != 1 {
		t.Fatalf("mints = %d, want 1 (single-flight)", minter.count())
	}
}

func TestMutationTokenFailureIsSingleFlightAndCanRetry(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountWork)
	mintErr := errors.New("mint failed")
	minter := &fakeMinter{delay: 100 * time.Millisecond, err: mintErr}

	errs := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := source.MutationToken(context.Background(), minter)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)

	if minter.count() != 1 {
		t.Fatalf("mints after concurrent failure = %d, want 1", minter.count())
	}
	for err := range errs {
		if !errors.Is(err, mintErr) {
			t.Errorf("MutationToken error = %v, want %v", err, mintErr)
		}
	}

	minter.err = nil
	minter.token = validToken(RouteMint)
	token, err := source.MutationToken(context.Background(), minter)
	if err != nil || token != "mut-tok" {
		t.Fatalf("MutationToken after failed flight = %q, %v", token, err)
	}
	if minter.count() != 2 {
		t.Fatalf("mints after retry = %d, want 2", minter.count())
	}
}

func TestMutationCredentialsNeverMint(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountWork)
	creds := source.MutationCredentials()

	if _, err := creds.AccessToken(context.Background()); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("AccessToken on empty slot = %v, want ErrExpiredToken", err)
	}

	minter := &fakeMinter{token: validToken(RouteMint)}
	if _, err := source.MutationToken(context.Background(), minter); err != nil {
		t.Fatal(err)
	}
	if token, err := creds.AccessToken(context.Background()); err != nil || token != "mut-tok" {
		t.Fatalf("AccessToken after mint = %q, %v", token, err)
	}

	if err := creds.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.AccessToken(context.Background()); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("AccessToken after Invalidate = %v, want ErrExpiredToken (non-minting)", err)
	}
	if minter.count() != 1 {
		t.Fatalf("mints = %d, want 1 (Invalidate/AccessToken never mint)", minter.count())
	}
}

func TestInvalidateMutationIsNonMinting(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountWork)
	minter := &fakeMinter{token: validToken(RouteMint)}
	if _, err := source.MutationToken(context.Background(), minter); err != nil {
		t.Fatal(err)
	}

	source.InvalidateMutation()
	if minter.count() != 1 {
		t.Fatalf("mints after InvalidateMutation = %d, want 1", minter.count())
	}
	if _, err := source.MutationToken(context.Background(), minter); err != nil {
		t.Fatal(err)
	}
	if minter.count() != 2 {
		t.Fatalf("mints after explicit re-request = %d, want 2 (caller decision)", minter.count())
	}
}

// Spec §3: mutation resolution never touches the disk cache, the broker, or
// the read keys. Spec §8: no-disk invariant.
func TestMutationResolutionTouchesNothingElse(t *testing.T) {
	clearCredentialEnv(t)
	stubs, cache := t.TempDir(), t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cache)
	t.Setenv("PATH", stubs) // an invoked broker would fail loudly: nothing on PATH
	dmi := filepath.Join(stubs, "sys_vendor")
	if err := os.WriteFile(dmi, []byte("Amazon EC2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_DMI_SYS_VENDOR", dmi) // on "EC2": broker would be the read route
	t.Setenv("GWS_WORK_READ_OAUTH", "decoy-should-not-be-read")
	t.Setenv("GWS_WORK_MAIL_OAUTH", "decoy-should-not-be-read")
	t.Setenv("GWS_WORK_MODIFY_OAUTH", oauthJSON())
	t.Setenv("MAILBOX_TOKEN_URL", tokenServer(t, http.StatusOK, `{"access_token":"env-mut-tok","expires_in":3600}`))

	before := hashDir(t, cache)
	source := NewSource(AccountWork)
	token, err := source.MutationToken(context.Background(), EnvOnlyMinter{Argv: []string{"archive", "1"}})
	if err != nil || token != "env-mut-tok" {
		t.Fatalf("MutationToken = %q, %v", token, err)
	}
	if source.MutationRoute() != RouteMutationEnv {
		t.Fatalf("MutationRoute = %q, want %q", source.MutationRoute(), RouteMutationEnv)
	}
	if after := hashDir(t, cache); after != before {
		t.Fatal("mutation flow changed the token cache directory (no-disk invariant)")
	}
}

func TestEnvOnlyMinterMissingKeyIsTyped(t *testing.T) {
	clearCredentialEnv(t)
	source := NewSource(AccountPersonal)
	_, err := source.MutationToken(context.Background(), EnvOnlyMinter{Argv: []string{"--account", "personal", "trash", "1 2"}})
	var needs *NeedsMutationCredError
	if !errors.As(err, &needs) {
		t.Fatalf("error = %v, want NeedsMutationCredError", err)
	}
	if needs.Account != AccountPersonal || needs.Key != "GWS_PERSONAL_MODIFY_OAUTH" {
		t.Fatalf("NeedsMutationCredError = %+v", needs)
	}
	wantCommand := `secrets GWS_PERSONAL_MODIFY_OAUTH -- mailbox --account personal trash '1 2'`
	if needs.Command() != wantCommand {
		t.Fatalf("Command() = %q, want %q", needs.Command(), wantCommand)
	}
	wantError := "mutation credentials for personal are human-tier; run: " + wantCommand
	if needs.Error() != wantError {
		t.Fatalf("Error() = %q, want %q", needs.Error(), wantError)
	}
}

func TestModifyEnvKeyNames(t *testing.T) {
	if got := ModifyEnvKey(AccountWork); got != "GWS_WORK_MODIFY_OAUTH" {
		t.Fatalf("ModifyEnvKey(work) = %q", got)
	}
	if got := ModifyEnvKey(AccountPersonal); got != "GWS_PERSONAL_MODIFY_OAUTH" {
		t.Fatalf("ModifyEnvKey(personal) = %q", got)
	}
}
