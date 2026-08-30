// Package toon encodes JSON-shaped values as TOON v4.1 documents.
package toon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// Encode marshals v with encoding/json, then encodes the resulting JSON
// document as TOON (spec v4.1; comma delimiter, 2-space indent, LF-only,
// no trailing newline). Encode-only: mailbox never decodes TOON at runtime.
func Encode(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return EncodeJSON(data)
}

// EncodeJSON encodes one JSON document (order-preserving) as TOON.
func EncodeJSON(data []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("TOON: trailing content after JSON value")
		}
		return "", fmt.Errorf("TOON: trailing content after JSON value: %w", err)
	}
	return encodeDocument(v), nil
}

type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

type value struct {
	kind kind
	str  string      // kindString
	b    bool        // kindBool
	num  json.Number // kindNumber (original literal)
	arr  []value     // kindArray
	obj  []field     // kindObject — encounter order preserved
}

type field struct {
	key string
	val value
}

func decodeValue(dec *json.Decoder) (value, error) {
	token, err := dec.Token()
	if err != nil {
		return value{}, err
	}
	switch token := token.(type) {
	case nil:
		return value{kind: kindNull}, nil
	case bool:
		return value{kind: kindBool, b: token}, nil
	case json.Number:
		return value{kind: kindNumber, num: token}, nil
	case string:
		if !utf8.ValidString(token) {
			return value{}, fmt.Errorf("TOON: string is not valid UTF-8")
		}
		return value{kind: kindString, str: token}, nil
	case json.Delim:
		switch token {
		case '{':
			fields := make([]field, 0)
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return value{}, err
				}
				key, ok := keyToken.(string)
				if !ok || !utf8.ValidString(key) {
					return value{}, fmt.Errorf("TOON: invalid object key")
				}
				child, err := decodeValue(dec)
				if err != nil {
					return value{}, err
				}
				fields = append(fields, field{key: key, val: child})
			}
			if _, err := dec.Token(); err != nil {
				return value{}, err
			}
			return value{kind: kindObject, obj: fields}, nil
		case '[':
			items := make([]value, 0)
			for dec.More() {
				child, err := decodeValue(dec)
				if err != nil {
					return value{}, err
				}
				items = append(items, child)
			}
			if _, err := dec.Token(); err != nil {
				return value{}, err
			}
			return value{kind: kindArray, arr: items}, nil
		}
	}
	return value{}, fmt.Errorf("TOON: unsupported JSON token %v", token)
}
