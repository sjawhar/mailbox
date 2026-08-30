package toon

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	integerLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	numericLike    = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?$`)
	plainKey       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

func encodePrimitive(v value) string {
	switch v.kind {
	case kindNull:
		return "null"
	case kindBool:
		if v.b {
			return "true"
		}
		return "false"
	case kindNumber:
		return encodeNumber(v.num.String())
	case kindString:
		return encodeString(v.str)
	default:
		panic("TOON: non-primitive value")
	}
}

func encodeNumber(literal string) string {
	if integerLiteral.MatchString(literal) {
		if literal == "-0" {
			return "0"
		}
		return literal
	}
	f, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		panic(fmt.Sprintf("TOON: invalid JSON number %q: %v", literal, err))
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strings.ToLower(strconv.FormatFloat(f, 'e', -1, 64))
}

func encodeString(s string) string {
	if !utf8.ValidString(s) {
		panic("TOON: string is not valid UTF-8")
	}
	if !mustQuote(s) {
		return s
	}
	return quote(s)
}

func encodeKey(key string) string {
	if plainKey.MatchString(key) {
		return key
	}
	return quote(key)
}

func mustQuote(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "#") {
		return true
	}
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return true
	}
	if s == "true" || s == "false" || s == "null" || numericLike.MatchString(s) {
		return true
	}
	for _, r := range s {
		switch r {
		case ':', '"', '\\', '[', ']', '{', '}', ',':
			return true
		}
		if r >= 0 && r <= 0x1f {
			return true
		}
	}
	return false
}

func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r >= 0 && r <= 0x1f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
