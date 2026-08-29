package tui

import (
	"testing"

	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestSwitchAccountUsesConfiguredDeclarationOrder(t *testing.T) {
	work := &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)}
	personal := &fakeAPI{threads: []*gmail.Thread{}, attachments: make(map[string][]byte)}
	model := newTestModel(work, "work")
	model = switchToPersonal(t, model, personal)
	if model.account != "personal" || model.ctx.api != personal {
		t.Fatalf("active account = %q, context = %#v", model.account, model.ctx)
	}
}

func TestSwitchAccountDiscardsPriorRequests(t *testing.T) {
	model, api := newTestApp(testThreads(1))
	stale := listThreadsCmd(model.currentRequest(listOperation), "")
	model = switchToPersonal(t, model, &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)})
	message := runCmd(t, stale)
	model, _ = update(t, model, message)
	if model.ctx.api == api && model.account != "personal" {
		t.Fatal("stale request changed the active account")
	}
}
