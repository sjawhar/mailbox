package toon_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/send"
	"github.com/sjawhar/mailbox/internal/toon"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

// adversarial strings injected into every string-typed field of every shape.
var adversarial = []string{
	"", " ", "  leading", "trailing  ", "\t", "a\tb",
	"line1\nline2", "cr\rlf\r\n", "a:b", "x,y", "[4]", "{k}", "]",
	"- item", "-", "#comment", "# x", `"quoted"`, `\backslash`,
	"true", "false", "null", "42", "-3.14", "05", "+1", "1e-6",
	"1E+03", "3E-02", "5E+00", "-1E+03",
	"\x00\x01\x1f", "\u202evisual", "Ａdmin", "ｍailbox", "😀 emoji",
	"  2-space-indent-lookalike\n    deeper", "a\nn: forged-field",
	strings.Repeat("x", 4096),
}

const payloadSeed = "seed"

// allPayloadShapes is the single iteration list for the property and fuzz
// suites: the toontest mirrors of every CLI surface plus exported send payloads.
func allPayloadShapes() []any {
	shapes := toontest.Shapes(payloadSeed, payloadSeed, payloadSeed)
	return append(shapes,
		send.EnvelopePayload{
			Account:   payloadSeed,
			Mode:      payloadSeed,
			ThreadID:  payloadSeed,
			Message:   payloadSeed,
			To:        []send.RecipientPayload{{Address: payloadSeed, Name: payloadSeed, Provenance: payloadSeed}},
			Cc:        []send.RecipientPayload{{Address: payloadSeed, Name: payloadSeed, Provenance: payloadSeed}},
			Bcc:       []send.RecipientPayload{{Address: payloadSeed, Name: payloadSeed, Provenance: payloadSeed}},
			Subject:   payloadSeed,
			BodyBytes: 7,
			InReplyTo: payloadSeed,
			References: []string{
				payloadSeed,
			},
			Forward:  &send.ForwardPayload{OriginalBytes: 7, Disclosure: payloadSeed},
			Sendable: true,
			Sent:     &send.SentPayload{ID: payloadSeed, ThreadID: payloadSeed},
			Scope:    payloadSeed,
			Warning:  payloadSeed,
			Attachments: []send.AttachmentPayload{{
				Filename: payloadSeed,
				Size:     7,
				MIMEType: payloadSeed,
				SHA256:   payloadSeed,
			}},
			DraftID: payloadSeed,
		},
		send.RefusalOf(payloadSeed, &send.Refusal{
			Rule:    payloadSeed,
			Code:    payloadSeed,
			Message: payloadSeed,
			ReplyTo: []send.Recipient{{Address: payloadSeed, Display: payloadSeed, Provenance: send.Provenance(payloadSeed)}},
			From:    []send.Recipient{{Address: payloadSeed, Display: payloadSeed, Provenance: send.Provenance(payloadSeed)}},
		}),
		send.RefusalOf(payloadSeed, send.NotInThreadRefusal(payloadSeed, payloadSeed)),
	)
}

func TestEncodeOracleRoundTripAllPayloadStringFields(t *testing.T) {
	for shapeIndex, payload := range allPayloadShapes() {
		fields := payloadStringFields(addressablePayload(payload), "$")
		if len(fields) == 0 {
			t.Fatalf("payload %d (%T) has no string fields", shapeIndex, payload)
		}
		for fieldIndex, field := range fields {
			for _, input := range adversarial {
				v, path := mutatePayloadString(payload, fieldIndex, input)
				if path != field.path {
					t.Fatalf("payload %d field %d path changed from %s to %s", shapeIndex, fieldIndex, field.path, path)
				}
				assertRoundTrip(t, v, fmt.Sprintf("payload %d %s = %q", shapeIndex, path, input))
			}
		}
	}
}

