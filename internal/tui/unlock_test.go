package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
)

type unlockRecorder struct {
	calls   int
	classes []auth.Class
	err     error
	note    string
}

func TestUnlockForceAbandonKillsProcessGroup(t *testing.T) {
	directory := t.TempDir()
	parentPath := filepath.Join(directory, "parent")
	childPath := filepath.Join(directory, "child")
	script := filepath.Join(directory, "hang")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$1\"\n(sleep 30) &\nprintf '%s' \"$!\" > \"$2\"\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_TOKEN", "")
	acct := testAccount("work")
	acct.Write = &auth.CredentialSource{
		Class:       auth.ClassWrite,
		Kind:        auth.SourceCmd,
		Argv:        []string{script, parentPath, childPath},
		Argv0:       script,
		Interactive: true,
		ConfigKey:   "accounts.work.write_credential_cmd",
	}
	cfg := testConfigWithAccounts(acct)
	ctx, err := newAccountCtx(cfg, acct)
	if err != nil {
		t.Fatal(err)
	}
	ctx.api = &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)}
	model := newApp(ctx)
	model.list.rows = testThreads(1)
	model.listLoaded = true
	model, fence := update(t, model, key("e"))
	armed := runCmd(t, fence)
	model, acquire := update(t, model, armed)
	batch, ok := acquire().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("unlock command = %T, want tea.BatchMsg", acquire())
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[0]() }()
	parent := waitForPID(t, parentPath)
	child := waitForPID(t, childPath)
	defer syscall.Kill(-parent, syscall.SIGKILL)

	model, _ = update(t, model, key("q"))
	model, quit := update(t, model, key("q"))
	if quit == nil {
		t.Fatal("second quit did not abandon unlock")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("unlock command did not observe the cancel context")
	}
	for _, pid := range []int{parent, child} {
		waitForStoppedProcess(t, pid)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(value)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("credential helper did not write PID at %s", path)
	return 0
}

