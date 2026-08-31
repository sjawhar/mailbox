package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"strings"

	"github.com/sjawhar/mailbox/internal/gmail"
	"golang.org/x/net/html/charset"
)

type Options struct {
	KeepQuotes bool
}

type Link struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Attachment's public JSON omits the Gmail transport identifiers.
type Attachment struct {
	N            int    `json:"n"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime"`
	Size         int64  `json:"size"`
	MessageID    string `json:"-"`
	AttachmentID string `json:"-"`
	Inline       []byte `json:"-"`
}

// MessageContent holds the preferred decoded body and message attachments.
type MessageContent struct {
	HTML        string
	Text        string
	Attachments []Attachment
	InlineParts map[string]*gmail.MessagePart
}

// ExtractContent walks a Gmail message payload depth-first to select its body
// and find attachments and CID-addressable inline images.
func ExtractContent(msg *gmail.Message) (*MessageContent, error) {
	if msg == nil || msg.Payload == nil {
		return nil, fmt.Errorf("message has no payload")
	}

	content := &MessageContent{InlineParts: make(map[string]*gmail.MessagePart)}
	var largestHTML string
	var hasPlain bool

	var walk func(*gmail.MessagePart) error
	walk = func(part *gmail.MessagePart) error {
		if part == nil {
			return nil
		}
		if len(part.Parts) > 0 {
			for _, child := range part.Parts {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}

		if isAttachment(part) {
			if contentID := partHeader(part, "Content-ID"); contentID != "" && strings.HasPrefix(strings.ToLower(part.MimeType), "image/") && !isExplicitAttachment(part) {
				content.InlineParts[trimContentID(contentID)] = part
				return nil
			}
			attachment := Attachment{
				Filename:     part.Filename,
				MimeType:     part.MimeType,
				Size:         part.Body.Size,
				MessageID:    msg.ID,
				AttachmentID: part.Body.AttachmentID,
			}
			if attachment.AttachmentID == "" {
				inline, decodeErr := decodePartData(part)
				if decodeErr != nil {
					return decodeErr
				}
				attachment.Inline = inline
			}
			content.Attachments = append(content.Attachments, attachment)
			return nil
		}

		if !strings.EqualFold(part.MimeType, "text/html") && !strings.EqualFold(part.MimeType, "text/plain") {
			return nil
		}
		body, err := decodeTextPart(part)
		if err != nil {
			return err
		}
		if strings.EqualFold(part.MimeType, "text/html") {
			if len(body) > len(largestHTML) {
				largestHTML = body
			}
			return nil
		}
		if !hasPlain {
			content.Text = body
			hasPlain = true
		}
		return nil
	}

	if err := walk(msg.Payload); err != nil {
		return nil, err
	}
	content.HTML = largestHTML
	return content, nil
}

func isAttachment(part *gmail.MessagePart) bool {
	if part.Body == nil {
		return false
	}
	if part.Body.AttachmentID != "" {
		return true
	}
	return part.Body.Data != "" && (part.Filename != "" || hasAttachmentDisposition(part))
}

func isExplicitAttachment(part *gmail.MessagePart) bool {
	return hasAttachmentDisposition(part) || (part.Body != nil && part.Filename != "" && part.Body.AttachmentID != "")
}

func hasAttachmentDisposition(part *gmail.MessagePart) bool {
	disposition, _, err := mime.ParseMediaType(partHeader(part, "Content-Disposition"))
	return err == nil && strings.EqualFold(disposition, "attachment")
}

func decodePartData(part *gmail.MessagePart) ([]byte, error) {
	if part.Body == nil || part.Body.Data == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(part.Body.Data)
	if err == nil {
		return decoded, nil
	}
	decoded, err = base64.URLEncoding.DecodeString(part.Body.Data)
	if err != nil {
		return nil, fmt.Errorf("decode MIME part %q: %w", part.PartID, err)
	}
	return decoded, nil
}

func decodeTextPart(part *gmail.MessagePart) (string, error) {
	label, err := partCharset(part)
	if err != nil {
		return "", err
	}

	decoded, err := decodePartData(part)
	if err != nil {
		return "", err
	}

	if label == "" || strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
		return string(decoded), nil
	}

	reader, err := charset.NewReaderLabel(label, bytes.NewReader(decoded))
	if err != nil {
		return "", fmt.Errorf("decode MIME part %q with charset %q: %w", part.PartID, label, err)
	}
	converted, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read MIME part %q with charset %q: %w", part.PartID, label, err)
	}
	return string(converted), nil
}

func partCharset(part *gmail.MessagePart) (string, error) {
	contentType := partHeader(part, "Content-Type")
	if contentType == "" {
		return "", nil
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parse Content-Type for MIME part %q: %w", part.PartID, err)
	}
	return params["charset"], nil
}

func partHeader(part *gmail.MessagePart, name string) string {
	for _, header := range part.Headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func trimContentID(contentID string) string {
	contentID = strings.TrimSpace(contentID)
	contentID = strings.TrimPrefix(contentID, "<")
	return strings.TrimSuffix(contentID, ">")
}
