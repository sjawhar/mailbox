package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type draftRig struct {
	readToken  string
	writeToken string
	sendToken  string
	sendHelper string
}

func newDraftRig(t *testing.T, g *gmailTestServer) *draftRig {
	t.Helper()
	dir := t.TempDir()
	rig := &draftRig{
		readToken:  "readtokenx1234567890",
		writeToken: "writetoken1234567890",
		sendToken:  "sendtokenx1234567890",
		sendHelper: filepath.Join(dir, "draft-send"),
	}
	config := `default_account = "work"
[accounts.work]
read_credential_env = "DRAFT_READ"
write_credential_cmd = ["draft-write"]
write_interactive = false
send_credential_cmd = ["draft-send"]
send_interactive = false
`
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"draft-write": rig.writeToken, "draft-send": rig.sendToken} {
		script := "#!/bin/sh\nprintf '%s\\n' " + token + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DRAFT_READ", rig.readToken)
	t.Setenv("MAILBOX_CONFIG", configPath)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	g.readToken = rig.readToken
	g.writeToken = rig.writeToken
	g.sendToken = rig.sendToken
	return rig
}

func newDraftCapableSendRig(t *testing.T, g *gmailTestServer) *sendRig {
	t.Helper()
	rig := newSendRig(t, g, nonInteractiveSendSource()+`write_credential_cmd = ["write-helper"]
write_interactive = false
`)
	writeHelper := filepath.Join(filepath.Dir(rig.helperPath), "write-helper")
	if err := os.WriteFile(writeHelper, []byte("#!/bin/sh\nprintf '%s\\n' write-token-1234567890\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_READ", cliSendToken)
	t.Setenv("MAILBOX_TOKEN", "")
	g.readToken = cliSendToken
	g.writeToken = "write-token-1234567890"
	return rig
}

func (r *draftRig) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runConfiguredSend(args...)
}

