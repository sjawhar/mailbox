package toontest

import "time"

// Shapes returns one fully-populated instance per mailbox payload shape:
// listing, read thread, status, action result, attachment list, attachment
// save, open, and credential error envelope. These mirror internal/cli's
// unexported JSON payloads; its contract test pins them field-for-field.
func Shapes(s1, s2, s3 string) []any {
	return []any{
		listingPayload{Account: s1, Threads: []threadRow{{N: 1, ID: s2, Subject: s3, From: s1, Date: s2, Snippet: s3, Unread: true, Labels: []string{s1}}}},
		readPayload{
			Account:      s1,
			ID:           s2,
			Subject:      s3,
			Participants: []string{s1},
			Messages: []renderedMessage{{
				ID:          s2,
				From:        s3,
				To:          s1,
				Date:        time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
				Markdown:    s2,
				Links:       []link{{N: 1, Text: s3, URL: s1}},
				Attachments: []attachment{{N: 1, Filename: s2, MimeType: s3, Size: 7}},
			}},
		},
		statusOutput{
			Config: s1,
			Accounts: []statusAccount{{
				Name:    s2,
				Default: true,
				Read:    statusSource{Kind: s3, Argv0: s1, Interactive: true, Label: s2},
				Write:   statusSource{Kind: s3, Argv0: s1, Interactive: true, Label: s2},
				Route:   s3,
				Cache:   statusCache{Path: s1, Exists: true, Valid: true, Expiry: s2},
				Profile: statusProfile{Email: s3},
				Pinned:  true,
				Error:   s1,
			}},
			OK: true,
		},
		actionPayload{Account: s1, Action: s2, ThreadIDs: []string{s3}, OK: true},
		attachmentListPayload{Account: s1, ThreadID: s2, Attachments: []attachment{{N: 1, Filename: s3, MimeType: s1, Size: 7}}},
		attachmentSavePayload{Account: s1, File: s2, Filename: s3, Size: 7},
		openPayload{Account: s1, ThreadID: s2, MessageID: s3, File: s1},
		errorEnvelope{Error: errorDetail{Code: s1, Account: s2, ConfigKey: s3, Config: s1}},
	}
}

type listingPayload struct {
	Account string      `json:"account"`
	Threads []threadRow `json:"threads"`
}

type threadRow struct {
	N       int      `json:"n"`
	ID      string   `json:"id"`
	Subject string   `json:"subject"`
	From    string   `json:"from"`
	Date    string   `json:"date"`
	Snippet string   `json:"snippet"`
	Unread  bool     `json:"unread"`
	Labels  []string `json:"labels"`
}

type readPayload struct {
	Account      string            `json:"account"`
	ID           string            `json:"id"`
	Subject      string            `json:"subject"`
	Participants []string          `json:"participants"`
	Messages     []renderedMessage `json:"messages"`
}

type renderedMessage struct {
	ID          string       `json:"id"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	Date        time.Time    `json:"date"`
	Markdown    string       `json:"markdown"`
	Links       []link       `json:"links"`
	Attachments []attachment `json:"attachments"`
}

type link struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	URL  string `json:"url"`
}

type attachment struct {
	N        int    `json:"n"`
	Filename string `json:"filename"`
	MimeType string `json:"mime"`
	Size     int64  `json:"size"`
}

type statusOutput struct {
	Config   string          `json:"config"`
	Accounts []statusAccount `json:"accounts"`
	OK       bool            `json:"ok"`
}

type statusAccount struct {
	Name    string        `json:"name"`
	Default bool          `json:"default"`
	Read    statusSource  `json:"read"`
	Write   statusSource  `json:"write"`
	Route   string        `json:"route"`
	Cache   statusCache   `json:"cache"`
	Profile statusProfile `json:"profile"`
	Pinned  bool          `json:"pinned"`
	Error   string        `json:"error"`
}

type statusSource struct {
	Kind        string `json:"kind"`
	Argv0       string `json:"argv0,omitempty"`
	Interactive bool   `json:"interactive"`
	Label       string `json:"label,omitempty"`
}

type statusCache struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Valid  bool   `json:"valid"`
	Expiry string `json:"expiry,omitempty"`
}

type statusProfile struct {
	Email string `json:"email"`
}

type actionPayload struct {
	Account   string   `json:"account"`
	Action    string   `json:"action"`
	ThreadIDs []string `json:"threadIds"`
	OK        bool     `json:"ok"`
}

type attachmentListPayload struct {
	Account     string       `json:"account"`
	ThreadID    string       `json:"threadId"`
	Attachments []attachment `json:"attachments"`
}

type attachmentSavePayload struct {
	Account  string `json:"account"`
	File     string `json:"file"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type openPayload struct {
	Account   string `json:"account"`
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
	File      string `json:"file"`
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Account   string `json:"account"`
	ConfigKey string `json:"config_key"`
	Config    string `json:"config"`
}
