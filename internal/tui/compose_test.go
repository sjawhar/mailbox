package tui

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/compose"
	"github.com/sjawhar/mailbox/internal/send"
)

func TestReplyKeyCreatesDraftAndSuspends(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	model := newThreadModel(t, replyThread())
	model.ctx.self = "me@example.test"

	model, cmd := press(t, model, keyReply)
	if cmd == nil || model.composeState.draftPath == "" {
		t.Fatalf("r must create a draft and return the exec command, state=%#v", model.composeState)
	}
	content, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), compose.ScissorsLine) || !strings.Contains(string(content), "subject:") {
		t.Fatalf("draft = %q, want envelope block above scissors", content)
	}
}

func TestComposePromptsToAndSubjectBeforeEditor(t *testing.T) {
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	model, _ := newTestApp(testThreads(1))
	model.ctx.self = "me@example.test"

	model, cmd := press(t, model, keyCompose)
	if model.view != composeToView || !model.composeTo.Focused() {
		t.Fatalf("c must start a focused To prompt, view=%v command=%v", model.view, cmd)
	}
	model.composeTo.SetValue("first@example.test, second@example.test")
	model, cmd = press(t, model, "enter")
	if model.view != composeSubjectView || !model.composeSubject.Focused() {
		t.Fatalf("To submit must start a focused Subject prompt, view=%v command=%v", model.view, cmd)
	}
	model.composeSubject.SetValue("new message")
	model, cmd = press(t, model, "enter")
	if cmd == nil || model.composeState.draftPath == "" {
		t.Fatalf("subject submit must create a draft and return the exec command, state=%#v", model.composeState)
	}
	content, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first@example.test", "second@example.test", "subject: new message", compose.ScissorsLine} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("draft missing %q:\n%s", want, content)
		}
	}
}

func TestInvalidEditorDoesNotCreateDraft(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MAILBOX_CACHE_DIR", cacheDir)
	t.Setenv("VISUAL", "'")
	model := newThreadModel(t, replyThread())
	model.ctx.self = "me@example.test"

	model, cmd := press(t, model, keyReply)
	if cmd != nil || model.composeState.draftPath != "" || !model.statusError {
		t.Fatalf("invalid editor must fail before creating a draft, command=%v state=%#v status=%q", cmd, model.composeState, model.status)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid editor created draft data: %v", entries)
	}
}

func TestEditorNonzeroExitAbandons(t *testing.T) {
	model := modelMidCompose(t, "edited body\n")
	dir := model.composeState.draftDir
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation), err: errors.New("exit 1")})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("draft directory must be removed on editor error")
	}
	if model.view == replyConfirmView || model.pendingSend != nil {
		t.Fatal("nonzero editor exit must send nothing")
	}
}

func TestEditorSuccessLandsOnConfirm(t *testing.T) {
	model := modelMidCompose(t, "hello from the editor\n")
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if model.view != replyConfirmView || model.reply.envelope == nil {
		t.Fatalf("save-and-quit must land on the confirm screen, view=%v", model.view)
	}
	if _, err := os.Stat(model.composeState.draftDir); !os.IsNotExist(err) {
		t.Fatal("draft directory must be removed once the body is captured")
	}
}

func TestMissingSentinelRefusesAndRemovesDraft(t *testing.T) { // [G9]
	model := modelMidCompose(t, "irrelevant\n")
	dir := model.composeState.draftDir
	raw, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(string(raw), compose.ScissorsLine+"\n", "")
	if err := os.WriteFile(model.composeState.draftPath, []byte(stripped), 0o600); err != nil {
		t.Fatal(err)
	}
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if !model.statusError || !strings.Contains(model.status, "scissors") {
		t.Fatalf("missing sentinel must refuse, status=%q", model.status)
	}
	if model.view == replyConfirmView || model.pendingSend != nil {
		t.Fatal("refusal must send nothing")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("draft directory must be removed on the missing-sentinel refusal path")
	}
}

func TestEmptyBodyIsR5AndRemovesDraft(t *testing.T) { // [G9]
	model := modelMidCompose(t, "   \n\t\n")
	dir := model.composeState.draftDir
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if !model.statusError || !strings.Contains(model.status, "R5") {
		t.Fatalf("empty body must surface R5, status=%q", model.status)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("draft directory must be removed on the R5 refusal path")
	}
}

func TestEditedEnvelopeBlockChangesNothing(t *testing.T) { // [G10]
	model := modelMidCompose(t, "body stays\n")
	original := *model.reply.envelope
	raw, err := os.ReadFile(model.composeState.draftPath)
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(string(raw), compose.ScissorsLine+"\n")
	if !found {
		t.Fatal("fixture draft lost its sentinel")
	}
	tampered := "To: attacker@example.test (From)\nCc: mole@example.test (CC)\nSubject: tampered\nmessage: m-tampered\n\n" +
		compose.ScissorsLine + "\n" + after
	if err := os.WriteFile(model.composeState.draftPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if model.view != replyConfirmView {
		t.Fatalf("view = %v, want confirm", model.view)
	}
	got := model.reply.envelope
	if !slices.Equal(recipientAddresses(got.To), recipientAddresses(original.To)) ||
		got.Subject != original.Subject || got.TargetMessageID != original.TargetMessageID {
		t.Fatalf("envelope changed via draft edits: got %+v, want %+v", got, original)
	}
	mime, err := send.BuildMIME(got, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mime, []byte("attacker@example.test")) || bytes.Contains(mime, []byte("tampered")) {
		t.Fatalf("tampered envelope text reached the wire:\n%s", mime)
	}
}

func TestEditorGuardsBlockDuringUnlockPendingAndSend(t *testing.T) {
	for _, arm := range []func(*app){
		func(m *app) { m.unlocking = true },
		func(m *app) { m.pending = &pendingAction{action: "archive"} },
		func(m *app) { m.pendingSend = &pendingSend{} },
		func(m *app) { m.pendingDraft = &pendingDraft{} },
	} {
		model := newThreadModel(t, replyThread())
		model.ctx.self = "me@example.test"
		arm(&model)
		model, cmd := press(t, model, keyReply)
		if cmd != nil || model.composeState.draftPath != "" {
			t.Fatal("editor must never start while an unlock, pending action, or pending send exists")
		}
	}
}

func modelMidCompose(t *testing.T, body string) app {
	t.Helper()
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("VISUAL", "true")
	model := newThreadModel(t, replyThread())
	model.ctx.self = "me@example.test"
	model, _ = press(t, model, keyReply)
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
	return model
}

func recipientAddresses(recipients []send.Recipient) []string {
	addresses := make([]string, len(recipients))
	for index, recipient := range recipients {
		addresses[index] = recipient.Address
	}
	return addresses
}

func TestEmptyDraftsStillDiscardSilently(t *testing.T) {
	model := modelMidCompose(t, "   \n\t\n")
	model, _ = update(t, model, editorDoneMsg{request: model.currentRequest(composeOperation)})
	if model.view == replyConfirmView || model.abandonPrompt {
		t.Fatalf("empty body reached the confirm/prompt surface: view=%v prompt=%v", model.view, model.abandonPrompt)
	}
}
