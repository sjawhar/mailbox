package render

import (
	"strings"
	"testing"
)

func TestCleanHTML(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		opts          Options
		wantPresent   []string
		wantAbsent    []string
		quoteStripped bool
	}{
		{
			name:          "strips gmail quote by default",
			source:        `<p>Reply</p><div class="gmail_quote">quoted history</div>`,
			wantPresent:   []string{"Reply"},
			wantAbsent:    []string{"quoted history"},
			quoteStripped: true,
		},
		{
			name:        "keeps gmail quote when requested",
			source:      `<p>Reply</p><div class="gmail_quote_container">quoted history</div>`,
			opts:        Options{KeepQuotes: true},
			wantPresent: []string{"Reply", "quoted history", "gmail_quote_container"},
		},
		{
			name:        "removes tracking pixel",
			source:      `<p>News</p><img src="https://example.com/pixel" width="1" height="1"><img alt="Hero" src="hero.png">`,
			wantPresent: []string{"News", "[image: Hero]"},
			wantAbsent:  []string{"pixel", "hero.png"},
		},
		{
			name:       "removes style tracking pixel",
			source:     `<img src="https://example.com/pixel" style="width: 1px; height: 1px">`,
			wantAbsent: []string{"pixel"},
		},
		{
			name:        "keeps image with border width style",
			source:      `<img alt="Company logo" src="logo.png" style="border-width: 1px">`,
			wantPresent: []string{"[image: Company logo]"},
			wantAbsent:  []string{"logo.png"},
		},
		{
			name:        "removes hidden preheader",
			source:      `<p>Message</p><span style="display:none">preheader</span>`,
			wantPresent: []string{"Message"},
			wantAbsent:  []string{"preheader"},
		},
		{
			name:        "removes opacity and clipped preheaders",
			source:      `<p>Message</p><span style="opacity: 0">faded preheader</span><div style="max-height:0; overflow:hidden">clipped preheader</div>`,
			wantPresent: []string{"Message"},
			wantAbsent:  []string{"faded preheader", "clipped preheader"},
		},
		{
			name:        "replaces alt image and removes altless image",
			source:      `<img alt="Sale banner" src="sale.jpg"><img alt="" src="spacer.jpg">`,
			wantPresent: []string{"[image: Sale banner]"},
			wantAbsent:  []string{"sale.jpg", "spacer.jpg"},
		},
		{
			name:        "strips zero width runes",
			source:      "<p>hel\u200blo\u200c \u200dworld\u2060\ufeff</p>",
			wantPresent: []string{"hello world"},
			wantAbsent:  []string{"\u200b", "\u200c", "\u200d", "\u2060", "\ufeff"},
		},
		{
			name:        "removes comments",
			source:      `<p>Visible</p><!-- private tracking note -->`,
			wantPresent: []string{"Visible"},
			wantAbsent:  []string{"private tracking note", "<!--"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanHTML(tt.source, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.QuoteStripped != tt.quoteStripped {
				t.Fatalf("QuoteStripped = %t, want %t; HTML = %q", got.QuoteStripped, tt.quoteStripped, got.HTML)
			}
			for _, text := range tt.wantPresent {
				if !strings.Contains(got.HTML, text) {
					t.Fatalf("HTML = %q, want %q", got.HTML, text)
				}
			}
			for _, text := range tt.wantAbsent {
				if strings.Contains(got.HTML, text) {
					t.Fatalf("HTML = %q, must not contain %q", got.HTML, text)
				}
			}
		})
	}
}

func TestCleanHTML_Fixtures(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:        "alternative strips quoted history",
			fixture:     "alternative_human.json",
			wantPresent: []string{"Thanks for the update."},
			wantAbsent:  []string{"Earlier message.", "gmail_quote"},
		},
		{
			name:        "github strips notification noise",
			fixture:     "github_notification.json",
			wantPresent: []string{"View PR", `href="https://github.com/o/r/pull/5"`},
			wantAbsent:  []string{"preheader", "tracking.gif"},
		},
		{
			name:        "marketing replaces sale banner",
			fixture:     "marketing_table.json",
			wantPresent: []string{"“Big Sale”", "[image: Sale banner]"},
			wantAbsent:  []string{"banner.jpg", "spacer.jpg"},
		},
		{
			name:        "nested quotes collapse with outer quote",
			fixture:     "nested_quotes.json",
			wantPresent: []string{"Newest reply."},
			wantAbsent:  []string{"First quoted reply.", "Original quoted message.", "gmail_quote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := ExtractContent(loadFixture(t, tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			got, err := CleanHTML(content.HTML, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range tt.wantPresent {
				if !strings.Contains(got.HTML, text) {
					t.Fatalf("HTML = %q, want %q", got.HTML, text)
				}
			}
			for _, text := range tt.wantAbsent {
				if strings.Contains(got.HTML, text) {
					t.Fatalf("HTML = %q, must not contain %q", got.HTML, text)
				}
			}
		})
	}
}
