// Package tui implements the interactive mailbox interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

// Run starts the interactive TUI on the configured account and blocks until quit.
func Run(cfg *auth.Config, initial *auth.AccountConfig) error {
	account, err := newAccountCtx(cfg, initial)
	if err != nil {
		return err
	}
	model := newApp(account)
	model.loading = true
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

type gmailAPI interface {
	ListThreads(ctx context.Context, opts gmail.ListOptions) (*gmail.ThreadList, error)
	GetThreadsMetadata(ctx context.Context, ids []string) ([]*gmail.Thread, error)
	GetThread(ctx context.Context, id, format string) (*gmail.Thread, error)
	ModifyThreads(ctx context.Context, ids, add, remove []string) error
	TrashThreads(ctx context.Context, ids []string) error
	ListLabels(ctx context.Context) ([]gmail.Label, error)
	GetAttachment(ctx context.Context, messageID, attachmentID string) ([]byte, error)
}

type accountCtx struct {
	cfg                 *auth.Config
	acct                *auth.AccountConfig
	account             string
	api                 gmailAPI
	lastRoute           func() auth.Route
	writeRoute          func() auth.Route
	writeReady          func() bool
	invalidateWrite     func()
	unlock              func(context.Context) error
	takeWriteDiagnostic func() string
	labels              []gmail.Label
	labelNameByID       map[string]string
}

var newAccountCtx = func(cfg *auth.Config, acct *auth.AccountConfig) (*accountCtx, error) {
	source := auth.NewSource(cfg, acct)
	writeCredentials := source.WriteCredentials()
	client := gmail.NewClient(gmail.ClientConfig{
		Read:    source.ReadCredentials(auth.ExecAcquirer{Cfg: cfg}),
		Write:   writeCredentials,
		Account: acct.Name,
	})
	return &accountCtx{
		cfg:        cfg,
		acct:       acct,
		account:    acct.Name,
		api:        client,
		lastRoute:  source.LastRoute,
		writeRoute: source.WriteRoute,
		writeReady: func() bool {
			_, err := writeCredentials.AccessToken(context.Background())
			return err == nil
		},
		invalidateWrite:     source.InvalidateWrite,
		takeWriteDiagnostic: func() string { return source.TakeDiagnostic(auth.ClassWrite) },
		unlock: func(ctx context.Context) error {
			_, err := source.WriteToken(ctx, auth.InteractiveExecAcquirer{Cfg: cfg})
			return err
		},
	}, nil
}

type viewState int

const (
	listView viewState = iota
	searchView
	labelPickerView
	threadView
	attachmentPickerView
)

type pendingAction struct {
	action  string
	ids     []string
	add     []string
	remove  []string
	advance bool
	retried bool
}

type app struct {
	cfg      *auth.Config
	account  string
	contexts map[string]*accountCtx
	ctx      *accountCtx

	view        viewState
	list        inboxModel
	thread      threadModel
	preview     previewModel
	search      textinput.Model
	label       textinput.Model
	labelCursor int
	viewport    viewport.Model
	spinner     spinner.Model

	status      string
	statusError bool
	loading     bool
	layout      layoutMetrics
	pending     *pendingAction
	unlocking   bool
	generations [asyncOperationCount]uint64
}

const envTokenIdentityNotice = "MAILBOX_TOKEN pins one identity for all accounts; account switching remains available"

func newApp(account *accountCtx) app {
	search := textinput.New()
	search.Prompt = searchPrompt
	search.Placeholder = "Gmail query"
	label := textinput.New()
	label.Prompt = labelPrompt
	label.Placeholder = "type to filter"
	layout := newLayoutMetrics(defaultTerminalWidth, defaultTerminalHeight)
	search.Width = layout.searchInputWidth
	label.Width = layout.labelInputWidth

	model := app{
		cfg:      account.cfg,
		account:  account.account,
		contexts: map[string]*accountCtx{account.account: account},
		ctx:      account,
		view:     listView,
		list:     newInboxModel(),
		preview:  newPreviewModel(),
		search:   search,
		label:    label,
		viewport: viewport.New(layout.readerWidth, defaultViewportHeight),
		spinner:  spinner.New(),
		layout:   layout,
	}
	model.generations[listOperation] = 1
	return model
}

func (m app) Init() tea.Cmd {
	return m.loadingCmd(listThreadsCmd(m.currentRequest(listOperation), m.list.query))
}

func (m app) loadingCmd(command tea.Cmd) tea.Cmd {
	return tea.Batch(command, m.spinnerCmd())
}

func (m app) spinnerCmd() tea.Cmd {
	return func() tea.Msg { return m.spinner.Tick() }
}

func (m app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(message.Width, message.Height)
		m.preview = newPreviewModel()
		if m.thread.thread != nil {
			if err := m.renderCurrentThread(); err != nil {
				m.surfaceError(err)
			}
		}
		if m.view == listView {
			command := m.requestPreview()
			return m, command
		}
		return m, nil
	case spinner.TickMsg:
		if m.loading {
			var command tea.Cmd
			m.spinner, command = m.spinner.Update(message)
			return m, command
		}
		return m, nil
	case threadsMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		if !m.unlocking {
			m.loading = false
		}
		m.list.setRows(message.threads)
		m.preview.requestedID = ""
		m.preview.content = ""
		m.preview.err = ""
		m.preview.loading = false
		m.clearListingStatus()
		if m.ctx.labels == nil {
			preview := m.requestPreview()
			labels := listLabelsCmd(m.beginRequest(labelOperation))
			return m, tea.Batch(preview, labels)
		}
		command := m.requestPreview()
		return m, command
	case threadMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.loading = false
		m.view = threadView
		m.thread = threadModel{thread: message.thread}
		if err := m.renderCurrentThread(); err != nil {
			m.surfaceError(err)
		}
		return m, nil
	case previewRequestMsg:
		if m.discardAsync(message) || !m.previewSelectionCurrent(message.threadID) {
			return m, nil
		}
		if content, cached := m.preview.cache[message.threadID]; cached {
			m.preview.content = content
			m.preview.err = ""
			m.preview.loading = false
			return m, nil
		}
		return m, getPreviewThreadCmd(message.request, message.threadID)
	case previewThreadMsg:
		if m.discardAsync(message) || !m.previewSelectionCurrent(message.threadID) {
			return m, nil
		}
		content, err := renderPreview(message.thread, m.previewWidth())
		if err != nil {
			m.preview.loading = false
			m.preview.err = err.Error()
			return m, nil
		}
		m.preview.cache[message.threadID] = content
		m.preview.content = content
		m.preview.err = ""
		m.preview.loading = false
		return m, nil
	case previewErrMsg:
		if m.discardAsync(message) || !m.previewSelectionCurrent(message.threadID) {
			return m, nil
		}
		m.preview.loading = false
		m.preview.err = render.SanitizeTerminal(message.err.Error())
		m.surfaceError(message.err)
		return m, nil
	case labelsMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		if !m.unlocking {
			m.loading = false
		}
		m.ctx.labels = message.labels
		if m.ctx.labels == nil {
			m.ctx.labels = []gmail.Label{}
		}
		m.ctx.labelNameByID = labelNames(m.ctx.labels)
		m.clearListingStatus()
		return m, nil
	case actionDoneMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.loading = false
		return m.finishAction(message)
	case attachmentSavedMsg:
		if m.discardAsync(message) || m.unlocking {
			return m, nil
		}
		m.loading = false
		m.view = threadView
		m.status = fmt.Sprintf("saved attachment: %s", render.SanitizeTerminal(message.path))
		m.statusError = false
		return m, nil
	case openedMsg:
		if m.discardAsync(message) || m.unlocking {
			return m, nil
		}
		if message.clearLoading {
			m.loading = false
		}
		m.status = fmt.Sprintf("handed to opener: %s", render.SanitizeTerminal(message.target))
		m.statusError = false
		return m, nil
	case unlockDoneMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.unlocking = false
		if message.err != nil {
			m.loading = false
			m.pending = nil
			m.status = render.SanitizeTerminal(message.err.Error())
			m.statusError = true
			return m, nil
		}
		if m.pending == nil {
			m.loading = false
			return m, nil
		}
		m.status = "unlocked " + m.account + " write credentials"
		if diagnostic := m.ctx.takeWriteDiagnostic(); diagnostic != "" {
			m.status += " · " + diagnostic
		}
		m.statusError = false
		return m.dispatchPending()
	case errMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		if m.unlocking && message.request.operation != actionOperation {
			return m, nil
		}
		if m.pending != nil && errors.Is(message.err, auth.ErrExpiredToken) && !m.pending.retried {
			m.pending.retried = true
			m.ctx.invalidateWrite()
			return m.startUnlock()
		}
		m.loading = false
		m.pending = nil
		m.unlocking = false
		m.surfaceError(message.err)
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(message)
	default:
		return m, nil
	}
}
func (m app) View() string {
	switch m.view {
	case searchView:
		return m.searchScreen()
	case labelPickerView:
		return m.labelPickerView()
	case threadView:
		return m.threadView()
	case attachmentPickerView:
		return m.attachmentPickerView()
	default:
		return m.inboxView()
	}
}

