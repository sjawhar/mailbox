package compose

import (
	"slices"
	"testing"
)

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{`code --wait`, []string{"code", "--wait"}, false},
		{`'/opt/My Editor/bin/edit' --flag`, []string{"/opt/My Editor/bin/edit", "--flag"}, false},
		{`"/opt/My Editor/bin/edit" -n`, []string{"/opt/My Editor/bin/edit", "-n"}, false},
		{`vi;rm -rf ~`, []string{"vi;rm", "-rf", "~"}, false},
		{`vi\ m x`, []string{"vi m", "x"}, false},
		{`ed "unterminated`, nil, true},
		{`ed 'unterminated`, nil, true},
		{`ed trailing\`, nil, true},
		{``, nil, true},
		{`   `, nil, true},
		{`echo "a\"b" '$HOME' c\$d`, []string{"echo", `a"b`, "$HOME", "c$d"}, false},
	}
	for _, c := range cases {
		got, err := SplitWords(c.in)
		if (err != nil) != c.err || (!c.err && !slices.Equal(got, c.want)) {
			t.Fatalf("SplitWords(%q) = %v, %v; want %v, err=%t", c.in, got, err, c.want, c.err)
		}
	}
}

func TestResolveEditorCommand(t *testing.T) {
	env := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) { v, ok := values[name]; return v, ok }
	}
	if argv, err := ResolveEditorCommand(env(map[string]string{"VISUAL": "code --wait", "EDITOR": "vi"})); err != nil || argv[0] != "code" {
		t.Fatalf("VISUAL wins: %v %v", argv, err)
	}
	if argv, err := ResolveEditorCommand(env(map[string]string{"EDITOR": "nano"})); err != nil || argv[0] != "nano" {
		t.Fatalf("EDITOR fallback: %v %v", argv, err)
	}
	if argv, err := ResolveEditorCommand(env(nil)); err != nil || len(argv) != 1 || argv[0] != "vi" {
		t.Fatalf("vi default: %v %v", argv, err)
	}
	if _, err := ResolveEditorCommand(env(map[string]string{"VISUAL": ""})); err == nil {
		t.Fatal("set-but-empty VISUAL is an error, not a fallthrough")
	}
	if _, err := ResolveEditorCommand(env(map[string]string{"VISUAL": `bad "quote`})); err == nil {
		t.Fatal("malformed VISUAL is an error")
	}
}
