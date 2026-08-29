// Package tui implements the interactive mailbox interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

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
	cfg             *auth.Config
	acct            *auth.AccountConfig
	account         string
	api             gmailAPI
	lastRoute       func() auth.Route
	writeRoute      func() auth.Route
	writeReady      func() bool
	invalidateWrite func()
	unlock          func(context.Context, auth.Class) (string, error)
	takeDiagnostic  func(auth.Class) string
	labels          []gmail.Label
	labelNameByID   map[string]string
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
		invalidateWrite: source.InvalidateWrite,
		unlock: func(ctx context.Context, class auth.Class) (string, error) {
			var err error
			switch class {
			case auth.ClassRead:
				_, err = source.Resolve(ctx, auth.InteractiveExecAcquirer{Cfg: cfg})
			case auth.ClassWrite:
				_, err = source.WriteToken(ctx, auth.InteractiveExecAcquirer{Cfg: cfg})
			default:
				err = fmt.Errorf("unsupported credential class %q", class)
			}
			return unlockStatusNote(source.TakeDiagnostic(class)), err
		},
		takeDiagnostic: func(class auth.Class) string {
			return unlockStatusNote(source.TakeDiagnostic(class))
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

	status             string
	statusError        bool
	statusNote         string
	loading            bool
	layout             layoutMetrics
	pending            *pendingAction
	unlocking          bool
	unlockCtx          context.Context
	unlockCancel       context.CancelFunc
	unlockClass        auth.Class
	unlockRetry        asyncOperation
	unlockRetryGen     uint64
	unlockRetryCommand func(asyncRequest) tea.Cmd
	pinned             bool
	generations        [asyncOperationCount]uint64
}

const envTokenIdentityNotice = "MAILBOX_TOKEN pins one identity for all accounts"

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
		pinned:   os.Getenv("MAILBOX_TOKEN") != "",
	}
	model.generations[listOperation] = 1
	model.unlockRetry = asyncOperationCount
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

func (m app) Update(msg tea.Msg) (model tea.Model, command tea.Cmd) {
	var diagnostic string
	var completedRetry bool
	if message, ok := msg.(asyncMessage); ok && !m.discardAsync(message) {
		if !m.unlocking {
			diagnostic = m.ctx.takeDiagnostic(auth.ClassRead)
		}
		if m.isUnlockRetry(message.requestRef()) {
			completedRetry, _ = retryResult(msg)
		}
	}
	defer func() {
		if !completedRetry && (diagnostic == "" || m.unlocking) {
			return
		}
		if completedRetry {
			m.clearUnlockRetry()
		}
		if diagnostic != "" && !m.unlocking {
			m.appendStatusNote(diagnostic)
		}
		model = m
	}()
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
		if updated, command, handled := m.handleReadUnlock(message.request, message.err, nil); handled {
			m = updated.(app)
			return m, command
		}
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
	case unlockArmedMsg:
		if m.discardAsync(message) || !m.unlocking || m.unlockClass != message.class {
			return m, nil
		}
		return m, m.loadingCmd(unlockCmd(message.request, message.class, m.unlockCtx))
	case unlockDoneMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.unlocking = false
		m.unlockCtx = nil
		m.unlockCancel = nil
		if message.err != nil {
			m.loading = false
			if message.class == auth.ClassWrite {
				m.pending = nil
			}
			m.clearUnlockRetry()
			m.status = m.unlockFailureStatus(message.class, message.err)
			m.statusError = true
			return m, nil
		}
		m.status = "unlocked " + render.SanitizeTerminal(m.account) + " " + string(message.class) + " credentials"
		if note := unlockStatusNote(message.note); note != "" {
			m.appendStatusNote(note)
		}
		m.statusError = false
		if message.class == auth.ClassRead {
			return m.retryUnlockedRead()
		}
		if m.pending == nil {
			m.loading = false
			return m, nil
		}
		return m.dispatchPending()
	case errMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		if m.unlocking {
			return m, nil
		}
		if updated, command, handled := m.handleReadUnlock(message.request, message.err, message.retry); handled {
			m = updated.(app)
			return m, command
		}
		if m.pending != nil && errors.Is(message.err, auth.ErrExpiredToken) && !m.pending.retried {
			m.pending.retried = true
			m.ctx.invalidateWrite()
			return m.startClassUnlock(auth.ClassWrite, actionOperation)
		}
		m.loading = false
		m.pending = nil
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
	m.statusNote = ""
}

