package send

import (
	"strings"
	"testing"
)

func renderHTML(t *testing.T, markdown string) string {
	t.Helper()
	html, err := RenderHTML(markdown)
	if err != nil {
		t.Fatal(err)
	}
	return html
}

func TestRenderHTMLBasicsGolden(t *testing.T) {
	got := renderHTML(t, "# Hi\n\nsome *body* text\n")
	want := "<h1>Hi</h1>\n<p>some <em>body</em> text</p>\n"
	if got != want {
		t.Fatalf("RenderHTML() = %q, want %q", got, want)
	}
}

func TestRenderHTMLRawHTMLBecomesOmissionMarker(t *testing.T) {
	got := renderHTML(t, "before\n\n<script>alert(1)</script>\n\nafter\n")
	if strings.Contains(got, "<script") || strings.Contains(got, "&lt;script") {
		t.Fatalf("raw HTML must be omitted, not rendered or escaped: %q", got)
	}
	if !strings.Contains(got, "<!-- raw HTML omitted -->") {
		t.Fatalf("want goldmark omission marker in %q", got)
	}
	inline := renderHTML(t, "a <b>bold</b> claim\n")
	if !strings.Contains(inline, "<!-- raw HTML omitted -->") || strings.Contains(inline, "<b>") {
		t.Fatalf("inline raw HTML must be omitted: %q", inline)
	}
}

func TestRenderHTMLSchemeAllowlist(t *testing.T) {
	cases := []struct {
		markdown string
		keep     bool
		fragment string
	}{
		{"[x](https://example.test/a)", true, `href="https://example.test/a"`},
		{"[x](HTTP://example.test/a)", true, `href="HTTP://example.test/a"`},
		{"[x](mailto:a@example.test)", true, `href="mailto:a@example.test"`},
		{"[x](#section)", true, `href="#section"`},
		{"[x](javascript:alert(1))", false, "javascript:"},
		{"[x](data:image/svg+xml,<svg onload=alert(1)>)", false, "data:"},
		{"[x](data:image/png;base64,AAAA)", false, "data:"},
		{"[x](proto-custom://x)", false, "proto-custom:"},
		{"[x](relative/path)", false, "relative/path"},
		{"![alt](data:image/svg+xml,payload)", false, "data:"},
	}
	for _, c := range cases {
		got := renderHTML(t, c.markdown+"\n")
		if c.keep && !strings.Contains(got, c.fragment) {
			t.Fatalf("RenderHTML(%q) = %q, want %q kept", c.markdown, got, c.fragment)
		}
		if !c.keep && strings.Contains(got, c.fragment) {
			t.Fatalf("RenderHTML(%q) = %q, want %q removed", c.markdown, got, c.fragment)
		}
	}
}

func TestRenderHTMLAutolinkAllowlist(t *testing.T) {
	if got := renderHTML(t, "<https://example.test/x>\n"); !strings.Contains(got, `href="https://example.test/x"`) {
		t.Fatalf("https autolink must survive: %q", got)
	}
	got := renderHTML(t, "<javascript:alert(1)>\n")
	if strings.Contains(got, "href") {
		t.Fatalf("disallowed autolink must not produce a link: %q", got)
	}
	if !strings.Contains(got, "javascript:alert(1)") {
		t.Fatalf("disallowed autolink text should remain visible as text: %q", got)
	}
}
