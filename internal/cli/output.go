package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/toon"
)

type Format uint8

const (
	FormatText Format = iota
	FormatJSON
	FormatTOON
)

// ResolveFormat selects the output format shared by every user-facing command.
func ResolveFormat(jsonFlag, textFlag, agentEnv, terminal bool) Format {
	switch {
	case jsonFlag:
		return FormatJSON
	case textFlag:
		return FormatText
	case agentEnv || !terminal:
		return FormatTOON
	default:
		return FormatText
	}
}

func agentEnvironment() bool {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "OPENCODE", "AGENT", "CI"} {
		if _, ok := os.LookupEnv(name); ok {
			return true
		}
	}
	return false
}

func (cc *cmdCtx) format() Format {
	return ResolveFormat(cc.json, cc.text, agentEnvironment(), isTerminal(cc.stdout))
}

type threadRow struct {
	N       int      `json:"n"`
	ID      string   `json:"id"`
	Subject string   `json:"subject"`
	From    string   `json:"from"`
	Date    string   `json:"date"`
	Snippet string   `json:"snippet"`
	Unread  bool     `json:"unread"`
	Labels  []string `json:"labels"`
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func (cc *cmdCtx) writeMachine(value any) error {
	if cc.format() == FormatJSON {
		return writeJSON(cc.stdout, value)
	}
	doc, err := toon.Encode(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cc.stdout, doc)
	return err
}

func normalizeRenderedThread(thread *render.RenderedThread) {
	if thread.Participants == nil {
		thread.Participants = []string{}
	}
	if thread.Messages == nil {
		thread.Messages = []render.RenderedMessage{}
	}
	for index := range thread.Messages {
		if thread.Messages[index].Links == nil {
			thread.Messages[index].Links = []render.Link{}
		}
		if thread.Messages[index].Attachments == nil {
			thread.Messages[index].Attachments = []render.Attachment{}
		}
	}
}

func normalizeAttachments(attachments []render.Attachment) []render.Attachment {
	if attachments == nil {
		return []render.Attachment{}
	}
	return attachments
}

func normalizeStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func normalizeFailures(values []filterActionFailure) []filterActionFailure {
	if values == nil {
		return []filterActionFailure{}
	}
	return values
}

func printThreads(output io.Writer, rows []threadRow, pretty bool) {
	unreadStyle := lipgloss.NewStyle().Bold(true)
	dateStyle := lipgloss.NewStyle().Faint(true)
	for _, row := range rows {
		sender := render.SanitizeTerminal(gmail.Sender(row.From))
		subject := render.SanitizeTerminal(row.Subject)
		date := render.SanitizeTerminal(row.Date)
		if pretty {
			if row.Unread {
				subject = unreadStyle.Render(subject)
			}
			date = dateStyle.Render(date)
		}
		fmt.Fprintf(output, "%d\t%s\t%t\t%s\t%s\t%s\n", row.N, render.SanitizeTerminal(row.ID), row.Unread, sender, subject, date)
	}
}

func isTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && isTTY(file)
}
