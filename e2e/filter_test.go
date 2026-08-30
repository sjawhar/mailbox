package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

type filterFixture struct {
	gmail     *fakeGmail
	cache     string
	env       map[string]string
	spawnFile string
}

func newFilterFixture(t *testing.T) *filterFixture {
	t.Helper()
	gmail := newFakeGmail(t)
	stubs := t.TempDir()
	spawnFile := filepath.Join(stubs, "write-spawns")
	writeExecutable(t, filepath.Join(stubs, "write-helper"), `#!/bin/sh
printf 'spawn\n' >> "$WRITE_SPAWN_FILE"
printf '%s\n' "$WRITE_CANARY"
`)
	config := writeE2EConfig(t, stubs, `default_account = "work"
[filters.github]
from = "notifications@github\\.com"
list = "ci\\.github\\.example"

[accounts.work]
read_credential_env = "PTY_READ_OAUTH"
write_credential_cmd = ["write-helper"]
write_interactive = false
credential_env_passthrough = ["WRITE_SPAWN_FILE"]
write_credential_env_passthrough = ["WRITE_CANARY"]
`)
	cache := t.TempDir()
	env := map[string]string{
		"HOME":                   os.Getenv("HOME"),
		"TERM":                   "xterm-256color",
		"PATH":                   stubs + ":/usr/bin:/bin",
		"MAILBOX_CONFIG":         config,
		"MAILBOX_GMAIL_BASE_URL": gmail.server.URL,
		"MAILBOX_CACHE_DIR":      cache,
		"WRITE_SPAWN_FILE":       spawnFile,
		"WRITE_CANARY":           "filter.write.token.value.1234567890",
	}
	seedReadCache(t, withEnvironment(env, map[string]string{
		"PTY_READ_OAUTH":    testAuthorizedUser,
		"MAILBOX_TOKEN_URL": gmail.token.URL,
	}))
	return &filterFixture{gmail: gmail, cache: cache, env: env, spawnFile: spawnFile}
}

func startFilterTUI(t *testing.T, fixture *filterFixture) *tmuxSession {
	t.Helper()
	session := newTmuxSession(t, fixture.env, buildMailbox(t))
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("PTY smoke", 15*time.Second)
	return session
}

