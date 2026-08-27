package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestNewerSameAccountListingDiscardsOlderRefreshResult(t *testing.T) {
	oldRows := testThreads(1)
	newRows := testThreads(1)
	oldRows[0].ID = "18d1a0b2c3d4e5f6"
	oldRows[0].Messages[0].ThreadID = oldRows[0].ID
	newRows[0].ID = "18d1a0b2c3d4e5f7"
	newRows[0].Messages[0].ThreadID = newRows[0].ID
	api := &fakeAPI{
		threads: append(oldRows, newRows...),
		listedIDs: map[string][]string{
			"old-search": {oldRows[0].ID},
			"new-search": {newRows[0].ID},
		},
		attachments: make(map[string][]byte),
	}
	model := newTestModel(api, auth.AccountWork)
	model.list.query = "old-search"

	model, olderRefresh := update(t, model, key("R"))
	model, _ = update(t, model, key("/"))
	model, _ = update(t, model, key("new-search"))
	model, newerSearch := update(t, model, key("enter"))
	model, _ = update(t, model, runCmd(t, newerSearch))
	model, _ = update(t, model, runCmd(t, olderRefresh))

	if got, want := threadIDs(model.list.rows), threadIDs(newRows); !reflect.DeepEqual(got, want) {
		t.Fatalf("same-account stale refresh rows = %v, want newer search rows %v", got, want)
	}
	if got, want := model.list.query, "new-search"; got != want {
		t.Fatalf("list query = %q, want %q", got, want)
	}
}

