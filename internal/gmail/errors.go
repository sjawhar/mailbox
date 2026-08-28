package gmail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIError is a non-success response returned by the Gmail API.
type APIError struct {
	Status     int
	Reason     string
	Message    string
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("gmail: API error %d (%s): %s", e.Status, e.Reason, e.Message)
	}
	return fmt.Sprintf("gmail: API error %d: %s", e.Status, e.Message)
}

// ErrInsufficientScope reports a Gmail 403 whose cause is a missing OAuth
// scope on a mutation call, carrying account + required scope (spec §4).
type ErrInsufficientScope struct {
	Account string
	Scope   string
	Err     error
}

func (e *ErrInsufficientScope) Error() string {
	return fmt.Sprintf("gmail: %s token lacks the %s scope: %v", e.Account, e.Scope, e.Err)
}

func (e *ErrInsufficientScope) Unwrap() error { return e.Err }

// IsInsufficientScope reports whether err means the token lacks a required Gmail scope.
func IsInsufficientScope(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		return false
	}
	return apiErr.Reason == "insufficientPermissions" ||
		apiErr.Reason == "ACCESS_TOKEN_SCOPE_INSUFFICIENT" ||
		strings.Contains(strings.ToLower(apiErr.Message), "insufficient authentication scopes")
}

// IsNotFound reports whether err is a Gmail API not-found response.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func isRateLimitError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusTooManyRequests {
		return true
	}
	return apiErr.Status == http.StatusForbidden &&
		(apiErr.Reason == "rateLimitExceeded" || apiErr.Reason == "userRateLimitExceeded")
}

type googleErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func decodeAPIError(resp *http.Response) error {
	return decodeAPIErrorBodyWithRetryAfter(
		resp.StatusCode,
		resp.Body,
		retryAfterDuration(resp.Header.Get("Retry-After")),
	)
}

func decodeAPIErrorBody(status int, body io.Reader) error {
	return decodeAPIErrorBodyWithRetryAfter(status, body, 0)
}

func decodeAPIErrorBodyWithRetryAfter(status int, body io.Reader, retryAfter time.Duration) error {
	var decoded googleErrorResponse
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return &APIError{
			Status:     status,
			Message:    fmt.Sprintf("unable to decode Gmail error response: %v", err),
			retryAfter: retryAfter,
		}
	}

	apiErr := &APIError{Status: status, Message: decoded.Error.Message, retryAfter: retryAfter}
	if len(decoded.Error.Errors) > 0 {
		apiErr.Reason = decoded.Error.Errors[0].Reason
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(status)
	}
	return apiErr
}

func retryAfterDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds >= int64(rateLimitRetryMaxDelay/time.Second) {
			return rateLimitRetryMaxDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return capRetryAfter(time.Until(when))
	}
	return 0
}

func capRetryAfter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	if delay > rateLimitRetryMaxDelay {
		return rateLimitRetryMaxDelay
	}
	return delay
}
