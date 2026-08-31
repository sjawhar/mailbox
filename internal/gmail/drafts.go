package gmail

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
)

// Draft is a Gmail server-side draft and its underlying message. Gmail
// rotates Message.ID on every server-side edit, which is what resume pinning
// relies on.
type Draft struct {
	ID      string   `json:"id"`
	Message *Message `json:"message"`
}

type createDraftRequest struct {
	Message sendMessageRequest `json:"message"`
}

// ListDrafts returns one page of drafts (Gmail caps maxResults at 500).
func (c *Client) ListDrafts(ctx context.Context, max int64) ([]*Draft, error) {
	query := url.Values{}
	if max > 0 {
		query.Set("maxResults", strconv.FormatInt(max, 10))
	}
	var response struct {
		Drafts []*Draft `json:"drafts"`
	}
	if err := c.call(ctx, opDraftsList, nil, query, nil, &response); err != nil {
		return nil, err
	}
	return response.Drafts, nil
}

// GetDraft fetches one draft; format is "full" or "metadata" ("" omits it).
func (c *Client) GetDraft(ctx context.Context, id, format string) (*Draft, error) {
	query := url.Values{}
	if format != "" {
		query.Set("format", format)
	}
	var draft Draft
	if err := c.call(ctx, opDraftsGet, []string{id}, query, nil, &draft); err != nil {
		return nil, err
	}
	return &draft, nil
}

// CreateDraft stores raw as a Gmail draft, threading it when threadID is set.
func (c *Client) CreateDraft(ctx context.Context, raw []byte, threadID string) (*Draft, error) {
	body := createDraftRequest{Message: sendMessageRequest{
		Raw:      base64.RawURLEncoding.EncodeToString(raw),
		ThreadID: threadID,
	}}
	var draft Draft
	if err := c.call(ctx, opDraftsCreate, nil, nil, body, &draft); err != nil {
		return nil, err
	}
	return &draft, nil
}

// DeleteDraft permanently removes a draft.
func (c *Client) DeleteDraft(ctx context.Context, id string) error {
	return c.call(ctx, opDraftsDelete, []string{id}, nil, nil, nil)
}
