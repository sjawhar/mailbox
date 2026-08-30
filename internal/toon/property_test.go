package toon_test

import (
	"encoding/json"
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
	"\x00\x01\x1f", "\u202evisual", "Ａdmin", "ｍailbox", "😀 emoji",
	"  2-space-indent-lookalike\n    deeper", "a\nn: forged-field",
	strings.Repeat("x", 4096),
}

// allPayloadShapes is the single iteration list for the property and fuzz
// suites: the toontest mirrors of every CLI surface. T10 appends the exported
// internal/send payload fixtures here.
func allPayloadShapes(s1, s2, s3 string) []any {
	shapes := toontest.Shapes(s1, s2, s3)
	return append(shapes,
		send.EnvelopePayload{
			Account:   s1,
			Mode:      s2,
			ThreadID:  s3,
			Message:   s1,
			To:        []send.RecipientPayload{{Address: s1, Name: s2, Provenance: s3}},
			Cc:        []send.RecipientPayload{{Address: s2, Name: s3, Provenance: s1}},
			Bcc:       []send.RecipientPayload{{Address: s3, Name: s1, Provenance: s2}},
			Subject:   s1,
			BodyBytes: 7,
			InReplyTo: s2,
			References: []string{
				s2,
				s3,
			},
			Forward:  &send.ForwardPayload{OriginalBytes: 7, Disclosure: s3},
			Sendable: true,
			Sent:     &send.SentPayload{ID: s1, ThreadID: s2},
			Scope:    s3,
			Warning:  s1,
		},
		send.RefusalOf(s1, &send.Refusal{
			Rule:    s2,
			Code:    s3,
			Message: s1,
			ReplyTo: []send.Recipient{{Address: s1, Display: s2, Provenance: send.Provenance(s3)}},
			From:    []send.Recipient{{Address: s3, Display: s1, Provenance: send.Provenance(s2)}},
		}),
		send.RefusalOf(s1, send.NotInThreadRefusal(s2, s3)),
	)
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