func (m *app) setSize(width, height int) {
	m.layout = newLayoutMetrics(width, height)
	m.search.Width = m.layout.searchInputWidth
	m.label.Width = m.layout.labelInputWidth
	m.viewport.Width = m.layout.readerWidth
	m.viewport.Height = m.layout.readerHeight
}

func (m *app) clearStatus() {
	if !m.canSurfaceStatus() {
		return
	}
	m.status = ""
	m.statusError = false
}

func (m *app) clearListingStatus() {
	if m.usesEnvToken() && m.status == envTokenIdentityNotice {
		return
	}
	m.clearStatus()
}

func (m app) usesEnvToken() bool {
	return m.ctx != nil && m.ctx.lastRoute() == auth.RouteEnvToken
}

func (m *app) surfaceError(err error) {
	if !m.canSurfaceStatus() {
		return
	}
	m.status = render.SanitizeTerminal(err.Error())
	m.statusError = true
	if gmail.IsInsufficientScope(err) {
		class, route, scope := auth.ClassRead, m.ctx.lastRoute(), "gmail.readonly"
		var typed *gmail.ErrInsufficientScope
		if errors.As(err, &typed) {
			scope = typed.Scope
			if scope == "gmail.modify" {
				class, route = auth.ClassWrite, m.ctx.writeRoute()
			}
		}
		m.status += " — provision: " + auth.ScopeHint(m.ctx.acct, class, route, scope)
	}
}

