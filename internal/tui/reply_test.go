package tui

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/compose"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/send"
)

func TestReplyOpensEditorWithDerivedRecipients(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	thread := replyThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.view = threadView
	model.thread = threadModel{thread: thread}

	model, command := update(t, model, key(keyReply))

	if command == nil || model.composeState.draftPath == "" {
		t.Fatalf("r must create a draft and return the exec command, state=%#v", model.composeState)
	}
	if model.reply.target == nil || model.reply.target.ID != "target-message" {
		t.Fatalf("pinned target = %#v, want newest target-message", model.reply.target)
	}
	content, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"to  sender@example.test  Sender  (From)",
		"cc  colleague@example.test  Colleague  (To)",
		"cc  copied@example.test  Copied  (CC)",
		"subject: Re: target subject",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("draft missing %q:\n%s", want, content)
		}
	}
}

func TestReplyConfirmRendersSharedEnvelope(t *testing.T) {
	thread := replyThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "Rendered body")

	var want strings.Builder
	send.RenderText(&want, model.account, model.reply.envelope, 0)
	if !strings.Contains(model.View(), want.String()) {
		t.Fatalf("confirm screen does not contain shared envelope:\n%s", model.View())
	}
}

func TestReplySanitizesRemoteEnvelopeText(t *testing.T) {
	view := replyEnvelopeText("work", &send.Envelope{
		Mode:    send.ModeReply,
		Subject: "target \x1b]52;c;untrusted\a subject",
	})

	if strings.Contains(view, "\x1b") {
		t.Fatalf("reply view contains unsanitized terminal control: %q", view)
	}
}
func TestReplyFetchesSelfBeforeResolving(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	api.profile = &gmail.Profile{EmailAddress: "me@example.test"}
	model.view = threadView
	model.thread = threadModel{thread: thread}

	model, command := update(t, model, key(keyReply))
	if !model.loading || model.view != threadView {
		t.Fatalf("profile lookup state = loading:%t view:%v", model.loading, model.view)
	}
	model, _ = update(t, model, runCmd(t, command))

	if api.profileCalls != 1 || model.ctx.self != "me@example.test" || model.composeState.draftPath == "" {
		t.Fatalf("profile calls:%d self:%q draft=%q", api.profileCalls, model.ctx.self, model.composeState.draftPath)
	}
}

func TestReplyPinsMessageAtOpenTime(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "Pinned reply body")
	thread.Messages = append(thread.Messages, replyMessage(
		"newer-message", thread.ID, 3,
		"Other <other@example.test>", "Me <me@example.test>", "", "newer subject", "<newer@example.test>", "",
	))

	model = finishConfirmedSend(t, model)
	if len(api.sendCalls) != 1 {
		t.Fatalf("send calls = %d, want 1", len(api.sendCalls))
	}
	call := api.sendCalls[0]
	if call.threadID != thread.ID {
		t.Fatalf("sent thread = %q, want %q", call.threadID, thread.ID)
	}
	for _, want := range []string{
		"Subject: Re: target subject\r\n",
		"In-Reply-To: <target@example.test>\r\n",
	} {
		if !bytes.Contains(call.raw, []byte(want)) {
			t.Fatalf("pinned MIME missing %q:\n%s", want, call.raw)
		}
	}
	if bytes.Contains(call.raw, []byte("newer subject")) || bytes.Contains(call.raw, []byte("<newer@example.test>")) {
		t.Fatalf("MIME followed newer thread message:\n%s", call.raw)
	}
}

func TestReplyRefusalPreventsDraftCreation(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
	t.Setenv("VISUAL", "true")
	selfOnly := replyThread()
	selfOnly.Messages[1] = replyMessage(
		"target-message", selfOnly.ID, 2,
		"Me <me@example.test>", "Me <me@example.test>", "", "target subject", "<target@example.test>", "",
	)
	model, _ := newTestApp([]*gmail.Thread{selfOnly})
	model.ctx.self = "me@example.test"
	model.view = threadView
	model.thread = threadModel{thread: selfOnly}

	model, _ = update(t, model, key(keyReply))
	if model.view != threadView || !strings.Contains(model.status, "R2") {
		t.Fatalf("R2 reply state = view:%v status:%q", model.view, model.status)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("R2 refusal created draft data: %v", entries)
	}
}

