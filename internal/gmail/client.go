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

// ClientConfig supplies credentials for a Gmail client. Read and Account are
// required. Write and Send are optional credential classes.
type ClientConfig struct {
	Read    Credentials
	Write   Credentials
	Send    Credentials
	Account string
}

// Client calls the Gmail REST API.
type Client struct {
	read    Credentials
	write   Credentials
	send    Credentials
	account string
	BaseURL string
	HTTP    *http.Client

	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// NewClient constructs a Gmail client.
func NewClient(config ClientConfig) *Client {
	if config.Read == nil {
		panic("gmail: client requires read credentials")
	}
	if config.Account == "" {
		panic("gmail: client requires an account")
	}
	baseURL := defaultBaseURL
	if override := os.Getenv("MAILBOX_GMAIL_BASE_URL"); override != "" {
		baseURL = override
	}
	return &Client{
		read:    config.Read,
		write:   config.Write,
		send:    config.Send,
		account: config.Account,
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
	PageToken  string
}

// WriteFailure is one terminal per-thread write outcome.
type WriteFailure struct {
	ID     string
	Status int
	Reason string
	Err    error
}

// WriteReceipts records confirmed outcomes for a thread write.
type WriteReceipts struct {
	Succeeded []string
	Failed    []WriteFailure
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
	if opts.PageToken != "" {
		query.Set("pageToken", opts.PageToken)
	}

	var threads ThreadList
	if err := c.call(ctx, opThreadsList, nil, query, nil, &threads); err != nil {
		return nil, err
	}
	return &threads, nil
}

// GetThread returns a Gmail thread in format.
func (c *Client) GetThread(ctx context.Context, id, format string) (*Thread, error) {
	var thread Thread
	query := url.Values{"format": {format}}
	if err := c.call(ctx, opThreadsGet, []string{id}, query, nil, &thread); err != nil {
		return nil, err
	}
	return &thread, nil
}

// GetMessage fetches a Gmail message with all metadata headers.
func (c *Client) GetMessage(ctx context.Context, id string) (*Message, error) {
	var message Message
	if err := c.call(ctx, opMessagesGet, []string{id}, url.Values{"format": {"metadata"}}, nil, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

// GetMessageRaw fetches a Gmail message with its complete raw content.
func (c *Client) GetMessageRaw(ctx context.Context, id string) (*Message, error) {
	var message Message
	if err := c.call(ctx, opMessagesGet, []string{id}, url.Values{"format": {"raw"}}, nil, &message); err != nil {
		return nil, err
	}
	return &message, nil
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
				method: routes[opThreadsGet].method,
				path:   routePath(opThreadsGet, []string{id}),
				query: url.Values{
					"format":          {"metadata"},
					"metadataHeaders": {"From", "To", "Cc", "Subject", "Date", "List-ID"},
				},
			})
		}
		results, failures, err := c.doBatch(ctx, opThreadsGet, items)
		if err != nil {
			if len(failures) > 0 {
				err = fmt.Errorf("%w; prior terminal batch failures: %s", err, newBatchFailure(failures))
			}
			return nil, err
		}
		if len(failures) > 0 {
			return nil, c.batchScopeMapped(opThreadsGet, newBatchFailure(failures))
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
		return c.call(ctx, opThreadsModify, []string{ids[0]}, nil, body, nil)
	}
	receipts, err := c.batchThreadReceipts(ctx, opThreadsModify, ids, body)
	return c.batchScopeMapped(opThreadsModify, receiptsError(receipts, err))
}

// TrashThreads moves ids to Gmail trash.
func (c *Client) TrashThreads(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) == 1 {
		return c.call(ctx, opThreadsTrash, []string{ids[0]}, nil, nil, nil)
	}
	receipts, err := c.batchThreadReceipts(ctx, opThreadsTrash, ids, nil)
	return c.batchScopeMapped(opThreadsTrash, receiptsError(receipts, err))
}

// ModifyThreadsReceipts adds and removes labels and returns per-thread outcomes.
func (c *Client) ModifyThreadsReceipts(ctx context.Context, ids, addLabelIDs, removeLabelIDs []string) (WriteReceipts, error) {
	if len(ids) == 0 {
		return WriteReceipts{}, nil
	}
	body := modifyThreadRequest{
		AddLabelIDs:    nonNilStrings(addLabelIDs),
		RemoveLabelIDs: nonNilStrings(removeLabelIDs),
	}
	ids = uniqueIDs(ids)
	if len(ids) == 1 {
		return c.singleWriteReceipt(ids[0], c.call(ctx, opThreadsModify, []string{ids[0]}, nil, body, nil))
	}
	receipts, err := c.batchThreadReceipts(ctx, opThreadsModify, ids, body)
	return receipts, err
}

