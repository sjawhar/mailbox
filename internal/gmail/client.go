package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://gmail.googleapis.com"

var errStillUnauthorized = errors.New("gmail: still unauthorized after re-minting credentials (check 'mailbox status')")

// Credentials supplies and invalidates Gmail access tokens.
type Credentials interface {
	AccessToken(ctx context.Context) (string, error)
	Invalidate(ctx context.Context) error
}

// Client calls the Gmail REST API.
type Client struct {
	Creds   Credentials
	BaseURL string
	HTTP    *http.Client

	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// NewClient returns a Gmail client using creds for every request.
func NewClient(creds Credentials) *Client {
	baseURL := defaultBaseURL
	if override := os.Getenv("MAILBOX_GMAIL_BASE_URL"); override != "" {
		baseURL = override
	}
	return &Client{
		Creds:   creds,
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		sleep:   sleepWithContext,
		jitter:  rateLimitJitter,
	}
}

// ListOptions controls a single Gmail threads.list page.
type ListOptions struct {
	Query      string
	LabelIDs   []string
	MaxResults int64
}

// ListThreads returns one page of threads matching opts.
func (c *Client) ListThreads(ctx context.Context, opts ListOptions) (*ThreadList, error) {
	query := url.Values{}
	if opts.MaxResults != 0 {
		query.Set("maxResults", strconv.FormatInt(opts.MaxResults, 10))
	}
	if opts.Query != "" {
		query.Set("q", opts.Query)
	}
	for _, labelID := range opts.LabelIDs {
		query.Add("labelIds", labelID)
	}

	var threads ThreadList
	if err := c.do(ctx, http.MethodGet, "/gmail/v1/users/me/threads", query, nil, &threads); err != nil {
		return nil, err
	}
	return &threads, nil
}

// GetThread returns a Gmail thread in format.
func (c *Client) GetThread(ctx context.Context, id, format string) (*Thread, error) {
	var thread Thread
	query := url.Values{"format": {format}}
	if err := c.do(ctx, http.MethodGet, "/gmail/v1/users/me/threads/"+url.PathEscape(id), query, nil, &thread); err != nil {
		return nil, err
	}
	return &thread, nil
}

// GetThreadsMetadata fetches metadata for ids in Gmail batch requests.
func (c *Client) GetThreadsMetadata(ctx context.Context, ids []string) ([]*Thread, error) {
	threads := make([]*Thread, 0, len(ids))
	for start := 0; start < len(ids); start += maxBatchParts {
		end := min(start+maxBatchParts, len(ids))
		items := make([]batchItem, 0, end-start)
		for _, id := range ids[start:end] {
			items = append(items, batchItem{
				id:     id,
				method: http.MethodGet,
				path:   "/gmail/v1/users/me/threads/" + url.PathEscape(id),
				query: url.Values{
					"format":          {"metadata"},
					"metadataHeaders": {"From", "To", "Subject", "Date"},
				},
			})
		}
		results, err := c.doBatch(ctx, items)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			var thread Thread
			if err := json.Unmarshal(result.body, &thread); err != nil {
				return nil, fmt.Errorf("gmail: decode batch thread metadata: %w", err)
			}
			threads = append(threads, &thread)
		}
	}
	return threads, nil
}

// ModifyThreads adds and removes labels from ids.
func (c *Client) ModifyThreads(ctx context.Context, ids, addLabelIDs, removeLabelIDs []string) error {
	if len(ids) == 0 {
		return nil
	}
	body := modifyThreadRequest{
		AddLabelIDs:    nonNilStrings(addLabelIDs),
		RemoveLabelIDs: nonNilStrings(removeLabelIDs),
	}
	if len(ids) == 1 {
		return c.do(ctx, http.MethodPost, "/gmail/v1/users/me/threads/"+url.PathEscape(ids[0])+"/modify", nil, body, nil)
	}
	return c.batchThreadOperations(ctx, ids, "/modify", body)
}

// TrashThreads moves ids to Gmail trash.
func (c *Client) TrashThreads(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) == 1 {
		return c.do(ctx, http.MethodPost, "/gmail/v1/users/me/threads/"+url.PathEscape(ids[0])+"/trash", nil, nil, nil)
	}
	return c.batchThreadOperations(ctx, ids, "/trash", nil)
}

// ListLabels returns all labels for the current user.
func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	var response struct {
		Labels []Label `json:"labels"`
	}
	if err := c.do(ctx, http.MethodGet, "/gmail/v1/users/me/labels", nil, nil, &response); err != nil {
		return nil, err
	}
	return response.Labels, nil
}