func TestReplyDivergentReplyToRefusalPreventsDraftCreation(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
	t.Setenv("VISUAL", "true")
	thread := replyThread()
	thread.Messages[1] = replyMessage(
		"target-message", thread.ID, 2,
		"Sender <sender@example.test>", "Me <me@example.test>", "", "target subject", "<target@example.test>", "Reply target <reply-to@example.test>",
	)
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.view = threadView
	model.thread = threadModel{thread: thread}

	model, _ = update(t, model, key(keyReply))

	if model.view != threadView || !strings.Contains(model.status, "needs_explicit_recipient") {
		t.Fatalf("R6 reply state = view:%v status:%q", model.view, model.status)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("R6 refusal created draft data: %v", entries)
	}
}

func TestConfirmSendRunsThroughClassSendUnlock(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.ctx.acct.Send = &auth.CredentialSource{
		Class:     auth.ClassSend,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/send-approver"},
		Argv0:     "/abs/send-approver",
		Label:     "hardware key touch",
		ConfigKey: "accounts.work.send_credential_cmd",
	}
	unlockCalls := 0
	model.ctx.unlock = func(_ context.Context, class auth.Class) (string, error) {
		unlockCalls++
		if class != auth.ClassSend {
			t.Fatalf("unlock class = %q, want send", class)
		}
		return "", nil
	}
	model = confirmWithBody(t, model, "Confirm body")

	model, fence := update(t, model, key(keyConfirmSend))
	if !model.unlocking || model.unlockClass != auth.ClassSend {
		t.Fatalf("unlock state = unlocking:%t class:%q", model.unlocking, model.unlockClass)
	}
	if unlockCalls != 0 || len(api.sendCalls) != 0 {
		t.Fatalf("activity before render fence = unlocks:%d sends:%d", unlockCalls, len(api.sendCalls))
	}
	if !strings.Contains(model.status, "hardware key touch") || !strings.Contains(model.View(), "hardware key touch") {
		t.Fatalf("attribution did not render before acquisition: %q", model.status)
	}

	armed := runCmd(t, fence)
	if unlockCalls != 0 {
		t.Fatalf("unlock ran before armed message: %d", unlockCalls)
	}
	model, acquire := update(t, model, armed)
	model, sendCommand := update(t, model, runCmd(t, acquire))
	model, _ = update(t, model, runCmd(t, sendCommand))

	if unlockCalls != 1 || len(api.sendCalls) != 1 || !strings.Contains(model.status, "sent — thread updated") {
		t.Fatalf("send result = unlocks:%d sends:%d status:%q", unlockCalls, len(api.sendCalls), model.status)
	}
}

func TestEscAbandonsWithoutTransmit(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	unlockCalls := 0
	model.ctx.unlock = func(context.Context, auth.Class) (string, error) {
		unlockCalls++
		return "", nil
	}
	model = confirmWithBody(t, model, "Draft remains")
	model, _ = update(t, model, key("esc"))
	model, _ = update(t, model, key("esc"))
	if model.view != threadView || model.pendingSend != nil {
		t.Fatalf("escape from confirm = view:%v pending:%#v", model.view, model.pendingSend)
	}
	if len(api.sendCalls) != 0 || unlockCalls != 0 {
		t.Fatalf("escape activity = sends:%d unlocks:%d", len(api.sendCalls), unlockCalls)
	}
}

