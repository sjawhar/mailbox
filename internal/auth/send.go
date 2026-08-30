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
	s.sendMu.Lock()
	if token, ok := validToken(s.sendToken, time.Now()); ok {
		s.sendRoute = token.Route
		s.sendMu.Unlock()
		return token, nil
	}
	if s.sendFlight != nil {
		flight := s.sendFlight
		s.sendMu.Unlock()
		return waitWriteFlight(ctx, flight)
	}
	flight := &writeFlight{done: make(chan struct{})}
	s.sendFlight = flight
	s.sendMu.Unlock()

	acquired, err := acq.Acquire(ctx, s.acct, ClassSend)
	if err == nil && acquired.Token.Expiry.IsZero() {
		acquired.Token.Expiry = time.Now().Add(time.Hour)
	}

	s.sendMu.Lock()
	flight.err = err
	if err == nil {
		flight.token = acquired.Token
		s.sendToken = &acquired.Token
		s.sendRoute = acquired.Token.Route
		s.sendDiagnostics = appendDiagnostic(s.sendDiagnostics, acquired.Diagnostic)
		if acquired.Token.Scope != "" {
			s.sendDiagnostics = appendDiagnostic(s.sendDiagnostics, "granted scope: "+acquired.Token.Scope)
		}
	}
	s.sendFlight = nil
	close(flight.done)
	s.sendMu.Unlock()
	if err != nil {
		return Token{}, err
	}
	return acquired.Token, nil
}

// InvalidateSend clears only the in-memory send slot. It never acquires.
func (s *Source) InvalidateSend() {
	s.sendMu.Lock()
	s.sendToken = nil
	s.sendMu.Unlock()
}

func (s *Source) SendRoute() Route {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.sendRoute
}

// SendScope returns the granted OAuth scope in the live send slot.
func (s *Source) SendScope() string {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendToken == nil {
		return ""
	}
	return s.sendToken.Scope
}

func (s *Source) SendCredentials() *SendCredentials {
	return &SendCredentials{source: s}
}

// SendCredentials presents the send slot to Gmail without acquiring.
type SendCredentials struct {
	source *Source
}

func (c *SendCredentials) AccessToken(ctx context.Context) (string, error) {
	s := c.source
	s.sendMu.Lock()
	if token, ok := validToken(s.sendToken, time.Now()); ok {
		s.sendRoute = token.Route
		s.sendMu.Unlock()
		return token.AccessToken, nil
	}
	flight := s.sendFlight
	s.sendMu.Unlock()
	if flight == nil {
		return "", ErrExpiredSendToken
	}
	token, err := waitWriteFlight(ctx, flight)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrExpiredSendToken
	}
	return token.AccessToken, nil
}

func (c *SendCredentials) Invalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.source.InvalidateSend()
	return nil
}
