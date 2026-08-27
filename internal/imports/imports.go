// Package imports pins the full v1 dependency graph in go.mod so parallel
// implementation lanes never edit go.mod/go.sum. The entry task (cmd/mailbox)
// deletes this package and runs `go mod tidy` once real imports exist.
package imports

import (
	_ "github.com/JohannesKaufmann/html-to-markdown/v2"
	_ "github.com/charmbracelet/bubbles/textinput"
	_ "github.com/charmbracelet/bubbles/viewport"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/glamour"
	_ "github.com/charmbracelet/lipgloss"
	_ "golang.org/x/net/html"
	_ "golang.org/x/net/html/charset"
	_ "golang.org/x/term"
)
