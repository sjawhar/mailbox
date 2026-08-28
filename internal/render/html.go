package render

import (
	"bytes"
	"strings"

	xhtml "golang.org/x/net/html"
)

type CleanResult struct {
	HTML          string
	QuoteStripped bool
}

// CleanHTML removes mail-client noise from htmlSrc while retaining content that
// is useful in a terminal rendering.
func CleanHTML(htmlSrc string, opts Options) (CleanResult, error) {
	doc, err := xhtml.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return CleanResult{}, err
	}

	result := CleanResult{}
	cleanNode(doc, opts, &result)

	var rendered bytes.Buffer
	if err := xhtml.Render(&rendered, doc); err != nil {
		return CleanResult{}, err
	}
	result.HTML = rendered.String()
	return result, nil
}

func cleanNode(node *xhtml.Node, opts Options, result *CleanResult) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling

		switch child.Type {
		case xhtml.CommentNode:
			node.RemoveChild(child)
		case xhtml.TextNode:
			child.Data = stripZeroWidth(child.Data)
		case xhtml.ElementNode:
			if !opts.KeepQuotes && isQuote(child) {
				node.RemoveChild(child)
				result.QuoteStripped = true
				break
			}
			if isHidden(child) {
				node.RemoveChild(child)
				break
			}
			if strings.EqualFold(child.Data, "img") {
				if isTrackingPixel(child) {
					node.RemoveChild(child)
					break
				}
				if alt := attribute(child, "alt"); alt != "" {
					node.InsertBefore(&xhtml.Node{Type: xhtml.TextNode, Data: "[image: " + alt + "]"}, child)
				}
				node.RemoveChild(child)
				break
			}
			cleanNode(child, opts, result)
		}

		child = next
	}
}

func isQuote(node *xhtml.Node) bool {
	for _, class := range strings.Fields(attribute(node, "class")) {
		if class == "gmail_quote" || class == "gmail_quote_container" {
			return true
		}
	}
	return false
}

func isHidden(node *xhtml.Node) bool {
	style := normalizedStyle(attribute(node, "style"))
	return strings.Contains(style, "display:none") ||
		strings.Contains(style, "opacity:0") ||
		(strings.Contains(style, "max-height:0") && strings.Contains(style, "overflow:hidden"))
}

func isTrackingPixel(node *xhtml.Node) bool {
	width := attribute(node, "width")
	height := attribute(node, "height")
	if (width == "0" || width == "1") && (height == "0" || height == "1") {
		return true
	}
	return stylePropertyMatches(attribute(node, "style"), "width", "0", "0px", "1px")
}

func stylePropertyMatches(style, property string, values ...string) bool {
	for _, declaration := range strings.Split(style, ";") {
		name, value, found := strings.Cut(declaration, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), property) {
			continue
		}
		value = strings.TrimSuffix(normalizedStyle(value), "!important")
		for _, expected := range values {
			if value == expected {
				return true
			}
		}
	}
	return false
}

func attribute(node *xhtml.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func normalizedStyle(style string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			return -1
		default:
			return r
		}
	}, strings.ToLower(style))
}

func stripZeroWidth(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return -1
		default:
			return r
		}
	}, value)
}