// GetAttachment returns decoded Gmail attachment bytes.
func (c *Client) GetAttachment(ctx context.Context, messageID, attachmentID string) ([]byte, error) {
	var response struct {
		Data string `json:"data"`
	}
	path := "/gmail/v1/users/me/messages/" + url.PathEscape(messageID) + "/attachments/" + url.PathEscape(attachmentID)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}

	data, err := base64.RawURLEncoding.DecodeString(response.Data)
	if err == nil {
		return data, nil
	}
	data, paddedErr := base64.URLEncoding.DecodeString(response.Data)
	if paddedErr != nil {
		return nil, fmt.Errorf("gmail: decode attachment data: %w", paddedErr)
	}
	return data, nil
}

// GetProfile returns the current Gmail profile.
func (c *Client) GetProfile(ctx context.Context) (*Profile, error) {
	var profile Profile
	if err := c.do(ctx, http.MethodGet, "/gmail/v1/users/me/profile", nil, nil, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// ResolveThreadID returns id when it names a thread, or its parent thread when it names a message.
func (c *Client) ResolveThreadID(ctx context.Context, id string) (string, error) {
	if _, err := c.GetThread(ctx, id, "minimal"); err == nil {
		return id, nil
	} else if !IsNotFound(err) {
		return "", err
	}

	var message struct {
		ThreadID string `json:"threadId"`
	}
	path := "/gmail/v1/users/me/messages/" + url.PathEscape(id)
	if err := c.do(ctx, http.MethodGet, path, url.Values{"format": {"minimal"}}, nil, &message); err != nil {
		if IsNotFound(err) {
			return "", fmt.Errorf("no thread or message with id '%s' in account", id)
		}
		return "", err
	}
	if message.ThreadID == "" {
		return "", fmt.Errorf("gmail: message '%s' response omitted threadId", id)
	}
	return message.ThreadID, nil
}

type modifyThreadRequest struct {
	AddLabelIDs    []string `json:"addLabelIds"`
	RemoveLabelIDs []string `json:"removeLabelIds"`
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (c *Client) batchThreadOperations(ctx context.Context, ids []string, suffix string, body any) error {
	for start := 0; start < len(ids); start += maxBatchParts {
		end := min(start+maxBatchParts, len(ids))
		items := make([]batchItem, 0, end-start)
		for _, id := range ids[start:end] {
			items = append(items, batchItem{
				id:     id,
				method: http.MethodPost,
				path:   "/gmail/v1/users/me/threads/" + url.PathEscape(id) + suffix,
				body:   body,
			})
		}
		if _, err := c.doBatch(ctx, items); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	unauthorizedRetries := 0
	rateLimitRetries := 0
	for {
		token, err := c.Creds.AccessToken(ctx)
		if err != nil {
			return err
		}
		req, err := c.newRequest(ctx, method, path, query, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			if unauthorizedRetries == 0 {
				unauthorizedRetries++
				if err := c.Creds.Invalidate(ctx); err != nil {
					return err
				}
				continue
			}
			return stillUnauthorizedError()
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			err := decodeAPIError(resp)
			resp.Body.Close()
			if !isRateLimitError(err) || rateLimitRetries == maxRateLimitRetries {
				return err
			}
			if err := c.waitForRateLimit(ctx, rateLimitRetries, retryAfter(err)); err != nil {
				return err
			}
			rateLimitRetries++
			continue
		}
		if out == nil {
			resp.Body.Close()
			return nil
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		return err
	}
}

func stillUnauthorizedError() error {
	return errStillUnauthorized
}

const (
	maxRateLimitRetries     = 3
	rateLimitRetryBaseDelay = 250 * time.Millisecond
	rateLimitRetryMaxDelay  = rateLimitRetryBaseDelay << (maxRateLimitRetries - 1)
)

func (c *Client) waitForRateLimit(ctx context.Context, retry int, retryAfter time.Duration) error {
	delay := rateLimitRetryBaseDelay << retry
	if retryAfter > delay {
		delay = retryAfter
	} else {
		jitter := c.jitter
		if jitter == nil {
			jitter = rateLimitJitter
		}
		delay += jitter(delay)
	}
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	return sleep(ctx, delay)
}

func retryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.retryAfter
	}
	return 0
}

func rateLimitJitter(delay time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(delay)))
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) endpoint(path string, query url.Values) (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", fmt.Errorf("gmail: parse base URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body any) (*http.Request, error) {
	endpoint, err := c.endpoint(path, query)
	if err != nil {
		return nil, err
	}

	var requestBody *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(encoded)
	} else {
		requestBody = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
