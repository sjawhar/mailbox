package gmail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxBatchParts = 100

type batchItem struct {
	id          string
	resultIndex int
	method      string
	path        string
	query       url.Values
	body        any
}

type batchResult struct {
	body []byte
}

func (c *Client) doBatch(ctx context.Context, creds Credentials, items []batchItem) ([]batchResult, []batchFailure, error) {
	results := make([]batchResult, len(items))
	pending := make([]batchItem, len(items))
	for index, item := range items {
		item.resultIndex = index
		pending[index] = item
	}

	var terminalFailures []batchFailure
	unauthorizedRetries := 0
	rateLimitRetries := 0
	for {
		outcome, err := c.doBatchRequest(ctx, creds, pending, &unauthorizedRetries)
		if err != nil {
			if isRateLimitError(err) && rateLimitRetries < maxRateLimitRetries {
				if err := c.waitForRateLimit(ctx, rateLimitRetries, retryAfter(err)); err != nil {
					return results, terminalFailures, err
				}
				rateLimitRetries++
				continue
			}

			var apiErr *APIError
			if errors.As(err, &apiErr) {
				for _, item := range pending {
					terminalFailures = append(terminalFailures, batchFailure{
						id:     item.id,
						status: apiErr.Status,
						err:    err,
					})
				}
				return results, terminalFailures, nil
			}
			return results, terminalFailures, err
		}

		mergeBatchResults(results, pending, outcome.results)
		terminalFailures = append(terminalFailures, outcome.failures...)
		if len(outcome.retryItems) == 0 {
			return results, terminalFailures, nil
		}
		if rateLimitRetries == maxRateLimitRetries {
			for _, retryItem := range outcome.retryItems {
				terminalFailures = append(terminalFailures, retryItem.failure)
			}
			return results, terminalFailures, nil
		}
		if err := c.waitForRateLimit(ctx, rateLimitRetries, batchRetryAfter(outcome.retryItems)); err != nil {
			return results, terminalFailures, err
		}
		rateLimitRetries++
		pending = make([]batchItem, len(outcome.retryItems))
		for index, retryItem := range outcome.retryItems {
			pending[index] = retryItem.item
		}
	}
}

func mergeBatchResults(results []batchResult, pending []batchItem, current []batchResult) {
	for index, result := range current {
		results[pending[index].resultIndex] = result
	}
}

func batchRetryAfter(items []batchRetryItem) time.Duration {
	var delay time.Duration
	for _, item := range items {
		if itemDelay := retryAfter(item.failure.err); itemDelay > delay {
			delay = itemDelay
		}
	}
	return delay
}

