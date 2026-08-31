package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func callsTo(g *fakeGmail, method, path string) []recordedCall {
	var out []recordedCall
	for _, call := range g.recordedCalls() {
		if call.Method == method && call.Path == path {
			out = append(out, call)
		}
	}
	return out
}

func callsUnder(g *fakeGmail, prefix string) []recordedCall {
	var out []recordedCall
	for _, call := range g.recordedCalls() {
		if strings.HasPrefix(call.Path, prefix) {
			out = append(out, call)
		}
	}
	return out
}

func TestCLIDraftLifecycleEndToEnd(t *testing.T) {
	fixture := newDraftFixture(t)
	attachment := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(attachment, []byte("draft attachment"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runBinary(t, fixture.env, "send", "--reply", "t1", "--body", "draft body", "--attach", attachment, "--save-draft", "--json")
	if code != 0 {
		t.Fatalf("save exit = %d, stderr=%q", code, stderr)
	}
	var saved struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.DraftID == "" {
		t.Fatalf("save payload = %q (%v)", stdout, err)
	}
	creates := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/drafts")
	if len(creates) != 1 || creates[0].Bearer != "Bearer "+writeCanary() {
		t.Fatalf("drafts.create calls = %+v, want one write-canary call", creates)
	}

	code, stdout, _ = runBinary(t, fixture.env, "drafts", "--json")
	if code != 0 || !strings.Contains(stdout, saved.DraftID) {
		t.Fatalf("drafts listing = %d %q", code, stdout)
	}
	lists := callsTo(fixture.gmail, http.MethodGet, "/gmail/v1/users/me/drafts")
	if len(lists) != 1 || lists[0].Bearer != "Bearer pty-mut-tok" {
		t.Fatalf("drafts.list calls = %+v, want one read-canary call", lists)
	}

	code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--json")
	if code != 0 {
		t.Fatalf("dry-run exit = %d: %q", code, stdout)
	}
	var preview struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil || preview.Message == "" {
		t.Fatalf("dry-run = %q (%v), want the pin target", stdout, err)
	}

	draftGets := callsTo(fixture.gmail, http.MethodGet, "/gmail/v1/users/me/drafts/"+saved.DraftID)
	if len(draftGets) == 0 {
		t.Fatalf("draft resume did not fetch the draft: %+v", fixture.gmail.recordedCalls())
	}
	for _, call := range draftGets {
		if call.Bearer != "Bearer pty-mut-tok" {
			t.Fatalf("drafts.get used %q, want the read bearer", call.Bearer)
		}
	}
	attachmentPath := "/gmail/v1/users/me/messages/m-d-e2e-1/attachments/" + saved.DraftID + "-a-0"
	attachments := callsTo(fixture.gmail, http.MethodGet, attachmentPath)
	if len(attachments) == 0 {
		t.Fatalf("draft resume did not fetch its carried attachment: %+v", fixture.gmail.recordedCalls())
	}
	for _, call := range attachments {
		if call.Bearer != "Bearer pty-mut-tok" {
			t.Fatalf("attachments.get used %q, want the read bearer", call.Bearer)
		}
	}

	code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--send", "--message", preview.Message, "--json")
	if code != 0 {
		t.Fatalf("resumed send exit = %d: %q", code, stdout)
	}
	sends := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/messages/send")
	if len(sends) != 1 || sends[0].Bearer != "Bearer "+sendCanary() {
		t.Fatalf("messages.send calls = %+v, want one send-canary call", sends)
	}
	deletes := callsTo(fixture.gmail, http.MethodDelete, "/gmail/v1/users/me/drafts/"+saved.DraftID)
	if len(deletes) != 1 || deletes[0].Bearer != "Bearer "+writeCanary() {
		t.Fatalf("drafts.delete calls = %+v, want one write-canary call", deletes)
	}
	if code, stdout, _ = runBinary(t, fixture.env, "drafts", "--json"); code != 0 || strings.Contains(stdout, saved.DraftID) {
		t.Fatalf("draft survived its resumed send: exit=%d output=%q", code, stdout)
	}
	lists = callsTo(fixture.gmail, http.MethodGet, "/gmail/v1/users/me/drafts")
	draftGets = callsTo(fixture.gmail, http.MethodGet, "/gmail/v1/users/me/drafts/"+saved.DraftID)
	attachments = callsTo(fixture.gmail, http.MethodGet, attachmentPath)
	if len(lists) != 2 || len(draftGets) < 2 || len(attachments) < 2 {
		t.Fatalf("incomplete draft custody map: list=%+v get=%+v attachments=%+v", lists, draftGets, attachments)
	}
	for _, class := range []struct {
		name  string
		calls []recordedCall
	}{
		{"drafts.list", lists},
		{"drafts.get", draftGets},
		{"attachments.get", attachments},
	} {
		for _, call := range class.calls {
			if call.Bearer != "Bearer pty-mut-tok" {
				t.Fatalf("%s used %q, want the read bearer", class.name, call.Bearer)
			}
		}
	}
	t.Logf("fixture custody: drafts.create=%s; drafts.list=%s; drafts.get=%s; attachments.get=%s; messages.send=%s; drafts.delete=%s",
		creates[0].Bearer, lists[0].Bearer, draftGets[0].Bearer, attachments[0].Bearer, sends[0].Bearer, deletes[0].Bearer)
	if unknown := fixture.gmail.unknownCalls(); len(unknown) != 0 {
		t.Fatalf("unknown fixture endpoints hit: %+v", unknown)
	}
	if hits := callsUnder(fixture.gmail, "/gmail/v1/users/me/drafts/send"); len(hits) != 0 {
		t.Fatalf("drafts.send reached: %+v", hits)
	}
}

func TestCLIDraftEditedPinRefuses(t *testing.T) {
	fixture := newDraftFixture(t)
	code, stdout, _ := runBinary(t, fixture.env, "send", "--reply", "t1", "--body", "will rotate", "--save-draft", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var saved struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.DraftID == "" {
		t.Fatalf("save payload = %q (%v)", stdout, err)
	}
	code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var preview struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil || preview.Message == "" {
		t.Fatalf("preview = %q (%v)", stdout, err)
	}

	response, err := http.Post(fixture.gmail.server.URL+"/gmail/v1/users/me/drafts/"+saved.DraftID+"/update", "application/json", nil)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("fixture rotation: %v %+v", err, response)
	}
	response.Body.Close()

	code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--send", "--message", preview.Message, "--json")
	if code != 1 {
		t.Fatalf("edited-pin exit = %d: %q", code, stdout)
	}
	var refused struct {
		Error struct {
			Code  string `json:"code"`
			Fresh struct {
				Message string `json:"message"`
			} `json:"fresh"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "draft_changed" || refused.Error.Fresh.Message == "" || refused.Error.Fresh.Message == preview.Message {
		t.Fatalf("draft_changed = %+v, want the rotated id in the fresh preview", refused)
	}
	if sends := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/messages/send"); len(sends) != 0 {
		t.Fatalf("edited draft transmitted: %+v", sends)
	}
	if deletes := callsTo(fixture.gmail, http.MethodDelete, "/gmail/v1/users/me/drafts/"+saved.DraftID); len(deletes) != 0 {
		t.Fatalf("edited draft was deleted: %+v", deletes)
	}
	if code, stdout, _ = runBinary(t, fixture.env, "drafts", "--json"); code != 0 || !strings.Contains(stdout, saved.DraftID) {
		t.Fatalf("edited draft was not retained: exit=%d output=%q", code, stdout)
	}

	code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--send", "--message", refused.Error.Fresh.Message, "--json")
	if code != 0 {
		t.Fatalf("fresh-pin send exit = %d: %q", code, stdout)
	}
	sends := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/messages/send")
	if len(sends) != 1 || sends[0].Bearer != "Bearer "+sendCanary() {
		t.Fatalf("fresh-pin messages.send calls = %+v, want one send-canary call", sends)
	}
	deletes := callsTo(fixture.gmail, http.MethodDelete, "/gmail/v1/users/me/drafts/"+saved.DraftID)
	if len(deletes) != 1 || deletes[0].Bearer != "Bearer "+writeCanary() {
		t.Fatalf("fresh-pin drafts.delete calls = %+v, want one write-canary call", deletes)
	}
}

func TestCLIDraftSendOnceOnIndeterminateResponses(t *testing.T) {
	arm := map[string]func(*fakeGmail){
		"garbage 200 body": func(g *fakeGmail) { g.armSendGarbage() },
		"decoded 5xx":      func(g *fakeGmail) { g.armSendStatus(http.StatusInternalServerError) },
	}
	for name, armLever := range arm {
		t.Run(name, func(t *testing.T) {
			fixture := newDraftFixture(t)
			code, stdout, _ := runBinary(t, fixture.env, "send", "--reply", "t1", "--body", "send once", "--save-draft", "--json")
			if code != 0 {
				t.Fatal(code)
			}
			var saved struct {
				DraftID string `json:"draft_id"`
			}
			if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.DraftID == "" {
				t.Fatalf("save payload = %q (%v)", stdout, err)
			}
			code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--json")
			if code != 0 {
				t.Fatal(code)
			}
			var preview struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(stdout), &preview); err != nil || preview.Message == "" {
				t.Fatalf("preview = %q (%v)", stdout, err)
			}

			armLever(fixture.gmail)
			code, stdout, _ = runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--send", "--message", preview.Message, "--json")
			if code == 0 || !strings.Contains(stdout, "draft_send_unknown") {
				t.Fatalf("%s: exit=%d stdout=%q, want draft_send_unknown", name, code, stdout)
			}
			if sends := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/messages/send"); len(sends) != 1 || sends[0].Bearer != "Bearer "+sendCanary() {
				t.Fatalf("%s: send calls = %+v, want exactly one send-canary call", name, sends)
			}
			if deletes := callsTo(fixture.gmail, http.MethodDelete, "/gmail/v1/users/me/drafts/"+saved.DraftID); len(deletes) != 0 {
				t.Fatalf("%s: indeterminate send deleted the draft: %+v", name, deletes)
			}
			if code, stdout, _ := runBinary(t, fixture.env, "drafts", "--json"); code != 0 || !strings.Contains(stdout, saved.DraftID) {
				t.Fatalf("%s: draft not retained: exit=%d output=%q", name, code, stdout)
			}
		})
	}
}

func TestTUIAbandonPromptSavesOrDiscardsInRealPTY(t *testing.T) {
	binary := buildMailbox(t)
	t.Run("s saves through the write fence with attribution before spawn", func(t *testing.T) {
		fixture := newDraftFixture(t)
		useTUIEditor(t, &fixture.sendFixture, "body to save")
		session := startSendTUI(t, binary, &fixture.sendFixture)
		session.SendEnter()
		session.WaitFor("r reply", 15*time.Second)
		session.SendKeys("r")
		session.WaitFor("Confirm send", 5*time.Second)
		if output, err := session.cmd("send-keys", "-t", session.name, "Escape").CombinedOutput(); err != nil {
			t.Fatalf("tmux send escape: %v: %s", err, output)
		}
		session.WaitFor("s save to Gmail drafts", 5*time.Second)
		session.SendKeys("s")
		attribution := "waiting for write approval; approve only this request — work write access via " + fixture.writeHelper
		session.WaitFor(attribution, 5*time.Second)
		session.WaitFor("draft saved", 15*time.Second)
		assertSpawnPaneContains(t, fixture.writeSpawnPaneFile, attribution)
		creates := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/drafts")
		if len(creates) != 1 || creates[0].Bearer != "Bearer "+writeCanary() {
			t.Fatalf("draft creates = %+v, want one write-canary create", creates)
		}
		if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
			t.Fatalf("saving a draft transmitted mail: %#v", sends)
		}
	})
	t.Run("esc esc discards with zero server writes and zero spawns", func(t *testing.T) {
		fixture := newDraftFixture(t)
		useTUIEditor(t, &fixture.sendFixture, "body to discard")
		session := startSendTUI(t, binary, &fixture.sendFixture)
		session.SendEnter()
		session.WaitFor("r reply", 15*time.Second)
		session.SendKeys("r")
		session.WaitFor("Confirm send", 5*time.Second)
		if output, err := session.cmd("send-keys", "-t", session.name, "Escape").CombinedOutput(); err != nil {
			t.Fatalf("tmux send escape: %v: %s", err, output)
		}
		session.WaitFor("d discard", 5*time.Second)
		if output, err := session.cmd("send-keys", "-t", session.name, "Escape").CombinedOutput(); err != nil {
			t.Fatalf("tmux send escape: %v: %s", err, output)
		}
		session.WaitFor("r reply", 5*time.Second)
		assertNoSpawns(t, fixture.writeSpawnFile)
		assertNoSpawns(t, fixture.spawnFile)
		if writes := fixture.gmail.recordedWriteAuths(); len(writes) != 0 {
			t.Fatalf("discard acquired write custody: %+v", writes)
		}
		if creates := callsTo(fixture.gmail, http.MethodPost, "/gmail/v1/users/me/drafts"); len(creates) != 0 {
			t.Fatalf("discard wrote a draft: %+v", creates)
		}
	})
}

func TestCLIDraftContentNeverPersists(t *testing.T) {
	fixture := newDraftFixture(t)
	canary := strings.Join([]string{"canary", "draft", "body", "0123456789abcdef"}, "-")
	before := snapshotFiles(t, fixture.cache)

	code, stdout, _ := runBinary(t, fixture.env, "send", "--reply", "t1", "--body", canary, "--save-draft", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var saved struct {
		DraftID string `json:"draft_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.DraftID == "" {
		t.Fatalf("save payload = %q (%v)", stdout, err)
	}
	if code, _, _ := runBinary(t, fixture.env, "send", "--draft", saved.DraftID, "--json"); code != 0 {
		t.Fatal(code)
	}
	assertFileSnapshotEqual(t, before, snapshotFiles(t, fixture.cache))
	assertNoCanaryOnDisk(t, canary, fixture.stubs, fixture.cache, filepath.Dir(buildMailbox(t)))

	fixture.gmail.setDraftSubject(saved.DraftID, "\x1b]0;pwn\x07 subject")
	if _, listing, _ := runBinary(t, fixture.env, "drafts", "--text"); strings.ContainsRune(listing, 0x1b) {
		t.Fatalf("text listing leaked a terminal escape: %q", listing)
	}
}
