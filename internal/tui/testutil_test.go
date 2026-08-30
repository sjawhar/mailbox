package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
)

type fakeAPI struct {
	threads   []*gmail.Thread
	labels    []gmail.Label
	listedIDs map[string][]string
	profile   *gmail.Profile

	listCalls     []gmail.ListOptions
	metadataCalls [][]string
	getCalls      []getCall
	modifyCalls   []modifyCall
	trashCalls    [][]string
	sendCalls     []sendCall
	profileCalls  int

	listErr     error
	metadataErr error
	getErr      error
	modifyErr   error
	trashErr    error
	modifyErrs  []error
	trashErrs   []error
	labelsErr   error
	labelsErrs  []error
	profileErr  error
	sendErr     error
	sendErrs    []error
	sent        *gmail.SentMessage
	attachments map[string][]byte
}

type getCall struct {
	id     string
	format string
}

type modifyCall struct {
	ids    []string
	add    []string
	remove []string
}

type sendCall struct {
	raw      []byte
	threadID string
}

func (f *fakeAPI) ListThreads(_ context.Context, opts gmail.ListOptions) (*gmail.ThreadList, error) {
	f.listCalls = append(f.listCalls, opts)
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := f.listedIDs[opts.Query]
	if f.listedIDs == nil {
		ids = make([]string, len(f.threads))
		for index, thread := range f.threads {
			ids[index] = thread.ID
		}
	}
	threads := make([]*gmail.Thread, len(ids))
	for index, id := range ids {
		threads[index] = &gmail.Thread{ID: id}
	}
	return &gmail.ThreadList{Threads: threads}, nil
}

func (f *fakeAPI) GetThreadsMetadata(_ context.Context, ids []string) ([]*gmail.Thread, error) {
	f.metadataCalls = append(f.metadataCalls, append([]string(nil), ids...))
	if f.metadataErr != nil {
		return nil, f.metadataErr
	}
	threads := make([]*gmail.Thread, 0, len(ids))
	for _, id := range ids {
		for _, thread := range f.threads {
			if thread.ID == id {
				threads = append(threads, thread)
				break
			}
		}
	}
	return threads, nil
}

func (f *fakeAPI) GetThread(_ context.Context, id, format string) (*gmail.Thread, error) {
	f.getCalls = append(f.getCalls, getCall{id: id, format: format})
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, thread := range f.threads {
		if thread.ID == id {
			return thread, nil
		}
	}
	return nil, errors.New("thread not found")
}

func (f *fakeAPI) ModifyThreads(_ context.Context, ids, add, remove []string) error {
	f.modifyCalls = append(f.modifyCalls, modifyCall{
		ids:    append([]string(nil), ids...),
		add:    append([]string(nil), add...),
		remove: append([]string(nil), remove...),
	})
	if len(f.modifyErrs) > 0 {
		err := f.modifyErrs[0]
		f.modifyErrs = f.modifyErrs[1:]
		return err
	}
	return f.modifyErr
}

func (f *fakeAPI) TrashThreads(_ context.Context, ids []string) error {
	f.trashCalls = append(f.trashCalls, append([]string(nil), ids...))
	if len(f.trashErrs) > 0 {
		err := f.trashErrs[0]
		f.trashErrs = f.trashErrs[1:]
		return err
	}
	return f.trashErr
}

func (f *fakeAPI) ListLabels(_ context.Context) ([]gmail.Label, error) {
	if len(f.labelsErrs) > 0 {
		err := f.labelsErrs[0]
		f.labelsErrs = f.labelsErrs[1:]
		return nil, err
	}
	if f.labelsErr != nil {
		return nil, f.labelsErr
	}
	return append([]gmail.Label(nil), f.labels...), nil
}

func (f *fakeAPI) GetAttachment(_ context.Context, messageID, attachmentID string) ([]byte, error) {
	return f.attachments[messageID+":"+attachmentID], nil
}