func TestEscAfterConfirmAbandonsPendingSend(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "Abandon body")

	model, fence := update(t, model, key(keyConfirmSend))
	request := model.currentRequest(unlockOperation)
	cancellations := 0
	model.unlockCancel = func() { cancellations++ }

	model, _ = update(t, model, key("esc"))
	model, _ = update(t, model, key("esc"))
	if model.view != threadView || model.unlocking || model.pendingSend != nil {
		t.Fatalf("escape state = view:%v unlocking:%t pending:%#v", model.view, model.unlocking, model.pendingSend)
	}
	if cancellations != 1 {
		t.Fatalf("unlock cancellations = %d, want 1", cancellations)
	}
	if model.generations[unlockOperation] == request.generation {
		t.Fatalf("unlock generation = %d, want invalidate %d", model.generations[unlockOperation], request.generation)
	}

	armed := runCmd(t, fence).(unlockArmedMsg)
	model, command := update(t, model, armed)
	if command != nil {
		t.Fatal("abandoned unlock armed a credential acquisition")
	}
	model, command = update(t, model, unlockDoneMsg{request: request, class: auth.ClassSend})
	if command != nil {
		model, _ = update(t, model, runCmd(t, command))
	}
	if len(api.sendCalls) != 0 || model.pendingSend != nil {
		t.Fatalf("abandoned send dispatched: sends:%d pending:%#v", len(api.sendCalls), model.pendingSend)
	}
}

func TestMidflightAbandonNeverTransmits(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "Abandon body")
	model, _ = update(t, model, key(keyConfirmSend))
	request := model.currentRequest(unlockOperation)
	cancellations := 0
	model.unlockCancel = func() { cancellations++ }

	model, firstQuit := update(t, model, key(keyQuit))
	if firstQuit != nil {
		t.Fatal("first quit press abandoned the unlock")
	}
	model, secondQuit := update(t, model, key(keyQuit))
	if secondQuit == nil || cancellations != 1 {
		t.Fatalf("force abandon = command:%v cancellations:%d", secondQuit != nil, cancellations)
	}
	model, _ = update(t, model, unlockDoneMsg{request: request, class: auth.ClassSend, err: context.Canceled})
	if model.pendingSend != nil || len(api.sendCalls) != 0 {
		t.Fatalf("canceled unlock state = pending:%#v sends:%d", model.pendingSend, len(api.sendCalls))
	}
	status := model.status
	model, _ = update(t, model, unlockDoneMsg{request: request, class: auth.ClassSend})
	if model.status != status || len(api.sendCalls) != 0 {
		t.Fatalf("late unlock completion was not discarded: status:%q sends:%d", model.status, len(api.sendCalls))
	}
}

func TestSendExpiryRetriesOnce(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	invalidations := 0
	unlockCalls := 0
	model.ctx.invalidateSend = func() { invalidations++ }
	model.ctx.unlock = func(_ context.Context, class auth.Class) (string, error) {
		unlockCalls++
		if class != auth.ClassSend {
			t.Fatalf("unlock class = %q, want send", class)
		}
		return "", nil
	}
	api.sendErrs = []error{auth.ErrExpiredSendToken, nil}
	model = confirmWithBody(t, model, "Retry body")

	model, retryFence := sendUntilResult(t, model)
	if !model.unlocking || model.unlockClass != auth.ClassSend || invalidations != 1 || model.pendingSend == nil || !model.pendingSend.retried {
		t.Fatalf("first expiry = unlocking:%t class:%q invalidations:%d pending:%#v", model.unlocking, model.unlockClass, invalidations, model.pendingSend)
	}
	model, _ = resolveUnlockForSend(t, model, retryFence)
	if len(api.sendCalls) != 2 || unlockCalls != 2 || !strings.Contains(model.status, "sent — thread updated") {
		t.Fatalf("retry success = sends:%d unlocks:%d status:%q", len(api.sendCalls), unlockCalls, model.status)
	}

	model, api = newTestApp([]*gmail.Thread{replyThread()})
	model.ctx.self = "me@example.test"
	model.ctx.invalidateSend = func() { invalidations++ }
	api.sendErrs = []error{auth.ErrExpiredSendToken, auth.ErrExpiredSendToken}
	model = confirmWithBody(t, model, "Second expiry")
	model, retryFence = sendUntilResult(t, model)
	model, _ = resolveUnlockForSend(t, model, retryFence)
	if !model.statusError || !strings.Contains(model.status, "send token expired") || model.pendingSend != nil {
		t.Fatalf("second expiry state = status:%q error:%t pending:%#v", model.status, model.statusError, model.pendingSend)
	}
}

