package gmail

import (
	"encoding/base64"
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

func TestHeaderDecodesRFC2047Latin1QEncoding(t *testing.T) {
	message := &Message{Payload: &MessagePart{Headers: []Header{{
		Name:  "Subject",
		Value: "=?ISO-8859-1?Q?R=E9sum=E9_for_Jos=E9?=",
	}}}}

	if got, want := message.Header("Subject"), "Résumé for José"; got != want {
		t.Fatalf("Header(Subject) = %q, want %q", got, want)
	}
}

func TestSender(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "plain display name", header: "Example User <notifications@example.com>", want: "Example User <notifications@example.com>"},
		{name: "quoted display name", header: `"Example Commenter (via Google Docs)" <comments-noreply@docs.google.com>`, want: "Example Commenter (via Google Docs) <comments-noreply@docs.google.com>"},
		{name: "bare address", header: "notices@example.com", want: "notices@example.com"},
		{name: "leading comment", header: "(Alice) <alice@example.test>", want: "Alice <alice@example.test>"},
		{name: "trailing comment", header: "Alice <alice@example.test> (notifications)", want: "Alice <alice@example.test>"},
		{name: "in-phrase comment", header: "Alice (notifications) <alice@example.test>", want: "Alice <alice@example.test>"},
		{name: "nested comment", header: "Alice <alice@example.test> (notifications (team))", want: "Alice <alice@example.test>"},
		{name: "bare address with comment", header: "alice@example.test (Alice)", want: "Alice <alice@example.test>"},
		{name: "quoted display name containing parens", header: `"Alice (nick)" <a@x>`, want: "Alice (nick) <a@x>"},
		{name: "encoded word literal", header: "=?UTF-8?Q?literal?= <sender@example.test>", want: "=?UTF-8?Q?literal?= <sender@example.test>"},
		{name: "unterminated comment", header: "Alice (billing <alice@example.test>", want: "Alice (billing <alice@example.test>"},
		{name: "unterminated quoted display name", header: `"Alice <alice@example.test>`, want: `"Alice <alice@example.test>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Sender(test.header); got != test.want {
				t.Fatalf("Sender(%q) = %q, want %q", test.header, got, test.want)
			}
		})
	}
}

func TestHeaderRepairsDoubleMojibake(t *testing.T) {
	message := &Message{Payload: &MessagePart{Headers: []Header{{
		Name:  "Subject",
		Value: "Re: Next step: Terminal, the Game Ã¢ÂœÂ¨",
	}}}}

	if got, want := message.Header("Subject"), "Re: Next step: Terminal, the Game ✨"; got != want {
		t.Fatalf("Header(Subject) = %q, want %q", got, want)
	}
}

func TestHeaderUnfoldsBeforeListRendering(t *testing.T) {
	message := &Message{Payload: &MessagePart{Headers: []Header{{
		Name:  "Subject",
		Value: "first\r\n second",
	}}}}
	if got, want := message.Header("Subject"), "first second"; got != want {
		t.Fatalf("Header(Subject) = %q, want %q", got, want)
	}
}

func TestHeaderPreservesOrdinaryWhitespaceWhileUnfolding(t *testing.T) {
	message := &Message{Payload: &MessagePart{Headers: []Header{{
		Name:  "Subject",
		Value: "Release  notes\r\n continued",
	}}}}
	if got, want := message.Header("Subject"), "Release  notes continued"; got != want {
		t.Fatalf("Header(Subject) = %q, want %q", got, want)
	}
}

func TestSenderDoesNotDecodeAnAlreadyDecodedLiteral(t *testing.T) {
	literal := "=?UTF-8?Q?literal?="
	decoded := literal + " <sender@example.test>"
	encoded := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(decoded)) + "?="
	message := &Message{Payload: &MessagePart{Headers: []Header{{Name: "From", Value: encoded}}}}

	if got := Sender(message.Header("From")); got != decoded {
		t.Fatalf("Sender(Header(From)) = %q, want literal %q", got, decoded)
	}
}