// TrashThreadsReceipts moves ids to Gmail trash and returns per-thread outcomes.
func (c *Client) TrashThreadsReceipts(ctx context.Context, ids []string) (WriteReceipts, error) {
	if len(ids) == 0 {
		return WriteReceipts{}, nil
	}
	ids = uniqueIDs(ids)
	if len(ids) == 1 {
		return c.singleWriteReceipt(ids[0], c.call(ctx, opThreadsTrash, []string{ids[0]}, nil, nil, nil))
	}
	receipts, err := c.batchThreadReceipts(ctx, opThreadsTrash, ids, nil)
	return receipts, err
}

// SendMessage sends a base64url-encoded MIME message using the send credentials.
func (c *Client) SendMessage(ctx context.Context, raw []byte, threadID string) (*SentMessage, error) {
	if c.send == nil {
		return nil, errors.New("gmail: client has no send credentials")
	}
	var sent SentMessage
	body := sendMessageRequest{
		Raw:      base64.RawURLEncoding.EncodeToString(raw),
		ThreadID: threadID,
	}
	if err := c.call(ctx, opMessagesSend, nil, nil, body, &sent); err != nil {
		return nil, err
	}
	return &sent, nil
}

// ListLabels returns all labels for the current user.
func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	var response struct {
		Labels []Label `json:"labels"`
	}
	if err := c.call(ctx, opLabelsList, nil, nil, nil, &response); err != nil {
		return nil, err
	}
	return response.Labels, nil
}

// GetAttachment returns decoded Gmail attachment bytes.
func (c *Client) GetAttachment(ctx context.Context, messageID, attachmentID string) ([]byte, error) {
	var response struct {
		Data string `json:"data"`
	}
	if err := c.call(ctx, opAttachmentsGet, []string{messageID, attachmentID}, nil, nil, &response); err != nil {
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
	if err := c.call(ctx, opProfileGet, nil, nil, nil, &profile); err != nil {
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
	if err := c.call(ctx, opMessagesGet, []string{id}, url.Values{"format": {"minimal"}}, nil, &message); err != nil {
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
type sendMessageRequest struct {
	Raw      string `json:"raw"`
	ThreadID string `json:"threadId,omitempty"`
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// scopeMapped makes the required Gmail scope available to every surface.
func (c *Client) scopeMapped(err error, scope string) error {
	if err == nil || !IsInsufficientScope(err) {
		return err
	}
	return &ErrInsufficientScope{Account: c.account, Scope: scope, Err: err}
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

func (c *Client) batchThreadReceipts(ctx context.Context, op operation, ids []string, body any) (WriteReceipts, error) {
	var receipts WriteReceipts
	for start := 0; start < len(ids); start += maxBatchParts {
		end := min(start+maxBatchParts, len(ids))
		items := make([]batchItem, 0, end-start)
		for _, id := range ids[start:end] {
			items = append(items, batchItem{
				id:     id,
				method: routes[op].method,
				path:   routePath(op, []string{id}),
				body:   body,
			})
		}
		results, failures, err := c.doBatch(ctx, op, items)
		failedIDs := make(map[string]struct{}, len(failures))
		for _, failure := range failures {
			failedIDs[failure.id] = struct{}{}
			receipts.Failed = append(receipts.Failed, writeFailure(failure))
		}
		if err != nil {
			for index, id := range ids[start:end] {
				if _, failed := failedIDs[id]; failed {
					continue
				}
				if index < len(results) && results[index].body != nil {
					receipts.Succeeded = append(receipts.Succeeded, id)
				}
			}
			return receipts, err
		}
		for _, id := range ids[start:end] {
			if _, failed := failedIDs[id]; !failed {
				receipts.Succeeded = append(receipts.Succeeded, id)
			}
		}
	}
	return receipts, nil
}

func writeFailure(failure batchFailure) WriteFailure {
	reason := ""
	var apiErr *APIError
	if errors.As(failure.err, &apiErr) {
		reason = apiErr.Reason
	}
	return WriteFailure{ID: failure.id, Status: failure.status, Reason: reason, Err: failure.err}
}

func (c *Client) singleWriteReceipt(id string, err error) (WriteReceipts, error) {
	if err == nil {
		return WriteReceipts{Succeeded: []string{id}}, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return WriteReceipts{
			Failed: []WriteFailure{{
				ID:     id,
				Status: apiErr.Status,
				Reason: apiErr.Reason,
				Err:    err,
			}},
		}, nil
	}
	return WriteReceipts{}, err
}

func receiptsError(receipts WriteReceipts, err error) error {
	if err != nil {
		return err
	}
	if len(receipts.Failed) == 0 {
		return nil
	}
	failures := make([]batchFailure, len(receipts.Failed))
	for index, failure := range receipts.Failed {
		failures[index] = batchFailure{id: failure.ID, status: failure.Status, err: failure.Err}
	}
	return newBatchFailure(failures)
}

func (c *Client) do(ctx context.Context, creds Credentials, method, path string, query url.Values, body, out any) error {
	unauthorizedRetries := 0
	rateLimitRetries := 0
	for {
		token, err := creds.AccessToken(ctx)
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
				if err := creds.Invalidate(ctx); err != nil {
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
