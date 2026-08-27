package gmail

import (
	"encoding/json"
	"testing"
)

const sampleMessage = `{
  "id": "19900001", "threadId": "19900000",
  "labelIds": ["INBOX", "UNREAD"],
  "snippet": "hello",
  "internalDate": "1756252800000",
  "sizeEstimate": 4096,
  "payload": {
    "partId": "", "mimeType": "multipart/alternative", "filename": "",
    "headers": [
      {"name": "From", "value": "Alice <alice@example.com>"},
      {"name": "Subject", "value": "Hi"}
    ],
    "body": {"size": 0},
    "parts": [
      {"partId": "0", "mimeType": "text/plain", "filename": "",
       "headers": [{"name": "Content-Type", "value": "text/plain; charset=UTF-8"}],
       "body": {"size": 5, "data": "aGVsbG8"}}
    ]
  }
}`

func TestMessageDecodeAndHelpers(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(sampleMessage), &m); err != nil {
		t.Fatal(err)
	}
	if m.InternalDate != 1756252800000 {
		t.Fatalf("InternalDate = %d", m.InternalDate)
	}
	if got := m.Header("from"); got != "Alice <alice@example.com>" {
		t.Fatalf("Header(from) = %q", got) // case-insensitive
	}
	if m.Header("x-missing") != "" {
		t.Fatal("missing header must be empty string")
	}
	if !m.HasLabel("UNREAD") || m.HasLabel("SPAM") {
		t.Fatal("HasLabel wrong")
	}
}
