package auth

import (
	"context"
	"errors"
	"time"
)

// ErrExpiredSendToken reports that a non-acquiring send credential view has no
// usable token. The surface owns the decision to unlock again.
var ErrExpiredSendToken = errors.New("send token expired; a new unlock is required")

// SendToken resolves the send credential. It uses an in-memory single flight
// and never writes a send token to the read-token cache.
func (s *Source) SendToken(ctx context.Context, acq Acquirer) (Token, error) {
	s.send.mu.Lock()
	if token, ok := validToken(s.send.token, time.Now()); ok {
		s.send.route = token.Route
		s.send.mu.Unlock()
		return token, nil
	}
	if s.send.flight != nil {
		flight := s.send.flight
		s.send.mu.Unlock()
		return waitTokenFlight(ctx, flight)
	}
	flight := &tokenFlight{done: make(chan struct{})}
	s.send.flight = flight
	s.send.mu.Unlock()

	acquired, err := acq.Acquire(ctx, s.acct, ClassSend)
	if err == nil && acquired.Token.Expiry.IsZero() {
		acquired.Token.Expiry = time.Now().Add(time.Hour)
	}

	if err == nil && acquired.Token.Scope != "" {
		s.send.complete(flight, acquired, err, acquired.Diagnostic, "granted scope: "+acquired.Token.Scope)
	} else {
		s.send.complete(flight, acquired, err, acquired.Diagnostic)
	}
	if err != nil {
		return Token{}, err
	}
	return acquired.Token, nil
}

// InvalidateSend clears only the in-memory send slot. It never acquires.
func (s *Source) InvalidateSend() {
	s.send.mu.Lock()
	s.send.token = nil
	s.send.mu.Unlock()
}

func (s *Source) SendRoute() Route {
	s.send.mu.Lock()
	defer s.send.mu.Unlock()
	return s.send.route
}

// SendScope returns the granted OAuth scope in the live send slot.
func (s *Source) SendScope() string {
	s.send.mu.Lock()
	defer s.send.mu.Unlock()
	if s.send.token == nil {
		return ""
	}
	return s.send.token.Scope
}

func (s *Source) SendCredentials() *SendCredentials {
	return &SendCredentials{source: s}
}

// SendCredentials presents the send slot to Gmail without acquiring.
type SendCredentials struct {
	source *Source
}

func (c *SendCredentials) AccessToken(ctx context.Context) (string, error) {
	return c.source.send.accessToken(ctx, ErrExpiredSendToken)
}

func (c *SendCredentials) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.source.InvalidateSend()
	return nil
}
