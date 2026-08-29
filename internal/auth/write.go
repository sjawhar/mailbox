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

type writeFlight struct {
	done  chan struct{}
	token Token
	err   error
}

// WriteToken resolves a write credential. It uses an in-memory single flight
// and never writes a write token to the read-token cache.
func (s *Source) WriteToken(ctx context.Context, acq Acquirer) (string, error) {
	if accessToken := os.Getenv("MAILBOX_TOKEN"); accessToken != "" {
		s.wrMu.Lock()
		s.wrRoute = RouteEnvToken
		s.wrMu.Unlock()
		return accessToken, nil
	}

	s.wrMu.Lock()
	if token, ok := validToken(s.wrToken, time.Now()); ok {
		s.wrRoute = token.Route
		s.wrMu.Unlock()
		return token.AccessToken, nil
	}
	if s.wrFlight != nil {
		flight := s.wrFlight
		s.wrMu.Unlock()
		token, err := waitWriteFlight(ctx, flight)
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	}
	flight := &writeFlight{done: make(chan struct{})}
	s.wrFlight = flight
	s.wrMu.Unlock()

	acquired, err := acq.Acquire(ctx, s.acct, ClassWrite)
	if err == nil && acquired.Token.Expiry.IsZero() {
		acquired.Token.Expiry = time.Now().Add(time.Hour)
	}

	s.wrMu.Lock()
	flight.err = err
	if err == nil {
		flight.token = acquired.Token
		s.wrToken = &acquired.Token
		s.wrRoute = acquired.Token.Route
		s.wrDiagnostics = appendDiagnostic(s.wrDiagnostics, acquired.Diagnostic)
	}
	s.wrFlight = nil
	close(flight.done)
	s.wrMu.Unlock()
	if err != nil {
		return "", err
	}
	return acquired.Token.AccessToken, nil
}

func waitWriteFlight(ctx context.Context, flight *writeFlight) (Token, error) {
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

// InvalidateWrite clears only the in-memory write slot. It never acquires.
func (s *Source) InvalidateWrite() {
	s.wrMu.Lock()
	s.wrToken = nil
	s.wrMu.Unlock()
}

func (s *Source) WriteRoute() Route {
	s.wrMu.Lock()
	defer s.wrMu.Unlock()
	return s.wrRoute
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
		w.source.wrMu.Lock()
		w.source.wrRoute = RouteEnvToken
		w.source.wrMu.Unlock()
		return accessToken, nil
	}

	s := w.source
	s.wrMu.Lock()
	if token, ok := validToken(s.wrToken, time.Now()); ok {
		s.wrRoute = token.Route
		s.wrMu.Unlock()
		return token.AccessToken, nil
	}
	flight := s.wrFlight
	s.wrMu.Unlock()
	if flight == nil {
		return "", ErrExpiredToken
	}
	token, err := waitWriteFlight(ctx, flight)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrExpiredToken
	}
	return token.AccessToken, nil
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