func (f *fakeAPI) GetProfile(_ context.Context) (*gmail.Profile, error) {
	f.profileCalls++
	if f.profileErr != nil {
		return nil, f.profileErr
	}
	return f.profile, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, raw []byte, threadID string) (*gmail.SentMessage, error) {
	f.sendCalls = append(f.sendCalls, sendCall{
		raw:      append([]byte(nil), raw...),
		threadID: threadID,
	})
	if len(f.sendErrs) > 0 {
		err := f.sendErrs[0]
		f.sendErrs = f.sendErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if f.sent != nil {
		return f.sent, nil
	}
	return &gmail.SentMessage{ID: "sent-message", ThreadID: threadID}, nil
}
func testConfig() *auth.Config {
	work := &auth.AccountConfig{Name: "work", Read: &auth.CredentialSource{Class: auth.ClassRead, Kind: auth.SourceEnv, EnvVar: "TEST_WORK", ConfigKey: "accounts.work.read_credential_env"}}
	personal := &auth.AccountConfig{Name: "personal", Read: &auth.CredentialSource{Class: auth.ClassRead, Kind: auth.SourceEnv, EnvVar: "TEST_PERSONAL", ConfigKey: "accounts.personal.read_credential_env"}}
	return &auth.Config{Accounts: []*auth.AccountConfig{work, personal}, DefaultAccount: "work"}
}

func testAccount(name string) *auth.AccountConfig {
	return &auth.AccountConfig{
		Name: name,
		Read: &auth.CredentialSource{
			Class:     auth.ClassRead,
			Kind:      auth.SourceEnv,
			EnvVar:    "TEST_" + strings.ToUpper(name),
			ConfigKey: "accounts." + name + ".read_credential_env",
		},
	}
}

func testConfigWithAccounts(accounts ...*auth.AccountConfig) *auth.Config {
	return &auth.Config{
		Path:           "/tmp/mailbox/config.toml",
		Accounts:       accounts,
		DefaultAccount: accounts[0].Name,
	}
}

func testAccountCtx(cfg *auth.Config, acct *auth.AccountConfig, api gmailAPI) *accountCtx {
	return &accountCtx{
		cfg:             cfg,
		acct:            acct,
		account:         acct.Name,
		api:             api,
		lastRoute:       func() auth.Route { return auth.RouteEnv },
		writeRoute:      func() auth.Route { return auth.RouteCmd },
		sendRoute:       func() auth.Route { return auth.RouteCmd },
		writeReady:      func() bool { return true },
		invalidateWrite: func() {},
		invalidateSend:  func() {},
		sendScope:       func() string { return "" },
		unlock: func(context.Context, auth.Class) (string, error) {
			return "", nil
		},
		takeDiagnostic: func(auth.Class) string {
			return ""
		},
	}
}

func newTestApp(rows []*gmail.Thread) (app, *fakeAPI) {
	api := &fakeAPI{threads: rows, attachments: make(map[string][]byte)}
	return newTestModel(api, "work"), api
}

func newTestModel(api gmailAPI, account string) app {
	cfg := testConfig()
	acct, _ := cfg.Account(account)
	model := newApp(testAccountCtx(cfg, acct, api))
	model.list.rows = append([]*gmail.Thread(nil), testAPIThreads(api)...)
	model.listLoaded = true
	return model
}

func newTestModelWithConfig(cfg *auth.Config, account string, api gmailAPI) app {
	acct, ok := cfg.Account(account)
	if !ok {
		panic("test account missing: " + account)
	}
	model := newApp(testAccountCtx(cfg, acct, api))
	model.list.rows = append([]*gmail.Thread(nil), testAPIThreads(api)...)
	return model
}

func switchToPersonal(t *testing.T, model app, api gmailAPI) app {
	t.Helper()
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	newAccountCtx = func(cfg *auth.Config, acct *auth.AccountConfig) (*accountCtx, error) {
		if acct.Name != "personal" {
			t.Fatalf("factory account = %q, want personal", acct.Name)
		}
		return testAccountCtx(cfg, acct, api), nil
	}
	model, command := update(t, model, key("tab"))
	if command == nil {
		t.Fatal("account switch returned no listing command")
	}
	return model
}

func testAPIThreads(api gmailAPI) []*gmail.Thread {
	fake, ok := api.(*fakeAPI)
	if !ok {
		return nil
	}
	return fake.threads
}

func update(t *testing.T, model app, msg tea.Msg) (app, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	updated, ok := next.(app)
	if !ok {
		t.Fatalf("Update() model type = %T, want tui.app", next)
	}
	return updated, cmd
}

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("Update returned no command")
	}
	return runMessage(t, cmd())
}

