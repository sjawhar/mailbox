package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sjawhar/mailbox/internal/gmail"
	xhtml "golang.org/x/net/html"
)

// AttachmentFetcher retrieves a decoded Gmail attachment body for CID inlining.
type AttachmentFetcher func(ctx context.Context, messageID, attachmentID string) ([]byte, error)

// OriginalHTML returns the newest decoded HTML body without terminal-oriented
// cleaning so it can be opened in a browser.
func OriginalHTML(thread *gmail.Thread) (msgID, html string, err error) {
	var newestDate int64
	found := false
	for _, message := range thread.Messages {
		content, extractErr := ExtractContent(message)
		if extractErr != nil {
			return "", "", extractErr
		}
		if content.HTML == "" || (found && message.InternalDate <= newestDate) {
			continue
		}
		msgID = message.ID
		html = content.HTML
		newestDate = message.InternalDate
		found = true
	}
	if !found {
		return "", "", errors.New("thread has no HTML part to open — use 'mailbox read'")
	}
	return msgID, html, nil
}

// InlineCIDs replaces CID image sources with browser-ready data URIs.
func InlineCIDs(ctx context.Context, html string, msg *gmail.Message, fetch AttachmentFetcher) (string, error) {
	content, err := ExtractContent(msg)
	if err != nil {
		return "", err
	}
	document, err := xhtml.Parse(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	if err := inlineCIDs(ctx, document, msg.ID, content.InlineParts, fetch); err != nil {
		return "", err
	}

	var output bytes.Buffer
	if err := xhtml.Render(&output, document); err != nil {
		return "", err
	}
	return output.String(), nil
}

const browserCSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'"

var browserElements = map[string]bool{
	"a": true, "b": true, "blockquote": true, "body": true, "br": true, "code": true,
	"dd": true, "div": true, "dl": true, "dt": true, "em": true, "font": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"head": true, "hr": true, "html": true, "i": true, "img": true, "li": true,
	"ol": true, "p": true, "pre": true, "s": true, "small": true, "span": true,
	"strike": true, "strong": true, "sub": true, "sup": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true, "tr": true,
	"u": true, "ul": true,
}

var browserUnsafeElements = map[string]bool{
	"base": true, "embed": true, "form": true, "frame": true, "frameset": true,
	"iframe": true, "input": true, "link": true, "meta": true, "object": true,
	"script": true, "svg": true, "template": true,
}

// BrowserSafeHTML produces a static browser document from untrusted mail HTML.
// It preserves only passive formatting and data URI images, then injects the
// Content Security Policy that backstops the allowlist.
func BrowserSafeHTML(html string) (string, error) {
	document, err := xhtml.Parse(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	sanitizeBrowserNode(document)
	head := browserHead(document)
	head.AppendChild(&xhtml.Node{
		Type: xhtml.ElementNode,
		Data: "meta",
		Attr: []xhtml.Attribute{
			{Key: "http-equiv", Val: "Content-Security-Policy"},
			{Key: "content", Val: browserCSP},
		},
	})

	var output bytes.Buffer
	if err := xhtml.Render(&output, document); err != nil {
		return "", err
	}
	return output.String(), nil
}

func sanitizeBrowserNode(node *xhtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == xhtml.CommentNode {
			node.RemoveChild(child)
		} else if child.Type == xhtml.ElementNode {
			element := strings.ToLower(child.Data)
			switch {
			case browserUnsafeElements[element]:
				node.RemoveChild(child)
			case browserElements[element]:
				child.Data = element
				child.Attr = safeBrowserAttributes(child)
				sanitizeBrowserNode(child)
			default:
				sanitizeBrowserNode(child)
				for grandchild := child.FirstChild; grandchild != nil; {
					following := grandchild.NextSibling
					child.RemoveChild(grandchild)
					node.InsertBefore(grandchild, child)
					grandchild = following
				}
				node.RemoveChild(child)
			}
		}
		child = next
	}
}

func safeBrowserAttributes(node *xhtml.Node) []xhtml.Attribute {
	attributes := make([]xhtml.Attribute, 0, len(node.Attr))
	for _, attribute := range node.Attr {
		name := strings.ToLower(attribute.Key)
		switch name {
		case "align", "border", "cellpadding", "cellspacing", "class", "colspan", "dir", "height", "id", "lang", "rowspan", "style", "title", "valign", "width":
			attributes = append(attributes, xhtml.Attribute{Key: name, Val: attribute.Val})
		case "alt":
			if node.Data == "img" {
				attributes = append(attributes, xhtml.Attribute{Key: name, Val: attribute.Val})
			}
		case "src":
			if node.Data == "img" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(attribute.Val)), "data:") {
				attributes = append(attributes, xhtml.Attribute{Key: name, Val: attribute.Val})
			}
		}
	}
	return attributes
}

