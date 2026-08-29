package auth

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sjawhar/mailbox/internal/render"
)

type Route string

const (
	RouteEnvToken Route = "env-token"
	RouteCache    Route = "cache"
)

type Token struct {
	AccessToken string
	Route       Route
	Expiry      time.Time
}

type CacheState struct {
	Path   string
	Exists bool
	Valid  bool
	Expiry time.Time
}

type readFlight struct {
	done  chan struct{}
	token Token
	err   error
}

// Source resolves one configured account's read and write credentials. It is
// safe for concurrent use and keeps each credential class independent.
type Source struct {
	cfg  *Config
	acct *AccountConfig

	mu             sync.Mutex
	mem            *Token
	lastRoute      Route
	readFlight     *readFlight
	readDiagnostic string

	wrMu         sync.Mutex
	wrToken      *Token
	wrFlight     *writeFlight
	wrRoute      Route
	wrDiagnostic string
}

func NewSource(cfg *Config, acct *AccountConfig) *Source {
	return &Source{cfg: cfg, acct: acct}
}

func (s *Source) Account() *AccountConfig {
	return s.acct
}

// Resolve resolves the read credential in priority order: the process override,
// memory, a fingerprint-bound cache entry, then the caller-authorized acquirer.
func (s *Source) Resolve(ctx context.Context, acq Acquirer) (Token, error) {
	if accessToken := os.Getenv("MAILBOX_TOKEN"); accessToken != "" {
		token := Token{AccessToken: accessToken, Route: RouteEnvToken}
		s.mu.Lock()
		s.lastRoute = token.Route
		s.mu.Unlock()
		return token, nil
	}

	s.mu.Lock()
	if token, ok := validToken(s.mem, time.Now()); ok {
		s.lastRoute = token.Route
		s.mu.Unlock()
		return token, nil
	}
	if s.readFlight != nil {
		flight := s.readFlight
		s.mu.Unlock()
		return waitReadFlight(ctx, flight)
	}

	fingerprint := sourceFingerprint(s.acct.Name, ClassRead, s.acct.Read)
	if fingerprint != "" {
		cached, err := readCache(s.acct.Name, fingerprint)
		if err != nil {
			s.mu.Unlock()
			return Token{}, err
		}
		if cached.valid(time.Now()) {
			token := Token{AccessToken: cached.AccessToken, Route: RouteCache, Expiry: cached.Expiry}
			s.mem = &token
			s.lastRoute = token.Route
			s.mu.Unlock()
			return token, nil
		}
	}

	flight := &readFlight{done: make(chan struct{})}
	s.readFlight = flight
	s.mu.Unlock()

	acquired, err := acq.Acquire(ctx, s.acct, ClassRead)
	if err == nil {
		token := acquired.Token
		if token.Expiry.IsZero() {
			token.Expiry = time.Now().Add(time.Hour)
		}
		if !acquired.Token.Expiry.IsZero() && fingerprint != "" {
			err = writeCache(s.acct.Name, fingerprint, cachedToken{AccessToken: token.AccessToken, Route: token.Route, Expiry: token.Expiry})
		}
		if err == nil {
			acquired.Token = token
		}
	}

	s.mu.Lock()
	flight.err = err
	if err == nil {
		flight.token = acquired.Token
		s.mem = &acquired.Token
		s.lastRoute = acquired.Token.Route
		s.readDiagnostic = acquired.Diagnostic
	}
	s.readFlight = nil
	close(flight.done)
	s.mu.Unlock()
	if err != nil {
		return Token{}, err
	}
	return acquired.Token, nil
}

func validToken(token *Token, now time.Time) (Token, bool) {
	if token == nil || !token.Expiry.Add(-2*time.Minute).After(now) {
		return Token{}, false
	}
	return *token, true
}

