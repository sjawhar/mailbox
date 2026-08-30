package toon_test

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/sjawhar/mailbox/internal/toon"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

func FuzzEncodeOracleRoundTrip(f *testing.F) {
	for _, s := range adversarial {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			t.Skip()
		}
		v := allPayloadShapes(a, b, "seed")[0] // 0 = listing: the richest shape
		doc, err := toon.Encode(v)
		if err != nil {
			t.Fatalf("encode rejected valid input %q/%q: %v", a, b, err)
		}
		decoded, err := toontest.Decode(doc)
		if err != nil {
			t.Fatalf("oracle rejected: %v\n%s", err, doc)
		}
		jsonBytes, _ := json.Marshal(v)
		if err := toontest.EqualJSON(decoded, jsonBytes); err != nil {
			t.Fatal(err)
		}
	})
}