func browserHead(document *xhtml.Node) *xhtml.Node {
	var htmlNode, head *xhtml.Node
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			switch strings.ToLower(node.Data) {
			case "html":
				htmlNode = node
			case "head":
				head = node
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if head != nil {
		return head
	}
	head = &xhtml.Node{Type: xhtml.ElementNode, Data: "head"}
	htmlNode.InsertBefore(head, htmlNode.FirstChild)
	return head
}

// WriteHTMLBackstop writes the newest HTML message as a browser-safe static
// document and returns its message ID and temporary path.
func WriteHTMLBackstop(ctx context.Context, thread *gmail.Thread, fetch AttachmentFetcher) (messageID, path string, err error) {
	messageID, html, err := OriginalHTML(thread)
	if err != nil {
		return "", "", err
	}
	message := messageByID(thread.Messages, messageID)
	if message == nil {
		return "", "", fmt.Errorf("thread %q has no message %q selected for HTML", thread.ID, messageID)
	}
	html, err = InlineCIDs(ctx, html, message, fetch)
	if err != nil {
		return "", "", err
	}
	html, err = BrowserSafeHTML(html)
	if err != nil {
		return "", "", err
	}
	file, err := os.CreateTemp("", "mailbox-*.html")
	if err != nil {
		return "", "", fmt.Errorf("create HTML file: %w", err)
	}
	path = file.Name()
	if _, err := file.WriteString(html); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", "", fmt.Errorf("write HTML file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("close HTML file: %w", err)
	}
	return messageID, path, nil
}

func messageByID(messages []*gmail.Message, id string) *gmail.Message {
	for _, message := range messages {
		if message.ID == id {
			return message
		}
	}
	return nil
}

// OpenURL opens a browser target with the caller-supplied scrubbed environment.
func OpenURL(target string, env []string) error {
	opener, err := exec.LookPath("xdg-open")
	if err != nil {
		return fmt.Errorf("find xdg-open: %w", err)
	}
	command := exec.Command(opener, target)
	command.Env = env
	if err := command.Run(); err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	return nil
}

func inlineCIDs(ctx context.Context, node *xhtml.Node, messageID string, parts map[string]*gmail.MessagePart, fetch AttachmentFetcher) error {
	if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "img") {
		for index := range node.Attr {
			attr := &node.Attr[index]
			if !strings.EqualFold(attr.Key, "src") || !strings.HasPrefix(attr.Val, "cid:") {
				continue
			}
			part := parts[strings.TrimPrefix(attr.Val, "cid:")]
			if part == nil {
				continue
			}

			data, err := cidData(ctx, messageID, part, fetch)
			if err != nil {
				return err
			}
			attr.Val = "data:" + part.MimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := inlineCIDs(ctx, child, messageID, parts, fetch); err != nil {
			return err
		}
	}
	return nil
}

func cidData(ctx context.Context, messageID string, part *gmail.MessagePart, fetch AttachmentFetcher) ([]byte, error) {
	if part.Body.Data != "" {
		data, err := base64.RawURLEncoding.DecodeString(part.Body.Data)
		if err == nil {
			return data, nil
		}
		data, err = base64.URLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			return nil, fmt.Errorf("decode inline CID part %q: %w", part.PartID, err)
		}
		return data, nil
	}
	if fetch == nil {
		return nil, fmt.Errorf("inline CID part %q needs an attachment fetcher", part.PartID)
	}
	return fetch(ctx, messageID, part.Body.AttachmentID)
}
