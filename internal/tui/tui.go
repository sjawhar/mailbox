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
	"github.com/sjawhar/mailbox/internal/filter"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/send"
)

// Run starts the interactive TUI on the configured account and blocks until quit.
func Run(cfg *auth.Config, initial *auth.AccountConfig, startFilter string) error {
	filterIndex, err := startFilterIndex(cfg, startFilter)
	if err != nil {
		return err
	}
	account, err := newAccountCtx(cfg, initial)
	if err != nil {
		return err
	}
	model := newApp(account)
	model.filterIndex = filterIndex
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
	GetProfile(ctx context.Context) (*gmail.Profile, error)
	SendMessage(ctx context.Context, raw []byte, threadID string) (*gmail.SentMessage, error)
	CreateDraft(ctx context.Context, raw []byte, threadID string) (*gmail.Draft, error)
}

type accountCtx struct {
	cfg             *auth.Config
	acct            *auth.AccountConfig
	account         string
	api             gmailAPI
	self            string
	lastRoute       func() auth.Route
	writeRoute      func() auth.Route
	sendRoute       func() auth.Route
	writeReady      func() bool
	invalidateWrite func()
	invalidateSend  func()
	sendScope       func() string
	unlock          func(context.Context, auth.Class) (string, error)
	takeDiagnostic  func(auth.Class) string
	labels          []gmail.Label
	labelNameByID   map[string]string
}