func runMessage(t *testing.T, message tea.Msg) tea.Msg {
	t.Helper()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return message
	}
	for _, command := range batch {
		message = command()
		if _, ticking := message.(spinner.TickMsg); ticking {
			continue
		}
		return runMessage(t, message)
	}
	t.Fatal("command batch contained no result message")
	return nil
}

func drainCommands(t *testing.T, model app, command tea.Cmd) app {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		command := queue[0]
		queue = queue[1:]
		if command == nil {
			continue
		}
		switch message := command().(type) {
		case spinner.TickMsg:
			continue
		case tea.BatchMsg:
			queue = append(queue, message...)
		default:
			var next tea.Cmd
			model, next = update(t, model, message)
			if next != nil {
				queue = append(queue, next)
			}
		}
	}
	return model
}

func key(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "j":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	case "k":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func testThreads(count int) []*gmail.Thread {
	threads := make([]*gmail.Thread, count)
	for i := range count {
		threads[i] = threadFixture(i+1, "<p>message</p>")
	}
	return threads
}

func threadFixture(number int, html string) *gmail.Thread {
	labels := []string{"INBOX", "UNREAD"}
	threadID := fmt.Sprintf("%016x", number)
	messageID := fmt.Sprintf("%016x", 1_000_000+number)
	displayNumber := strconv.Itoa(number)
	return &gmail.Thread{
		ID: threadID,
		Messages: []*gmail.Message{{
			ID:           messageID,
			ThreadID:     threadID,
			LabelIDs:     labels,
			InternalDate: 1_788_000_000_000 + int64(number),
			Payload: &gmail.MessagePart{
				MimeType: "text/html",
				Headers: []gmail.Header{
					{Name: "From", Value: "Sender " + displayNumber + " <sender@example.test>"},
					{Name: "To", Value: "Receiver <receiver@example.test>"},
					{Name: "Subject", Value: "Subject " + displayNumber},
				},
				Body: &gmail.PartBody{Data: base64.RawURLEncoding.EncodeToString([]byte(html))},
			},
		}},
	}
}

func quotedThread() *gmail.Thread {
	return threadFixture(1, `<p>new body</p><div class="gmail_quote">quoted marker</div>`)
}

func linkedThread() *gmail.Thread {
	return threadFixture(1, `<p><a href="https://example.test/one">one</a> <a href="https://example.test/two">two</a></p>`)
}

func attachmentThread() *gmail.Thread {
	thread := threadFixture(1, "")
	message := thread.Messages[0]
	message.Payload.MimeType = "multipart/mixed"
	message.Payload.Body = nil
	message.Payload.Parts = []*gmail.MessagePart{
		{
			MimeType: "text/html",
			Body:     &gmail.PartBody{Data: base64.RawURLEncoding.EncodeToString([]byte("<p>attachment body</p>"))},
		},
		{
			MimeType: "application/pdf",
			Filename: "report.pdf",
			Body: &gmail.PartBody{
				AttachmentID: "attachment-1",
				Size:         15,
			},
		},
	}
	return thread
}

func threadIDs(rows []*gmail.Thread) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids
}
