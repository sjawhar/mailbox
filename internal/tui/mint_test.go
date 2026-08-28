package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
)

type mintRecorder struct {
	mu    sync.Mutex
	calls int
	note  string
	err   error
}

func (m *mintRecorder) mint(_ context.Context, stderr io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.note != "" {
		io.WriteString(stderr, m.note)
	}
	return m.err
}

func newMintApp(t *testing.T, rows []*gmail.Thread) (app, *fakeAPI, *mintRecorder, *int) {
	t.Helper()
	api := &fakeAPI{threads: rows, attachments: make(map[string][]byte)}
	recorder := &mintRecorder{}
	invalidations := 0
	ctx := &accountCtx{
		account:            auth.AccountWork,
		api:                api,
		lastRoute:          func() auth.Route { return auth.RouteBroker },
		mutationRoute:      func() auth.Route { return auth.RouteMint },
		invalidateMutation: func() { invalidations++ },
		mint:               recorder.mint,
	}
	model := newApp(ctx)
	model.list.rows = append([]*gmail.Thread(nil), rows...)
	return model, api, recorder, &invalidations
}

// TestFirstMutationKeypressMintsThenActs catches a missing single retry after
// a keypress-initiated mint when the mutation credential slot is empty.
func TestFirstMutationKeypressMintsThenActs(t *testing.T) {
	rows := testThreads(2)
	model, api, recorder, invalidations := newMintApp(t, rows)
	api.modifyErrs = []error{auth.ErrExpiredToken}

	model, cmd := update(t, model, key("e"))
	failed := runCmd(t, cmd)
	model, mint := update(t, model, failed)
	if want := "unlocking work mutations (GWS_WORK_MODIFY_OAUTH) — touch your YubiKey if it blinks"; model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
	if !model.minting || model.pending == nil {
		t.Fatalf("minting = %v, pending = %#v, want buffered action while minting", model.minting, model.pending)
	}

	done := runCmd(t, mint)
	model, retry := update(t, model, done)
	if recorder.calls != 1 || *invalidations != 1 {
		t.Fatalf("mints = %d, invalidations = %d, want 1 and 1", recorder.calls, *invalidations)
	}
	action := runCmd(t, retry)
	model, _ = update(t, model, action)
	if len(api.modifyCalls) != 2 {
		t.Fatalf("ModifyThreads calls = %d, want 2 (initial + single retry)", len(api.modifyCalls))
	}
	if !strings.Contains(model.status, "archive completed") {
		t.Fatalf("status = %q, want completed action", model.status)
	}
}

// TestSecondExpiryAfterRemintSurfaces catches a second automatic mint attempt
// after the sole permitted retry reports an expired mutation credential again.
func TestSecondExpiryAfterRemintSurfaces(t *testing.T) {
	rows := testThreads(1)
	model, api, recorder, _ := newMintApp(t, rows)
	api.modifyErr = auth.ErrExpiredToken

	model, cmd := update(t, model, key("e"))
	model, mint := update(t, model, runCmd(t, cmd))
	model, retry := update(t, model, runCmd(t, mint))
	model, _ = update(t, model, runCmd(t, retry))

	if recorder.calls != 1 {
		t.Fatalf("mints = %d, want exactly 1", recorder.calls)
	}
	if model.pending != nil || model.minting {
		t.Fatalf("pending = %#v, minting = %v, want cleared", model.pending, model.minting)
	}
	if !model.statusError {
		t.Fatalf("status = %q, want surfaced error", model.status)
	}
}

// TestMintFailureDropsBufferedAction catches an action replayed after its
// keypress-initiated mutation mint failed.
func TestMintFailureDropsBufferedAction(t *testing.T) {
	rows := testThreads(1)
	model, api, recorder, _ := newMintApp(t, rows)
	api.modifyErrs = []error{auth.ErrExpiredToken}
	recorder.err = errors.New("secrets: request DENIED")
	recorder.note = "REQUEST sent to daemon\n"

	model, cmd := update(t, model, key("e"))
	model, mint := update(t, model, runCmd(t, cmd))
	model, after := update(t, model, runCmd(t, mint))
	if after != nil {
		t.Fatal("mint failure still dispatched the buffered action")
	}
	if model.pending != nil {
		t.Fatalf("pending = %#v, want dropped", model.pending)
	}
	for _, want := range []string{"DENIED", "provision GWS_WORK_MODIFY_OAUTH per README (human tier)"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("status = %q, want %q", model.status, want)
		}
	}
	if len(api.modifyCalls) != 1 {
		t.Fatalf("ModifyThreads calls = %d, want 1 (no retry after failed mint)", len(api.modifyCalls))
	}
}

