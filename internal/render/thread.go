package render

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/mailbox/internal/gmail"
)

// RenderedMessage is a message body with thread-wide references and attachments.
type RenderedMessage struct {
	ID          string       `json:"id"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	Date        time.Time    `json:"date"`
	Markdown    string       `json:"markdown"`
	Links       []Link       `json:"links"`
	Attachments []Attachment `json:"attachments"`
}

// RenderedThread is the complete terminal-friendly representation of a thread.
type RenderedThread struct {
	ID           string            `json:"id"`
	Subject      string            `json:"subject"`
	Participants []string          `json:"participants"`
	Messages     []RenderedMessage `json:"messages"`
}

// RenderThread renders every message in newest-to-oldest order.
func RenderThread(thread *gmail.Thread, opts Options) (*RenderedThread, error) {
	messages, contents, _, err := threadContent(thread)
	if err != nil {
		return nil, err
	}

	rendered := &RenderedThread{ID: thread.ID}
	if len(messages) > 0 {
		rendered.Subject = messages[len(messages)-1].Header("Subject")
	}
	seenParticipants := make(map[string]struct{})
	nextLinkN := 1
	for index, message := range messages {
		body, err := RenderBody(contents[index], opts, nextLinkN)
		if err != nil {
			return nil, err
		}
		nextLinkN += len(body.Links)

		from := message.Header("From")
		if _, seen := seenParticipants[from]; !seen {
			seenParticipants[from] = struct{}{}
			rendered.Participants = append(rendered.Participants, from)
		}
		rendered.Messages = append(rendered.Messages, RenderedMessage{
			ID:          message.ID,
			From:        from,
			To:          message.Header("To"),
			Date:        time.UnixMilli(message.InternalDate).UTC(),
			Markdown:    body.Markdown,
			Links:       body.Links,
			Attachments: contents[index].Attachments,
		})
	}
	return rendered, nil
}

// AllLinks returns each rendered message's links in thread order.
func (thread *RenderedThread) AllLinks() []Link {
	var links []Link
	for _, message := range thread.Messages {
		links = append(links, message.Links...)
	}
	return links
}

// Markdown returns the complete thread in the documented terminal format.
func (thread *RenderedThread) Markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# %s\n\n(newest first)\n\n", SanitizeTerminal(thread.Subject))
	for index, message := range thread.Messages {
		fmt.Fprintf(&output, "## %s → %s, %s\n\n", SanitizeTerminal(message.From), SanitizeTerminal(message.To), message.Date.UTC().Format("2006-01-02 15:04 MST"))
		markdown := SanitizeTerminal(TerminalMarkdown(message.Markdown, message.Links))
		output.WriteString(markdown)
		if !strings.HasSuffix(markdown, "\n") {
			output.WriteByte('\n')
		}
		if len(message.Attachments) > 0 {
			output.WriteByte('\n')
			output.WriteString("Attachments:")
			for attachmentIndex, attachment := range message.Attachments {
				if attachmentIndex > 0 {
					output.WriteString(",")
				}
				fmt.Fprintf(&output, " [%d] %s (%s, %s)", attachment.N, SanitizeTerminal(attachment.Filename), SanitizeTerminal(attachment.MimeType), formatSize(attachment.Size))
			}
			output.WriteByte('\n')
		}
		if index+1 < len(thread.Messages) {
			output.WriteString("\n---\n\n")
		}
	}
	return output.String()
}

const terminalLinkDisplayLimit = 96

// TerminalMarkdown replaces only generated trailing link-table definitions
// with safe display values. RenderedMessage retains its original Markdown and
// Link URLs for JSON consumers and opening links.
func TerminalMarkdown(markdown string, links []Link) string {
	if len(links) == 0 {
		return markdown
	}

	var source, display strings.Builder
	for _, link := range links {
		fmt.Fprintf(&source, "[%d]: %s\n", link.N, link.URL)
		fmt.Fprintf(&display, "[%d]: %s\n", link.N, terminalLinkDisplayURL(link.URL))
	}
	definitions := source.String()
	if !strings.HasSuffix(markdown, definitions) {
		return markdown
	}
	return strings.TrimSuffix(markdown, definitions) + display.String()
}

func terminalLinkDisplayURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return truncateTerminalLinkURL(raw)
	}
	display := parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		display += "…"
	}
	return truncateTerminalLinkURL(display)
}

func truncateTerminalLinkURL(value string) string {
	if utf8.RuneCountInString(value) <= terminalLinkDisplayLimit {
		return value
	}
	runes := []rune(value)
	return string(runes[:terminalLinkDisplayLimit-1]) + "…"
}

// ThreadAttachments returns every attachment in the same order and numbering as
// RenderThread.
func ThreadAttachments(thread *gmail.Thread) ([]Attachment, error) {
	_, _, attachments, err := threadContent(thread)
	if err != nil {
		return nil, err
	}
	return attachments, nil
}

func threadContent(thread *gmail.Thread) ([]*gmail.Message, []*MessageContent, []Attachment, error) {
	messages := append([]*gmail.Message(nil), thread.Messages...)
	sort.SliceStable(messages, func(left, right int) bool {
		return messages[left].InternalDate > messages[right].InternalDate
	})

	contents := make([]*MessageContent, len(messages))
	var attachments []Attachment
	nextAttachmentN := 1
	for index, message := range messages {
		content, err := ExtractContent(message)
		if err != nil {
			return nil, nil, nil, err
		}
		for attachmentIndex := range content.Attachments {
			content.Attachments[attachmentIndex].N = nextAttachmentN
			nextAttachmentN++
			attachments = append(attachments, content.Attachments[attachmentIndex])
		}
		contents[index] = content
	}
	return messages, contents, attachments, nil
}

func formatSize(size int64) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%.1f B", float64(size))
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}