func waitForStoppedProcess(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		stat, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if readErr == nil && strings.Contains(string(stat), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("credential process %d survived abandon", pid)
}

func newUnlockApp(rows int) (app, *fakeAPI, *unlockRecorder, *int) {
	threads := testThreads(rows)
	api := &fakeAPI{threads: threads, attachments: make(map[string][]byte)}
	cfg := testConfig()
	acct, _ := cfg.Account("work")
	ctx := testAccountCtx(cfg, acct, api)
	recorder := &unlockRecorder{}
	invalidations := 0
	ready := false
	ctx.writeReady = func() bool { return ready }
	ctx.invalidateWrite = func() { invalidations++ }
	ctx.unlock = func(_ context.Context, class auth.Class) (string, error) {
		recorder.calls++
		recorder.classes = append(recorder.classes, class)
		if recorder.err == nil && class == auth.ClassWrite {
			ready = true
		}
		return recorder.note, recorder.err
	}
	ctx.takeDiagnostic = func(auth.Class) string { return "" }
	model := newApp(ctx)
	model.list.rows = threads
	model.listLoaded = true
	return model, api, recorder, &invalidations
}

func resolveUnlock(t *testing.T, model app, fence tea.Cmd) (app, tea.Cmd) {
	t.Helper()
	armed := runCmd(t, fence)
	model, acquire := update(t, model, armed)
	done := runCmd(t, acquire)
	return update(t, model, done)
}

func TestThreadCompletionDuringUnlockIsDiscarded(t *testing.T) {
	model, api, _, _ := newUnlockApp(1)
	model.loading = true
	model.unlocking = true
	model.unlockClass = auth.ClassWrite
	model.status = "waiting for credential approval"
	request := model.currentRequest(threadOperation)

	model, command := update(t, model, threadMsg{request: request, thread: api.threads[0]})

	if command != nil {
		t.Fatalf("thread completion returned command %v", command)
	}
	if !model.unlocking || !model.loading {
		t.Fatalf("thread completion changed unlock spinner state: unlocking=%t loading=%t", model.unlocking, model.loading)
	}
	if model.view != listView {
		t.Fatalf("thread completion changed view during unlock: %v", model.view)
	}
	if model.status != "waiting for credential approval" {
		t.Fatalf("thread completion changed unlock status: %q", model.status)
	}
	if model.thread.thread != nil {
		t.Fatalf("thread completion changed the hidden action target: %#v", model.thread.thread)
	}
}

func TestThreadCompletionDuringFailedUnlockCannotRetargetArchive(t *testing.T) {
	model, api, recorder, _ := newUnlockApp(2)
	model.view = threadView
	model, _ = update(t, model, threadMsg{
		request: model.currentRequest(threadOperation),
		thread:  api.threads[0],
	})

	model, fetch := update(t, model, key(keyNext))
	if fetch == nil {
		t.Fatal("next thread did not start a fetch")
	}
	fetchRequest := model.currentRequest(threadOperation)
	recorder.err = errors.New("approval denied")
	model, fence := update(t, model, key(keyArchive))
	if !model.unlocking {
		t.Fatal("archive did not start a write unlock")
	}
	model, _ = update(t, model, threadMsg{request: fetchRequest, thread: api.threads[1]})
	model, _ = resolveUnlock(t, model, fence)

	if model.pending != nil {
		t.Fatalf("failed unlock left pending action %#v", model.pending)
	}
	recorder.err = nil
	model, retryFence := update(t, model, key(keyArchive))
	model, action := resolveUnlock(t, model, retryFence)
	model, _ = update(t, model, runCmd(t, action))

	if len(api.modifyCalls) != 1 {
		t.Fatalf("archive calls = %#v, want one", api.modifyCalls)
	}
	if ids := api.modifyCalls[0].ids; len(ids) != 1 || ids[0] != api.threads[0].ID {
		t.Fatalf("archive targeted %v, want rendered thread %q", ids, api.threads[0].ID)
	}
}

func TestFirstWriteKeypressUnlocksBeforeActing(t *testing.T) {
	model, api, recorder, invalidations := newUnlockApp(2)
	model, fence := update(t, model, key("e"))
	if len(api.modifyCalls) != 0 {
		t.Fatalf("write call ran before unlock: %d", len(api.modifyCalls))
	}
	model, action := resolveUnlock(t, model, fence)
	if recorder.calls != 1 || *invalidations != 0 {
		t.Fatalf("unlocks, invalidations = %d, %d", recorder.calls, *invalidations)
	}
	model, _ = update(t, model, runCmd(t, action))
	if len(api.modifyCalls) != 1 || !strings.Contains(model.status, "archive completed") {
		t.Fatalf("writes, status = %v, %q", api.modifyCalls, model.status)
	}
}

func TestSecondExpiryAfterOneRetrySurfacesError(t *testing.T) {
	model, api, recorder, invalidations := newUnlockApp(1)
	api.modifyErr = auth.ErrExpiredToken
	model, fence := update(t, model, key("e"))
	model, action := resolveUnlock(t, model, fence)
	model, retryFence := update(t, model, runCmd(t, action))
	model, retryAction := resolveUnlock(t, model, retryFence)
	model, _ = update(t, model, runCmd(t, retryAction))
	if recorder.calls != 2 || *invalidations != 1 || !model.statusError || !strings.Contains(model.status, "write token expired") {
		t.Fatalf("unlocks, invalidations, status = %d, %d, %q", recorder.calls, *invalidations, model.status)
	}
}

func TestUnlockErrorCancelsPendingAction(t *testing.T) {
	model, api, recorder, _ := newUnlockApp(1)
	recorder.err = errors.New("approval denied")
	model, fence := update(t, model, key("e"))
	model, _ = resolveUnlock(t, model, fence)
	if recorder.calls != 1 || model.pending != nil || !model.statusError || len(api.modifyCalls) != 0 {
		t.Fatalf("unlock failure state = calls:%d pending:%#v status:%q writes:%v", recorder.calls, model.pending, model.status, api.modifyCalls)
	}
}

func TestUnlockDiagnosticAppearsOnlyAfterCompletion(t *testing.T) {
	model, _, recorder, _ := newUnlockApp(1)
	recorder.note = "approval completes soon"
	model, fence := update(t, model, key("e"))
	if strings.Contains(model.status, recorder.note) {
		t.Fatalf("status emitted completion note before unlock: %q", model.status)
	}
	model, _ = resolveUnlock(t, model, fence)
	if !strings.Contains(model.status, recorder.note) {
		t.Fatalf("status omitted completion note: %q", model.status)
	}
}

func TestUnlockStatusUsesLabelAndArgv0(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver", "--scope", "gmail.modify"},
		Argv0:     "/abs/approver",
		Label:     "Acme approval",
		ConfigKey: "accounts.work.write_credential_cmd",
	}

	model, command := update(t, model, key("e"))

	const want = "waiting for Acme approval; approve only this request — work write access via /abs/approver"
	if model.status != want {
		t.Fatalf("unlock status = %q, want %q", model.status, want)
	}
	if strings.Contains(model.status, "--scope") || strings.Contains(model.status, "gmail.modify") {
		t.Fatalf("unlock status exposed command arguments: %q", model.status)
	}
	if command == nil {
		t.Fatal("write key returned no unlock fence")
	}
}

