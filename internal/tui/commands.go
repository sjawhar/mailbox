package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

type threadsMsg struct {
	request asyncRequest
	threads []*gmail.Thread
}

func (message threadsMsg) requestRef() asyncRequest { return message.request }

type threadMsg struct {
	request asyncRequest
	thread  *gmail.Thread
}

func (message threadMsg) requestRef() asyncRequest { return message.request }

type previewRequestMsg struct {
	request  asyncRequest
	threadID string
}

func (message previewRequestMsg) requestRef() asyncRequest { return message.request }

type previewThreadMsg struct {
	request  asyncRequest
	threadID string
	thread   *gmail.Thread
}

func (message previewThreadMsg) requestRef() asyncRequest { return message.request }

type previewErrMsg struct {
	request  asyncRequest
	threadID string
	err      error
}

func (message previewErrMsg) requestRef() asyncRequest { return message.request }

type actionDoneMsg struct {
	request asyncRequest
	action  string
	ids     []string
}

func (message actionDoneMsg) requestRef() asyncRequest { return message.request }

type errMsg struct {
	request asyncRequest
	err     error
}

func (message errMsg) requestRef() asyncRequest { return message.request }

type mintDoneMsg struct {
	request asyncRequest
	note    string
	err     error
}

func (message mintDoneMsg) requestRef() asyncRequest { return message.request }

type labelsMsg struct {
	request asyncRequest
	labels  []gmail.Label
}

func (message labelsMsg) requestRef() asyncRequest { return message.request }

type attachmentSavedMsg struct {
	request asyncRequest
	path    string
}

func (message attachmentSavedMsg) requestRef() asyncRequest { return message.request }

type openedMsg struct {
	request      asyncRequest
	target       string
	clearLoading bool
}

func (message openedMsg) requestRef() asyncRequest { return message.request }

var openURL = render.OpenURL

func listThreadsCmd(request asyncRequest, query string) tea.Cmd {
	return func() tea.Msg {
		opts := gmail.ListOptions{Query: query, MaxResults: 50}
		if query == "" {
			opts.LabelIDs = []string{"INBOX"}
		}
		listed, err := request.ctx.api.ListThreads(context.Background(), opts)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		ids := make([]string, len(listed.Threads))
		for i, thread := range listed.Threads {
			ids[i] = thread.ID
		}
		threads, err := request.ctx.api.GetThreadsMetadata(context.Background(), ids)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		if query == "" {
			threads = gmail.FilterThreadsWithLabel(threads, "INBOX")
		}
		return threadsMsg{request: request, threads: threads}
	}
}

func getThreadCmd(request asyncRequest, id string) tea.Cmd {
	return func() tea.Msg {
		thread, err := request.ctx.api.GetThread(context.Background(), id, "full")
		if err != nil {
			return errMsg{request: request, err: err}
		}
		return threadMsg{request: request, thread: thread}
	}
}

const previewDebounce = 125 * time.Millisecond

func previewDebounceCmd(request asyncRequest, threadID string) tea.Cmd {
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg {
		return previewRequestMsg{request: request, threadID: threadID}
	})
}

func getPreviewThreadCmd(request asyncRequest, threadID string) tea.Cmd {
	return func() tea.Msg {
		thread, err := request.ctx.api.GetThread(context.Background(), threadID, "full")
		if err != nil {
			return previewErrMsg{request: request, threadID: threadID, err: err}
		}
		return previewThreadMsg{request: request, threadID: threadID, thread: thread}
	}
}

func modifyThreadsCmd(request asyncRequest, action string, ids, add, remove []string) tea.Cmd {
	return func() tea.Msg {
		if err := request.ctx.api.ModifyThreads(context.Background(), ids, add, remove); err != nil {
			return errMsg{request: request, err: err}
		}
		return actionDoneMsg{request: request, action: action, ids: ids}
	}
}

func trashThreadsCmd(request asyncRequest, ids []string) tea.Cmd {
	return func() tea.Msg {
		if err := request.ctx.api.TrashThreads(context.Background(), ids); err != nil {
			return errMsg{request: request, err: err}
		}
		return actionDoneMsg{request: request, action: "trash", ids: ids}
	}
}

// mintCmd runs the account's minter and reports the child's sanitized stderr
// tail alongside the outcome.
func mintCmd(request asyncRequest) tea.Cmd {
	return func() tea.Msg {
		var stderr bytes.Buffer
		err := request.ctx.mint(context.Background(), &stderr)
		return mintDoneMsg{request: request, note: mintNote(stderr.String()), err: err}
	}
}

// mintNote reduces child stderr to a status-line-safe fragment: the last
// non-empty sanitized line, capped at 200 characters.
func mintNote(stderr string) string {
	lines := strings.Split(render.SanitizeTerminal(strings.TrimSpace(stderr)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		return line
	}
	return ""
}

func listLabelsCmd(request asyncRequest) tea.Cmd {
	return func() tea.Msg {
		labels, err := request.ctx.api.ListLabels(context.Background())
		if err != nil {
			return errMsg{request: request, err: err}
		}
		return labelsMsg{request: request, labels: labels}
	}
}

func saveAttachmentCmd(request asyncRequest, attachment render.Attachment) tea.Cmd {
	return func() tea.Msg {
		directory, err := os.Getwd()
		if err != nil {
			return errMsg{request: request, err: err}
		}
		path, overwrite, err := render.AttachmentDestination(directory, attachment.Filename)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		contents, err := request.ctx.api.GetAttachment(context.Background(), attachment.MessageID, attachment.AttachmentID)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		if err := render.WriteAttachment(path, contents, overwrite); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errMsg{request: request, err: fmt.Errorf("refusing to overwrite existing attachment %q", path)}
			}
			return errMsg{request: request, err: err}
		}
		return attachmentSavedMsg{request: request, path: path}
	}
}

func openLinkCmd(request asyncRequest, target string) tea.Cmd {
	return func() tea.Msg {
		if err := openURL(target); err != nil {
			return errMsg{request: request, err: err}
		}
		return openedMsg{request: request, target: target}
	}
}

func openHTMLCmd(request asyncRequest, thread *gmail.Thread) tea.Cmd {
	return func() tea.Msg {
		_, path, err := render.WriteHTMLBackstop(context.Background(), thread, request.ctx.api.GetAttachment)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		if err := openURL(path); err != nil {
			return errMsg{request: request, err: err}
		}
		return openedMsg{request: request, target: path, clearLoading: true}
	}
}
