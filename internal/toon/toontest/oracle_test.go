package toontest

import "testing"

func TestDecodeEncoderForms(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want Value
	}{
		{
			name: "nested object",
			doc:  "parent:\n  child: value",
			want: object(field("parent", object(field("child", stringValue("value"))))),
		},
		{
			name: "inline array",
			doc:  "items[2]: a,2",
			want: object(field("items", array(stringValue("a"), numberValue("2")))),
		},
		{
			name: "list form",
			doc:  "items[2]:\n  - a\n  - b",
			want: object(field("items", array(stringValue("a"), stringValue("b")))),
		},
		{
			name: "tabular nested group",
			doc:  "orders[1]{id,customer{name,country}}:\n  1,Ada,DK",
			want: object(field("orders", array(object(field("id", numberValue("1")), field("customer", object(field("name", stringValue("Ada")), field("country", stringValue("DK")))))))),
		},
		{
			name: "keyed tabular",
			doc:  "servers[1:]{host,port}:\n  alpha: a,1",
			want: object(field("servers", object(field("alpha", object(
				field("host", stringValue("a")),
				field("port", numberValue("1")),
			))))),
		},
		{
			name: "object list first field on hyphen",
			doc:  "items[1]:\n  - name: Ada\n    active: true",
			want: object(field("items", array(object(field("name", stringValue("Ada")), field("active", boolValue(true)))))),
		},
		{
			name: "quoted keys values and escapes",
			doc:  "\"a\\tb\": \"quote\\\" slash\\\\ lf\\n cr\\r tab\\t ctrl\\u0001\"",
			want: object(field("a\tb", stringValue("quote\" slash\\ lf\n cr\r tab\t ctrl\x01"))),
		},
		{
			name: "empty root array",
			doc:  "[]",
			want: array(),
		},
		{
			name: "empty object field array",
			doc:  "items: []",
			want: object(field("items", array())),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Decode(test.doc)
			if err != nil {
				t.Fatal(err)
			}
			if !equalValue(got, test.want) {
				t.Fatalf("Decode(%q) = %#v, want %#v", test.doc, got, test.want)
			}
		})
	}
}

func TestDecodeRejectsMalformedEncoderOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		doc  string
	}{
		{"wrong row count", "items[2]{id}:\n  1"},
		{"wrong cell width", "items[1]{id,name}:\n  1"},
		{"three space indent", "item:\n   value: x"},
		{"CR byte", "item: bad\rvalue"},
		{"blank line in header span", "items[2]:\n  - a\n\n  - b"},
		{"text after closing quote", "item: \"value\" forged"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.doc); err == nil {
				t.Fatalf("Decode(%q) succeeded", test.doc)
			}
		})
	}
}

func field(key string, val Value) Field { return Field{Key: key, Val: val} }
func object(fields ...Field) Value      { return Value{Kind: Object, Obj: fields} }
func array(values ...Value) Value       { return Value{Kind: Array, Arr: values} }
func stringValue(s string) Value        { return Value{Kind: String, Str: s} }
func numberValue(s string) Value        { return Value{Kind: Number, Num: s} }
func boolValue(b bool) Value            { return Value{Kind: Bool, Bool: b} }

func equalValue(got, want Value) bool {
	if got.Kind != want.Kind || got.Str != want.Str || got.Bool != want.Bool || got.Num != want.Num || len(got.Arr) != len(want.Arr) || len(got.Obj) != len(want.Obj) {
		return false
	}
	for i := range got.Arr {
		if !equalValue(got.Arr[i], want.Arr[i]) {
			return false
		}
	}
	for i := range got.Obj {
		if got.Obj[i].Key != want.Obj[i].Key || !equalValue(got.Obj[i].Val, want.Obj[i].Val) {
			return false
		}
	}
	return true
}
