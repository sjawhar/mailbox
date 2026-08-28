package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

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
