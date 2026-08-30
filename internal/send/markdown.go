package send

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// htmlRenderer intentionally uses goldmark's safe default: no extensions and
// no html.WithUnsafe(), so raw HTML in mail bodies renders as goldmark's
// "<!-- raw HTML omitted -->" marker.
var htmlRenderer = goldmark.New()

// RenderHTML renders a markdown mail body to the text/html alternative leaf.
// Link, image, and autolink destinations pass an explicit allowlist before
// rendering: https, http, mailto, and bare fragment/empty destinations.
// Every other destination — including all data: URLs and unrecognized custom
// schemes — is removed.
func RenderHTML(markdown string) (string, error) {
	source := sanitizeMarkdownDestinations([]byte(markdown))
	document := htmlRenderer.Parser().Parse(text.NewReader(source))
	sanitizeDestinations(document, source)

	var out bytes.Buffer
	if err := htmlRenderer.Renderer().Render(&out, source, document); err != nil {
		return "", fmt.Errorf("send: render markdown body: %w", err)
	}
	return out.String(), nil
}

// sanitizeMarkdownDestinations handles malformed Markdown link syntax that
// goldmark otherwise leaves as visible source text before its AST sanitizer
// can inspect the destination. Valid links are also checked in
// sanitizeDestinations below.
func sanitizeMarkdownDestinations(source []byte) []byte {
	var sanitized []byte
	written := 0
	for searchFrom := 0; searchFrom < len(source); {
		offset := bytes.Index(source[searchFrom:], []byte("]("))
		if offset < 0 {
			break
		}
		linkEnd := searchFrom + offset
		searchFrom = linkEnd + 2
		if !hasMarkdownLinkLabel(source, linkEnd) {
			continue
		}
		destinationEnd, ok := markdownDestinationEnd(source, searchFrom)
		if !ok {
			continue
		}
		if allowedDestination(bytes.TrimSpace(source[searchFrom:destinationEnd])) {
			searchFrom = destinationEnd + 1
			continue
		}
		if sanitized == nil {
			sanitized = make([]byte, 0, len(source))
		}
		sanitized = append(sanitized, source[written:searchFrom]...)
		sanitized = append(sanitized, ')')
		written = destinationEnd + 1
		searchFrom = written
	}
	if sanitized == nil {
		return source
	}
	return append(sanitized, source[written:]...)
}

func hasMarkdownLinkLabel(source []byte, linkEnd int) bool {
	for index := linkEnd - 1; index >= 0 && source[index] != '\n'; index-- {
		if source[index] == '[' && (index == 0 || source[index-1] != '\\') {
			return true
		}
	}
	return false
}

func markdownDestinationEnd(source []byte, start int) (int, bool) {
	depth := 1
	for index := start; index < len(source); index++ {
		switch source[index] {
		case '\\':
			index++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index, true
			}
		case '\n':
			return 0, false
		}
	}
	return 0, false
}

func sanitizeDestinations(document ast.Node, source []byte) {
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch typed := node.(type) {
		case *ast.Link:
			if !allowedDestination(typed.Destination) {
				typed.Destination = nil
			}
		case *ast.Image:
			if !allowedDestination(typed.Destination) {
				typed.Destination = nil
			}
		case *ast.AutoLink:
			if !allowedDestination(typed.URL(source)) {
				replaceWithText(typed, typed.Label(source))
				return ast.WalkSkipChildren, nil
			}
		}
		return ast.WalkContinue, nil
	})
}

func replaceWithText(node ast.Node, literal []byte) {
	replacement := ast.NewString(literal)
	replacement.SetRaw(false)
	parent := node.Parent()
	parent.ReplaceChild(parent, node, replacement)
}

func allowedDestination(destination []byte) bool {
	if len(destination) == 0 || destination[0] == '#' {
		return true
	}

	value := string(destination)
	colon := strings.IndexByte(value, ':')
	if colon < 0 {
		return false
	}

	switch strings.ToLower(value[:colon]) {
	case "https", "http", "mailto":
		return true
	default:
		return false
	}
}