func TestTUISendSurfacesBroadScopeWarning(t *testing.T) {
	thread := replyThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.ctx.acct.Send = &auth.CredentialSource{Class: auth.ClassSend, ConfigKey: "accounts.work.send_credential_cmd"}
	model.ctx.sendScope = func() string {
		return "https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"
	}
	model = finishConfirmedSend(t, confirmWithBody(t, model, "Broad scope"))
	if !strings.Contains(model.status, "accounts.work.send_credential_cmd") {
		t.Fatalf("broad scope status = %q", model.status)
	}

	thread = replyThread()
	model, _ = newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.ctx.acct.Send = &auth.CredentialSource{Class: auth.ClassSend, ConfigKey: "accounts.work.send_credential_cmd"}
	model.ctx.sendScope = func() string { return "https://www.googleapis.com/auth/gmail.send" }
	model = finishConfirmedSend(t, confirmWithBody(t, model, "Exact scope"))
	if strings.Contains(model.status, "granted scope exceeds") {
		t.Fatalf("exact scope unexpectedly warned: %q", model.status)
	}
}
func TestTUISendScopeFailureNamesSendCredentialConfig(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model.ctx.acct.Send = &auth.CredentialSource{
		Class:     auth.ClassSend,
		Kind:      auth.SourceCmd,
		Argv0:     "/abs/send-approver",
		ConfigKey: "accounts.work.send_credential_cmd",
	}
	api.sendErr = &gmail.ErrInsufficientScope{
		Account: "work",
		Scope:   "gmail.send",
		Err: &gmail.APIError{
			Status: 403,
			Reason: "insufficientPermissions",
		},
	}
	model = finishConfirmedSend(t, confirmWithBody(t, model, "Scope test"))

	if !model.statusError || !strings.Contains(model.status, "accounts.work.send_credential_cmd") {
		t.Fatalf("send scope status = %q", model.status)
	}
	if len(api.sendCalls) != 1 {
		t.Fatalf("send calls = %d, want 1", len(api.sendCalls))
	}
}

func confirmWithBody(t *testing.T, model app, body string) app {
	t.Helper()
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	if model.thread.thread == nil {
		if len(model.list.rows) == 0 {
			t.Fatal("confirmWithBody requires a replyable thread")
		}
		model.view = threadView
		model.thread = threadModel{thread: model.list.rows[0]}
	}
	model, _ = update(t, model, key(keyReply))
	if model.composeState.draftPath == "" {
		t.Fatalf("reply did not create a draft: status=%q", model.status)
	}
	raw, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model.composeState.draftPath, append(raw, []byte(body)...), 0o600); err != nil {
		t.Fatal(err)
	}
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if model.view != replyConfirmView {
		t.Fatalf("confirmWithBody landed on %v, want replyConfirmView (status %q)", model.view, model.status)
	}
	return model
}

func finishConfirmedSend(t *testing.T, model app) app {
	t.Helper()
	model, _ = sendUntilResult(t, model)
	if model.unlocking {
		t.Fatal("successful send unexpectedly started a retry unlock")
	}
	return model
}

func sendUntilResult(t *testing.T, model app) (app, tea.Cmd) {
	t.Helper()
	model, fence := update(t, model, key(keyConfirmSend))
	model, command := resolveUnlockForSend(t, model, fence)
	return model, command
}

func resolveUnlockForSend(t *testing.T, model app, fence tea.Cmd) (app, tea.Cmd) {
	t.Helper()
	armed := runCmd(t, fence)
	model, acquire := update(t, model, armed)
	model, sendCommand := update(t, model, runCmd(t, acquire))
	return update(t, model, runCmd(t, sendCommand))
}

func replyThread() *gmail.Thread {
	threadID := "thread-reply"
	return &gmail.Thread{
		ID: threadID,
		Messages: []*gmail.Message{
			replyMessage("older-message", threadID, 1, "Older <older@example.test>", "Me <me@example.test>", "", "older subject", "<older@example.test>", ""),
			replyMessage("target-message", threadID, 2, "Sender <sender@example.test>", "Me <me@example.test>, Colleague <colleague@example.test>", "Copied <copied@example.test>", "target subject", "<target@example.test>", ""),
		},
	}
}