func TestUnlockStatusDefaultsLabelToArgv0(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver", "--scope", "gmail.modify"},
		Argv0:     "/abs/approver",
		ConfigKey: "accounts.work.write_credential_cmd",
	}

	model, _ = update(t, model, key("e"))

	const want = "waiting for /abs/approver; approve only this request — work write access via /abs/approver"
	if model.status != want {
		t.Fatalf("unlock status = %q, want %q", model.status, want)
	}
}

func TestUnlockStatusRendersBeforeAcquisition(t *testing.T) {
	model, _, recorder, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver"},
		Argv0:     "/abs/approver",
		ConfigKey: "accounts.work.write_credential_cmd",
	}

	model, fence := update(t, model, key("e"))
	if recorder.calls != 0 {
		t.Fatalf("unlock acquired before its status frame: %d calls", recorder.calls)
	}
	const want = "waiting for /abs/approver; approve only this request — work write access via /abs/approver"
	if model.status != want || !strings.Contains(model.View(), want) {
		t.Fatalf("status frame = status:%q view:%q", model.status, model.View())
	}

	armed := runCmd(t, fence)
	if recorder.calls != 0 {
		t.Fatalf("unlock acquired before the armed message was processed: %d calls", recorder.calls)
	}
	model, acquire := update(t, model, armed)
	_ = runCmd(t, acquire)
	if recorder.calls != 1 {
		t.Fatalf("unlock acquisitions = %d, want 1 after the armed message", recorder.calls)
	}
}

func TestUnlockDeflectsThenForceAbandons(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model, _ = update(t, model, key("e"))
	cancellations := 0
	model.unlockCancel = func() { cancellations++ }

	model, first := update(t, model, key("q"))
	if first != nil {
		t.Fatal("first quit press abandoned the unlock")
	}
	if !strings.Contains(model.status, "waiting for unlock… (press again to abandon)") {
		t.Fatalf("first quit status = %q", model.status)
	}

	model, second := update(t, model, key("q"))
	if second == nil {
		t.Fatal("second quit press did not quit")
	}
	quitMsg := runCmd(t, second)
	if _, ok := quitMsg.(tea.QuitMsg); !ok {
		t.Fatalf("second quit command = %T, want tea.QuitMsg", quitMsg)
	}
	if cancellations != 1 {
		t.Fatalf("unlock cancel calls = %d, want 1", cancellations)
	}
}

func TestUnlockTimeoutReleasesProtectedStatus(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver"},
		Argv0:     "/abs/approver",
		ConfigKey: "accounts.work.write_credential_cmd",
	}
	model, fence := update(t, model, key("e"))
	armed := runCmd(t, fence).(unlockArmedMsg)
	model, _ = update(t, model, armed)

	model, _ = update(t, model, unlockDoneMsg{
		request: armed.request,
		class:   auth.ClassWrite,
		err:     auth.ErrCredentialTimeout,
	})

	if model.unlocking {
		t.Fatal("credential timeout left the unlock protected")
	}
	if !model.statusError ||
		!strings.Contains(model.status, "credential command timed out") ||
		!strings.Contains(model.status, "accounts.work.write_credential_cmd") ||
		!strings.Contains(model.status, model.cfg.Path) {
		t.Fatalf("timeout status = %q", model.status)
	}
	_, quit := update(t, model, key("q"))
	if quit == nil {
		t.Fatal("quit remained deflected after the timeout")
	}
}

func TestUnlockFailureNamesConfigKey(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver"},
		Argv0:     "/abs/approver",
		ConfigKey: "accounts.work.write_credential_cmd",
	}
	model, fence := update(t, model, key("e"))
	armed := runCmd(t, fence).(unlockArmedMsg)
	model, _ = update(t, model, armed)

	model, _ = update(t, model, unlockDoneMsg{
		request: armed.request,
		class:   auth.ClassWrite,
		err:     errors.New("approval denied"),
	})

	if !strings.Contains(model.status, "accounts.work.write_credential_cmd") ||
		!strings.Contains(model.status, model.cfg.Path) {
		t.Fatalf("unlock failure status = %q", model.status)
	}
	for _, forbidden := range []string{"GWS", "secrets", "YubiKey"} {
		if strings.Contains(model.status, forbidden) {
			t.Fatalf("unlock failure leaked %q: %q", forbidden, model.status)
		}
	}
}

