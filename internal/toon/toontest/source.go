package toontest

import (
	"fmt"
	"strings"
)

type sourceLine struct {
	depth   int
	content string
}

func sourceLines(document string) ([]sourceLine, error) {
	raw := strings.Split(document, "\n")
	lines := make([]sourceLine, 0, len(raw))
	for _, line := range raw {
		spaces := 0
		for spaces < len(line) && line[spaces] == ' ' {
			spaces++
		}
		if spaces < len(line) && line[spaces] == '#' {
			continue
		}
		content := strings.TrimRight(line[spaces:], " ")
		if content == "" {
			lines = append(lines, sourceLine{content: ""})
			continue
		}
		if strings.HasPrefix(content, "\t") {
			return nil, fmt.Errorf("TOON oracle: tab indentation")
		}
		if spaces%2 != 0 {
			return nil, fmt.Errorf("TOON oracle: indentation %d is not a multiple of 2", spaces)
		}
		lines = append(lines, sourceLine{depth: spaces / 2, content: content})
	}
	return lines, nil
}
