package auth

import (
	"context"
	"errors"
	"os"
	"time"
)

// ErrExpiredToken reports that a non-acquiring write credential view has no
// usable token. The surface owns the decision to unlock again.
var ErrExpiredToken = errors.New("write token expired; a new unlock is required")

// WriteToken resolves a write credential. It uses an in-memory single flight
// and never writes a write token to the read-token cache.
func (s *Source) WriteToken(ctx context.Context, acq Acquirer) (string, error) {
	if accessToken := os.Getenv("MAILBOX_TOKEN"); accessToken != "" {
		s.write.mu.Lock()
		s.write.route = RouteEnvToken
		s.write.mu.Unlock()
		return accessToken, nil
	}

	s.write.mu.Lock()
	if token, ok := validToken(s.write.token, time.Now()); ok {
		s.write.route = token.Route
		s.write.mu.Unlock()
		return token.AccessToken, nil
	}
	if s.write.flight != nil {
		flight := s.write.flight
		s.write.mu.Unlock()
		token, err := waitTokenFlight(ctx, flight)
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	}
	flight := &tokenFlight{done: make(chan struct{})}
	s.write.flight = flight
	s.write.mu.Unlock()

	acquired, err := acq.Acquire(ctx, s.acct, ClassWrite)
	if err == nil && acquired.Token.Expiry.IsZero() {
		acquired.Token.Expiry = time.Now().Add(time.Hour)
	}

	s.write.complete(flight, acquired, err, acquired.Diagnostic)
	if err != nil {
		return "", err
	}
	return acquired.Token.AccessToken, nil
}

// InvalidateWrite clears only the in-memory write slot. It never acquires.
func (s *Source) InvalidateWrite() {
	s.write.mu.Lock()
	s.write.token = nil
	s.write.mu.Unlock()
}

func (s *Source) WriteRoute() Route {
	s.write.mu.Lock()
	defer s.write.mu.Unlock()
	return s.write.route
}

func (s *Source) WriteCredentials() *WriteCredentials {
	return &WriteCredentials{source: s}
}

// WriteCredentials presents the write slot to Gmail without acquiring.
type WriteCredentials struct {
	source *Source
}

func (w *WriteCredentials) AccessToken(ctx context.Context) (string, error) {
	if accessToken := os.Getenv("MAILBOX_TOKEN"); accessToken != "" {
		w.source.write.mu.Lock()
		w.source.write.route = RouteEnvToken
		w.source.write.mu.Unlock()
		return accessToken, nil
	}

	return w.source.write.accessToken(ctx, ErrExpiredToken)
}

func (w *WriteCredentials) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getenv("MAILBOX_TOKEN") != "" {
		return nil
	}
	w.source.InvalidateWrite()
	return nil
}
