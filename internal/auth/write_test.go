package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type countingAcquirer struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	token Token
	err   error
}

func (a *countingAcquirer) Acquire(ctx context.Context, acct *AccountConfig, class Class) (Acquired, error) {
	if class != ClassWrite {
		return Acquired{}, errors.New("unexpected credential class")
	}
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	if a.delay > 0 {
		select {
		case <-ctx.Done():
			return Acquired{}, ctx.Err()
		case <-time.After(a.delay):
		}
	}
	if a.err != nil {
		return Acquired{}, a.err
	}
	return Acquired{Token: a.token}, nil
}

func (a *countingAcquirer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func writeTestSource() *Source {
	acct := &AccountConfig{
		Name:  "work",
		Read:  &CredentialSource{Class: ClassRead, Kind: SourceEnv, EnvVar: "TEST_READ"},
		Write: &CredentialSource{Class: ClassWrite, Kind: SourceEnv, EnvVar: "TEST_WRITE"},
	}
	return NewSource(&Config{Accounts: []*AccountConfig{acct}, CredentialTimeout: defaultCredentialTimeout}, acct)
}

func TestWriteTokenUsesCallerOverrideWithoutAcquisition(t *testing.T) {
	t.Setenv("MAILBOX_TOKEN", "caller-token")
	source := writeTestSource()
	acquirer := &countingAcquirer{token: Token{AccessToken: "acquired-token", Route: RouteCmd, Expiry: time.Now().Add(time.Hour)}}
	got, err := source.WriteToken(context.Background(), acquirer)
	if err != nil || got != "caller-token" || acquirer.count() != 0 || source.WriteRoute() != RouteEnvToken {
		t.Fatalf("WriteToken = %q, %v; calls, route = %d, %q", got, err, acquirer.count(), source.WriteRoute())
	}
}

func TestWriteTokenUsesOneFlightForConcurrentUnlocks(t *testing.T) {
	source := writeTestSource()
	acquirer := &countingAcquirer{delay: 100 * time.Millisecond, token: Token{AccessToken: "write-token", Route: RouteCmd, Expiry: time.Now().Add(time.Hour)}}
	var group sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			got, err := source.WriteToken(context.Background(), acquirer)
			if err != nil || got != "write-token" {
				errs <- errors.New("write credential was not shared")
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if acquirer.count() != 1 {
		t.Fatalf("acquisitions = %d, want 1", acquirer.count())
	}
}

func TestWriteTokenFailureIsSingleFlightAndCanRetry(t *testing.T) {
	source := writeTestSource()
	want := errors.New("approval denied")
	acquirer := &countingAcquirer{delay: 100 * time.Millisecond, err: want}
	var group sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := source.WriteToken(context.Background(), acquirer)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("concurrent WriteToken error = %v, want %v", err, want)
		}
	}
	if acquirer.count() != 1 {
		t.Fatalf("failed acquisitions = %d, want 1", acquirer.count())
	}

	acquirer.err = nil
	acquirer.token = Token{AccessToken: "retried-token", Route: RouteCmd, Expiry: time.Now().Add(time.Hour)}
	if got, err := source.WriteToken(context.Background(), acquirer); err != nil || got != "retried-token" {
		t.Fatalf("WriteToken retry = %q, %v", got, err)
	}
	if acquirer.count() != 2 {
		t.Fatalf("acquisitions after retry = %d, want 2", acquirer.count())
	}
}

func TestWriteTokenReusesMemorySlotSequentially(t *testing.T) {
	source := writeTestSource()
	acquirer := &countingAcquirer{token: Token{AccessToken: "write-token", Route: RouteCmd, Expiry: time.Now().Add(time.Hour)}}
	for range 3 {
		if got, err := source.WriteToken(context.Background(), acquirer); err != nil || got != "write-token" {
			t.Fatalf("WriteToken = %q, %v", got, err)
		}
	}
	if acquirer.count() != 1 {
		t.Fatalf("sequential acquisitions = %d, want 1", acquirer.count())
	}
}

func TestWriteCredentialsNeverAcquiresAfterInvalidation(t *testing.T) {
	source := writeTestSource()
	acquirer := &countingAcquirer{token: Token{AccessToken: "write-token", Route: RouteCmd, Expiry: time.Now().Add(time.Hour)}}
	if _, err := source.WriteToken(context.Background(), acquirer); err != nil {
		t.Fatal(err)
	}
	credentials := source.WriteCredentials()
	if got, err := credentials.AccessToken(context.Background()); err != nil || got != "write-token" {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	if err := credentials.Invalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.AccessToken(context.Background()); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("AccessToken after Invalidate = %v, want ErrExpiredToken", err)
	}
	if acquirer.count() != 1 {
		t.Fatalf("acquisitions = %d, want original acquisition only", acquirer.count())
	}
}