func (m *app) clearListingStatus() {
	if m.statusNote != "" || (m.usesEnvToken() && m.status == envTokenIdentityNotice) {
		return
	}
	m.clearStatus()
}

func (m app) usesEnvToken() bool { return m.pinned }

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

// unlockStatusNote renders only the most recent queued diagnostic because the
// TUI status surface is one line and cannot display multiple notes at once.
func unlockStatusNote(value string) string {
	value = strings.TrimSpace(render.SanitizeTerminal(value))
	if newline := strings.LastIndex(value, "\n"); newline >= 0 {
		value = strings.TrimSpace(value[newline+1:])
	}
	if utf8.RuneCountInString(value) <= 200 {
		return value
	}
	return string([]rune(value)[:200])
}

func (m *app) appendStatusNote(note string) {
	if note = unlockStatusNote(note); note == "" {
		return
	}
	m.statusNote = note
	if m.status == "" {
		m.status = note
		return
	}
	m.status += " · " + note
}

func (m app) credentialSource(class auth.Class) *auth.CredentialSource {
	switch class {
	case auth.ClassRead:
		return m.ctx.acct.Read
	case auth.ClassWrite:
		return m.ctx.acct.Write
	default:
		return nil
	}
}

func (m app) credentialConfigKey(class auth.Class) string {
	if source := m.credentialSource(class); source != nil {
		return render.SanitizeTerminal(source.ConfigKey)
	}
	return fmt.Sprintf("accounts.%s.%s_credential_cmd", render.SanitizeTerminal(m.account), class)
}

