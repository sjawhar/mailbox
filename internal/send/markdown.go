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
	source := []byte(markdown)
	document := htmlRenderer.Parser().Parse(text.NewReader(source))
	sanitizeDestinations(document, source)

	var out bytes.Buffer
	if err := htmlRenderer.Renderer().Render(&out, source, document); err != nil {
		return "", fmt.Errorf("send: render markdown body: %w", err)
	}
	return out.String(), nil
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
