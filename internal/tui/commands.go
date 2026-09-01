package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/filter"
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

type profileMsg struct {
	request asyncRequest
	email   string
}

func (message profileMsg) requestRef() asyncRequest { return message.request }

type sendDoneMsg struct {
	request asyncRequest
	sent    *gmail.SentMessage
}

func (message sendDoneMsg) requestRef() asyncRequest { return message.request }

type draftSavedMsg struct {
	request asyncRequest
	id      string
}

func (message draftSavedMsg) requestRef() asyncRequest { return message.request }

type errMsg struct {
	request asyncRequest
	err     error
}

func (message errMsg) requestRef() asyncRequest { return message.request }

// unlockArmedMsg creates a status-to-spawn fence for credential commands.
// Bubble Tea renders the attribution set by startUnlock before this message
// dispatches the acquirer.
type unlockArmedMsg struct {
	request asyncRequest
	class   auth.Class
}

func (message unlockArmedMsg) requestRef() asyncRequest { return message.request }

type unlockDoneMsg struct {
	request asyncRequest
	class   auth.Class
	note    string
	err     error
}

func (message unlockDoneMsg) requestRef() asyncRequest { return message.request }

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

func listThreadsCmd(request asyncRequest, query string, f *filter.Filter) tea.Cmd {
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
		threads = filter.FilterThreads(f, threads)
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

func getProfileCmd(request asyncRequest) tea.Cmd {
	return func() tea.Msg {
		profile, err := request.ctx.api.GetProfile(context.Background())
		if err != nil {
			return errMsg{request: request, err: err}
		}
		return profileMsg{request: request, email: profile.EmailAddress}
	}
}

func sendCmd(request asyncRequest, raw []byte, threadID string) tea.Cmd {
	return func() tea.Msg {
		sent, err := request.ctx.api.SendMessage(context.Background(), raw, threadID)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		return sendDoneMsg{request: request, sent: sent}
	}
}

func saveDraftCmd(request asyncRequest, raw []byte, threadID string) tea.Cmd {
	return func() tea.Msg {
		draft, err := request.ctx.api.CreateDraft(context.Background(), raw, threadID)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		return draftSavedMsg{request: request, id: draft.ID}
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

func unlockCmd(request asyncRequest, class auth.Class, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		note, err := request.ctx.unlock(ctx, class)
		return unlockDoneMsg{request: request, class: class, note: note, err: err}
	}
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
		contents, err := render.ResolveAttachmentBytes(context.Background(), request.ctx.api, attachment)
		if err != nil {
			return errMsg{request: request, err: err}
		}
		name, _ := render.CanonicalFilename(attachment.Filename, attachment.N-1)
		if err := render.SaveAttachment(directory, name, contents); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errMsg{request: request, err: fmt.Errorf("refusing to overwrite existing attachment %q", name)}
			}
			return errMsg{request: request, err: err}
		}
		return attachmentSavedMsg{request: request, path: filepath.Join(directory, name)}
	}
}

func openLinkCmd(request asyncRequest, target string) tea.Cmd {
	return func() tea.Msg {
		if err := openURL(target, auth.ScrubbedEnviron(request.ctx.cfg)); err != nil {
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
		if err := openURL(path, auth.ScrubbedEnviron(request.ctx.cfg)); err != nil {
			return errMsg{request: request, err: err}
		}
		return openedMsg{request: request, target: path, clearLoading: true}
	}
}