func (m app) unlockFailureStatus(class auth.Class, err error) string {
	return render.SanitizeTerminal(err.Error()) +
		" — set " + m.credentialConfigKey(class) +
		" (config: " + render.SanitizeTerminal(m.cfg.Path) + ")"
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

func (m *app) quitUnlock() (bool, tea.Cmd) {
	if !m.unlocking {
		return false, nil
	}
	const waiting = "waiting for unlock…"
	const abandon = waiting + " (press again to abandon)"
	if strings.Contains(m.status, abandon) {
		if m.unlockCancel != nil {
			m.unlockCancel()
		}
		return true, tea.Quit
	}
	if strings.Contains(m.status, waiting) {
		m.status = strings.Replace(m.status, waiting, abandon, 1)
	} else {
		m.status += " · " + abandon
	}
	m.statusError = false
	return true, nil
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
	pressed := message.String()
	if m.unlocking && (pressed == "ctrl+c" || pressed == keyQuit) {
		_, command := m.quitUnlock()
		return m, command
	}
	if pressed == "ctrl+c" {
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
	if m.usesEnvToken() {
		m.status = envTokenIdentityNotice
		m.statusError = false
		return m, nil
	}
	if len(m.cfg.Accounts) == 0 {
		m.surfaceError(errors.New("no configured accounts"))
		return m, nil
	}
	if len(m.cfg.Accounts) == 1 {
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
			m.status = render.SanitizeTerminal(err.Error()) +
				" — provision: set accounts." + render.SanitizeTerminal(target.Name) +
				".read_credential_cmd/_env (config: " + render.SanitizeTerminal(m.cfg.Path) + ")"
			m.statusError = true
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
	m.loading = true
	m.clearStatus()
	request := m.beginRequest(listOperation)
	return m, m.loadingCmd(listThreadsCmd(request, m.list.query))
}

func (m app) startUnlock() (tea.Model, tea.Cmd) {
	return m.startClassUnlock(auth.ClassWrite, actionOperation)
}

// unlockRenderFence waits one-plus Bubble Tea frame intervals before arming an
// interactive credential command. It never spawns a credential command or
// refreshes automatically: it only delays a human-caused unlock. Bubble Tea has
// no render acknowledgement, so this proves a frame interval elapsed with the
// attribution in View, not that bytes reached a terminal. In practice helper
// spawn, IPC, decrypt, and hardware approval take orders of magnitude longer.
const unlockRenderFence = 50 * time.Millisecond

func (m app) startClassUnlock(class auth.Class, retry asyncOperation) (tea.Model, tea.Cmd) {
	if m.unlocking {
		m.deflectUnlock()
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.unlocking = true
	m.unlockCtx = ctx
	m.unlockCancel = cancel
	m.unlockClass = class
	m.unlockRetry = retry
	m.unlockRetryGen = 0
	if class != auth.ClassRead {
		m.unlockRetryCommand = nil
	}
	m.loading = true
	m.status = m.unlockStatus(class)
	m.statusError = false
	request := m.beginRequest(unlockOperation)
	return m, tea.Tick(unlockRenderFence, func(time.Time) tea.Msg {
		return unlockArmedMsg{request: request, class: class}
	})
}

func (m app) unlockStatus(class auth.Class) string {
	account := render.SanitizeTerminal(m.account)
	source := m.credentialSource(class)
	if source == nil || source.Kind == auth.SourceEnv {
		return fmt.Sprintf(
			"refreshing %s %s access (%s)",
			account,
			class,
			m.credentialConfigKey(class),
		)
	}
	label := source.Label
	if label == "" {
		label = source.Argv0
	}
	return fmt.Sprintf(
		"waiting for %s; approve only this request — %s %s access via %s",
		render.SanitizeTerminal(label),
		account,
		class,
		render.SanitizeTerminal(source.Argv0),
	)
}

func (m app) retryUnlockedRead() (tea.Model, tea.Cmd) {
	switch m.unlockRetry {
	case listOperation:
		request := m.beginRequest(listOperation)
		m.unlockRetryGen = request.generation
		return m, m.loadingCmd(listThreadsCmd(request, m.list.query))
	case threadOperation:
		if len(m.list.rows) == 0 {
			m.loading = false
			m.clearUnlockRetry()
			return m, nil
		}
		request := m.beginRequest(threadOperation)
		m.unlockRetryGen = request.generation
		return m, m.loadingCmd(getThreadCmd(request, m.list.rows[m.list.cursor].ID))
	case previewOperation:
		command := m.requestPreview()
		if command == nil {
			m.loading = false
			m.clearUnlockRetry()
			return m, nil
		}
		m.unlockRetryGen = m.generations[previewOperation]
		return m, m.loadingCmd(command)
	case labelOperation:
		request := m.beginRequest(labelOperation)
		m.unlockRetryGen = request.generation
		return m, m.loadingCmd(listLabelsCmd(request))
	case attachmentOperation, openOperation:
		if m.unlockRetryCommand == nil {
			m.loading = false
			m.clearUnlockRetry()
			return m, nil
		}
		request := m.beginRequest(m.unlockRetry)
		m.unlockRetryGen = request.generation
		return m, m.loadingCmd(m.unlockRetryCommand(request))
	default:
		m.loading = false
		m.clearUnlockRetry()
		return m, nil
	}
}

func (m app) isUnlockRetry(request asyncRequest) bool {
	return m.unlockClass == auth.ClassRead &&
		m.unlockRetry == request.operation &&
		m.unlockRetryGen == request.generation &&
		m.unlockRetryGen != 0
}

func (m *app) clearUnlockRetry() {
	m.unlockRetry = asyncOperationCount
	m.unlockRetryGen = 0
	m.unlockRetryCommand = nil
}

func retryResult(message tea.Msg) (complete, success bool) {
	switch message.(type) {
	case threadsMsg, threadMsg, previewThreadMsg, labelsMsg, actionDoneMsg, attachmentSavedMsg, openedMsg:
		return true, true
	case errMsg, previewErrMsg:
		return true, false
	default:
		return false, false
	}
}

func (m app) handleReadUnlock(request asyncRequest, err error, retry func(asyncRequest) tea.Cmd) (tea.Model, tea.Cmd, bool) {
	var needsCredential *auth.NeedsCredentialError
	if !errors.As(err, &needsCredential) ||
		needsCredential.Class != auth.ClassRead ||
		needsCredential.Reason != auth.ReasonInteractive {
		return m, nil, false
	}
	if m.isUnlockRetry(request) {
		m.loading = false
		m.pending = nil
		m.surfaceError(err)
		return m, nil, true
	}
	m.unlockRetryCommand = retry
	model, command := m.startClassUnlock(auth.ClassRead, request.operation)
	return model, command, true
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