func (m app) canSurfaceStatus() bool { return !m.unlocking }

// deflectUnlock preserves the unlock attribution while the child command runs.
func (m *app) deflectUnlock() bool {
	if !m.unlocking {
		return false
	}
	const waiting = "waiting for unlock…"
	if !strings.Contains(m.status, waiting) {
		m.status += " · " + waiting
	}
	m.statusError = false
	return true
}

func (m app) statusView() string {
	status := m.status
	if m.loading {
		status = m.spinner.View() + " " + status
	}
	if status == "" {
		status = "ready"
	}
	if m.statusError {
		return errorStyle.Render(status)
	}
	return helpStyle.Render(status)
}

func (m app) updateKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if message.String() == "ctrl+c" {
		if m.deflectUnlock() {
			return m, nil
		}
		return m, tea.Quit
	}
	switch m.view {
	case listView:
		return m.updateListKey(message)
	case searchView:
		return m.updateSearchKey(message)
	case labelPickerView:
		return m.updateLabelKey(message)
	case threadView:
		return m.updateThreadKey(message)
	case attachmentPickerView:
		return m.updateAttachmentKey(message)
	default:
		return m, nil
	}
}

func (m app) switchAccount() (tea.Model, tea.Cmd) {
	if m.deflectUnlock() {
		return m, nil
	}
	if len(m.cfg.Accounts) == 0 {
		m.surfaceError(errors.New("no configured accounts"))
		return m, nil
	}
	index := 0
	for candidate, configured := range m.cfg.Accounts {
		if configured.Name == m.account {
			index = candidate
			break
		}
	}
	target := m.cfg.Accounts[(index+1)%len(m.cfg.Accounts)]
	account, exists := m.contexts[target.Name]
	if !exists {
		var err error
		account, err = newAccountCtx(m.cfg, target)
		if err != nil {
			m.surfaceError(err)
			return m, nil
		}
		m.contexts[target.Name] = account
	}
	m.account = target.Name
	m.ctx = account
	m.invalidateRequests()
	m.view = listView
	m.list = newInboxModel()
	m.preview = newPreviewModel()
	m.thread = threadModel{}
	m.pending = nil
	m.unlocking = false
	m.loading = true
	if m.usesEnvToken() {
		m.status = envTokenIdentityNotice
		m.statusError = false
	} else {
		m.clearStatus()
	}
	request := m.beginRequest(listOperation)
	return m, m.loadingCmd(listThreadsCmd(request, m.list.query))
}

