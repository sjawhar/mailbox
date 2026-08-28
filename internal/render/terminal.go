package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
)

// SanitizeTerminal removes terminal control sequences from untrusted text while
// retaining newlines and tabs needed to preserve document structure.
func SanitizeTerminal(value string) string {
	var output strings.Builder
	changed := false
	writePrefix := func(index int) {
		if changed {
			return
		}
		changed = true
		output.Grow(len(value))
		output.WriteString(value[:index])
	}

	for index := 0; index < len(value); {
		if value[index] >= 0x80 && value[index] <= 0x9f {
			writePrefix(index)
			switch value[index] {
			case 0x9b:
				index = skipCSI(value, index+1)
			case 0x90, 0x9d, 0x9e, 0x9f:
				index = skipStringControl(value, index+1)
			default:
				index++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		switch {
		case r == '\x1b':
			writePrefix(index)
			index = skipEscapeSequence(value, index+size)
		case r == '\x9b':
			writePrefix(index)
			index = skipCSI(value, index+size)
		case r == '\x90' || r == '\x9d' || r == '\x9e' || r == '\x9f':
			writePrefix(index)
			index = skipStringControl(value, index+size)
		case r == '\n' || r == '\t':
			if changed {
				output.WriteRune(r)
			}
			index += size
		case r < ' ' || (r >= '\x7f' && r <= '\x9f'):
			writePrefix(index)
			index += size
		default:
			if changed {
				output.WriteString(value[index : index+size])
			}
			index += size
		}
	}
	if !changed {
		return value
	}
	return output.String()
}

// RenderTerminalMarkdown sanitizes untrusted Markdown before rendering it for a
// terminal with the selected Glamour style.
func RenderTerminalMarkdown(markdown string, width int, style string) (string, error) {
	if width < 1 {
		width = 1
	}
	options := []glamour.TermRendererOption{glamour.WithWordWrap(width)}
	if style == "" {
		options = append(options, glamour.WithAutoStyle())
	} else {
		options = append(options, glamour.WithStylePath(style))
	}
	renderer, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return "", fmt.Errorf("create terminal renderer: %w", err)
	}
	return renderer.Render(SanitizeTerminal(markdown))
}

func skipEscapeSequence(value string, index int) int {
	if index >= len(value) {
		return index
	}
	r, size := utf8.DecodeRuneInString(value[index:])
	index += size
	switch r {
	case '[':
		return skipCSI(value, index)
	case ']', 'P', '^', '_':
		return skipStringControl(value, index)
	}
	for index < len(value) {
		r, size = utf8.DecodeRuneInString(value[index:])
		if r < '\x20' || r > '/' {
			if r >= '0' && r <= '~' {
				return index + size
			}
			return index
		}
		index += size
	}
	return index
}

func skipCSI(value string, index int) int {
	for index < len(value) {
		r, size := utf8.DecodeRuneInString(value[index:])
		index += size
		if r >= '@' && r <= '~' {
			return index
		}
	}
	return index
}

func skipStringControl(value string, index int) int {
	for index < len(value) {
		if value[index] == 0x9c {
			return index + 1
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == '\a' || r == '\x9c' {
			return index + size
		}
		if r == '\x1b' {
			next, nextSize := utf8.DecodeRuneInString(value[index+size:])
			if next == '\\' {
				return index + size + nextSize
			}
		}
		index += size
	}
	return index
}