func (c *Client) doBatchRequest(ctx context.Context, creds Credentials, items []batchItem, unauthorizedRetries *int) (batchOutcome, error) {
	for {
		token, err := creds.AccessToken(ctx)
		if err != nil {
			return batchOutcome{}, err
		}

		body, contentType, err := buildBatchBody(items)
		if err != nil {
			return batchOutcome{}, err
		}
		endpoint, err := c.endpoint("/batch/gmail/v1", nil)
		if err != nil {
			return batchOutcome{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
		if err != nil {
			return batchOutcome{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", contentType)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return batchOutcome{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			if *unauthorizedRetries == 0 {
				*unauthorizedRetries++
				if err := creds.Invalidate(ctx); err != nil {
					return batchOutcome{}, err
				}
				continue
			}
			return batchOutcome{}, stillUnauthorizedError()
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			err := decodeAPIError(resp)
			resp.Body.Close()
			return batchOutcome{}, err
		}

		outcome, err := parseBatchResponse(resp, items)
		resp.Body.Close()
		if err != nil {
			return batchOutcome{}, err
		}
		if !outcome.unauthorized {
			return outcome, nil
		}
		if *unauthorizedRetries == 1 {
			return batchOutcome{}, stillUnauthorizedError()
		}
		*unauthorizedRetries++
		if err := creds.Invalidate(ctx); err != nil {
			return batchOutcome{}, err
		}
	}
}

func buildBatchBody(items []batchItem) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, item := range items {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", "application/http")
		headers.Set("Content-ID", fmt.Sprintf("<item%d>", index))
		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, "", err
		}

		target := (&url.URL{Path: item.path, RawQuery: item.query.Encode()}).RequestURI()
		if _, err := fmt.Fprintf(part, "%s %s HTTP/1.1\r\n", item.method, target); err != nil {
			return nil, "", err
		}
		if item.body == nil {
			if _, err := fmt.Fprint(part, "\r\n"); err != nil {
				return nil, "", err
			}
			continue
		}

		encoded, err := json.Marshal(item.body)
		if err != nil {
			return nil, "", err
		}
		if _, err := fmt.Fprintf(part, "Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(encoded), encoded); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &body, fmt.Sprintf("multipart/mixed; boundary=%s", writer.Boundary()), nil
}

func parseBatchResponse(resp *http.Response, items []batchItem) (batchOutcome, error) {
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return batchOutcome{}, fmt.Errorf("gmail: parse batch response Content-Type: %w", err)
	}
	if mediaType != "multipart/mixed" {
		return batchOutcome{}, fmt.Errorf("gmail: batch response Content-Type = %q, want multipart/mixed", mediaType)
	}

	outcome := batchOutcome{results: make([]batchResult, len(items))}
	received := make([]bool, len(items))
	reader := multipart.NewReader(resp.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return batchOutcome{}, fmt.Errorf("gmail: read batch response part: %w", err)
		}

		index, err := responseItemIndex(part.Header.Get("Content-ID"))
		if err != nil {
			return batchOutcome{}, err
		}
		if index < 0 || index >= len(items) {
			return batchOutcome{}, fmt.Errorf("gmail: batch response item index %d is out of range", index)
		}
		if received[index] {
			return batchOutcome{}, fmt.Errorf("gmail: batch response contains duplicate result for thread '%s'", items[index].id)
		}

		inner, err := http.ReadResponse(bufio.NewReader(part), nil)
		if err != nil {
			return batchOutcome{}, fmt.Errorf("gmail: read batch HTTP response: %w", err)
		}
		body, err := io.ReadAll(inner.Body)
		inner.Body.Close()
		if err != nil {
			return batchOutcome{}, fmt.Errorf("gmail: read batch response body: %w", err)
		}
		received[index] = true
		if inner.StatusCode == http.StatusUnauthorized {
			outcome.unauthorized = true
			continue
		}
		if inner.StatusCode < http.StatusOK || inner.StatusCode >= http.StatusMultipleChoices {
			failure := batchFailure{
				id:     items[index].id,
				status: inner.StatusCode,
				err: decodeAPIErrorBodyWithRetryAfter(
					inner.StatusCode,
					bytes.NewReader(body),
					retryAfterDuration(inner.Header.Get("Retry-After")),
				),
			}
			if isRateLimitError(failure.err) {
				outcome.retryItems = append(outcome.retryItems, batchRetryItem{
					item:    items[index],
					failure: failure,
				})
			} else {
				outcome.failures = append(outcome.failures, failure)
			}
			continue
		}
		outcome.results[index] = batchResult{body: body}
	}

	if outcome.unauthorized {
		return batchOutcome{unauthorized: true}, nil
	}
	for index, found := range received {
		if !found {
			return batchOutcome{}, fmt.Errorf("gmail: batch response missing result for thread '%s'", items[index].id)
		}
	}
	return outcome, nil
}

type batchFailure struct {
	id     string
	status int
	err    error
}

type batchOutcome struct {
	results      []batchResult
	retryItems   []batchRetryItem
	failures     []batchFailure
	unauthorized bool
}

type batchRetryItem struct {
	item    batchItem
	failure batchFailure
}

type batchFailuresError struct {
	failures []batchFailure
}

func (e *batchFailuresError) Error() string {
	details := make([]string, len(e.failures))
	for index, failure := range e.failures {
		var apiErr *APIError
		if errors.As(failure.err, &apiErr) && apiErr.Reason != "" {
			details[index] = fmt.Sprintf("%s (%d %s)", failure.id, failure.status, apiErr.Reason)
			continue
		}
		details[index] = fmt.Sprintf("%s (%d)", failure.id, failure.status)
	}
	return fmt.Sprintf("gmail: batch failed for thread(s) %s", strings.Join(details, ", "))
}

func (e *batchFailuresError) Unwrap() []error {
	errs := make([]error, len(e.failures))
	for index, failure := range e.failures {
		errs[index] = failure.err
	}
	return errs
}

func newBatchFailure(failures []batchFailure) *batchFailuresError {
	return &batchFailuresError{failures: failures}
}

func responseItemIndex(contentID string) (int, error) {
	contentID = strings.Trim(strings.TrimSpace(contentID), "<>")
	const prefix = "response-item"
	if !strings.HasPrefix(contentID, prefix) {
		return 0, fmt.Errorf("gmail: batch response has invalid Content-ID %q", contentID)
	}
	index, err := strconv.Atoi(strings.TrimPrefix(contentID, prefix))
	if err != nil || index < 0 {
		return 0, fmt.Errorf("gmail: batch response has invalid Content-ID %q", contentID)
	}
	return index, nil
}