// TestPayloadStringFieldMutationCompleteness makes the corpus contract explicit:
// every serializable string location in each payload type must be selected by
// mutatePayloadString. Nil pointers and empty slices therefore fail loudly when
// a new nested string field is added without a populated shape fixture. Multiple
// payload instances of the same type share coverage because they have one shape.
func TestPayloadStringFieldMutationCompleteness(t *testing.T) {
	expected := make(map[reflect.Type]map[string]struct{})
	mutated := make(map[reflect.Type]map[string]struct{})
	for _, payload := range allPayloadShapes() {
		typ := reflect.TypeOf(payload)
		if expected[typ] == nil {
			expected[typ] = make(map[string]struct{})
			for _, path := range stringFieldPaths(typ, "$") {
				expected[typ][path] = struct{}{}
			}
			mutated[typ] = make(map[string]struct{})
		}
		fields := payloadStringFields(addressablePayload(payload), "$")
		for fieldIndex := range fields {
			_, path := mutatePayloadString(payload, fieldIndex, "coverage")
			mutated[typ][path] = struct{}{}
		}
	}
	for typ, paths := range expected {
		for path := range paths {
			if _, ok := mutated[typ][path]; !ok {
				t.Fatalf("payload type %v string field %s is not mutated by the adversarial corpus", typ, path)
			}
		}
	}
}

func assertRoundTrip(t testing.TB, value any, context string) {
	t.Helper()
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("%s: marshal: %v", context, err)
	}
	doc, err := toon.Encode(value)
	if err != nil {
		t.Fatalf("%s: encode: %v", context, err)
	}
	decoded, err := toontest.Decode(doc)
	if err != nil {
		t.Fatalf("%s: oracle rejected encoder output: %v\ndoc:\n%s", context, err, doc)
	}
	if err := toontest.EqualJSON(decoded, jsonBytes); err != nil {
		t.Fatalf("%s: semantic divergence: %v\ndoc:\n%s", context, err, doc)
	}
}

type stringField struct {
	path  string
	value reflect.Value
}

func addressablePayload(payload any) reflect.Value {
	root := reflect.New(reflect.TypeOf(payload)).Elem()
	root.Set(reflect.ValueOf(payload))
	return root
}

func mutatePayloadString(payload any, fieldIndex int, replacement string) (any, string) {
	root := addressablePayload(payload)
	fields := payloadStringFields(root, "$")
	if fieldIndex < 0 || fieldIndex >= len(fields) {
		panic(fmt.Sprintf("string field index %d out of range for %T", fieldIndex, payload))
	}
	target := fields[fieldIndex]
	if !target.value.CanSet() {
		panic(fmt.Sprintf("string field %s is not settable", target.path))
	}
	target.value.SetString(replacement)
	return root.Interface(), target.path
}

func payloadStringFields(root reflect.Value, path string) []stringField {
	var fields []stringField
	var visit func(reflect.Value, string)
	visit = func(value reflect.Value, path string) {
		switch value.Kind() {
		case reflect.String:
			fields = append(fields, stringField{path: path, value: value})
		case reflect.Pointer, reflect.Interface:
			if !value.IsNil() {
				visit(value.Elem(), path)
			}
		case reflect.Struct:
			typ := value.Type()
			for index := range value.NumField() {
				field := typ.Field(index)
				if field.PkgPath != "" || jsonIgnored(field) {
					continue
				}
				visit(value.Field(index), path+"."+field.Name)
			}
		case reflect.Slice, reflect.Array:
			for index := range value.Len() {
				visit(value.Index(index), fmt.Sprintf("%s[%d]", path, index))
			}
		}
	}
	visit(root, path)
	return fields
}

func stringFieldPaths(typ reflect.Type, path string) []string {
	switch typ.Kind() {
	case reflect.String:
		return []string{path}
	case reflect.Pointer:
		return stringFieldPaths(typ.Elem(), path)
	case reflect.Struct:
		var paths []string
		for index := range typ.NumField() {
			field := typ.Field(index)
			if field.PkgPath != "" || jsonIgnored(field) {
				continue
			}
			paths = append(paths, stringFieldPaths(field.Type, path+"."+field.Name)...)
		}
		return paths
	case reflect.Slice:
		return stringFieldPaths(typ.Elem(), path+"[0]")
	case reflect.Array:
		var paths []string
		for index := range typ.Len() {
			paths = append(paths, stringFieldPaths(typ.Elem(), fmt.Sprintf("%s[%d]", path, index))...)
		}
		return paths
	default:
		return nil
	}
}

func jsonIgnored(field reflect.StructField) bool {
	tag := field.Tag.Get("json")
	return strings.Split(tag, ",")[0] == "-"
}
