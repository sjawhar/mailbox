// Package gmail is a minimal Gmail REST client with batch support.
package gmail

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"
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
	Raw          string       `json:"raw,omitempty"`
}

// SentMessage is Gmail's response after accepting a sent message.
type SentMessage struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

var headerDecoder = &mime.WordDecoder{
	CharsetReader: func(charsetName string, input io.Reader) (io.Reader, error) {
		return charset.NewReaderLabel(charsetName, input)
	},
}

// Header returns the first header with the given name, case-insensitively,
// decoding RFC 2047 encoded words. It returns "" if the header is absent.
func (m *Message) Header(name string) string {
	if m.Payload == nil {
		return ""
	}
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			decoded, err := headerDecoder.DecodeHeader(h.Value)
			if err != nil {
				return unfoldHeader(repairMojibake(h.Value))
			}
			return unfoldHeader(repairMojibake(decoded))
		}
	}
	return ""
}

// RawBytes decodes the raw Gmail message content.
func (m *Message) RawBytes() ([]byte, error) {
	data, err := base64.RawURLEncoding.DecodeString(m.Raw)
	if err == nil {
		return data, nil
	}
	data, paddedErr := base64.URLEncoding.DecodeString(m.Raw)
	if paddedErr != nil {
		return nil, fmt.Errorf("gmail: decode raw message: %w", paddedErr)
	}
	return data, nil
}

// HasLabel reports whether id is in LabelIDs.
func (m *Message) HasLabel(id string) bool {
	return slices.Contains(m.LabelIDs, id)
}

// Sender formats an already-decoded From header as a display name plus address.
// Bare addresses remain addresses.
func Sender(from string) string {
	unfolded := unfoldHeader(from)
	value, comment, complete := stripComments(unfolded)
	if !complete {
		return unfolded
	}
	if end := strings.LastIndex(value, ">"); end == len(value)-1 {
		if start := strings.LastIndex(value[:end], "<"); start >= 0 {
			name := strings.Trim(strings.TrimSpace(value[:start]), `"`)
			address := strings.TrimSpace(value[start+1 : end])
			if name == "" {
				name = comment
			}
			if name == "" {
				return address
			}
			return name + " <" + address + ">"
		}
	}
	if strings.Contains(value, "@") {
		if comment != "" {
			return comment + " <" + value + ">"
		}
	}
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func stripComments(value string) (base, first string, complete bool) {
	var stripped, comment []rune
	var quoted, escaped, commentEscaped bool
	var afterComment, pendingSpace bool
	commentDepth := 0

	startComment := func() {
		for len(stripped) > 0 && unicode.IsSpace(stripped[len(stripped)-1]) {
			stripped = stripped[:len(stripped)-1]
			pendingSpace = true
		}
		commentDepth = 1
		commentEscaped = false
		comment = comment[:0]
		afterComment = true
	}

	for _, r := range value {
		if commentDepth > 0 {
			if commentEscaped {
				comment = append(comment, r)
				commentEscaped = false
				continue
			}
			if r == '\\' {
				comment = append(comment, r)
				commentEscaped = true
				continue
			}
			switch r {
			case '(':
				commentDepth++
				comment = append(comment, r)
			case ')':
				commentDepth--
				if commentDepth == 0 {
					if text := strings.TrimSpace(string(comment)); first == "" && text != "" {
						first = text
					}
				} else {
					comment = append(comment, r)
				}
			default:
				comment = append(comment, r)
			}
			continue
		}

		if quoted {
			stripped = append(stripped, r)
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
			continue
		}

		if afterComment {
			if unicode.IsSpace(r) {
				pendingSpace = true
				continue
			}
			if r != '(' {
				if pendingSpace {
					stripped = append(stripped, ' ')
				}
				afterComment = false
				pendingSpace = false
			}
		}

		switch r {
		case '"':
			quoted = true
			stripped = append(stripped, r)
		case '(':
			startComment()
		default:
			stripped = append(stripped, r)
		}
	}

	if commentDepth != 0 || quoted || escaped || commentEscaped {
		return value, "", false
	}
	return strings.TrimSpace(string(stripped)), first, true
}

func repairMojibake(value string) string {
	for range 2 {
		score := mojibakeScore(value)
		if score == 0 {
			return value
		}
		bytes, err := charmap.Windows1252.NewEncoder().Bytes([]byte(value))
		if err != nil || !utf8.Valid(bytes) {
			return value
		}
		candidate := restoreWindows1252Controls(string(bytes))
		if mojibakeScore(candidate) >= score {
			return value
		}
		value = candidate
	}
	return value
}

func mojibakeScore(value string) int {
	return strings.Count(value, "Ã") + strings.Count(value, "Â") + strings.Count(value, "â")
}

func unfoldHeader(value string) string {
	var unfolded strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\r' && index+1 < len(value) && value[index+1] == '\n' {
			index += 2
			for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
				index++
			}
			unfolded.WriteByte(' ')
			continue
		}
		if value[index] == '\n' {
			index++
			for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
				index++
			}
			unfolded.WriteByte(' ')
			continue
		}
		unfolded.WriteByte(value[index])
		index++
	}
	return unfolded.String()
}

func restoreWindows1252Controls(value string) string {
	return strings.Map(func(r rune) rune {
		if replacement, found := windows1252Controls[r]; found {
			return replacement
		}
		return r
	}, value)
}

var windows1252Controls = map[rune]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†',
	0x87: '‡', 0x88: 'ˆ', 0x89: '‰', 0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ',
	0x8E: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•',
	0x96: '–', 0x97: '—', 0x98: '˜', 0x99: '™', 0x9A: 'š', 0x9B: '›',
	0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ',
}

type Thread struct {
	ID       string     `json:"id"`
	Snippet  string     `json:"snippet,omitempty"`
	Messages []*Message `json:"messages,omitempty"`
}

// FilterThreadsWithLabel returns threads containing at least one message with
// labelID. It returns the original slice without allocating when all threads
// match.
func FilterThreadsWithLabel(threads []*Thread, labelID string) []*Thread {
	for index, thread := range threads {
		if threadHasLabel(thread, labelID) {
			continue
		}
		filtered := make([]*Thread, 0, len(threads)-1)
		filtered = append(filtered, threads[:index]...)
		for _, remaining := range threads[index+1:] {
			if threadHasLabel(remaining, labelID) {
				filtered = append(filtered, remaining)
			}
		}
		return filtered
	}
	return threads
}

func threadHasLabel(thread *Thread, labelID string) bool {
	if thread == nil {
		return false
	}
	for _, message := range thread.Messages {
		if message != nil && message.HasLabel(labelID) {
			return true
		}
	}
	return false
}

// LatestMessage returns the newest message in a thread by Gmail internal date.
func LatestMessage(thread *Thread) *Message {
	var newest *Message
	for _, message := range thread.Messages {
		if newest == nil || message.InternalDate > newest.InternalDate {
			newest = message
		}
	}
	return newest
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
