package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
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

func TestTabCyclesAccountsInDeclarationOrder(t *testing.T) {
	cfg := testConfigWithAccounts(testAccount("work"), testAccount("personal"), testAccount("receipts"))
	apis := map[string]gmailAPI{
		"work":     &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)},
		"personal": &fakeAPI{attachments: make(map[string][]byte)},
		"receipts": &fakeAPI{attachments: make(map[string][]byte)},
	}
	model := newTestModelWithConfig(cfg, "work", apis["work"])
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	var constructed []string
	newAccountCtx = func(cfg *auth.Config, acct *auth.AccountConfig) (*accountCtx, error) {
		constructed = append(constructed, acct.Name)
		return testAccountCtx(cfg, acct, apis[acct.Name]), nil
	}

	for _, want := range []string{"personal", "receipts", "work"} {
		var command tea.Cmd
		model, command = update(t, model, key("tab"))
		if model.account != want {
			t.Fatalf("active account = %q, want %q", model.account, want)
		}
		if command == nil {
			t.Fatalf("switch to %q returned no initial-listing command", want)
		}
	}
	if !reflect.DeepEqual(constructed, []string{"personal", "receipts"}) {
		t.Fatalf("constructed accounts = %q, want declaration-order targets", constructed)
	}
}

func TestTabSingleAccountIsNoOp(t *testing.T) {
	cfg := testConfigWithAccounts(testAccount("work"))
	api := &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)}
	model := newTestModelWithConfig(cfg, "work", api)
	model.status = "keep this status"
	originalCtx := model.ctx

	model, command := update(t, model, key("tab"))

	if command != nil {
		t.Fatal("single-account Tab returned a command")
	}
	if model.ctx != originalCtx || model.account != "work" {
		t.Fatalf("single-account Tab switched context: account=%q ctx=%p", model.account, model.ctx)
	}
	if model.status != "keep this status" {
		t.Fatalf("single-account Tab changed status to %q", model.status)
	}
}

func TestTabUnderEnvTokenIsNoOpWithNotice(t *testing.T) {
	cfg := testConfigWithAccounts(testAccount("work"), testAccount("personal"))
	api := &fakeAPI{threads: testThreads(1), attachments: make(map[string][]byte)}
	model := newTestModelWithConfig(cfg, "work", api)
	model.ctx.lastRoute = func() auth.Route { return auth.RouteEnvToken }
	originalFactory := newAccountCtx
	t.Cleanup(func() { newAccountCtx = originalFactory })
	factoryCalls := 0
	newAccountCtx = func(*auth.Config, *auth.AccountConfig) (*accountCtx, error) {
		factoryCalls++
		return nil, nil
	}

	model, command := update(t, model, key("tab"))

	if command != nil {
		t.Fatal("pinned Tab returned a command")
	}
	if model.account != "work" {
		t.Fatalf("pinned Tab switched account to %q", model.account)
	}
	if factoryCalls != 0 {
		t.Fatalf("pinned Tab constructed %d account contexts", factoryCalls)
	}
	if model.status != envTokenIdentityNotice {
		t.Fatalf("pinned Tab status = %q, want %q", model.status, envTokenIdentityNotice)
	}
	if !strings.Contains(model.View(), "[pinned]") {
		t.Fatalf("pinned inbox title = %q", model.View())
	}
}
