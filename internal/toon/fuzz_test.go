package toon_test

import (
	"fmt"
	"testing"
	"unicode/utf8"
)

func FuzzEncodeOracleRoundTrip(f *testing.F) {
	for _, s := range adversarial {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) {
			t.Skip()
		}
		for shapeIndex, payload := range allPayloadShapes() {
			for fieldIndex := range payloadStringFields(addressablePayload(payload), "$") {
				v, path := mutatePayloadString(payload, fieldIndex, input)
				assertRoundTrip(t, v, fmt.Sprintf("payload %d %s = %q", shapeIndex, path, input))
			}
		}
	})
}