// TestKeypressDuringMintIsDroppedWithFeedback catches a second mutation
// action replacing the single buffered action while minting is in flight.
func TestKeypressDuringMintIsDroppedWithFeedback(t *testing.T) {
	rows := testThreads(2)
	model, api, _, _ := newMintApp(t, rows)
	api.modifyErrs = []error{auth.ErrExpiredToken}

	model, cmd := update(t, model, key("e"))
	model, _ = update(t, model, runCmd(t, cmd))
	buffered := model.pending
	model, second := update(t, model, key("d"))
	if second != nil {
		t.Fatal("keypress during mint dispatched a command")
	}
	if model.status != "waiting for unlock…" {
		t.Fatalf("status = %q, want waiting feedback", model.status)
	}
	if model.pending != buffered {
		t.Fatalf("pending changed: %#v", model.pending)
	}
	if len(api.trashCalls) != 0 {
		t.Fatal("dropped keypress reached the API")
	}
}

// TestAccountSwitchDuringMintKeepsMintAttribution catches an account switch
// clearing the status that attributes an in-flight secrets request.
func TestAccountSwitchDuringMintKeepsMintAttribution(t *testing.T) {
	rows := testThreads(1)
	model, api, recorder, _ := newMintApp(t, rows)
	api.modifyErrs = []error{auth.ErrExpiredToken}

	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	newAccountCtx = func(account auth.Account) (*accountCtx, error) {
		if account != auth.AccountPersonal {
			t.Fatalf("factory account = %q, want personal", account)
		}
		return &accountCtx{
			account:            account,
			api:                &fakeAPI{threads: testThreads(1)},
			lastRoute:          func() auth.Route { return auth.RouteBroker },
			mutationRoute:      func() auth.Route { return auth.RouteMint },
			invalidateMutation: func() {},
			mint:               func(context.Context, io.Writer) error { return nil },
		}, nil
	}

	model, action := update(t, model, key("e"))
	model, mint := update(t, model, runCmd(t, action))
	unlockStatus := model.status
	model, switchCmd := update(t, model, key("tab"))
	if switchCmd != nil {
		t.Fatal("account switch started while a mutation mint was in flight")
	}
	if model.account != auth.AccountWork {
		t.Fatalf("account = %q, want work while minting", model.account)
	}
	if model.status != unlockStatus+" · waiting for unlock…" {
		t.Fatalf("status = %q, want preserved unlock attribution with waiting feedback", model.status)
	}

	model, retry := update(t, model, runCmd(t, mint))
	if recorder.calls != 1 {
		t.Fatalf("mints = %d, want exactly one", recorder.calls)
	}
	if model.minting {
		t.Fatal("minting remains true after the mint command resolved")
	}
	model, switchCmd = update(t, model, key("tab"))
	if switchCmd == nil || model.account != auth.AccountPersonal {
		t.Fatalf("switch after mint command: cmd=%v account=%q, want personal listing command", switchCmd, model.account)
	}
	_ = retry
}

// TestMintSuccessNoteIsSurfaced catches loss of the child stderr note after a
// successful keypress-initiated mint.
func TestMintSuccessNoteIsSurfaced(t *testing.T) {
	rows := testThreads(1)
	model, api, recorder, _ := newMintApp(t, rows)
	api.modifyErrs = []error{auth.ErrExpiredToken}
	recorder.note = "approval granted via session scope\n"

	model, cmd := update(t, model, key("e"))
	model, mint := update(t, model, runCmd(t, cmd))
	model, _ = update(t, model, runCmd(t, mint))
	for _, want := range []string{"unlocked work mutations", "approval granted via session scope"} {
		if !strings.Contains(model.status, want) {
			t.Fatalf("status = %q, want %q", model.status, want)
		}
	}
}

// TestMutationScopeErrorUsesMutationRoute catches a mutation scope error
// rendered with the read credential route rather than the mutation route.
func TestMutationScopeErrorUsesMutationRoute(t *testing.T) {
	model, api, _, _ := newMintApp(t, testThreads(1))
	api.modifyErr = &gmail.ErrInsufficientScope{
		Account: "work",
		Scope:   "gmail.modify",
		Err: &gmail.APIError{
			Status:  403,
			Reason:  "insufficientPermissions",
			Message: "scope missing",
		},
	}

	model, command := update(t, model, key("e"))
	model, _ = update(t, model, runCmd(t, command))
	if !strings.Contains(model.status, "re-run the provisioning ceremony") {
		t.Fatalf("status = %q, want mutation-route provisioning hint", model.status)
	}
	if strings.Contains(model.status, "broker token is read-only") {
		t.Fatalf("status = %q, must not use the read credential route", model.status)
	}
}
