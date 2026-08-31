package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

func TestDraftsListsNewestFirstInAllFormats(t *testing.T) {
	g := newGmailTestServer(t)
	configureDraftListing(g)
	rig := newReadRig(t, g)

	code, stdout, _ := rig.run(t, "drafts", "--json")
	if code != 0 {
		t.Fatalf("drafts exit = %d: %q", code, stdout)
	}
	var payload struct {
		Account string `json:"account"`
		Drafts  []struct {
			DraftID  string `json:"draft_id"`
			ThreadID string `json:"thread_id"`
			To       string `json:"to"`
			Subject  string `json:"subject"`
			Updated  string `json:"updated"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Drafts) != 2 || payload.Drafts[0].DraftID != "d-new" || payload.Drafts[1].DraftID != "d-old" {
		t.Fatalf("rows = %+v, want newest-first", payload.Drafts)
	}
	wantNewTo := "\x1b]0;pwn\x07\x1bP+q\x1b\\ \u202eevil\r\ninjected\tcol <e@example.test>"
	if payload.Account != "work" ||
		payload.Drafts[0].ThreadID != "t-new" || payload.Drafts[0].To != wantNewTo ||
		payload.Drafts[0].Subject != "new" || payload.Drafts[0].Updated != "1970-01-01T00:00:02Z" ||
		payload.Drafts[1].ThreadID != "t-old" || payload.Drafts[1].To != "A <a@example.test>" ||
		payload.Drafts[1].Subject != "old" || payload.Drafts[1].Updated != "1970-01-01T00:00:01Z" {
		t.Fatalf("draft payload = %+v, want complete raw draft rows", payload)
	}
	if _, err := time.Parse(time.RFC3339, payload.Drafts[0].Updated); err != nil {
		t.Fatalf("updated = %q, want RFC3339: %v", payload.Drafts[0].Updated, err)
	}

	code, stdout, _ = rig.run(t, "drafts") // pipe default → TOON
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("TOON decode: %v\n%s", err, stdout)
	}
	if len(g.draftReadBearers) != 6 {
		t.Fatalf("draft read requests = %d, want list plus two metadata fetches per format", len(g.draftReadBearers))
	}
	for _, bearer := range g.draftReadBearers {
		if bearer != "Bearer "+g.readToken {
			t.Fatalf("draft listing bearer = %q, want read-class bearer", bearer)
		}
	}
}

func TestDraftsMaxClampAndQuery(t *testing.T) {
	g := newGmailTestServer(t)
	configureDraftListing(g)
	rig := newReadRig(t, g)
	if code, _, _ := rig.run(t, "drafts", "--max", "1", "--json"); code != 0 || g.draftListMax != "1" {
		t.Fatalf("exit=%d maxResults=%q, want 0 and \"1\"", code, g.draftListMax)
	}
	for _, bad := range []string{"0", "501"} {
		if code, _, stderr := rig.run(t, "drafts", "--max", bad); code != 2 {
			t.Fatalf("--max %s exit = %d (stderr %q), want 2", bad, code, stderr)
		}
	}
}

func TestDraftsTextModeSanitizesHostileHeaders(t *testing.T) { // [G9][R8]
	g := newGmailTestServer(t)
	configureDraftListing(g) // d-new's To carries OSC, DCS, a bidi override, CRLF, and a TAB (see fixture)
	rig := newReadRig(t, g)
	code, stdout, _ := rig.run(t, "drafts", "--text")
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.HasPrefix(stdout, "draft_id\tthread_id\tto\tsubject\tupdated\n") {
		t.Fatalf("text header = %q", stdout)
	}
	if strings.ContainsRune(stdout, 0x1b) || strings.Contains(stdout, "\x07") {
		t.Fatalf("text output leaked a terminal escape/control byte: %q", stdout)
	}
	// Layout safety: SanitizeTerminal alone preserves newlines and tabs, so
	// hostile CRLF/TAB in a header would split rows or skew columns. Every
	// field goes through send.VisibleOneLine instead. [R8]
	rows := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("text listing rows = %d, want 3 (header + 2 drafts) — CRLF must not split rows:\n%q", len(rows), stdout)
	}
	for _, row := range rows {
		if strings.Count(row, "\t") != 4 {
			t.Fatalf("column drift (hostile TAB/CRLF must render as visible markers): %q", row)
		}
	}
	if strings.Contains(stdout, "\r") || !strings.Contains(stdout, "␍␊") {
		t.Fatalf("CRLF must render as ␍␊ markers, never raw: %q", stdout)
	}
	code, stdout, _ = rig.run(t, "drafts", "--json")
	if code != 0 || !strings.Contains(stdout, `\u001b]0;pwn`) {
		t.Fatalf("machine output must stay raw structured data: %q", stdout)
	}
}

func TestDraftsBinaryFixtureOutputsAllFormats(t *testing.T) {
	g := newGmailTestServer(t)
	configureDraftListing(g)
	g.readToken = "read-token-1234567890"

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("default_account = \"work\"\n[accounts.work]\nread_credential_env = \"CLI_READ\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "mailbox")
	build := exec.Command("go", "build", "-o", binary, "github.com/sjawhar/mailbox/cmd/mailbox")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mailbox: %v\n%s", err, output)
	}

	run := func(args ...string) (int, string, string) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(),
			"MAILBOX_CONFIG="+configPath,
			"MAILBOX_GMAIL_BASE_URL="+g.server.URL,
			"MAILBOX_CACHE_DIR="+t.TempDir(),
			"MAILBOX_TOKEN=",
			"CLI_READ="+g.readToken,
		)
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		if err == nil {
			return 0, stdout.String(), stderr.String()
		}
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run mailbox %v: %v", args, err)
		}
		return exitError.ExitCode(), stdout.String(), stderr.String()
	}

	code, stdout, stderr := run("drafts", "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("mailbox drafts --help = (%d, %q, %q), want success", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Lists Gmail server-side drafts newest-first") ||
		!strings.Contains(stdout, "Listing is read-class (no unlock).") {
		t.Fatalf("drafts help = %q, want listing and read-class guidance", stdout)
	}
	t.Logf("help: %s", strings.TrimSuffix(stdout, "\n"))

	code, stdout, stderr = run("drafts", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("mailbox drafts --json = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var jsonPayload draftsPayload
	if err := json.Unmarshal([]byte(stdout), &jsonPayload); err != nil {
		t.Fatalf("decode binary JSON: %v\n%s", err, stdout)
	}
	if len(jsonPayload.Drafts) != 2 || jsonPayload.Drafts[0].DraftID != "d-new" {
		t.Fatalf("binary JSON payload = %+v, want newest-first rows", jsonPayload)
	}
	t.Logf("JSON: %s", strings.TrimSuffix(stdout, "\n"))

	code, stdout, stderr = run("drafts")
	if code != 0 || stderr != "" {
		t.Fatalf("mailbox drafts = (%d, %q, %q), want success", code, stdout, stderr)
	}
	if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("decode binary TOON: %v\n%s", err, stdout)
	}
	t.Logf("TOON: %s", strings.TrimSuffix(stdout, "\n"))

	code, stdout, stderr = run("drafts", "--text")
	if code != 0 || stderr != "" {
		t.Fatalf("mailbox drafts --text = (%d, %q, %q), want success", code, stdout, stderr)
	}
	const wantText = "draft_id\tthread_id\tto\tsubject\tupdated\n" +
		"d-new\tt-new\t \u202eevil␍␊injected␉col <e@example.test>\tnew\t1970-01-01T00:00:02Z\n" +
		"d-old\tt-old\tA <a@example.test>\told\t1970-01-01T00:00:01Z\n"
	if stdout != wantText {
		t.Fatalf("binary text = %q, want %q", stdout, wantText)
	}
	t.Logf("text: %s", strings.TrimSuffix(stdout, "\n"))
}
