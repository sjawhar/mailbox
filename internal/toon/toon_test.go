package toon

import (
	"strings"
	"testing"
)

func TestEncodeMailboxListingShape(t *testing.T) {
	got, err := Encode(struct {
		Account string `json:"account"`
		Threads []struct {
			N       int      `json:"n"`
			ID      string   `json:"id"`
			Subject string   `json:"subject"`
			Unread  bool     `json:"unread"`
			Labels  []string `json:"labels"`
		} `json:"threads"`
	}{Account: "work", Threads: []struct {
		N       int      `json:"n"`
		ID      string   `json:"id"`
		Subject string   `json:"subject"`
		Unread  bool     `json:"unread"`
		Labels  []string `json:"labels"`
	}{{1, "t1", "Hello", true, []string{"INBOX", "UNREAD"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "account: work\n" +
		"threads[1]:\n" +
		"  - n: 1\n" +
		"    id: t1\n" +
		"    subject: Hello\n" +
		"    unread: true\n" +
		"    labels[2]: INBOX,UNREAD"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEncodeMailboxShapes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "error envelope",
			in: struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}{Error: struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: "denied", Message: "no access"}},
			want: "error:\n  code: denied\n  message: no access",
		},
		{
			name: "tabular array",
			in: struct {
				Items []struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				} `json:"items"`
			}{Items: []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			}{{1, "Ada"}, {2, "Bob"}}},
			want: "items[2]{id,name}:\n  1,Ada\n  2,Bob",
		},
		{
			name: "keyed tabular object",
			in: map[string]map[string]struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			}{"servers": {"alpha": {"a", 1}, "beta": {"b", 2}}},
			want: "servers[2:]{host,port}:\n  alpha: a,1\n  beta: b,2",
		},
		{
			name: "hostile strings",
			in: struct {
				Hash    string `json:"hash"`
				Hyphen  string `json:"hyphen"`
				Colon   string `json:"colon"`
				Comma   string `json:"comma"`
				Control string `json:"control"`
				Numeric string `json:"numeric"`
			}{"#comment", "- item", "a:b", "x,y", "line1\r\nline2", "42"},
			want: "hash: \"#comment\"\nhyphen: \"- item\"\ncolon: \"a:b\"\ncomma: \"x,y\"\ncontrol: \"line1\\r\\nline2\"\nnumeric: \"42\"",
		},
		{
			name: "empty arrays",
			in: struct {
				Items  []string `json:"items"`
				Nested struct {
					Values []int `json:"values"`
				} `json:"nested"`
			}{
				Items: []string{},
				Nested: struct {
					Values []int `json:"values"`
				}{Values: []int{}},
			},
			want: "items: []\nnested:\n  values: []",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Encode(test.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestEncodeJSONRejectsTrailingContent(t *testing.T) {
	_, err := EncodeJSON([]byte(`{"ok":true} {"extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "trailing content") {
		t.Fatalf("EncodeJSON trailing input error = %v", err)
	}
}