func waitReadFlight(ctx context.Context, flight *readFlight) (Token, error) {
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

func (s *Source) ReadCredentials(acq Acquirer) *ReadCredentials {
	return &ReadCredentials{source: s, acq: acq}
}

// ReadCredentials adapts a source and the caller-authorized acquirer to the
// Gmail credential seam.
type ReadCredentials struct {
	source *Source
	acq    Acquirer
}

func (r *ReadCredentials) AccessToken(ctx context.Context) (string, error) {
	token, err := r.source.Resolve(ctx, r.acq)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func (r *ReadCredentials) Invalidate(ctx context.Context) error {
	return r.source.Invalidate(ctx)
}

// Invalidate clears the read memory slot and its fingerprint-bound disk cache.
// It never acquires a replacement token.
func (s *Source) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("MAILBOX_TOKEN") != "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem = nil
	fingerprint := sourceFingerprint(s.acct.Name, ClassRead, s.acct.Read)
	if fingerprint == "" {
		return nil
	}
	path, err := cachePath(s.acct.Name, fingerprint)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete token cache %s: %w", path, err)
	}
	return nil
}

func (s *Source) CacheState() CacheState {
	s.mu.Lock()
	defer s.mu.Unlock()
	fingerprint := sourceFingerprint(s.acct.Name, ClassRead, s.acct.Read)
	if fingerprint == "" {
		return CacheState{}
	}
	path, err := cachePath(s.acct.Name, fingerprint)
	if err != nil {
		return CacheState{}
	}
	state := CacheState{Path: path}
	if _, err := os.Stat(path); err != nil {
		return state
	}
	state.Exists = true
	cached, err := readCache(s.acct.Name, fingerprint)
	if err != nil || cached == nil {
		return state
	}
	state.Expiry = cached.Expiry
	state.Valid = cached.valid(time.Now())
	return state
}

func (s *Source) LastRoute() Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRoute
}

// TakeDiagnostic returns and clears the credential-command completion note for
// one class, ensuring a surface emits it at most once.
func (s *Source) TakeDiagnostic(class Class) string {
	switch class {
	case ClassRead:
		s.mu.Lock()
		defer s.mu.Unlock()
		diagnostic := s.readDiagnostic
		s.readDiagnostic = ""
		return diagnostic
	case ClassWrite:
		s.wrMu.Lock()
		defer s.wrMu.Unlock()
		diagnostic := s.wrDiagnostic
		s.wrDiagnostic = ""
		return diagnostic
	default:
		return ""
	}
}

// ScopeHint identifies the configured source without exposing credential
// environment variable names or command arguments.
func ScopeHint(acct *AccountConfig, class Class, route Route, scope string) string {
	if route == RouteEnvToken {
		return fmt.Sprintf("MAILBOX_TOKEN lacks the %s scope; see README", scope)
	}
	src := sourceFor(acct, class)
	if src == nil {
		return fmt.Sprintf("no configured %s credential lacks the %s scope; see README", class, scope)
	}
	if src.Kind == SourceCmd {
		return fmt.Sprintf("%s (via %s) lacks the %s scope; see README", safeForTerminal(src.ConfigKey), safeForTerminal(src.Argv0), scope)
	}
	return fmt.Sprintf("%s lacks the %s scope; see README", safeForTerminal(src.ConfigKey), scope)
}

func sourceFor(acct *AccountConfig, class Class) *CredentialSource {
	if acct == nil {
		return nil
	}
	switch class {
	case ClassRead:
		return acct.Read
	case ClassWrite:
		return acct.Write
	default:
		return nil
	}
}

func credentialError(cfg *Config, acct *AccountConfig, class Class, src *CredentialSource, reason CredentialReason) *NeedsCredentialError {
	account := "default"
	if acct != nil {
		account = acct.Name
	}
	configPath := ""
	if cfg != nil {
		configPath = cfg.Path
		if cfg.NoConfig() {
			configPath = cfg.DefaultPath
		}
	}
	configKey := ""
	if src != nil {
		configKey = src.ConfigKey
	} else if cfg != nil && !cfg.NoConfig() {
		configKey = fmt.Sprintf("accounts.%s.%s_credential_cmd", account, class)
	}
	if cfg == nil || cfg.NoConfig() {
		reason = ReasonNoConfig
	}
	return &NeedsCredentialError{Account: account, Class: class, ConfigKey: configKey, ConfigPath: configPath, Reason: reason}
}

func safeForTerminal(value string) string {
	return render.SanitizeTerminal(value)
}