func (m app) startUnlock() (tea.Model, tea.Cmd) {
	m.unlocking = true
	m.loading = true
	label := "write credentials"
	if src := m.ctx.acct.Write; src != nil && src.Label != "" {
		label = render.SanitizeTerminal(src.Label)
	}
	m.status = fmt.Sprintf("unlocking %s for %s", label, m.account)
	m.statusError = false
	request := m.beginRequest(unlockOperation)
	return m, m.loadingCmd(unlockCmd(request))
}

// dispatchPending re-issues the buffered action exactly once after an unlock.
func (m app) dispatchPending() (tea.Model, tea.Cmd) {
	pending := m.pending
	m.loading = true
	request := m.beginRequest(actionOperation)
	if pending.action == "trash" {
		return m, m.loadingCmd(trashThreadsCmd(request, pending.ids))
	}
	return m, m.loadingCmd(modifyThreadsCmd(request, pending.action, pending.ids, pending.add, pending.remove))
}

func (m app) finishAction(done actionDoneMsg) (tea.Model, tea.Cmd) {
	if m.pending == nil {
		m.surfaceError(fmt.Errorf("received unexpected completed action %q", done.action))
		return m, nil
	}
	pending := m.pending
	m.pending = nil
	if pending.action != done.action || !sameIDs(pending.ids, done.ids) {
		m.surfaceError(fmt.Errorf("completed action %q does not match the pending action", done.action))
		return m, nil
	}

	switch pending.action {
	case "archive", "trash":
		removedIndex := m.list.remove(done.ids)
		m.status = fmt.Sprintf("%s completed", pending.action)
		m.statusError = false
		if pending.advance {
			if removedIndex >= len(m.list.rows) {
				m.view = listView
				command := m.requestPreview()
				return m, command
			}
			m.list.cursor = removedIndex
			m.loading = true
			request := m.beginRequest(threadOperation)
			return m, m.loadingCmd(getThreadCmd(request, m.list.rows[removedIndex].ID))
		}
	case "mark", "label":
		m.list.updateLabels(done.ids, pending.add, pending.remove)
		m.status = fmt.Sprintf("%s completed", pending.action)
		m.statusError = false
	default:
		m.surfaceError(fmt.Errorf("unknown completed action %q", pending.action))
	}
	if m.view == listView {
		command := m.requestPreview()
		return m, command
	}
	return m, nil
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
