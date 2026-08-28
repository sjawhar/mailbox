package render

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update render golden files")

func TestExtractFootnotes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		firstN int
		wantMD string
		want   []Link
	}{
		{
			name:   "single link",
			input:  "[Review PR](https://github.com/o/r/pull/5)",
			firstN: 1,
			wantMD: "[Review PR][1]",
			want:   []Link{{N: 1, Text: "Review PR", URL: "https://github.com/o/r/pull/5"}},
		},
		{
			name:   "two distinct links",
			input:  "[one](https://one.example) and [two](http://two.example)",
			firstN: 1,
			wantMD: "[one][1] and [two][2]",
			want: []Link{
				{N: 1, Text: "one", URL: "https://one.example"},
				{N: 2, Text: "two", URL: "http://two.example"},
			},
		},
		{
			name:   "duplicate URL shares footnote",
			input:  "[first](https://example.com) then [again](https://example.com)",
			firstN: 1,
			wantMD: "[first][1] then [again][1]",
			want:   []Link{{N: 1, Text: "first", URL: "https://example.com"}},
		},
		{
			name:   "starts at requested number",
			input:  "[later](https://example.com/later)",
			firstN: 4,
			wantMD: "[later][4]",
			want:   []Link{{N: 4, Text: "later", URL: "https://example.com/later"}},
		},
		{
			name:   "escaped brackets are not links",
			input:  `\[not a link\](https://example.com)`,
			firstN: 1,
			wantMD: `\[not a link\](https://example.com)`,
		},
		{
			name:   "parentheses after a link remain text",
			input:  "[documentation](https://example.com/docs) (updated today)",
			firstN: 1,
			wantMD: "[documentation][1] (updated today)",
			want:   []Link{{N: 1, Text: "documentation", URL: "https://example.com/docs"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMD, got := ExtractFootnotes(tt.input, tt.firstN)
			if gotMD != tt.wantMD {
				t.Fatalf("ExtractFootnotes() markdown = %q, want %q", gotMD, tt.wantMD)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExtractFootnotes() links = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestStripQuoteTails(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "trailing blockquote removed",
			input: "Reply\n\n> quoted history\n",
			want:  "Reply",
		},
		{
			name:  "attribution and quote tail removed",
			input: "Reply\n\nOn Tue, Aug 25, 2026 at 9:14 AM Bob <bob@x.com> wrote: \n> Earlier message\n",
			want:  "Reply",
		},
		{
			name:  "mid text blockquote remains",
			input: "Intro\n> relevant quoted text\nReply\n",
			want:  "Intro\n> relevant quoted text\nReply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripQuoteTails(tt.input); got != tt.want {
				t.Fatalf("StripQuoteTails() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTidyWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "collapses four blank lines",
			input: "first\n\n\n\n\nsecond",
			want:  "first\n\nsecond\n",
		},
		{
			name:  "strips zero width and trailing spaces",
			input: "hel\u200blo  \nworld\u2060\t\n",
			want:  "hello\nworld\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TidyWhitespace(tt.input); got != tt.want {
				t.Fatalf("TidyWhitespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderBody_TextOnlyLeavesBareURLsInline(t *testing.T) {
	body, err := RenderBody(&MessageContent{Text: "See https://example.com\n\n> old message\n"}, Options{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if body.Markdown != "See https://example.com\n" {
		t.Fatalf("RenderBody() markdown = %q, want bare URL inline and quote tail removed", body.Markdown)
	}
	if len(body.Links) != 0 {
		t.Fatalf("RenderBody() links = %#v, want no footnotes for text-only content", body.Links)
	}
}

func TestRenderBody_TextOnlyKeepsQuoteTailWhenRequested(t *testing.T) {
	body, err := RenderBody(&MessageContent{Text: "Reply\n> prior message\n"}, Options{KeepQuotes: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if body.Markdown != "Reply\n> prior message\n" {
		t.Fatalf("RenderBody() markdown = %q, want quote tail retained with KeepQuotes", body.Markdown)
	}
}

func TestRenderBody_Golden(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		golden  string
	}{
		{name: "alternative human", fixture: "alternative_human.json", golden: "alternative_human.md"},
		{name: "GitHub notification", fixture: "github_notification.json", golden: "github_notification.md"},
		{name: "marketing table", fixture: "marketing_table.json", golden: "marketing_table.md"},
		{name: "nested quotes", fixture: "nested_quotes.json", golden: "nested_quotes.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := ExtractContent(loadFixture(t, tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			body, err := RenderBody(content, Options{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			assertGoldenProperties(t, tt.golden, body.Markdown)

			goldenPath := filepath.Join("testdata", "golden", tt.golden)
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(body.Markdown), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if body.Markdown != string(want) {
				t.Fatalf("RenderBody() markdown for %s =\n%s\nwant:\n%s", tt.fixture, body.Markdown, want)
			}
		})
	}
}

func assertGoldenProperties(t *testing.T, golden, markdown string) {
	t.Helper()

	switch golden {
	case "github_notification.md":
		for _, want := range []string{"[View PR][1]", "[1]: https://github.com/o/r/pull/5"} {
			if !strings.Contains(markdown, want) {
				t.Fatalf("GitHub markdown = %q, want %q", markdown, want)
			}
		}
		if strings.Contains(markdown, "display:none") {
			t.Fatalf("GitHub markdown = %q, must not retain hidden content", markdown)
		}
	case "alternative_human.md":
		if strings.Contains(markdown, "On Tue,") {
			t.Fatalf("alternative markdown = %q, must not contain quote attribution", markdown)
		}
		for _, line := range strings.Split(markdown, "\n") {
			if strings.HasPrefix(line, ">") {
				t.Fatalf("alternative markdown = %q, must not contain quote lines", markdown)
			}
		}
	case "marketing_table.md":
		for _, want := range []string{"[image: Sale banner]", "“Big Sale”"} {
			if !strings.Contains(markdown, want) {
				t.Fatalf("marketing markdown = %q, want %q", markdown, want)
			}
		}
		if strings.Contains(markdown, "|") || strings.Contains(markdown, "<table") {
			t.Fatalf("marketing markdown = %q, must linearize layout tables without table markup", markdown)
		}
	}
}
