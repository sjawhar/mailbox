// Package toontest provides a strict test-only decoder for TOON emitted by the
// mailbox encoder. Production code deliberately never decodes TOON.
package toontest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind identifies a TOON JSON-model value.
type Kind uint8

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is the ordered JSON-model tree produced by Decode.
type Value struct {
	Kind Kind
	Str  string
	Bool bool
	Num  string
	Arr  []Value
	Obj  []Field

	relaxedOrder bool
}

// Field is one ordered object member.
type Field struct {
	Key string
	Val Value
}

type sourceLine struct {
	depth   int
	content string
}

type parser struct {
	lines []sourceLine
	pos   int
}

type headerField struct {
	key      string
	children []headerField
}

type header struct {
	hasKey bool
	key    string
	count  int
	keyed  bool
	fields []headerField
	after  string
}

var jsonNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?$`)

// Decode parses the subset of TOON v4.1 that the mailbox encoder emits,
// strictly (counts, widths, indentation, quoting). Built against the
// recorded revision in internal/toon/testdata/REVISION.
func Decode(document string) (Value, error) {
	if strings.ContainsRune(document, '\r') {
		return Value{}, fmt.Errorf("TOON oracle: CR byte is forbidden")
	}
	lines, err := sourceLines(document)
	if err != nil {
		return Value{}, err
	}
	p := parser{lines: lines}
	p.skipBlank()
	if p.pos == len(p.lines) {
		return Value{Kind: Object, Obj: []Field{}}, nil
	}
	first := p.lines[p.pos]
	if first.depth != 0 {
		return Value{}, fmt.Errorf("TOON oracle: root starts at depth %d", first.depth)
	}
	if first.content == "[]" {
		p.pos++
		if p.hasContent() {
			return Value{}, fmt.Errorf("TOON oracle: trailing content after root array")
		}
		return Value{Kind: Array, Arr: []Value{}}, nil
	}
	if h, ok, err := parseHeader(first.content); err != nil {
		return Value{}, err
	} else if ok && !h.hasKey {
		p.pos++
		v, err := p.decodeHeader(h, 0, 1)
		if err != nil {
			return Value{}, err
		}
		if p.hasContent() {
			return Value{}, fmt.Errorf("TOON oracle: trailing content after root header")
		}
		return v, nil
	}
	if unquotedIndex(first.content, ':') < 0 {
		p.pos++
		if p.hasContent() {
			return Value{}, fmt.Errorf("TOON oracle: multiple root scalar lines")
		}
		return parseValueToken(first.content)
	}
	return p.decodeObject(0)
}

func sourceLines(document string) ([]sourceLine, error) {
	raw := strings.Split(document, "\n")
	lines := make([]sourceLine, 0, len(raw))
	for _, line := range raw {
		spaces := 0
		for spaces < len(line) && line[spaces] == ' ' {
			spaces++
		}
		if spaces < len(line) && line[spaces] == '#' {
			continue
		}
		content := strings.TrimRight(line[spaces:], " ")
		if content == "" {
			lines = append(lines, sourceLine{content: ""})
			continue
		}
		if strings.HasPrefix(content, "\t") {
			return nil, fmt.Errorf("TOON oracle: tab indentation")
		}
		if spaces%2 != 0 {
			return nil, fmt.Errorf("TOON oracle: indentation %d is not a multiple of 2", spaces)
		}
		lines = append(lines, sourceLine{depth: spaces / 2, content: content})
	}
	return lines, nil
}

func (p *parser) skipBlank() {
	for p.pos < len(p.lines) && p.lines[p.pos].content == "" {
		p.pos++
	}
}

func (p *parser) hasContent() bool {
	p.skipBlank()
	return p.pos < len(p.lines)
}

func (p *parser) decodeObject(depth int) (Value, error) {
	fields, err := p.decodeObjectFields(depth)
	if err != nil {
		return Value{}, err
	}
	return Value{Kind: Object, Obj: fields}, nil
}

func (p *parser) decodeObjectFields(depth int) ([]Field, error) {
	fields := make([]Field, 0)
	for {
		p.skipBlank()
		if p.pos == len(p.lines) || p.lines[p.pos].depth < depth {
			return fields, nil
		}
		if p.lines[p.pos].depth != depth {
			return nil, fmt.Errorf("TOON oracle: unexpected indentation at %q", p.lines[p.pos].content)
		}
		if p.lines[p.pos].content == "-" || strings.HasPrefix(p.lines[p.pos].content, "- ") {
			return nil, fmt.Errorf("TOON oracle: list item outside list")
		}
		f, err := p.decodeField(p.lines[p.pos].content, depth, depth+1)
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
}

func (p *parser) decodeField(content string, logicalDepth, childDepth int) (Field, error) {
	if h, ok, err := parseHeader(content); err != nil {
		return Field{}, err
	} else if ok {
		if !h.hasKey {
			return Field{}, fmt.Errorf("TOON oracle: keyless header in object")
		}
		p.pos++
		v, err := p.decodeHeader(h, logicalDepth, childDepth)
		return Field{Key: h.key, Val: v}, err
	}
	colon := unquotedIndex(content, ':')
	if colon < 0 {
		return Field{}, fmt.Errorf("TOON oracle: missing field colon in %q", content)
	}
	key, err := parseKey(content[:colon])
	if err != nil {
		return Field{}, err
	}
	after := trimSpaces(content[colon+1:])
	p.pos++
	if after == "[]" {
		return Field{Key: key, Val: Value{Kind: Array, Arr: []Value{}}}, nil
	}
	if after != "" {
		v, err := parseValueToken(after)
		return Field{Key: key, Val: v}, err
	}
	p.skipBlank()
	if p.pos == len(p.lines) || p.lines[p.pos].depth <= logicalDepth {
		return Field{Key: key, Val: Value{Kind: Object, Obj: []Field{}}}, nil
	}
	if p.lines[p.pos].depth != childDepth {
		return Field{}, fmt.Errorf("TOON oracle: nested object depth jump")
	}
	v, err := p.decodeObject(childDepth)
	return Field{Key: key, Val: v}, err
}

func (p *parser) decodeHeader(h header, headerDepth, contentDepth int) (Value, error) {
	if len(h.fields) > 0 && h.after != "" {
		return Value{}, fmt.Errorf("TOON oracle: content after fields-bearing header")
	}
	if h.keyed {
		if len(h.fields) == 0 {
			return Value{}, fmt.Errorf("TOON oracle: keyed header without fields")
		}
		return p.decodeKeyed(h, contentDepth)
	}
	if len(h.fields) > 0 {
		return p.decodeTable(h, contentDepth)
	}
	if h.after != "" {
		cells, err := splitTokens(h.after, ',')
		if err != nil {
			return Value{}, err
		}
		if len(cells) != h.count {
			return Value{}, fmt.Errorf("TOON oracle: inline count %d, want %d", len(cells), h.count)
		}
		values := make([]Value, len(cells))
		for i, cell := range cells {
			values[i], err = parseValueToken(cell)
			if err != nil {
				return Value{}, err
			}
		}
		return Value{Kind: Array, Arr: values}, nil
	}
	return p.decodeList(h.count, contentDepth)
}

func (p *parser) decodeTable(h header, rowDepth int) (Value, error) {
	values := make([]Value, 0, h.count)
	started := false
	for {
		if p.pos < len(p.lines) && p.lines[p.pos].content == "" {
			if started {
				return Value{}, fmt.Errorf("TOON oracle: blank line in table span")
			}
			p.skipBlank()
			continue
		}
		if p.pos == len(p.lines) || p.lines[p.pos].depth < rowDepth {
			break
		}
		if p.lines[p.pos].depth != rowDepth {
			return Value{}, fmt.Errorf("TOON oracle: table row depth")
		}
		if unquotedIndex(p.lines[p.pos].content, ':') >= 0 {
			return Value{}, fmt.Errorf("TOON oracle: key-value line in table rows")
		}
		cells, err := splitTokens(p.lines[p.pos].content, ',')
		if err != nil {
			return Value{}, err
		}
		if len(cells) != leafCount(h.fields) {
			return Value{}, fmt.Errorf("TOON oracle: table row width %d, want %d", len(cells), leafCount(h.fields))
		}
		p.pos++
		started = true
		v, err := fieldsValue(h.fields, cells, true)
		if err != nil {
			return Value{}, err
		}
		values = append(values, v)
	}
	if len(values) != h.count {
		return Value{}, fmt.Errorf("TOON oracle: table row count %d, want %d", len(values), h.count)
	}
	return Value{Kind: Array, Arr: values}, nil
}

func (p *parser) decodeKeyed(h header, rowDepth int) (Value, error) {
	fields := make([]Field, 0, h.count)
	started := false
	for {
		if p.pos < len(p.lines) && p.lines[p.pos].content == "" {
			if started {
				return Value{}, fmt.Errorf("TOON oracle: blank line in keyed table span")
			}
			p.skipBlank()
			continue
		}
		if p.pos == len(p.lines) || p.lines[p.pos].depth < rowDepth {
			break
		}
		if p.lines[p.pos].depth != rowDepth {
			return Value{}, fmt.Errorf("TOON oracle: keyed row depth")
		}
		line := p.lines[p.pos].content
		colon := unquotedIndex(line, ':')
		if colon < 0 {
			return Value{}, fmt.Errorf("TOON oracle: keyed entry lacks colon")
		}
		key, err := parseKey(line[:colon])
		if err != nil {
			return Value{}, err
		}
		cells, err := splitTokens(trimSpaces(line[colon+1:]), ',')
		if err != nil {
			return Value{}, err
		}
		if len(cells) != leafCount(h.fields) {
			return Value{}, fmt.Errorf("TOON oracle: keyed row width %d, want %d", len(cells), leafCount(h.fields))
		}
		p.pos++
		started = true
		v, err := fieldsValue(h.fields, cells, true)
		if err != nil {
			return Value{}, err
		}
		fields = append(fields, Field{Key: key, Val: v})
	}
	if len(fields) != h.count {
		return Value{}, fmt.Errorf("TOON oracle: keyed row count %d, want %d", len(fields), h.count)
	}
	return Value{Kind: Object, Obj: fields}, nil
}

func (p *parser) decodeList(count, itemDepth int) (Value, error) {
	items := make([]Value, 0, count)
	started := false
	for {
		if p.pos < len(p.lines) && p.lines[p.pos].content == "" {
			if started {
				return Value{}, fmt.Errorf("TOON oracle: blank line in list span")
			}
			p.skipBlank()
			continue
		}
		if p.pos == len(p.lines) || p.lines[p.pos].depth < itemDepth {
			break
		}
		if p.lines[p.pos].depth != itemDepth {
			return Value{}, fmt.Errorf("TOON oracle: list item depth")
		}
		line := p.lines[p.pos].content
		if line == "-" {
			p.pos++
			items = append(items, Value{Kind: Object, Obj: []Field{}})
			started = true
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			return Value{}, fmt.Errorf("TOON oracle: list item missing hyphen")
		}
		p.pos++
		v, err := p.decodeListRemainder(line[2:], itemDepth)
		if err != nil {
			return Value{}, err
		}
		items = append(items, v)
		started = true
	}
	if len(items) != count {
		return Value{}, fmt.Errorf("TOON oracle: list item count %d, want %d", len(items), count)
	}
	return Value{Kind: Array, Arr: items}, nil
}

func (p *parser) decodeListRemainder(content string, itemDepth int) (Value, error) {
	if content == "[]" {
		return Value{Kind: Array, Arr: []Value{}}, nil
	}
	if h, ok, err := parseHeader(content); err != nil {
		return Value{}, err
	} else if ok {
		if !h.hasKey {
			if len(h.fields) > 0 || h.keyed {
				return Value{}, fmt.Errorf("TOON oracle: keyless table list item")
			}
			return p.decodeHeader(h, itemDepth, itemDepth+1)
		}
		v, err := p.decodeHeader(h, itemDepth+1, itemDepth+2)
		if err != nil {
			return Value{}, err
		}
		rest, err := p.decodeObjectFields(itemDepth + 1)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: Object, Obj: append([]Field{{Key: h.key, Val: v}}, rest...)}, nil
	}
	if unquotedIndex(content, ':') < 0 {
		return parseValueToken(content)
	}
	f, err := p.decodeInlineObjectField(content, itemDepth)
	if err != nil {
		return Value{}, err
	}
	rest, err := p.decodeObjectFields(itemDepth + 1)
	if err != nil {
		return Value{}, err
	}
	return Value{Kind: Object, Obj: append([]Field{f}, rest...)}, nil
}

func (p *parser) decodeInlineObjectField(content string, itemDepth int) (Field, error) {
	colon := unquotedIndex(content, ':')
	if colon < 0 {
		return Field{}, fmt.Errorf("TOON oracle: missing list object field colon")
	}
	key, err := parseKey(content[:colon])
	if err != nil {
		return Field{}, err
	}
	after := trimSpaces(content[colon+1:])
	if after == "[]" {
		return Field{Key: key, Val: Value{Kind: Array, Arr: []Value{}}}, nil
	}
	if after != "" {
		v, err := parseValueToken(after)
		return Field{Key: key, Val: v}, err
	}
	p.skipBlank()
	if p.pos == len(p.lines) || p.lines[p.pos].depth <= itemDepth {
		return Field{Key: key, Val: Value{Kind: Object, Obj: []Field{}}}, nil
	}
	if p.lines[p.pos].depth != itemDepth+2 {
		return Field{}, fmt.Errorf("TOON oracle: list object nested depth")
	}
	v, err := p.decodeObject(itemDepth + 2)
	return Field{Key: key, Val: v}, err
}

func parseHeader(content string) (header, bool, error) {
	bracket := unquotedIndex(content, '[')
	if bracket < 0 {
		return header{}, false, nil
	}
	colon := unquotedIndex(content, ':')
	if colon >= 0 && colon < bracket {
		return header{}, false, nil
	}
	var h header
	if bracket > 0 {
		keyToken := content[:bracket]
		if strings.ContainsAny(keyToken, " \t") {
			return header{}, false, fmt.Errorf("TOON oracle: whitespace before header bracket")
		}
		key, err := parseKey(keyToken)
		if err != nil {
			return header{}, false, err
		}
		h.hasKey, h.key = true, key
	}
	close := strings.IndexByte(content[bracket:], ']')
	if close < 0 {
		return header{}, false, fmt.Errorf("TOON oracle: unclosed header bracket")
	}
	close += bracket
	inside := content[bracket+1 : close]
	if inside == "" {
		return header{}, false, fmt.Errorf("TOON oracle: empty header length")
	}
	if strings.HasSuffix(inside, ":") {
		h.keyed = true
		inside = strings.TrimSuffix(inside, ":")
	}
	if inside == "" || (len(inside) > 1 && inside[0] == '0') {
		return header{}, false, fmt.Errorf("TOON oracle: invalid header length")
	}
	count, err := strconv.Atoi(inside)
	if err != nil || count < 0 {
		return header{}, false, fmt.Errorf("TOON oracle: invalid header length %q", inside)
	}
	h.count = count
	rest := content[close+1:]
	if strings.HasPrefix(rest, "{") {
		end, err := closingBrace(rest)
		if err != nil {
			return header{}, false, err
		}
		fields, err := parseFields(rest[1:end])
		if err != nil {
			return header{}, false, err
		}
		h.fields = fields
		rest = rest[end+1:]
	}
	if !strings.HasPrefix(rest, ":") {
		return header{}, false, fmt.Errorf("TOON oracle: malformed header %q", content)
	}
	h.after = trimSpaces(rest[1:])
	if h.keyed && len(h.fields) == 0 {
		return header{}, false, fmt.Errorf("TOON oracle: keyed header lacks fields")
	}
	return h, true, nil
}

func closingBrace(s string) (int, error) {
	depth := 0
	quoted := false
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if quoted && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
			if depth < 0 {
				return 0, fmt.Errorf("TOON oracle: unmatched brace")
			}
		}
	}
	return 0, fmt.Errorf("TOON oracle: unmatched brace")
}

func parseFields(content string) ([]headerField, error) {
	if content == "" {
		return nil, fmt.Errorf("TOON oracle: empty field list")
	}
	parts, err := splitTopLevel(content, ',')
	if err != nil {
		return nil, err
	}
	fields := make([]headerField, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("TOON oracle: empty field")
		}
		brace := unquotedIndex(part, '{')
		var field headerField
		if brace < 0 {
			key, err := parseKey(part)
			if err != nil {
				return nil, err
			}
			field.key = key
		} else {
			if !strings.HasSuffix(part, "}") {
				return nil, fmt.Errorf("TOON oracle: malformed nested field group")
			}
			key, err := parseKey(part[:brace])
			if err != nil {
				return nil, err
			}
			children, err := parseFields(part[brace+1 : len(part)-1])
			if err != nil {
				return nil, err
			}
			field = headerField{key: key, children: children}
		}
		if _, duplicate := seen[field.key]; duplicate {
			return nil, fmt.Errorf("TOON oracle: duplicate header field %q", field.key)
		}
		seen[field.key] = struct{}{}
		fields = append(fields, field)
	}
	return fields, nil
}

func splitTopLevel(s string, delimiter byte) ([]string, error) {
	parts := make([]string, 0)
	start, braces := 0, 0
	quoted, escaped := false, false
	for i := range s {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch c {
		case '{':
			braces++
		case '}':
			braces--
			if braces < 0 {
				return nil, fmt.Errorf("TOON oracle: unmatched brace")
			}
		case delimiter:
			if braces == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if quoted || braces != 0 {
		return nil, fmt.Errorf("TOON oracle: malformed quoted or grouped field list")
	}
	return append(parts, s[start:]), nil
}

func splitTokens(s string, delimiter byte) ([]string, error) {
	parts := make([]string, 0)
	start := 0
	quoted, escaped := false, false
	for i := range s {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && c == delimiter {
			parts = append(parts, trimSpaces(s[start:i]))
			start = i + 1
		}
	}
	if quoted {
		return nil, fmt.Errorf("TOON oracle: unterminated quoted token")
	}
	return append(parts, trimSpaces(s[start:])), nil
}

func unquotedIndex(s string, target byte) int {
	quoted, escaped := false, false
	for i := range s {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && c == target {
			return i
		}
	}
	return -1
}

func trimSpaces(s string) string { return strings.Trim(s, " ") }

func parseKey(token string) (string, error) {
	token = trimSpaces(token)
	if token == "" {
		return "", fmt.Errorf("TOON oracle: empty unquoted key")
	}
	if strings.HasPrefix(token, "\"") {
		return parseQuoted(token)
	}
	return token, nil
}

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

func leafCount(fields []headerField) int {
	n := 0
	for _, field := range fields {
		if len(field.children) == 0 {
			n++
		} else {
			n += leafCount(field.children)
		}
	}
	return n
}

func fieldsValue(fields []headerField, cells []string, relaxed bool) (Value, error) {
	index := 0
	var build func([]headerField) (Value, error)
	build = func(fields []headerField) (Value, error) {
		object := Value{Kind: Object, Obj: make([]Field, 0, len(fields)), relaxedOrder: relaxed}
		for _, field := range fields {
			var value Value
			var err error
			if len(field.children) == 0 {
				value, err = parseValueToken(cells[index])
				index++
			} else {
				value, err = build(field.children)
			}
			if err != nil {
				return Value{}, err
			}
			object.Obj = append(object.Obj, Field{Key: field.key, Val: value})
		}
		return object, nil
	}
	return build(fields)
}

// EqualJSON reports semantic equality between v and one JSON document,
// per TOON §2 JSON-model equality (ordered objects, numeric value equality).
func EqualJSON(v Value, jsonDoc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(jsonDoc))
	dec.UseNumber()
	want, err := decodeJSONValue(dec)
	if err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON oracle: trailing content")
		}
		return err
	}
	return equalAt(v, want, "$")
}

func decodeJSONValue(dec *json.Decoder) (Value, error) {
	token, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch token := token.(type) {
	case nil:
		return Value{Kind: Null}, nil
	case bool:
		return Value{Kind: Bool, Bool: token}, nil
	case string:
		return Value{Kind: String, Str: token}, nil
	case json.Number:
		return Value{Kind: Number, Num: token.String()}, nil
	case json.Delim:
		switch token {
		case '[':
			values := make([]Value, 0)
			for dec.More() {
				value, err := decodeJSONValue(dec)
				if err != nil {
					return Value{}, err
				}
				values = append(values, value)
			}
			_, err := dec.Token()
			return Value{Kind: Array, Arr: values}, err
		case '{':
			fields := make([]Field, 0)
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				value, err := decodeJSONValue(dec)
				if err != nil {
					return Value{}, err
				}
				fields = append(fields, Field{Key: key.(string), Val: value})
			}
			_, err := dec.Token()
			return Value{Kind: Object, Obj: fields}, err
		}
	}
	return Value{}, fmt.Errorf("JSON oracle: unsupported token")
}

func equalAt(got, want Value, path string) error {
	if got.Kind != want.Kind {
		return fmt.Errorf("%s: kind %v, want %v", path, got.Kind, want.Kind)
	}
	switch got.Kind {
	case Null:
		return nil
	case Bool:
		if got.Bool != want.Bool {
			return fmt.Errorf("%s: bool %v, want %v", path, got.Bool, want.Bool)
		}
	case String:
		if got.Str != want.Str {
			return fmt.Errorf("%s: string %q, want %q", path, got.Str, want.Str)
		}
	case Number:
		if !numbersEqual(got.Num, want.Num) {
			return fmt.Errorf("%s: number %s, want %s", path, got.Num, want.Num)
		}
	case Array:
		if len(got.Arr) != len(want.Arr) {
			return fmt.Errorf("%s: array length %d, want %d", path, len(got.Arr), len(want.Arr))
		}
		for i := range got.Arr {
			if err := equalAt(got.Arr[i], want.Arr[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case Object:
		if len(got.Obj) != len(want.Obj) {
			return fmt.Errorf("%s: object field count %d, want %d", path, len(got.Obj), len(want.Obj))
		}
		if got.relaxedOrder {
			for _, field := range got.Obj {
				matched := -1
				for i, candidate := range want.Obj {
					if candidate.Key == field.Key {
						matched = i
						break
					}
				}
				if matched < 0 {
					return fmt.Errorf("%s: missing field %q", path, field.Key)
				}
				if err := equalAt(field.Val, want.Obj[matched].Val, path+"."+field.Key); err != nil {
					return err
				}
			}
			return nil
		}
		for i := range got.Obj {
			if got.Obj[i].Key != want.Obj[i].Key {
				return fmt.Errorf("%s: field %d is %q, want %q", path, i, got.Obj[i].Key, want.Obj[i].Key)
			}
			if err := equalAt(got.Obj[i].Val, want.Obj[i].Val, path+"."+got.Obj[i].Key); err != nil {
				return err
			}
		}
	}
	return nil
}

func numbersEqual(left, right string) bool {
	l, lok := numberRat(left)
	r, rok := numberRat(right)
	return lok && rok && l.Cmp(r) == 0
}

func numberRat(token string) (*big.Rat, bool) {
	sign := 1
	if strings.HasPrefix(token, "-") {
		sign = -1
		token = token[1:]
	}
	exponent := 0
	if index := strings.IndexAny(token, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(token[index+1:])
		if err != nil {
			return nil, false
		}
		exponent = parsed
		token = token[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(token, '.'); index >= 0 {
		fractionDigits = len(token) - index - 1
		token = token[:index] + token[index+1:]
	}
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(token, 10); !ok {
		return nil, false
	}
	if sign < 0 {
		coefficient.Neg(coefficient)
	}
	scale := fractionDigits - exponent
	if scale < 0 {
		coefficient.Mul(coefficient, pow10(-scale))
		return new(big.Rat).SetInt(coefficient), true
	}
	return new(big.Rat).SetFrac(coefficient, pow10(scale)), true
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
