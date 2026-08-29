package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
)

type unlockRecorder struct {
	calls int
	err   error
	note  string
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
	ctx.unlock = func(context.Context) error {
		recorder.calls++
		if recorder.err == nil {
			ready = true
		}
		return recorder.err
	}
	ctx.takeWriteDiagnostic = func() string { return recorder.note }
	model := newApp(ctx)
	model.list.rows = threads
	return model, api, recorder, &invalidations
}

func TestFirstWriteKeypressUnlocksBeforeActing(t *testing.T) {
	model, api, recorder, invalidations := newUnlockApp(2)
	model, command := update(t, model, key("e"))
	if len(api.modifyCalls) != 0 {
		t.Fatalf("write call ran before unlock: %d", len(api.modifyCalls))
	}
	done := runCmd(t, command)
	if _, ok := done.(unlockDoneMsg); !ok {
		t.Fatalf("first command = %T, want unlockDoneMsg", done)
	}
	model, action := update(t, model, done)
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
	model, unlock := update(t, model, key("e"))
	model, action := update(t, model, runCmd(t, unlock))
	model, retryUnlock := update(t, model, runCmd(t, action))
	model, retryAction := update(t, model, runCmd(t, retryUnlock))
	model, _ = update(t, model, runCmd(t, retryAction))
	if recorder.calls != 2 || *invalidations != 1 || !model.statusError || !strings.Contains(model.status, "write token expired") {
		t.Fatalf("unlocks, invalidations, status = %d, %d, %q", recorder.calls, *invalidations, model.status)
	}
}

func TestUnlockErrorCancelsPendingAction(t *testing.T) {
	model, api, recorder, _ := newUnlockApp(1)
	recorder.err = errors.New("approval denied")
	model, command := update(t, model, key("e"))
	model, _ = update(t, model, runCmd(t, command))
	if recorder.calls != 1 || model.pending != nil || !model.statusError || len(api.modifyCalls) != 0 {
		t.Fatalf("unlock failure state = calls:%d pending:%#v status:%q writes:%v", recorder.calls, model.pending, model.status, api.modifyCalls)
	}
}

func TestUnlockDiagnosticAppearsOnlyAfterCompletion(t *testing.T) {
	model, _, recorder, _ := newUnlockApp(1)
	recorder.note = "approval completes soon"
	model, command := update(t, model, key("e"))
	if strings.Contains(model.status, recorder.note) {
		t.Fatalf("status emitted completion note before unlock: %q", model.status)
	}
	model, _ = update(t, model, runCmd(t, command))
	if !strings.Contains(model.status, recorder.note) {
		t.Fatalf("status omitted completion note: %q", model.status)
	}
}
