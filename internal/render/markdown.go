package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// Body is the terminal-friendly content and reference links for one message.
type Body struct {
	Markdown string
	Links    []Link
}

// RenderBody renders a decoded message body, preserving URLs in text-only
// messages and extracting references from HTML-derived Markdown.
func RenderBody(content *MessageContent, opts Options, firstLinkN int) (Body, error) {
	if content.HTML == "" {
		markdown := content.Text
		if !opts.KeepQuotes {
			markdown = StripQuoteTails(markdown)
		}
		return Body{Markdown: TidyWhitespace(markdown)}, nil
	}

	clean, err := CleanHTML(content.HTML, opts)
	if err != nil {
		return Body{}, err
	}
	markdown, err := htmltomarkdown.ConvertString(clean.HTML)
	if err != nil {
		return Body{}, err
	}
	if !opts.KeepQuotes {
		markdown = StripQuoteTails(markdown)
	}
	markdown, links := ExtractFootnotes(markdown, firstLinkN)
	markdown = TidyWhitespace(markdown)
	if len(links) == 0 {
		return Body{Markdown: markdown}, nil
	}

	var table strings.Builder
	table.Grow(len(markdown) + len(links)*32)
	table.WriteString(markdown)
	table.WriteByte('\n')
	for _, link := range links {
		fmt.Fprintf(&table, "[%d]: %s\n", link.N, link.URL)
	}
	return Body{Markdown: table.String(), Links: links}, nil
}

// StripQuoteTails removes the Markdown quote tail and its preceding attribution.
func StripQuoteTails(markdown string) string {
	lines := strings.Split(markdown, "\n")
	for range 2 {
		end := len(lines)
		for end > 0 {
			line := lines[end-1]
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, ">") {
				end--
				continue
			}
			break
		}
		lines = lines[:end]
		if end > 0 && isQuoteAttribution(lines[end-1]) {
			lines = lines[:end-1]
		}
	}
	return strings.Join(lines, "\n")
}

func isQuoteAttribution(line string) bool {
	if strings.HasSuffix(line, " ") {
		line = strings.TrimSuffix(line, " ")
	}
	if !strings.HasPrefix(line, "On ") || !strings.HasSuffix(line, " wrote:") {
		return false
	}

	middle := line[len("On ") : len(line)-len(" wrote:")]
	length := utf8.RuneCountInString(middle)
	return length >= 4 && length <= 200
}

// ExtractFootnotes turns converter-produced Markdown links into numbered
// references while preserving autolinks and escaped bracket sequences.
func ExtractFootnotes(markdown string, firstN int) (string, []Link) {
	input := []rune(markdown)
	var output strings.Builder
	output.Grow(len(markdown))

	byURL := make(map[string]int)
	var links []Link
	for index := 0; index < len(input); {
		if input[index] != '[' || isEscaped(input, index) {
			output.WriteRune(input[index])
			index++
			continue
		}

		textEnd, ok := closingBracket(input, index+1, ']')
		if !ok || textEnd+1 >= len(input) || input[textEnd+1] != '(' {
			output.WriteRune(input[index])
			index++
			continue
		}
		urlEnd, ok := closingParen(input, textEnd+2)
		if !ok {
			output.WriteRune(input[index])
			index++
			continue
		}

		url := string(input[textEnd+2 : urlEnd])
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			output.WriteRune(input[index])
			index++
			continue
		}

		n, found := byURL[url]
		if !found {
			n = firstN + len(links)
			byURL[url] = n
			links = append(links, Link{N: n, Text: string(input[index+1 : textEnd]), URL: url})
		}
		output.WriteString(string(input[index : textEnd+1]))
		fmt.Fprintf(&output, "[%d]", n)
		index = urlEnd + 1
	}
	return output.String(), links
}

func closingBracket(input []rune, start int, closing rune) (int, bool) {
	for index := start; index < len(input); index++ {
		if input[index] == closing && !isEscaped(input, index) {
			return index, true
		}
	}
	return 0, false
}

func closingParen(input []rune, start int) (int, bool) {
	depth := 1
	for index := start; index < len(input); index++ {
		if isEscaped(input, index) {
			continue
		}
		switch input[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index, true
			}
		}
	}
	return 0, false
}

func isEscaped(input []rune, index int) bool {
	backslashes := 0
	for index > 0 && input[index-1] == '\\' {
		backslashes++
		index--
	}
	return backslashes%2 == 1
}

// TidyWhitespace removes invisible runes and output noise from Markdown.
func TidyWhitespace(markdown string) string {
	lines := strings.Split(stripZeroWidth(markdown), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t\r")
	}

	var output []string
	for index := 0; index < len(lines); {
		if lines[index] != "" {
			output = append(output, lines[index])
			index++
			continue
		}

		end := index
		for end < len(lines) && lines[end] == "" {
			end++
		}
		if end == len(lines) {
			break
		}
		blanks := end - index
		if blanks >= 3 {
			blanks = 1
		}
		for range blanks {
			output = append(output, "")
		}
		index = end
	}

	return strings.Join(output, "\n") + "\n"
}
