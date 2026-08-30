package filter

import (
	"strings"
	"testing"
)

func TestCompileValidTable(t *testing.T) {
	f, err := Compile("github", map[string]string{
		"from":    `notifications@github\.com`,
		"subject": `(?i)ci`,
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if f.Name != "github" || len(f.Rules) != 2 {
		t.Fatalf("Compile() = %+v, want name github with 2 rules", f)
	}
}

func TestCompileFailuresNameFilterAndField(t *testing.T) {
	cases := []struct {
		name  string
		rules map[string]string
		want  string // substring of the error
	}{
		{"github", map[string]string{"from": `(unclosed`}, "filters.github.from: invalid regexp"},
		{"github", map[string]string{"sender": `x`}, `filters.github: unknown field "sender" (fields: cc, from, list, subject, to)`},
		{"GitHub", map[string]string{"from": `x`}, "filters.GitHub: invalid filter name"},
		{"-lead", map[string]string{"from": `x`}, "filters.-lead: invalid filter name"},
		{"a_b", map[string]string{"from": `x`}, "filters.a_b: invalid filter name"},
		{"github", map[string]string{}, "filters.github: at least one rule is required"},
	}
	for _, c := range cases {
		_, err := Compile(c.name, c.rules)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("Compile(%q, %v) error = %v, want containing %q", c.name, c.rules, err, c.want)
		}
	}
}
