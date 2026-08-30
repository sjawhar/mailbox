package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/filter"
	"github.com/sjawhar/mailbox/internal/gmail"
)

func TestFCyclesFiltersInDeclarationOrderAndRefetches(t *testing.T) {
	cfg := cfgWithFilters(t, "zeta", "alpha")
	model := newFilteredListModel(t, cfg, []*gmail.Thread{
		filterThread("t1", "Zeta <zeta@example.test>"),
		filterThread("t2", "Alpha <alpha@example.test>"),
	})

	model, cmd := press(t, model, "f")
	if model.filterIndex != 1 || cmd == nil {
		t.Fatalf("first f = index %d, command=%v; want 1 (zeta) with a refetch command", model.filterIndex, cmd)
	}
	if got := threadIDs(runListCmd(t, cmd)); !slices.Equal(got, []string{"t1"}) {
		t.Fatalf("zeta view = %v, want [t1]", got)
	}

	model, _ = press(t, model, "f")
	if model.filterIndex != 2 {
		t.Fatalf("second f = index %d, want 2 (alpha)", model.filterIndex)
	}
	model, _ = press(t, model, "f")
	if model.filterIndex != 0 {
		t.Fatalf("cycle must wrap to none, index=%d", model.filterIndex)
	}
}

func TestFilterCycleMatchesHydratedCcOnNonLatestMessage(t *testing.T) {
	model := newFilteredListModel(t, cfgWithFilterRules(t, "carbon", map[string]string{"cc": `carol@`}),
		[]*gmail.Thread{threadWithCcOnOlderMessage("t1", "carol@example.test")})

	model, cmd := press(t, model, "f")
	if got := threadIDs(runListCmd(t, cmd)); !slices.Equal(got, []string{"t1"}) {
		t.Fatalf("cc-on-non-latest view = %v, want [t1]", got)
	}
}

func TestFilterCyclePressClearsSelectionAndTitleNamesFilter(t *testing.T) {
	model := newFilteredListModel(t, cfgWithFilters(t, "github"), []*gmail.Thread{filterThread("t1", "GitHub <github@example.test>")})
	model, _ = press(t, model, "v")
	model, _ = press(t, model, "a")
	model, _ = press(t, model, "f")

	if len(model.list.selected) != 0 || model.listLoaded {
		t.Fatal("f must clear the selection and mark the listing loading before the fetch")
	}
	if title := model.list.title(model.account, false, "github"); !strings.Contains(title, "filter: github") {
		t.Fatalf("title = %q, want the active filter name", title)
	}
}

func TestStartupFilterSeedsViewAndUnknownErrors(t *testing.T) {
	cfg := cfgWithFilters(t, "zeta", "alpha")
	model := newAppWithStartFilter(t, cfg, "alpha")
	if model.filterIndex != 2 {
		t.Fatalf("startFilter alpha = index %d, want 2", model.filterIndex)
	}
	if err := runWithStartFilter(t, cfg, "missing"); err == nil ||
		!strings.Contains(err.Error(), `unknown filter "missing"; defined filters: zeta, alpha`) {
		t.Fatalf("unknown startup filter error = %v", err)
	}

	empty := &auth.Config{DefaultPath: "/tmp/none/config.toml", Accounts: cfg.Accounts}
	if err := runWithStartFilter(t, empty, "x"); err == nil ||
		!strings.Contains(err.Error(), "no filters are defined (config: /tmp/none/config.toml)") {
		t.Fatalf("no-filters startup error = %v", err)
	}
}

func TestFWithNoFiltersIsInertWithStatus(t *testing.T) {
	model := newFilteredListModel(t, cfgWithFilters(t), []*gmail.Thread{filterThread("t1", "Sender <sender@example.test>")})
	before := model.generations[listOperation]
	model, cmd := press(t, model, "f")
	if model.filterIndex != 0 || cmd != nil || model.generations[listOperation] != before {
		t.Fatal("f with zero filters must not refetch")
	}
	if model.status != "no filters defined" {
		t.Fatalf("status = %q, want the inert notice", model.status)
	}
}

func cfgWithFilters(t *testing.T, names ...string) *auth.Config {
	t.Helper()
	cfg := testConfig()
	for _, name := range names {
		compiled, err := filter.Compile(name, map[string]string{"from": name + "@"})
		if err != nil {
			t.Fatalf("compile filter %q: %v", name, err)
		}
		cfg.Filters = append(cfg.Filters, compiled)
	}
	return cfg
}

func cfgWithFilterRules(t *testing.T, name string, rules map[string]string) *auth.Config {
	t.Helper()
	cfg := testConfig()
	compiled, err := filter.Compile(name, rules)
	if err != nil {
		t.Fatalf("compile filter %q: %v", name, err)
	}
	cfg.Filters = []*filter.Filter{compiled}
	return cfg
}

func newFilteredListModel(t *testing.T, cfg *auth.Config, rows []*gmail.Thread) app {
	t.Helper()
	api := &fakeAPI{threads: rows, attachments: make(map[string][]byte)}
	model := newTestModelWithConfig(cfg, "work", api)
	model, _ = update(t, model, threadsMsg{request: model.currentRequest(listOperation), threads: rows})
	return model
}

func runListCmd(t *testing.T, cmd tea.Cmd) []*gmail.Thread {
	t.Helper()
	message := runCmd(t, cmd)
	threads, ok := message.(threadsMsg)
	if !ok {
		t.Fatalf("list command = %T, want threadsMsg", message)
	}
	return threads.threads
}

func newAppWithStartFilter(t *testing.T, cfg *auth.Config, name string) app {
	t.Helper()
	index, err := startFilterIndex(cfg, name)
	if err != nil {
		t.Fatalf("startFilterIndex(%q): %v", name, err)
	}
	account, ok := cfg.Account(cfg.DefaultAccount)
	if !ok {
		t.Fatalf("account %q missing", cfg.DefaultAccount)
	}
	model := newApp(testAccountCtx(cfg, account, &fakeAPI{attachments: make(map[string][]byte)}))
	model.filterIndex = index
	return model
}

func runWithStartFilter(t *testing.T, cfg *auth.Config, name string) error {
	t.Helper()
	_, err := startFilterIndex(cfg, name)
	return err
}

func filterThread(id, from string) *gmail.Thread {
	return &gmail.Thread{
		ID: id,
		Messages: []*gmail.Message{{
			ID:           id + "-message",
			ThreadID:     id,
			LabelIDs:     []string{"INBOX"},
			InternalDate: 2,
			Payload: &gmail.MessagePart{Headers: []gmail.Header{
				{Name: "From", Value: from},
				{Name: "Subject", Value: "subject"},
			}},
		}},
	}
}

func threadWithCcOnOlderMessage(id, cc string) *gmail.Thread {
	thread := filterThread(id, "Latest <latest@example.test>")
	thread.Messages = append(thread.Messages, &gmail.Message{
		ID:           id + "-older-message",
		ThreadID:     id,
		LabelIDs:     []string{"INBOX"},
		InternalDate: 1,
		Payload: &gmail.MessagePart{Headers: []gmail.Header{
			{Name: "Cc", Value: cc},
			{Name: "Subject", Value: "older subject"},
		}},
	})
	return thread
}
