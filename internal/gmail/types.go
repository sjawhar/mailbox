// Package gmail is a minimal Gmail REST client with batch support.
package gmail

import (
	"slices"
	"strings"
)

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PartBody struct {
	AttachmentID string `json:"attachmentId,omitempty"`
	Size         int64  `json:"size"`
	Data         string `json:"data,omitempty"` // base64url (RFC 4648 §5)
}

type MessagePart struct {
	PartID   string         `json:"partId"`
	MimeType string         `json:"mimeType"`
	Filename string         `json:"filename"`
	Headers  []Header       `json:"headers"`
	Body     *PartBody      `json:"body"`
	Parts    []*MessagePart `json:"parts,omitempty"`
}

type Message struct {
	ID           string       `json:"id"`
	ThreadID     string       `json:"threadId"`
	LabelIDs     []string     `json:"labelIds"`
	Snippet      string       `json:"snippet"`
	InternalDate int64        `json:"internalDate,string"` // ms since epoch; Gmail sends it as a JSON string
	SizeEstimate int64        `json:"sizeEstimate"`
	Payload      *MessagePart `json:"payload"`
}

// Header returns the first header with the given name, case-insensitively; "" if absent.
func (m *Message) Header(name string) string {
	if m.Payload == nil {
		return ""
	}
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// HasLabel reports whether id is in LabelIDs.
func (m *Message) HasLabel(id string) bool {
	return slices.Contains(m.LabelIDs, id)
}

type Thread struct {
	ID       string     `json:"id"`
	Snippet  string     `json:"snippet,omitempty"`
	Messages []*Message `json:"messages,omitempty"`
}

type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type Profile struct {
	EmailAddress  string `json:"emailAddress"`
	MessagesTotal int64  `json:"messagesTotal"`
	ThreadsTotal  int64  `json:"threadsTotal"`
}

type ThreadList struct {
	Threads       []*Thread `json:"threads"`
	NextPageToken string    `json:"nextPageToken"`
}