func (r *draftRig) setSendHelper(t *testing.T, script string) {
	t.Helper()
	if err := os.WriteFile(r.sendHelper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func configureResumableDraft(g *gmailTestServer) {
	g.drafts = map[string]map[string]any{
		"d1": {
			"id": "d1",
			"message": map[string]any{
				"id":       "m-d1",
				"threadId": "t1",
				"payload": map[string]any{
					"mimeType": "multipart/mixed",
					"headers": []map[string]any{
						{"name": "To", "value": "A <a@example.test>"},
						{"name": "Subject", "value": "Re: PTY smoke"},
						{"name": "In-Reply-To", "value": "<m-t1@example.test>"},
						{"name": "References", "value": "<m-t1@example.test>"},
					},
					"parts": []map[string]any{
						{
							"mimeType": "text/plain",
							"body": map[string]any{
								"data": base64.RawURLEncoding.EncodeToString([]byte("resumed body")),
							},
						},
						{
							"mimeType": "application/pdf",
							"filename": "carried.pdf",
							"body": map[string]any{
								"attachmentId": "a-d1",
								"size":         len(draftAttachmentBytes("a-d1")),
							},
						},
					},
				},
			},
		},
	}
	g.attachmentBytes["a-d1"] = draftAttachmentBytes("a-d1")
}

func draftAttachmentBytes(id string) []byte {
	return []byte("draft-attachment-" + id)
}

func TestSaveDraftAndSendAreMutuallyExclusive(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	code, stdout, stderr := rig.run(t, "send", "--to", "a@example.test", "--subject", "s", "--body", "b", "--save-draft", "--send", "--json")
	if code != 2 || !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want usage refusal", code, stdout, stderr)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Error.Code != "usage" {
		t.Fatalf("usage payload = %q (%v)", stdout, err)
	}
}

func TestSaveDraftRunsFullResolutionThenCreatesDraftUnderWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "reply", args: []string{"--reply", "t1"}},
		{name: "forward", args: []string{"--forward", "t1", "--to", "a@example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			configureSendMessages(g)
			rig := newDraftCapableSendRig(t, g)
			attachment := filepath.Join(t.TempDir(), "report.txt")
			if err := os.WriteFile(attachment, []byte("draft attachment"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"send"}, tc.args...)
			args = append(args, "--body", "hi", "--attach", attachment, "--save-draft", "--json")
			code, stdout, stderr := rig.run(t, args...)
			if code != 0 {
				t.Fatalf("save-draft exit = %d, stderr=%q", code, stderr)
			}
			var payload struct {
				DraftID     string `json:"draft_id"`
				Sendable    bool   `json:"sendable"`
				Attachments []struct {
					Filename string `json:"filename"`
				} `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.DraftID == "" || !payload.Sendable ||
				len(payload.Attachments) != 1 || payload.Attachments[0].Filename != "report.txt" {
				t.Fatalf("save-draft payload = %q (%v), want envelope with draft_id", stdout, err)
			}
			if len(g.draftCreates) != 1 {
				t.Fatalf("drafts.create calls = %d, want one", len(g.draftCreates))
			}
			if got := g.draftCreates[0].ThreadID; got != "t1" {
				t.Fatalf("drafts.create threadId = %q, want t1", got)
			}
			if got := g.draftCreates[0].Bearer; got != "Bearer write-token-1234567890" {
				t.Fatalf("drafts.create authorization = %q, want write credential", got)
			}
			raw, err := base64.RawURLEncoding.DecodeString(g.draftCreates[0].Raw)
			if err != nil || !strings.Contains(string(raw), "report.txt") {
				t.Fatalf("draft raw = %q (%v), want attached MIME", g.draftCreates[0].Raw, err)
			}
			if len(g.sentBodies) != 0 {
				t.Fatalf("save-draft transmitted: %v", g.sentBodies)
			}

			refusalArgs := append([]string{"send"}, tc.args...)
			refusalArgs = append(refusalArgs, "--body", "   ", "--attach", attachment, "--save-draft", "--json")
			code, stdout, _ = rig.run(t, refusalArgs...)
			if code != 1 || !strings.Contains(stdout, "empty_body") || len(g.draftCreates) != 1 {
				t.Fatalf("R5 must refuse before any write: exit=%d stdout=%q creates=%d", code, stdout, len(g.draftCreates))
			}
		})
	}
}

func TestDraftResumeDryRunReconstructsAndPinsCurrentMessageID(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	rig := newDraftRig(t, g)
	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--json")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr=%q", code, stderr)
	}
	var payload struct {
		Mode        string `json:"mode"`
		Message     string `json:"message"`
		DraftID     string `json:"draft_id"`
		ThreadID    string `json:"threadId"`
		Attachments []struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(draftAttachmentBytes("a-d1"))
	if payload.Mode != "reply" || payload.Message != "m-d1" || payload.DraftID != "d1" || payload.ThreadID != "t1" ||
		len(payload.Attachments) != 1 || payload.Attachments[0].Filename != "carried.pdf" ||
		payload.Attachments[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("payload = %+v", payload)
	}
	if g.sendCalls() != 0 || g.draftDeletes() != 0 || len(g.draftCreates) != 0 {
		t.Fatal("dry-run mutated server state")
	}
	for _, bearer := range g.draftReadBearers {
		if bearer != "Bearer "+rig.readToken {
			t.Fatalf("fetch phase used %q, want the read bearer only", bearer)
		}
	}
}
func TestDraftResumeDoesNotFetchProfile(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	rig := newDraftRig(t, g)

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--json")
	if code != 0 {
		t.Fatalf("resume exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if g.profileCalls != 0 {
		t.Fatalf("profile calls = %d, want draft reconstruction to avoid the profile boundary", g.profileCalls)
	}
}

func TestDraftResumePreflightsLocalAttachmentsBeforeRead(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newDraftRig(t, g)
	missing := filepath.Join(t.TempDir(), "missing.pdf")

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--attach", missing, "--json")
	if code != 1 || !strings.Contains(stdout, "attachment_unreadable") {
		t.Fatalf("preflight = (%d, %q, %q), want attachment_unreadable", code, stdout, stderr)
	}
	var payload struct {
		Error struct {
			Account string `json:"account"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Account != "work" {
		t.Fatalf("preflight account = %q, want resolved account", payload.Error.Account)
	}
	if len(g.draftReadBearers) != 0 || g.profileCalls != 0 {
		t.Fatalf("local refusal consumed read custody: draft reads=%v profile=%d", g.draftReadBearers, g.profileCalls)
	}
}

func TestDraftResumeHTMLOnlyBodyWithTextAttachmentUsesHTMLBody(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	html := "<p>html draft body</p>"
	g.draftPayload("d1")["parts"] = []map[string]any{
		{
			"mimeType": "text/html",
			"body": map[string]any{
				"data": base64.RawURLEncoding.EncodeToString([]byte(html)),
				"size": len(html),
			},
		},
		{
			"filename": "note.txt",
			"mimeType": "text/plain",
			"body": map[string]any{
				"data": base64.RawURLEncoding.EncodeToString([]byte("attachment bytes")),
				"size": len("attachment bytes"),
			},
		},
	}
	rig := newDraftRig(t, g)

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 0 {
		t.Fatalf("resume HTML-only draft with text attachment = exit %d stdout=%q stderr=%q; want send success", code, stdout, stderr)
	}
	if g.sendCalls() != 1 {
		t.Fatalf("send calls = %d, want one", g.sendCalls())
	}
	raw, err := base64.RawURLEncoding.DecodeString(g.sentBodies[0]["raw"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte("html draft body\n")))) {
		t.Fatalf("sent MIME = %q, want reconstructed HTML body", raw)
	}
	if !bytes.Contains(raw, []byte(`filename=note.txt`)) {
		t.Fatalf("sent MIME = %q, want text attachment", raw)
	}
}

func TestDraftResumeKeepsNamelessAndInlineCarriedPartsWithoutEmptyLookup(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.setDraftAttachmentName("d1", "")
	inline := []byte("inline carried bytes")
	parts := g.draftPayload("d1")["parts"].([]map[string]any)
	g.draftPayload("d1")["parts"] = append(parts, map[string]any{
		"filename": "inline.txt",
		"mimeType": "text/plain",
		"body": map[string]any{
			"data": base64.RawURLEncoding.EncodeToString(inline),
			"size": len(inline),
		},
	})
	rig := newDraftRig(t, g)

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--json")
	if code != 0 {
		t.Fatalf("resume exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Attachments []struct {
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 2 || payload.Attachments[0].Filename != "attachment-0" || payload.Attachments[1].Filename != "inline.txt" {
		t.Fatalf("carried attachments = %+v, want nameless and inline parts", payload.Attachments)
	}
	if got := strings.Join(g.attachmentRequestIDs, ","); got != "a-d1" {
		t.Fatalf("attachment requests = %q, want the external attachment only", got)
	}
}

func TestDraftResumeCanonicalizesCarriedAndFreshAttachmentsInOneIndexSpace(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.setDraftAttachmentName("d1", "")
	rig := newDraftRig(t, g)
	local := filepath.Join(t.TempDir(), "...")
	if err := os.WriteFile(local, []byte("fresh bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--attach", local, "--json")
	if code != 0 {
		t.Fatalf("resume exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Attachments []struct {
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 2 || payload.Attachments[0].Filename != "attachment-0" || payload.Attachments[1].Filename != "attachment-1" {
		t.Fatalf("attachment names = %+v, want final merged indexes", payload.Attachments)
	}
}

func TestDraftResumeOverridesWinAndAttachAppends(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	rig := newDraftRig(t, g)
	local := filepath.Join(t.TempDir(), "local.pdf")
	if err := os.WriteFile(local, []byte("%PDF-local"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--to", "b@example.test", "--attach", local, "--json")
	if code != 0 {
		t.Fatalf("exit = %d: stdout=%q stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		To []struct {
			Address string `json:"address"`
		} `json:"to"`
		Attachments []struct {
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.To) != 1 || payload.To[0].Address != "b@example.test" {
		t.Fatalf("to = %+v, want the explicit override only", payload.To)
	}
	if len(payload.Attachments) != 2 || payload.Attachments[0].Filename != "carried.pdf" || payload.Attachments[1].Filename != "local.pdf" {
		t.Fatalf("attachments = %+v, want carried then new", payload.Attachments)
	}
}

func TestDraftResumeHostileDraftRefusesSafely(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*gmailTestServer)
		rule   string
	}{
		{"crlf recipient header", func(g *gmailTestServer) { g.setDraftHeader("d1", "To", "x@example.test\r\nBcc: y@example.test") }, "R4"},
		{"crlf carried filename", func(g *gmailTestServer) { g.setDraftAttachmentName("d1", "cr\rlf.pdf") }, "R4"},
		{"crlf threading", func(g *gmailTestServer) { g.setDraftHeader("d1", "In-Reply-To", "<a@b>\r\n<c@d>") }, "R4"},
		{"whitespace body", func(g *gmailTestServer) { g.setDraftBody("d1", "   \n") }, "R5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			configureResumableDraft(g)
			c.mutate(g)
			rig := newDraftRig(t, g)
			code, stdout, _ := rig.run(t, "send", "--draft", "d1", "--json")
			if code != 1 {
				t.Fatalf("exit = %d: %q", code, stdout)
			}
			var payload struct {
				Error struct {
					Rule string `json:"rule"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Error.Rule != c.rule {
				t.Fatalf("refusal = %q (%v), want %s", stdout, err, c.rule)
			}
			if g.sendCalls() != 0 || g.draftDeletes() != 0 || len(g.draftCreates) != 0 {
				t.Fatal("refusal touched write/send custody")
			}
		})
	}
}

func TestDraftResumeConflictsAreUsageErrors(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newDraftRig(t, g)
	for _, args := range [][]string{
		{"send", "--draft", "d1", "--reply", "t1", "--body", "b"},
		{"send", "--draft", "d1", "--forward", "t1", "--body", "b"},
		{"send", "--draft", "d1", "--save-draft"},
	} {
		if code, _, stderr := rig.run(t, args...); code != 2 {
			t.Fatalf("%v exit = %d (stderr %q), want 2", args, code, stderr)
		}
	}
}

func TestDraftResumeSendRequiresPinAndRefusesRotatedDraft(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	rig := newDraftRig(t, g)
	if code, _, stderr := rig.run(t, "send", "--draft", "d1", "--send"); code != 2 || !strings.Contains(stderr, "--message") {
		t.Fatalf("pinless send exit = %d (stderr %q), want usage error demanding the pin", code, stderr)
	}
	g.rotateDraft("d1")
	code, stdout, _ := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 {
		t.Fatalf("rotated pin exit = %d: %q", code, stdout)
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Pinned  string `json:"pinned"`
			Current string `json:"current"`
			Fresh   struct {
				Message string `json:"message"`
			} `json:"fresh"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "draft_changed" || payload.Error.Pinned != "m-d1" || payload.Error.Current != "m-d1r" || payload.Error.Fresh.Message != "m-d1r" {
		t.Fatalf("draft_changed envelope = %+v", payload)
	}
	if g.sendCalls() != 0 || g.draftDeletes() != 0 {
		t.Fatal("rotated pin reached send/delete")
	}
}

func TestDraftResumeSendAcquiresWriteThenSendThenDeletes(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	rig := newDraftRig(t, g)
	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 0 {
		t.Fatalf("send exit = %d, stderr=%q", code, stderr)
	}
	var payload struct {
		DraftID string `json:"draft_id"`
		Sent    struct {
			ID string `json:"id"`
		} `json:"sent"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Sent.ID == "" || payload.DraftID != "d1" {
		t.Fatalf("payload = %q (%v)", stdout, err)
	}
	if bearers := g.sendRequestBearers(); len(bearers) != 1 || bearers[0] != "Bearer "+rig.sendToken {
		t.Fatalf("messages.send bearers = %v, want one send-token call", bearers)
	}
	if deletes := g.draftDeleteRequestBearers(); len(deletes) != 1 || deletes[0] != "Bearer "+rig.writeToken {
		t.Fatalf("drafts.delete bearers = %v, want one write-token delete after decoded success", deletes)
	}
}

func TestDraftResumeSendOnceOnGarbageResponse(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.armSendGarbage()
	rig := newDraftRig(t, g)
	code, stdout, _ := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 {
		t.Fatalf("exit = %d: %q", code, stdout)
	}
	if !hasDraftErrorCode(t, stdout, "draft_send_unknown") {
		t.Fatalf("envelope = %q", stdout)
	}
	if g.sendCalls() != 1 {
		t.Fatalf("send attempts = %d, want exactly one", g.sendCalls())
	}
	if g.draftDeletes() != 0 || !g.draftExists("d1") {
		t.Fatal("indeterminate send mutated the draft")
	}
}

func TestDraftResumeDecoded5xxIsIndeterminate(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.sendStatus = http.StatusInternalServerError
	rig := newDraftRig(t, g)
	code, stdout, _ := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 {
		t.Fatalf("exit = %d: %q", code, stdout)
	}
	if !hasDraftErrorCode(t, stdout, "draft_send_unknown") {
		t.Fatalf("decoded 5xx must be indeterminate, got %q", stdout)
	}
	if g.sendCalls() != 1 || g.draftDeletes() != 0 || !g.draftExists("d1") {
		t.Fatalf("5xx custody: sends=%d deletes=%d exists=%v, want 1/0/true", g.sendCalls(), g.draftDeletes(), g.draftExists("d1"))
	}
}

func TestDraftResumeDecoded4xxIsConcreteRejection(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.sendStatus = http.StatusBadRequest
	rig := newDraftRig(t, g)
	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 || strings.Contains(stdout, "draft_send_unknown") {
		t.Fatalf("decoded 4xx is a proven rejection, not indeterminate: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if g.sendCalls() != 1 || g.draftDeletes() != 0 || !g.draftExists("d1") {
		t.Fatalf("4xx custody: sends=%d deletes=%d exists=%v, want 1/0/true", g.sendCalls(), g.draftDeletes(), g.draftExists("d1"))
	}
}

func TestDraftResumeSendTokenReacquisitionFailureIsConcrete(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.sendStatus = http.StatusUnauthorized
	rig := newDraftRig(t, g)
	t.Setenv("DRAFT_SEND_STATE", filepath.Join(t.TempDir(), "send-state"))
	rig.setSendHelper(t, "#!/bin/sh\nif [ -e \"$DRAFT_SEND_STATE\" ]; then\n  printf 'reacquisition failed\\n' >&2\n  exit 1\nfi\ntouch \"$DRAFT_SEND_STATE\"\nprintf '%s\\n' "+rig.sendToken+"\n")

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 || strings.Contains(stdout, "draft_send_unknown") {
		t.Fatalf("pre-request token failure must remain concrete: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if g.sendCalls() != 1 || g.draftDeletes() != 0 || !g.draftExists("d1") {
		t.Fatalf("reacquisition custody: sends=%d deletes=%d exists=%v, want 1/0/true", g.sendCalls(), g.draftDeletes(), g.draftExists("d1"))
	}
}
func TestDraftResumePersistentUnauthorizedIsConcreteSendCredentialError(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.sendStatus = http.StatusUnauthorized
	g.sendPersistentStatus = true
	rig := newDraftRig(t, g)

	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 1 || strings.Contains(stdout, "draft_send_unknown") {
		t.Fatalf("persistent 401 = (%d, %q, %q), want a concrete send credential error", code, stdout, stderr)
	}
	var payload struct {
		Error struct {
			Code      string `json:"code"`
			ConfigKey string `json:"config_key"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "needs_send_credential" || payload.Error.ConfigKey != "accounts.work.send_credential_cmd" {
		t.Fatalf("persistent 401 payload = %+v, want send config guidance", payload.Error)
	}
	if g.sendCalls() != 2 || g.draftDeletes() != 0 || !g.draftExists("d1") {
		t.Fatalf("persistent 401 custody: sends=%d deletes=%d exists=%v, want rejected sends and intact draft", g.sendCalls(), g.draftDeletes(), g.draftExists("d1"))
	}
}

func TestDraftResumeDeleteFailureAfterSuccessIsWarning(t *testing.T) {
	g := newGmailTestServer(t)
	configureResumableDraft(g)
	g.draftDeleteStatus = http.StatusInternalServerError
	rig := newDraftRig(t, g)
	code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — the send succeeded", code)
	}
	var payload struct {
		Sent struct {
			ID string `json:"id"`
		} `json:"sent"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Sent.ID == "" || !strings.Contains(payload.Warning, "d1") {
		t.Fatalf("payload = %q (%v), want sent + warning naming the draft", stdout, err)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "d1") {
		t.Fatalf("delete warning stderr = %q, want a warning naming d1", stderr)
	}
}

func TestDraftResumeUnknownDraft(t *testing.T) {
	g := newGmailTestServer(t)
	rig := newDraftRig(t, g)
	code, stdout, _ := rig.run(t, "send", "--draft", "d-missing", "--json")
	if code != 1 || !strings.Contains(stdout, "draft_not_found") {
		t.Fatalf("exit=%d stdout=%q, want draft_not_found envelope", code, stdout)
	}
}

func TestDraftResumeTextModeSanitizesEveryResultPath(t *testing.T) {
	hostile := "\x1b]0;pwn\x07\x1bP+q\x1b\\ \u202eevil"
	assertCleanTerminal := func(t *testing.T, label, output string) {
		t.Helper()
		if strings.ContainsRune(output, 0x1b) || strings.ContainsRune(output, 0x07) || strings.ContainsRune(output, 0x202e) {
			t.Fatalf("%s leaked a terminal escape/control byte: %q", label, output)
		}
	}

	t.Run("dry-run preview", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureResumableDraft(g)
		g.setDraftSubject("d1", "Re: "+hostile)
		rig := newDraftRig(t, g)
		code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--text")
		if code != 0 {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		assertCleanTerminal(t, "dry-run stdout", stdout)
	})
	t.Run("refusal", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureResumableDraft(g)
		g.setDraftHeader("d1", "To", hostile+"@example.test, junk")
		rig := newDraftRig(t, g)
		code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--text")
		if code != 1 {
			t.Fatalf("exit = %d", code)
		}
		assertCleanTerminal(t, "refusal stderr", stderr)
		assertCleanTerminal(t, "refusal stdout", stdout)
	})
	t.Run("draft_changed fresh preview", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureResumableDraft(g)
		g.setDraftSubject("d1", "Re: "+hostile)
		rig := newDraftRig(t, g)
		g.rotateDraft("d1")
		code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--text")
		if code != 1 {
			t.Fatalf("exit = %d", code)
		}
		assertCleanTerminal(t, "draft_changed stderr", stderr)
		assertCleanTerminal(t, "draft_changed stdout (fresh preview)", stdout)
	})
	t.Run("delete warning", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureResumableDraft(g)
		g.setDraftSubject("d1", "Re: "+hostile)
		g.draftDeleteStatus = http.StatusInternalServerError
		rig := newDraftRig(t, g)
		code, stdout, stderr := rig.run(t, "send", "--draft", "d1", "--send", "--message", "m-d1", "--text")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		assertCleanTerminal(t, "warning stderr", stderr)
		assertCleanTerminal(t, "sent stdout", stdout)
	})
}

func hasDraftErrorCode(t *testing.T, stdout, want string) bool {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Error.Code == want
}