func TestUnlockStatusSanitizesLabelAndArgv0(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver"},
		Argv0:     "/abs/\x1b]52;c;argv\aapprover",
		Label:     "Acme \x1b]52;c;label\aapproval",
		ConfigKey: "accounts.work.write_credential_cmd",
	}

	model, _ = update(t, model, key("e"))

	if strings.Contains(model.status, "\x1b") || strings.Contains(model.View(), "\x1b") {
		t.Fatalf("unlock status rendered an escape sequence: status=%q view=%q", model.status, model.View())
	}
	if !strings.Contains(model.status, "Acme") {
		t.Fatalf("unlock status lost sanitized label: %q", model.status)
	}
}

func TestUnlockStatusUsesConfigKeyForEnvSource(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceEnv,
		EnvVar:    "ACME_SECRET_TOKEN",
		ConfigKey: "accounts.work.write_credential_env",
	}

	model, _ = update(t, model, key("e"))

	const want = "refreshing work write access (accounts.work.write_credential_env)"
	if model.status != want {
		t.Fatalf("env unlock status = %q, want %q", model.status, want)
	}
	if strings.Contains(model.status, "ACME_SECRET_TOKEN") {
		t.Fatalf("env unlock status leaked a variable name: %q", model.status)
	}
}

func TestReadDiagnosticReachesStatusOnThreadPreviewAndAttachmentPaths(t *testing.T) {
	nextDiagnostic := func(value string, model *app) {
		model.ctx.takeDiagnostic = func(auth.Class) string {
			model.ctx.takeDiagnostic = func(auth.Class) string { return "" }
			return value
		}
	}

	model, _ := newTestApp(testThreads(1))
	nextDiagnostic("grant expires soon", &model)
	threadRequest := model.beginRequest(threadOperation)
	model, _ = update(t, model, threadMsg{request: threadRequest, thread: model.list.rows[0]})
	if !strings.Contains(model.status, "grant expires soon") {
		t.Fatalf("thread diagnostic status = %q", model.status)
	}

	model, _ = newTestApp(testThreads(1))
	model.setSize(160, 45)
	model.preview.requestedID = model.list.rows[0].ID
	nextDiagnostic("grant expires soon", &model)
	previewRequest := model.beginRequest(previewOperation)
	model, _ = update(t, model, previewThreadMsg{
		request:  previewRequest,
		threadID: model.list.rows[0].ID,
		thread:   model.list.rows[0],
	})
	if !strings.Contains(model.status, "grant expires soon") {
		t.Fatalf("preview diagnostic status = %q", model.status)
	}

	model, _ = newTestApp(testThreads(1))
	nextDiagnostic("grant expires soon", &model)
	attachmentRequest := model.beginRequest(attachmentOperation)
	model, _ = update(t, model, attachmentSavedMsg{request: attachmentRequest, path: "/tmp/report.pdf"})
	if !strings.Contains(model.status, "grant expires soon") {
		t.Fatalf("attachment diagnostic status = %q", model.status)
	}
}

func TestUnlockFailureSanitizesConfigPath(t *testing.T) {
	model, _, _, _ := newUnlockApp(1)
	model.cfg.Path = "/tmp/\x1b]52;c;path\aconfig.toml"
	model.ctx.acct.Write = &auth.CredentialSource{
		Class:     auth.ClassWrite,
		Kind:      auth.SourceCmd,
		Argv:      []string{"/abs/approver"},
		Argv0:     "/abs/approver",
		ConfigKey: "accounts.work.write_credential_cmd",
	}
	model, fence := update(t, model, key("e"))
	armed := runCmd(t, fence).(unlockArmedMsg)
	model, _ = update(t, model, armed)
	model, _ = update(t, model, unlockDoneMsg{
		request: armed.request,
		class:   auth.ClassWrite,
		err:     errors.New("approval denied"),
	})

	if strings.Contains(model.status, "\x1b") || strings.Contains(model.View(), "\x1b") {
		t.Fatalf("unlock failure rendered an escape sequence: status=%q view=%q", model.status, model.View())
	}
}