func TestCLIInboxFilterTOONAndText(t *testing.T) {
	fixture := newFilterFixture(t)
	fixture.gmail.setListPages([][]string{{"t1", "t-gh", "t2"}})

	code, stdout, stderr := runBinary(t, fixture.env, "inbox", "--filter", "github")
	if code != 0 {
		t.Fatalf("filtered TOON inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	payload := decodeTOON(t, stdout)
	if got := toonString(t, payload, "filter"); got != "github" {
		t.Fatalf("TOON filter = %q, want github", got)
	}
	if got := toonThreadIDs(t, toonField(t, payload, "threads")); !sameStrings(got, []string{"t-gh"}) {
		t.Fatalf("TOON filtered thread ids = %q, want [t-gh]", got)
	}
	code, stdout, stderr = runBinary(t, fixture.env, "inbox", "--filter", "github", "--text")
	if code != 0 {
		t.Fatalf("filtered text inbox exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	first, _, _ := strings.Cut(stdout, "\n")
	if first != "filter: github" {
		t.Fatalf("filtered text first line = %q, want filter: github; full output:\n%s", first, stdout)
	}
}

func TestCLIArchiveFilterWholeInboxReceipts(t *testing.T) {
	fixture := newFilterFixture(t)
	fixture.gmail.setListPages([][]string{
		{"t1", "t-gh"},
		{"t2", "t-gh-2"},
		{"t-gh-3"},
	})

	code, stdout, stderr := runBinary(t, fixture.env, "archive", "--filter", "github")
	if code != 0 {
		t.Fatalf("filtered archive exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	payload := decodeTOON(t, stdout)
	if got := toonString(t, payload, "action"); got != "archive" {
		t.Fatalf("TOON action = %q, want archive", got)
	}
	if got := toonString(t, payload, "filter"); got != "github" {
		t.Fatalf("TOON filter = %q, want github", got)
	}
	for _, expectation := range []struct {
		field string
		want  string
	}{
		{"matched", "3"},
		{"attempted", "3"},
	} {
		value := toonField(t, payload, expectation.field)
		if value.Kind != toontest.Number || value.Num != expectation.want {
			t.Fatalf("TOON %s = %#v, want number %s", expectation.field, value, expectation.want)
		}
	}
	wantIDs := []string{"t-gh", "t-gh-2", "t-gh-3"}
	if got := toonStringArray(t, toonField(t, payload, "succeeded")); !sameStrings(got, wantIDs) {
		t.Fatalf("TOON succeeded = %q, want %q", got, wantIDs)
	}
	failed := toonField(t, payload, "failed")
	if failed.Kind != toontest.Array || len(failed.Arr) != 0 {
		t.Fatalf("TOON failed = %#v, want an empty array", failed)
	}
	if got := toonBool(t, payload, "ok"); !got {
		t.Fatalf("TOON ok = false, want true")
	}
	if got := fixture.gmail.recordedModified(); !sameStrings(got, wantIDs) {
		t.Fatalf("modified thread ids = %q, want %q", got, wantIDs)
	}
	if got := fileLines(t, fixture.spawnFile); !sameStrings(got, []string{"spawn"}) {
		t.Fatalf("write helper spawns = %q, want one invocation", got)
	}
}

func TestTUISelectAllArchive(t *testing.T) {
	fixture := newFilterFixture(t)
	session := startFilterTUI(t, fixture)

	session.SendKeys("v")
	session.SendKeys("a")
	session.SendKeys("e")
	session.WaitFor("archive completed", 15*time.Second)
	if got := fixture.gmail.recordedModified(); !sameStrings(got, []string{"t1", "t2"}) {
		t.Fatalf("archived thread ids = %q, want [t1 t2]", got)
	}
}

func TestTUIFilterCycleShowsName(t *testing.T) {
	fixture := newFilterFixture(t)
	fixture.gmail.setListPages([][]string{{"t1", "t-gh", "t2"}})
	session := startFilterTUI(t, fixture)

	session.SendKeys("f")
	session.WaitFor("filter: github", 15*time.Second)
	pane, found := session.findText("GitHub notification", 15*time.Second)
	if !found {
		t.Fatalf("timed out waiting for the GitHub filter row; batch requests=%q; last pane:\n%s", fixture.gmail.recordedBatchRequests(), pane)
	}
	if strings.Contains(pane, "PTY smoke") {
		t.Fatalf("filtered pane still shows an unfiltered fixture row:\n%s", pane)
	}
	if !containsListIDMetadataRequest(fixture.gmail.recordedBatchRequests(), "t-gh") {
		t.Fatalf("GitHub filter never hydrated t-gh with List-ID metadata; batch requests=%q", fixture.gmail.recordedBatchRequests())
	}

	session.SendKeys("f")
	pane = session.WaitFor("PTY smoke", 15*time.Second)
	if strings.Contains(pane, "filter: github") {
		t.Fatalf("plain inbox still shows github filter after cycling back:\n%s", pane)
	}
}

func TestTUIHashTrashesCursorRow(t *testing.T) {
	fixture := newFilterFixture(t)
	session := startFilterTUI(t, fixture)

	before := session.WaitForStable("PTY smoke", 5*time.Second)
	session.SendKeys("d")
	after := session.WaitForStable("PTY smoke", 5*time.Second)
	if after != before {
		t.Fatalf("d changed the pane despite being unbound:\nbefore: %q\nafter: %q", before, after)
	}
	assertNoSpawns(t, fixture.spawnFile)

	session.SendKeys("#")
	session.WaitFor("trash completed", 15*time.Second)
	if got := fixture.gmail.recordedTrashed(); !sameStrings(got, []string{"t1"}) {
		t.Fatalf("trashed thread ids = %q, want [t1]", got)
	}
}

func TestTUIStaleSelectionRace(t *testing.T) {
	fixture := newFilterFixture(t)
	session := startFilterTUI(t, fixture)
	fixture.gmail.setListPages([][]string{{"t-gh"}})
	fixture.gmail.setListDelay(2 * time.Second)

	session.SendKeys("v")
	session.SendKeys("a")
	session.SendKeys("R")
	session.SendKeys("e")
	session.WaitFor("GitHub notification", 15*time.Second)
	if got := fixture.gmail.recordedModified(); len(got) != 0 {
		t.Fatalf("stale selection dispatched archive writes: %q", got)
	}
	assertNoSpawns(t, fixture.spawnFile)
}

func toonThreadIDs(t *testing.T, value toontest.Value) []string {
	t.Helper()
	if value.Kind != toontest.Array {
		t.Fatalf("TOON value = %#v, want array", value)
	}
	ids := make([]string, len(value.Arr))
	for index, item := range value.Arr {
		ids[index] = toonString(t, item, "id")
	}
	return ids
}

func toonStringArray(t *testing.T, value toontest.Value) []string {
	t.Helper()
	if value.Kind != toontest.Array {
		t.Fatalf("TOON value = %#v, want array", value)
	}
	values := make([]string, len(value.Arr))
	for index, item := range value.Arr {
		if item.Kind != toontest.String {
			t.Fatalf("TOON array item = %#v, want string", item)
		}
		values[index] = item.Str
	}
	return values
}

func containsListIDMetadataRequest(requests []string, id string) bool {
	prefix := "GET /gmail/v1/users/me/threads/" + id + "?"
	for _, request := range requests {
		if strings.HasPrefix(request, prefix) && strings.Contains(request, "metadataHeaders=List-ID") {
			return true
		}
	}
	return false
}

func sameStrings(got, want []string) bool {
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}
