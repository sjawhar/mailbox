// Package compose provides the editor-command grammar and private draft files
// used by mailbox composition.
package compose

import (
	"fmt"
	"strings"
	"unicode"
)

type wordState uint8

const (
	bare wordState = iota
	singleQuoted
	doubleQuoted
	escapedBare
	escapedDoubleQuoted
)

// SplitWords parses an editor command as POSIX shell words, limited to
// whitespace splitting, quotes, and backslash escaping. It never expands or
// interprets shell syntax.
func SplitWords(value string) ([]string, error) {
	var words []string
	var word strings.Builder
	state := bare
	started := false

	for _, r := range value {
		switch state {
		case bare:
			switch {
			case unicode.IsSpace(r):
				if started {
					words = append(words, word.String())
					word.Reset()
					started = false
				}
			case r == '\\':
				state = escapedBare
				started = true
			case r == '\'':
				state = singleQuoted
				started = true
			case r == '"':
				state = doubleQuoted
				started = true
			default:
				word.WriteRune(r)
				started = true
			}
		case escapedBare:
			word.WriteRune(r)
			state = bare
		case singleQuoted:
			if r == '\'' {
				state = bare
			} else {
				word.WriteRune(r)
			}
		case doubleQuoted:
			switch r {
			case '"':
				state = bare
			case '\\':
				state = escapedDoubleQuoted
			default:
				word.WriteRune(r)
			}
		case escapedDoubleQuoted:
			if r == '"' || r == '\\' {
				word.WriteRune(r)
			} else {
				word.WriteRune('\\')
				word.WriteRune(r)
			}
			state = doubleQuoted
		}
	}

	switch state {
	case singleQuoted, doubleQuoted:
		return nil, fmt.Errorf("compose: unterminated quote in editor command")
	case escapedBare, escapedDoubleQuoted:
		return nil, fmt.Errorf("compose: trailing backslash in editor command")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("compose: empty editor command")
	}
	return words, nil
}

// ResolveEditorCommand selects VISUAL, then EDITOR, then vi, parsing a set
// environment variable even when it is empty or malformed.
func ResolveEditorCommand(lookup func(string) (string, bool)) ([]string, error) {
	if value, ok := lookup("VISUAL"); ok {
		return SplitWords(value)
	}
	if value, ok := lookup("EDITOR"); ok {
		return SplitWords(value)
	}
	return []string{"vi"}, nil
}