func TestDiscardAsyncRejectsOtherAccountAcrossOperations(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	other := &accountCtx{account: auth.AccountPersonal}
	stale := func(operation asyncOperation) asyncRequest {
		request := model.beginRequest(operation)
		request.ctx = other
		return request
	}
	assertDiscarded := func(message asyncMessage) {
		t.Helper()
		if !model.discardAsync(message) {
			t.Fatalf("discardAsync(%T) = false, want cross-account result discarded", message)
		}
	}

	assertDiscarded(threadsMsg{request: stale(listOperation)})
	assertDiscarded(threadMsg{request: stale(threadOperation)})
	assertDiscarded(previewRequestMsg{request: stale(previewOperation)})
	assertDiscarded(previewThreadMsg{request: stale(previewOperation)})
	assertDiscarded(previewErrMsg{request: stale(previewOperation), err: errors.New("preview failed")})
	assertDiscarded(actionDoneMsg{request: stale(actionOperation)})
	assertDiscarded(labelsMsg{request: stale(labelOperation)})
	assertDiscarded(attachmentSavedMsg{request: stale(attachmentOperation)})
	assertDiscarded(openedMsg{request: stale(openOperation)})
	assertDiscarded(errMsg{request: stale(openOperation), err: errors.New("operation failed")})
}
func TestTabSwitchLazyContext(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	personalAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	factoryCalls := 0
	newAccountCtx = func(account auth.Account) (*accountCtx, error) {
		factoryCalls++
		if account != auth.AccountPersonal {
			t.Fatalf("factory account = %q, want personal", account)
		}
		return &accountCtx{account: account, api: personalAPI, lastRoute: func() auth.Route { return auth.RouteOAuthRefresh }}, nil
	}

	model, cmd := update(t, model, key("tab"))
	if model.account != auth.AccountPersonal || model.ctx != model.contexts[auth.AccountPersonal] {
		t.Fatalf("active context = %#v, want personal", model.ctx)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	runCmd(t, cmd)
	if len(personalAPI.listCalls) != 1 {
		t.Fatalf("personal listing calls = %d, want 1", len(personalAPI.listCalls))
	}
}

func TestTabSwitchAuthFailureStaysPut(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	newAccountCtx = func(auth.Account) (*accountCtx, error) {
		return nil, &auth.NeedsSecretsError{Key: "GWS_PERSONAL_MAIL_OAUTH"}
	}

	model, cmd := update(t, model, key("tab"))
	if model.account != auth.AccountWork {
		t.Fatalf("active account = %q, want work", model.account)
	}
	if !strings.Contains(model.status, "GWS_PERSONAL_MAIL_OAUTH") ||
		!strings.Contains(model.status, "provision:") ||
		!strings.Contains(model.status, "secrets") {
		t.Fatalf("status = %q, want personal credential provisioning error", model.status)
	}
	view := model.View()
	if !strings.Contains(view, "Mailbox — work inbox") || !strings.Contains(view, "Subject 1") || strings.Contains(view, "\nready") {
		t.Fatalf("factory failure silently relabeled or hid the work listing: %q", view)
	}
	if cmd != nil {
		t.Fatal("failed account switch returned a command")
	}
}

func TestTabSwitchListingAuthFailureDoesNotRelabelWorkRows(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	personalAPI := &fakeAPI{
		threads: testThreads(1),
		listErr: &gmail.APIError{Status: 403, Reason: "insufficientPermissions", Message: "scope missing"},
	}
	model := newTestModel(workAPI, auth.AccountWork)
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	newAccountCtx = func(account auth.Account) (*accountCtx, error) {
		return &accountCtx{account: account, api: personalAPI, lastRoute: func() auth.Route { return auth.RouteOAuthRefresh }}, nil
	}

	model, command := update(t, model, key("tab"))
	message := runCmd(t, command)
	model, _ = update(t, model, message)
	if model.account != auth.AccountPersonal || len(model.list.rows) != 0 {
		t.Fatalf("personal auth failure retained work rows: account=%q rows=%v", model.account, threadIDs(model.list.rows))
	}
	if !strings.Contains(model.status, "provision:") || !strings.Contains(model.status, "GWS_PERSONAL_MAIL_OAUTH") {
		t.Fatalf("status = %q, want personal provisioning hint", model.status)
	}
}

func TestStaleListingDoesNotOverwriteActiveAccount(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	personalAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	staleListing := listThreadsCmd(model.currentRequest(listOperation), "")

	model = switchToPersonal(t, model, personalAPI)
	message := runCmd(t, staleListing)
	model, _ = update(t, model, message)
	if model.account != auth.AccountPersonal || len(model.list.rows) != 0 {
		t.Fatalf("stale work listing changed personal model: account=%q rows=%v", model.account, threadIDs(model.list.rows))
	}
}

func TestStaleThreadDoesNotOpenInActiveAccount(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	personalAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	staleThread := getThreadCmd(model.currentRequest(threadOperation), workAPI.threads[0].ID)

	model = switchToPersonal(t, model, personalAPI)
	message := runCmd(t, staleThread)
	model, _ = update(t, model, message)
	if model.account != auth.AccountPersonal || model.view != listView || model.thread.thread != nil {
		t.Fatalf("stale work thread changed personal model: account=%q view=%v thread=%#v", model.account, model.view, model.thread.thread)
	}
}

func TestStaleErrorDoesNotClearActiveAccountOrUseItsRoute(t *testing.T) {
	workAPI := &fakeAPI{
		threads: testThreads(1),
		getErr:  &gmail.APIError{Status: 403, Reason: "insufficientPermissions", Message: "scope missing"},
	}
	personalAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	staleFailure := getThreadCmd(model.currentRequest(threadOperation), workAPI.threads[0].ID)

	model = switchToPersonal(t, model, personalAPI)
	message := runCmd(t, staleFailure)
	model, _ = update(t, model, message)
	if model.account != auth.AccountPersonal || !model.loading || model.status != "" {
		t.Fatalf("stale work error changed personal model: account=%q loading=%v status=%q", model.account, model.loading, model.status)
	}
}

func TestStaleThreadDoesNotOpenAfterReturningToItsAccount(t *testing.T) {
	workAPI := &fakeAPI{threads: testThreads(1)}
	personalAPI := &fakeAPI{threads: testThreads(1)}
	model := newTestModel(workAPI, auth.AccountWork)
	staleThread := getThreadCmd(model.currentRequest(threadOperation), workAPI.threads[0].ID)

	model = switchToPersonal(t, model, personalAPI)
	model, switchBack := update(t, model, key("tab"))
	if switchBack == nil {
		t.Fatal("switching back to work returned no listing command")
	}
	message := runCmd(t, staleThread)
	model, _ = update(t, model, message)

	if model.account != auth.AccountWork || model.view != listView || model.thread.thread != nil || !model.loading {
		t.Fatalf("stale work thread changed round-tripped model: account=%q view=%v thread=%#v loading=%v", model.account, model.view, model.thread.thread, model.loading)
	}
}
