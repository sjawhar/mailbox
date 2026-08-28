package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	RouteMutationEnv Route = "mutation-env"
	RouteMint        Route = "mint"
)

// ErrExpiredToken reports an empty or expired mutation slot to a caller whose
// AccessToken path never mints (F2). Retry policy lives with the surfaces.
var ErrExpiredToken = errors.New("mutation token expired; a new mint is required")

type mutationFlight struct {
	done  chan struct{}
	token Token
	err   error
}

// Minter acquires a gmail.modify access token for one account.
type Minter interface {
	Mint(ctx context.Context, account Account) (Token, error)
}

// ModifyEnvKey names the human-tier mutation credential for account.
// The v0.1.0 MAIL names are burned (spec §1, F1) and must never be used.
func ModifyEnvKey(account Account) string {
	if account == AccountPersonal {
		return "GWS_PERSONAL_MODIFY_OAUTH"
	}
	return "GWS_WORK_MODIFY_OAUTH"
}

// NeedsMutationCredError reports the absent human-tier credential. The CLI
// owns rendering its invocation-specific remedy.
type NeedsMutationCredError struct {
	Account Account
	Key     string
}

func (e *NeedsMutationCredError) Error() string {
	return fmt.Sprintf("mutation credentials for %s are human-tier", e.Account)
}

// EnvOnlyMinter resolves the mutation credential from this process's own
// environment or fails with a typed error. It has no subprocess capability.
type EnvOnlyMinter struct{}

func (EnvOnlyMinter) Mint(ctx context.Context, account Account) (Token, error) {
	token, found, err := mintFromEnv(ctx, account)
	if err != nil {
		return Token{}, err
	}
	if !found {
		return Token{}, &NeedsMutationCredError{Account: account, Key: ModifyEnvKey(account)}
	}
	return token, nil
}

// mintFromEnv refreshes the modify credential when present in this process's
// environment. found reports whether the key was set at all.
func mintFromEnv(ctx context.Context, account Account) (token Token, found bool, err error) {
	key := ModifyEnvKey(account)
	rawJSON := os.Getenv(key)
	if rawJSON == "" {
		return Token{}, false, nil
	}
	accessToken, expiry, err := refreshAccessToken(ctx, key, rawJSON)
	if err != nil {
		return Token{}, true, err
	}
	return Token{AccessToken: accessToken, Route: RouteMutationEnv, Expiry: expiry}, true, nil
}

// mutationEnvToken applies the global override shared by minting and
// non-minting mutation callers.
func (s *Source) mutationEnvToken() (string, bool) {
	token := os.Getenv("MAILBOX_TOKEN")
	if token == "" {
		return "", false
	}
	s.mutMu.Lock()
	s.mutRoute = RouteEnvToken
	s.mutMu.Unlock()
	return token, true
}

// validMutationTokenLocked returns the cached mutation token when it remains
// usable and records its route. The caller must hold mutMu.
func (s *Source) validMutationTokenLocked() (Token, bool) {
	if s.mutToken == nil || !s.mutToken.Expiry.Add(-2*time.Minute).After(time.Now()) {
		return Token{}, false
	}
	token := *s.mutToken
	s.mutRoute = token.Route
	return token, true
}

func waitMutationFlight(ctx context.Context, flight *mutationFlight) (Token, error) {
	select {
	case <-ctx.Done():
		return Token{}, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return Token{}, flight.err
		}
		return flight.token, nil
	}
}

// MutationToken is the may-mint policy: it returns the minter's real error.
// MutationCredentials.AccessToken below is the may-not-mint policy: no flight
// is ErrExpiredToken and a failed flight maps back to ErrExpiredToken.
func (s *Source) MutationToken(ctx context.Context, minter Minter) (string, error) {
	if token, ok := s.mutationEnvToken(); ok {
		return token, nil
	}

	s.mutMu.Lock()
	if token, ok := s.validMutationTokenLocked(); ok {
		s.mutMu.Unlock()
		return token.AccessToken, nil
	}
	if s.mutFlight != nil {
		flight := s.mutFlight
		s.mutMu.Unlock()
		token, err := waitMutationFlight(ctx, flight)
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	}

	flight := &mutationFlight{done: make(chan struct{})}
	s.mutFlight = flight
	s.mutMu.Unlock()

	token, err := minter.Mint(ctx, s.account)

	s.mutMu.Lock()
	flight.token = token
	flight.err = err
	s.mutFlight = nil
	if err == nil {
		s.mutToken = &token
		s.mutRoute = token.Route
	}
	close(flight.done)
	s.mutMu.Unlock()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// InvalidateMutation clears the in-memory mutation slot. It never mints:
// re-minting is exclusively a caller decision (F2).
func (s *Source) InvalidateMutation() {
	s.mutMu.Lock()
	s.mutToken = nil
	s.mutMu.Unlock()
}

// MutationRoute reports how the last mutation token was resolved. The read
// marker keeps using LastRoute; this is the per-class counterpart (spec §3).
func (s *Source) MutationRoute() Route {
	s.mutMu.Lock()
	defer s.mutMu.Unlock()
	return s.mutRoute
}

// MutationCredentials returns the gmail-facing view of the mutation slot.
func (s *Source) MutationCredentials() *MutationCredentials {
	return &MutationCredentials{source: s}
}

// MutationCredentials adapts the mutation slot to the gmail client's
// credential seam. AccessToken never mints (F2): an empty slot with no
// keypress-initiated mint in flight is ErrExpiredToken.
type MutationCredentials struct {
	source *Source
}

func (m *MutationCredentials) AccessToken(ctx context.Context) (string, error) {
	s := m.source
	if token, ok := s.mutationEnvToken(); ok {
		return token, nil
	}

	s.mutMu.Lock()
	if token, ok := s.validMutationTokenLocked(); ok {
		s.mutMu.Unlock()
		return token.AccessToken, nil
	}
	flight := s.mutFlight
	s.mutMu.Unlock()
	if flight == nil {
		return "", ErrExpiredToken
	}
	token, err := waitMutationFlight(ctx, flight)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrExpiredToken
	}
	return token.AccessToken, nil
}

func (m *MutationCredentials) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("MAILBOX_TOKEN") != "" {
		return nil
	}
	m.source.InvalidateMutation()
	return nil
}
