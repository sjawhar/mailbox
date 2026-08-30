// Package toontest provides a strict test-only decoder for TOON emitted by the
// mailbox encoder. Production code deliberately never decodes TOON.
package toontest

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
