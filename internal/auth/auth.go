package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	Scope       string
}

type CacheState struct {
	Path   string
	Exists bool
	Valid  bool
	Expiry time.Time
}

type tokenFlight struct {
	done  chan struct{}
	token Token
	err   error
}

type credentialSlot struct {
	mu          sync.Mutex
	token       *Token
	route       Route
	flight      *tokenFlight
	diagnostics []string
}

func (slot *credentialSlot) accessToken(ctx context.Context, expiredErr error) (string, error) {
	slot.mu.Lock()
	if token, ok := validToken(slot.token, time.Now()); ok {
		slot.route = token.Route
		slot.mu.Unlock()
		return token.AccessToken, nil
	}
	flight := slot.flight
	slot.mu.Unlock()
	if flight == nil {
		return "", expiredErr
	}
	token, err := waitTokenFlight(ctx, flight)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", expiredErr
	}
	return token.AccessToken, nil
}

func (slot *credentialSlot) appendDiagnostic(diagnostic string) {
	slot.diagnostics = appendDiagnostic(slot.diagnostics, diagnostic)
}

func (slot *credentialSlot) complete(flight *tokenFlight, acquired Acquired, err error, diagnostics ...string) {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	flight.err = err
	if err == nil {
		flight.token = acquired.Token
		slot.token = &acquired.Token
		slot.route = acquired.Token.Route
		for _, diagnostic := range diagnostics {
			slot.appendDiagnostic(diagnostic)
		}
	}
	slot.flight = nil
	close(flight.done)
}

func (slot *credentialSlot) takeDiagnostic() string {
	slot.mu.Lock()
	defer slot.mu.Unlock()

	diagnostics := strings.Join(slot.diagnostics, "\n")
	slot.diagnostics = nil
	return diagnostics
}

const maxPendingDiagnostics = 32

// Source resolves one configured account's read, write, and send credentials. It is
// safe for concurrent use and keeps each credential class independent.
type Source struct {
	cfg  *Config
	acct *AccountConfig

	read  credentialSlot
	write credentialSlot
	send  credentialSlot
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
		s.read.mu.Lock()
		s.read.route = token.Route
		s.read.mu.Unlock()
		return token, nil
	}

	s.read.mu.Lock()
	if token, ok := validToken(s.read.token, time.Now()); ok {
		s.read.route = token.Route
		s.read.mu.Unlock()
		return token, nil
	}
	if s.read.flight != nil {
		flight := s.read.flight
		s.read.mu.Unlock()
		return waitTokenFlight(ctx, flight)
	}

	fingerprint := sourceFingerprint(s.acct.Name, ClassRead, s.acct.Read)
	if fingerprint != "" {
		cached, err := readCache(s.acct.Name, fingerprint)
		if err != nil {
			s.read.mu.Unlock()
			return Token{}, err
		}
		if cached.valid(time.Now()) {
			token := Token{AccessToken: cached.AccessToken, Route: RouteCache, Expiry: cached.Expiry}
			s.read.token = &token
			s.read.route = token.Route
			s.read.mu.Unlock()
			return token, nil
		}
	}

	flight := &tokenFlight{done: make(chan struct{})}
	s.read.flight = flight
	s.read.mu.Unlock()

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

	s.read.complete(flight, acquired, err, acquired.Diagnostic)
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

func waitTokenFlight(ctx context.Context, flight *tokenFlight) (Token, error) {
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

	s.read.mu.Lock()
	defer s.read.mu.Unlock()
	s.read.token = nil
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
	s.read.mu.Lock()
	defer s.read.mu.Unlock()
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
	s.read.mu.Lock()
	defer s.read.mu.Unlock()
	return s.read.route
}

// TakeDiagnostic drains queued credential-command completion notes for one
// class in acquisition order. The queue is bounded; when full, oldest notes
// are dropped before newer completed acquisitions are appended.
func (s *Source) TakeDiagnostic(class Class) string {
	slot := s.slotForClass(class)
	if slot == nil {
		return ""
	}
	return slot.takeDiagnostic()
}

func (s *Source) slotForClass(class Class) *credentialSlot {
	switch class {
	case ClassRead:
		return &s.read
	case ClassWrite:
		return &s.write
	case ClassSend:
		return &s.send
	default:
		return nil
	}
}

func appendDiagnostic(diagnostics []string, diagnostic string) []string {
	if diagnostic == "" {
		return diagnostics
	}
	diagnostics = append(diagnostics, diagnostic)
	if len(diagnostics) <= maxPendingDiagnostics {
		return diagnostics
	}
	return append([]string(nil), diagnostics[len(diagnostics)-maxPendingDiagnostics:]...)
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
	case ClassSend:
		return acct.Send
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