var newAccountCtx = func(cfg *auth.Config, acct *auth.AccountConfig) (*accountCtx, error) {
	source := auth.NewSource(cfg, acct)
	writeCredentials := source.WriteCredentials()
	sendCredentials := source.SendCredentials()
	client := gmail.NewClient(gmail.ClientConfig{
		Read:    source.ReadCredentials(auth.ExecAcquirer{Cfg: cfg}),
		Write:   writeCredentials,
		Send:    sendCredentials,
		Account: acct.Name,
	})
	return &accountCtx{
		cfg:        cfg,
		acct:       acct,
		account:    acct.Name,
		api:        client,
		lastRoute:  source.LastRoute,
		writeRoute: source.WriteRoute,
		sendRoute:  source.SendRoute,
		writeReady: func() bool {
			_, err := writeCredentials.AccessToken(context.Background())
			return err == nil
		},
		invalidateWrite: source.InvalidateWrite,
		invalidateSend:  source.InvalidateSend,
		sendScope:       source.SendScope,
		unlock: func(ctx context.Context, class auth.Class) (string, error) {
			var err error
			switch class {
			case auth.ClassRead:
				_, err = source.Resolve(ctx, auth.ExecAcquirer{Cfg: cfg})
			case auth.ClassWrite:
				_, err = source.WriteToken(ctx, auth.ExecAcquirer{Cfg: cfg})
			case auth.ClassSend:
				_, err = source.SendToken(ctx, auth.ExecAcquirer{Cfg: cfg})
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
	composeToView
	composeSubjectView
	replyConfirmView
)

type pendingAction struct {
	action            string
	ids               []string
	add               []string
	remove            []string
	advance           bool
	retried           bool
	listingGeneration uint64
}

type app struct {
	cfg      *auth.Config
	account  string
	contexts map[string]*accountCtx
	ctx      *accountCtx

	view           viewState
	list           inboxModel
	thread         threadModel
	reply          replyModel
	composeState   composeState
	composeOrigin  viewState
	preview        previewModel
	search         textinput.Model
	label          textinput.Model
	composeTo      textinput.Model
	composeSubject textinput.Model
	labelCursor    int
	viewport       viewport.Model
	spinner        spinner.Model

	status      string
	statusError bool
	statusNote  string
	// loading drives the global status spinner. beginLoading starts it and
	// settleLoading handles shared list/label completions; per-message clear
	// policies, including openedMsg.clearLoading, remain explicit in Update.
	loading       bool
	listLoaded    bool
	filterIndex   int
	layout        layoutMetrics
	pending       *pendingAction
	pendingSend   *pendingSend
	pendingDraft  *pendingDraft
	abandonPrompt bool
	unlocking     bool
	unlockCtx     context.Context
	unlockCancel  context.CancelFunc
	unlockClass   auth.Class
	pinned        bool
	generations   [asyncOperationCount]uint64
}

const envTokenIdentityNotice = "MAILBOX_TOKEN pins one identity for all accounts"

type initialReadMsg struct{}

func newApp(account *accountCtx) app {
	search := textinput.New()
	search.Prompt = searchPrompt
	search.Placeholder = "Gmail query"
	label := textinput.New()
	label.Prompt = labelPrompt
	label.Placeholder = "type to filter"
	composeTo := newComposeInput("To: ", "recipient@example.test")
	composeSubject := newComposeInput("Subject: ", "Message subject")
	layout := newLayoutMetrics(defaultTerminalWidth, defaultTerminalHeight)
	search.Width = layout.searchInputWidth
	label.Width = layout.labelInputWidth
	composeTo.Width = layout.searchInputWidth
	composeSubject.Width = layout.searchInputWidth
	model := app{
		cfg:            account.cfg,
		account:        account.account,
		contexts:       map[string]*accountCtx{account.account: account},
		ctx:            account,
		view:           listView,
		list:           newInboxModel(),
		preview:        newPreviewModel(),
		search:         search,
		label:          label,
		composeTo:      composeTo,
		composeSubject: composeSubject,
		viewport:       viewport.New(layout.readerWidth, defaultViewportHeight),
		spinner:        spinner.New(),
		layout:         layout,
		pinned:         os.Getenv("MAILBOX_TOKEN") != "",
	}
	// Seed the listing generation so zero-valued listing tags never look current.
	model.generations[listOperation] = 1
	model.status = model.readCommandStatus()
	return model
}

func (m app) Init() tea.Cmd {
	return m.firstReadCommand()
}

func (m app) readCommandStatus() string {
	source := m.ctx.acct.Read
	if source == nil || source.Kind != auth.SourceCmd {
		return ""
	}
	return m.unlockStatus(auth.ClassRead)
}

func (m app) firstReadCommand() tea.Cmd {
	if m.readCommandStatus() != "" {
		return tea.Tick(unlockRenderFence, func(time.Time) tea.Msg { return initialReadMsg{} })
	}
	return m.listReadCommand()
}

func (m app) listReadCommand() tea.Cmd {
	return m.loadingCmd(listThreadsCmd(m.currentRequest(listOperation), m.list.query, m.activeFilter()))
}

func (m app) activeFilter() *filter.Filter {
	if m.filterIndex == 0 {
		return nil
	}
	return m.cfg.Filters[m.filterIndex-1]
}

func (m app) activeFilterName() string {
	if active := m.activeFilter(); active != nil {
		return active.Name
	}
	return ""
}

func startFilterIndex(cfg *auth.Config, name string) (int, error) {
	if name == "" {
		return 0, nil
	}
	for i, active := range cfg.Filters {
		if active.Name == name {
			return i + 1, nil
		}
	}
	if names := cfg.FilterNames(); len(names) > 0 {
		return 0, fmt.Errorf("unknown filter %q; defined filters: %s", name, strings.Join(names, ", "))
	}
	return 0, fmt.Errorf("unknown filter %q; no filters are defined (config: %s)", name, cfg.DisplayPath())
}

// beginListing synchronously clears stale selection, marks rows unloaded, and
// bumps both generations. The threadOperation bump discards stale thread
// successes and errors before they can clear the replacement listing's spinner;
// the listOperation bump tags the replacement listing.
func (m *app) beginListing() asyncRequest {
	m.list.clearSelection()
	m.listLoaded = false
	m.beginRequest(threadOperation)
	return m.beginRequest(listOperation)
}

// refreshListing invalidates the visible rows and refetches the listing with
// the current query and filter.
func (m *app) refreshListing() tea.Cmd {
	m.loading = true
	return m.loadingCmd(listThreadsCmd(m.beginListing(), m.list.query, m.activeFilter()))
}

func (m app) loadingCmd(command tea.Cmd) tea.Cmd {
	return tea.Batch(command, m.spinnerCmd())
}

func (m app) spinnerCmd() tea.Cmd {
	return func() tea.Msg { return m.spinner.Tick() }
}

func (m app) Update(msg tea.Msg) (model tea.Model, command tea.Cmd) {
	var diagnostic string
	if message, ok := msg.(asyncMessage); ok && !m.discardAsync(message) && !m.unlocking {
		diagnostic = m.ctx.takeDiagnostic(auth.ClassRead)
	}
	defer func() {
		if diagnostic == "" || m.unlocking {
			return
		}
		m.appendStatusNote(diagnostic)
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
	case initialReadMsg:
		return m, m.listReadCommand()
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
		m.settleLoading()
		m.list.setRows(message.threads)
		m.listLoaded = true
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
		if m.discardAsync(message) || !m.currentRows(message.request.listingGeneration) {
			return m, nil
		}
		m.loading = false
		m.view = threadView
		m.thread = threadModel{thread: message.thread, listingGeneration: message.request.listingGeneration}
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
		m.settleLoading()
		m.ctx.labels = message.labels
		if m.ctx.labels == nil {
			m.ctx.labels = []gmail.Label{}
		}
		m.ctx.labelNameByID = labelNames(m.ctx.labels)
		m.clearListingStatus()
		return m, nil
	case editorDoneMsg:
		return m.finishEditor(message)
	case profileMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.loading = false
		m.ctx.self = message.email
		return m.openReply()
	case draftSavedMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.pendingDraft = nil
		m.loading = false
		m.status = "draft saved — " + render.SanitizeTerminal(message.id)
		m.statusError = false
		if m.reply.envelope != nil && m.reply.envelope.Mode == send.ModeCompose {
			m.view = listView
		} else {
			m.view = threadView
		}
		m.reply = replyModel{}
		m.composeState = composeState{}
		return m, nil
	case sendDoneMsg:
		if m.discardAsync(message) {
			return m, nil
		}
		m.loading = false
		m.pendingSend = nil
		m.reply = replyModel{}
		m.composeState = composeState{}
		m.status = "sent — thread updated"
		m.statusError = false
		m.appendStatusNote(auth.SendScopeWarning(m.ctx.sendScope(), m.credentialConfigKey(auth.ClassSend)))
		m.appendStatusNote(m.ctx.takeDiagnostic(auth.ClassSend))
		m.view = threadView
		request := m.beginRequest(threadOperation)
		return m, m.loadingCmd(getThreadCmd(request, message.sent.ThreadID))
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
			m.clearPendingWrites(message.class)
			if message.class == auth.ClassSend {
				m.beginRequest(unlockOperation)
			}
			m.status = m.unlockFailureStatus(message.class, message.err)
			m.statusError = true
			return m, nil
		}
		m.status = "unlocked " + render.SanitizeTerminal(m.account) + " " + string(message.class) + " credentials"
		if note := unlockStatusNote(message.note); note != "" {
			m.appendStatusNote(note)
		}
		m.statusError = false
		if message.class == auth.ClassSend {
			if m.pendingSend == nil {
				m.loading = false
				return m, nil
			}
			request := m.beginRequest(sendOperation)
			return m, m.loadingCmd(sendCmd(request, m.pendingSend.mime, m.pendingSend.threadID))
		}
		if message.class == auth.ClassWrite && m.pendingDraft != nil {
			request := m.beginRequest(draftOperation)
			return m, m.loadingCmd(saveDraftCmd(request, m.pendingDraft.mime, m.pendingDraft.threadID))
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
		if m.pendingSend != nil && errors.Is(message.err, auth.ErrExpiredSendToken) && !m.pendingSend.retried {
			m.pendingSend.retried = true
			m.ctx.invalidateSend()
			return m.startClassUnlock(auth.ClassSend)
		}
		if m.pendingSend != nil {
			m.loading = false
			m.pendingSend = nil
			m.surfaceError(message.err)
			return m, nil
		}
		if m.pendingDraft != nil && errors.Is(message.err, auth.ErrExpiredToken) && !m.pendingDraft.retried {
			m.pendingDraft.retried = true
			m.ctx.invalidateWrite()
			return m.startClassUnlock(auth.ClassWrite)
		}
		if m.pendingDraft != nil {
			m.loading = false
			m.pendingDraft = nil
			m.surfaceError(message.err)
			return m, nil
		}
		if m.pending != nil && errors.Is(message.err, auth.ErrExpiredToken) {
			if !m.listingCurrent(m.pending.listingGeneration) {
				m.loading = false
				m.pending = nil
				m.status = "write retry canceled — listing changed"
				m.statusError = false
				return m, nil
			}
			if !m.pending.retried {
				m.pending.retried = true
				m.ctx.invalidateWrite()
				return m.startClassUnlock(auth.ClassWrite)
			}
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
	case composeToView:
		return m.composeToScreen()
	case composeSubjectView:
		return m.composeSubjectScreen()
	case replyConfirmView:
		return m.replyConfirmScreen()
	default:
		return m.inboxView()
	}
}

func (m *app) setSize(width, height int) {
	m.layout = newLayoutMetrics(width, height)
	m.search.Width = m.layout.searchInputWidth
	m.label.Width = m.layout.labelInputWidth
	m.composeTo.Width = m.layout.searchInputWidth
	m.composeSubject.Width = m.layout.searchInputWidth
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
			switch scope {
			case "gmail.modify":
				class, route = auth.ClassWrite, m.ctx.writeRoute()
			case "gmail.send":
				class, route = auth.ClassSend, m.ctx.sendRoute()
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
	case auth.ClassSend:
		return m.ctx.acct.Send
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
		m.abandonUnlock()
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

// clearPendingWrites drops buffered work associated with an unlock class.
func (m *app) clearPendingWrites(class auth.Class) {
	switch class {
	case auth.ClassWrite:
		m.pending = nil
		m.pendingDraft = nil
	case auth.ClassSend:
		m.pendingSend = nil
	}
}

func (m *app) abandonUnlock() {
	if !m.unlocking {
		return
	}
	if m.unlockCancel != nil {
		m.unlockCancel()
	}
	m.clearPendingWrites(m.unlockClass)
	m.unlocking = false
	m.unlockCtx = nil
	m.unlockCancel = nil
	m.loading = false
	m.beginRequest(unlockOperation)
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
	case composeToView:
		return m.updateComposeToKey(message)
	case composeSubjectView:
		return m.updateComposeSubjectKey(message)
	case replyConfirmView:
		return m.updateReplyConfirmKey(message)
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
	pendingWrite := m.pending != nil || m.pendingDraft != nil

	m.account = target.Name
	m.ctx = account
	m.invalidateRequests()
	m.view = listView
	m.list = newInboxModel()
	m.listLoaded = false
	m.preview = newPreviewModel()
	m.thread = threadModel{}
	m.reply = replyModel{}
	m.pending = nil
	m.pendingSend = nil
	m.pendingDraft = nil
	m.loading = true
	m.clearStatus()
	m.status = m.readCommandStatus()
	if pendingWrite {
		m.appendStatusNote("write action continues in previous account")
	}
	return m, m.firstReadCommand()
}

func (m app) startUnlock() (tea.Model, tea.Cmd) {
	return m.startClassUnlock(auth.ClassWrite)
}

// unlockRenderFence waits one-plus Bubble Tea frame intervals before arming a
// credential command. TUI uses it after rendering command-source attribution
// and before a human-caused unlock. Bubble Tea has no render acknowledgement,
// so this proves a frame interval elapsed with the attribution in View, not that
// bytes reached a terminal. In practice helper spawn, IPC, decrypt, and hardware
// approval take orders of magnitude longer.
const unlockRenderFence = 50 * time.Millisecond

func (m app) startClassUnlock(class auth.Class) (tea.Model, tea.Cmd) {
	if m.unlocking {
		m.deflectUnlock()
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.unlocking = true
	m.unlockCtx = ctx
	m.unlockCancel = cancel
	m.unlockClass = class
	m.status = m.unlockStatus(class)
	m.statusError = false
	request := m.beginLoading(unlockOperation)
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

// dispatchPending re-issues the buffered action exactly once after an unlock.
func (m app) dispatchPending() (tea.Model, tea.Cmd) {
	pending := m.pending
	request := m.beginLoading(actionOperation)
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
			request := m.beginLoading(threadOperation)
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
