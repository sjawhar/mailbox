package toon_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/toon"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

// adversarial strings injected into every string-typed field of every shape.
var adversarial = []string{
	"", " ", "  leading", "trailing  ", "\t", "a\tb",
	"line1\nline2", "cr\rlf\r\n", "a:b", "x,y", "[4]", "{k}", "]",
	"- item", "-", "#comment", "# x", `"quoted"`, `\backslash`,
	"true", "false", "null", "42", "-3.14", "05", "+1", "1e-6",
	"\x00\x01\x1f", "\u202evisual", "Ａdmin", "ｍailbox", "😀 emoji",
	"  2-space-indent-lookalike\n    deeper", "a\nn: forged-field",
	strings.Repeat("x", 4096),
}

// allPayloadShapes is the single iteration list for the property and fuzz
// suites: the toontest mirrors of every CLI surface. T10 appends the exported
// internal/send payload fixtures here.
func allPayloadShapes(s1, s2, s3 string) []any {
	return toontest.Shapes(s1, s2, s3)
}

func TestEncodeOracleRoundTripAllShapes(t *testing.T) {
	for shapeIndex := range allPayloadShapes("", "", "") {
		for _, a := range adversarial {
			for _, b := range adversarial[:8] {
				v := allPayloadShapes(a, b, "constant")[shapeIndex]
				jsonBytes, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				doc, err := toon.Encode(v)
				if err != nil {
					t.Fatalf("encode: %v (input %q/%q)", err, a, b)
				}
				decoded, err := toontest.Decode(doc)
				if err != nil {
					t.Fatalf("oracle rejected encoder output: %v\ninput %q/%q\ndoc:\n%s", err, a, b, doc)
				}
				if err := toontest.EqualJSON(decoded, jsonBytes); err != nil {
					t.Fatalf("semantic divergence: %v\ninput %q/%q\ndoc:\n%s", err, a, b, doc)
				}
			}
		}
	}
}