func replyMessage(id, threadID string, internalDate int64, from, to, cc, subject, messageID, replyTo string) *gmail.Message {
	headers := []gmail.Header{
		{Name: "From", Value: from},
		{Name: "To", Value: to},
		{Name: "Cc", Value: cc},
		{Name: "Subject", Value: subject},
		{Name: "Message-ID", Value: messageID},
	}
	if replyTo != "" {
		headers = append(headers, gmail.Header{Name: "Reply-To", Value: replyTo})
	}
	return &gmail.Message{
		ID:           id,
		ThreadID:     threadID,
		InternalDate: internalDate,
		Payload:      &gmail.MessagePart{Headers: headers},
	}
}

func TestEscAtConfirmOffersAbandonPromptInsteadOfSilentShred(t *testing.T) {
	thread := replyThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "keep me")
	model, _ = update(t, model, key("esc"))
	if !model.abandonPrompt || model.view != replyConfirmView || model.reply.envelope == nil {
		t.Fatalf("esc must prompt, not discard: prompt=%v view=%v env=%v", model.abandonPrompt, model.view, model.reply.envelope)
	}
	view := model.View()
	for _, want := range []string{"d discard", "s save", "e keep editing"} {
		if !strings.Contains(view, want) {
			t.Fatalf("prompt line missing %q:\n%s", want, view)
		}
	}
}

func TestAbandonPromptEscEnterAndDDiscardWithoutServerWrites(t *testing.T) {
	for _, discard := range []string{"esc", "enter", "d"} {
		thread := replyThread()
		model, api := newTestApp([]*gmail.Thread{thread})
		model.ctx.self = "me@example.test"
		model = confirmWithBody(t, model, "discard me")
		model, _ = update(t, model, key("esc"))
		model, _ = update(t, model, key(discard))
		if model.abandonPrompt || model.view != threadView || model.reply.envelope != nil {
			t.Fatalf("%q must discard back to the thread view", discard)
		}
		if len(api.draftCreates) != 0 || len(api.sendCalls) != 0 {
			t.Fatalf("%q wrote to the server: drafts=%d sends=%d", discard, len(api.draftCreates), len(api.sendCalls))
		}
	}
}

func TestAbandonPromptSavesDraftThroughWriteUnlockFence(t *testing.T) {
	thread := replyThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	unlocked := []auth.Class(nil)
	model.ctx.unlock = func(_ context.Context, class auth.Class) (string, error) {
		unlocked = append(unlocked, class)
		return "", nil
	}
	model = confirmWithBody(t, model, "save me")
	model, _ = update(t, model, key("esc"))
	model, fence := update(t, model, key("s"))
	if !model.unlocking || model.unlockClass != auth.ClassWrite || model.pendingDraft == nil {
		t.Fatalf("s must arm a WRITE unlock with a pending draft: unlocking=%v class=%v", model.unlocking, model.unlockClass)
	}
	if len(api.draftCreates) != 0 {
		t.Fatal("draft created before the unlock fence resolved (attribution-before-spawn violated)")
	}
	model = drainCommands(t, model, fence)
	if len(unlocked) != 1 || unlocked[0] != auth.ClassWrite {
		t.Fatalf("unlock classes = %v, want [write]", unlocked)
	}
	if len(api.draftCreates) != 1 || api.draftCreates[0].threadID != thread.ID {
		t.Fatalf("draft creates = %+v, want one carrying the thread id", api.draftCreates)
	}
	if !strings.Contains(model.status, "draft saved") || model.view != threadView || model.pendingDraft != nil || model.abandonPrompt {
		t.Fatalf("post-save state: status=%q view=%v", model.status, model.view)
	}
}

func TestAbandonPromptEditReopensSeededEditor(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	thread := replyThread()
	model, _ := newTestApp([]*gmail.Thread{thread})
	model.ctx.self = "me@example.test"
	model = confirmWithBody(t, model, "seed body")
	model, _ = update(t, model, key("esc"))
	model, _ = update(t, model, key("e"))
	if model.composeState.draftPath == "" {
		t.Fatal("e must reopen the editor with a fresh scissors draft")
	}
	raw, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	body, found := compose.ParseDraft(raw)
	if !found || body != "seed body" {
		t.Fatalf("reopened draft body = (%q, %v), want the current body below the scissors line", body, found)
	}
}
