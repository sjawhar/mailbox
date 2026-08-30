package toontest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TOON v4.1 number grammar, including uppercase exponent markers.
var jsonNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func parseValueToken(token string) (Value, error) {
	token = trimSpaces(token)
	if strings.HasPrefix(token, "\"") {
		s, err := parseQuoted(token)
		return Value{Kind: String, Str: s}, err
	}
	switch token {
	case "true":
		return Value{Kind: Bool, Bool: true}, nil
	case "false":
		return Value{Kind: Bool, Bool: false}, nil
	case "null":
		return Value{Kind: Null}, nil
	}
	if jsonNumber.MatchString(token) && !forbiddenLeadingZero(token) {
		return Value{Kind: Number, Num: token}, nil
	}
	return Value{Kind: String, Str: token}, nil
}

func forbiddenLeadingZero(token string) bool {
	s := strings.TrimPrefix(token, "-")
	integer := s
	if cut := strings.IndexAny(integer, ".eE"); cut >= 0 {
		integer = integer[:cut]
	}
	return len(integer) > 1 && integer[0] == '0'
}

func parseQuoted(token string) (string, error) {
	if len(token) < 2 || token[0] != '"' {
		return "", fmt.Errorf("TOON oracle: malformed quoted token")
	}
	var b strings.Builder
	for i := 1; i < len(token); {
		if token[i] == '"' {
			if i != len(token)-1 {
				return "", fmt.Errorf("TOON oracle: text after quoted token")
			}
			return b.String(), nil
		}
		if token[i] != '\\' {
			r, width := utf8.DecodeRuneInString(token[i:])
			if r == utf8.RuneError && width == 1 {
				return "", fmt.Errorf("TOON oracle: invalid UTF-8")
			}
			b.WriteRune(r)
			i += width
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("TOON oracle: dangling escape")
		}
		switch token[i+1] {
		case '\\':
			b.WriteByte('\\')
			i += 2
		case '"':
			b.WriteByte('"')
			i += 2
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'u':
			if i+6 > len(token) {
				return "", fmt.Errorf("TOON oracle: short unicode escape")
			}
			code, err := strconv.ParseUint(token[i+2:i+6], 16, 16)
			if err != nil || code >= 0xd800 && code <= 0xdfff {
				return "", fmt.Errorf("TOON oracle: invalid unicode escape")
			}
			b.WriteRune(rune(code))
			i += 6
		default:
			return "", fmt.Errorf("TOON oracle: invalid escape \\%c", token[i+1])
		}
	}
	return "", fmt.Errorf("TOON oracle: unterminated quoted token")
}
