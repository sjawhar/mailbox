package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/send"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

type readRig struct{}

func newReadRig(t *testing.T, g *gmailTestServer) *readRig {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("default_account = \"work\"\n[accounts.work]\nread_credential_env = \"CLI_READ\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILBOX_CONFIG", configPath)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("CLI_READ", "read-token-1234567890")
	g.readToken = "read-token-1234567890"
	return &readRig{}
}

func (r *readRig) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestAttachmentListIsMessageScopedZeroIndexed(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)

	code, stdout, stderr := rig.run(t, "attachment", "m-att", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON list = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var payload struct {
		Message     string `json:"message"`
		Attachments []struct {
			Index    int    `json:"index"`
			Filename string `json:"filename"`
			MIMEType string `json:"mime_type"`
			Size     int64  `json:"size"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message != "m-att" || len(payload.Attachments) != 2 ||
		payload.Attachments[0].Index != 0 || payload.Attachments[0].Filename != "evil\u202e.pdf" ||
		payload.Attachments[0].MIMEType != "application/pdf" || payload.Attachments[0].Size != 22 ||
		payload.Attachments[1].Index != 1 || payload.Attachments[1].Filename != "report.pdf" ||
		payload.Attachments[1].MIMEType != "application/pdf" || payload.Attachments[1].Size != 20 {
		t.Fatalf("JSON listing = %+v, want sanitized zero-indexed message parts", payload)
	}

	code, stdout, stderr = rig.run(t, "attachment", "m-att", "--text")
	if code != 0 || stderr != "" {
		t.Fatalf("text list = (%d, %q, %q), want success", code, stdout, stderr)
	}
	if got, want := stdout, "index\tfilename\tmime\tsize\n0\tevil.pdf\tapplication/pdf\t22\n1\treport.pdf\tapplication/pdf\t20\n"; got != want {
		t.Fatalf("text listing = %q, want %q", got, want)
	}

	code, stdout, stderr = rig.run(t, "attachment", "m-att")
	if code != 0 || stderr != "" {
		t.Fatalf("TOON list = (%d, %q, %q), want success", code, stdout, stderr)
	}
	if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
		t.Fatalf("decode attachment TOON: %v\n%s", err, stdout)
	}
}

func TestAttachmentFetchesExternalAndInlinePartBodiesWithoutEmptyAttachmentLookup(t *testing.T) {
	g := newGmailTestServer(t)
	external := attachmentFixtureBytes("a-nameless")
	inlineNamed := []byte("inline named bytes")
	inlineDisposition := []byte("inline disposition bytes")
	cidExternal := []byte("image attachment bytes")
	g.messages = map[string]map[string]any{
		"m-part-shapes": {
			"id": "m-part-shapes",
			"payload": map[string]any{"parts": []map[string]any{
				{
					"mimeType": "application/octet-stream",
					"body": map[string]any{
						"attachmentId": "a-nameless",
						"size":         len(external),
					},
				},
				{
					"filename": "inline.txt",
					"mimeType": "text/plain",
					"body": map[string]any{
						"data": base64.RawURLEncoding.EncodeToString(inlineNamed),
						"size": len(inlineNamed),
					},
				},
				{
					"mimeType": "application/octet-stream",
					"headers": []map[string]any{
						{"name": "Content-Disposition", "value": "attachment"},
					},
					"body": map[string]any{
						"data": base64.RawURLEncoding.EncodeToString(inlineDisposition),
						"size": len(inlineDisposition),
					},
				},
				{
					"filename": "photo.png",
					"mimeType": "image/png",
					"headers": []map[string]any{
						{"name": "Content-ID", "value": "<photo>"},
						{"name": "Content-Disposition", "value": `attachment; filename="photo.png"`},
					},
					"body": map[string]any{
						"attachmentId": "a-image",
						"size":         len(cidExternal),
					},
				},
			}},
		},
	}
	g.attachmentBytes["a-nameless"] = external
	g.attachmentBytes["a-image"] = cidExternal
	rig := newReadRig(t, g)

	code, stdout, stderr := rig.run(t, "attachment", "m-part-shapes", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var listing struct {
		Attachments []struct {
			Index    int    `json:"index"`
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Attachments) != 4 ||
		listing.Attachments[0].Index != 0 || listing.Attachments[0].Filename != "attachment-0" ||
		listing.Attachments[1].Index != 1 || listing.Attachments[1].Filename != "inline.txt" ||
		listing.Attachments[2].Index != 2 || listing.Attachments[2].Filename != "attachment-2" ||
		listing.Attachments[3].Index != 3 || listing.Attachments[3].Filename != "photo.png" {
		t.Fatalf("listing = %+v, want all external and inline attachment forms", listing.Attachments)
	}

	t.Chdir(t.TempDir())
	code, _, stderr = rig.run(t, "attachment", "m-part-shapes", "attachment-0", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("external fetch = (%d, %q), want success", code, stderr)
	}
	if got, err := os.ReadFile("attachment-0"); err != nil || !bytes.Equal(got, external) {
		t.Fatalf("external bytes = %q, %v", got, err)
	}
	for _, tc := range []struct {
		selector string
		want     []byte
	}{
		{"inline.txt", inlineNamed},
		{"attachment-2", inlineDisposition},
		{"photo.png", cidExternal},
	} {
		code, stdout, stderr = rig.run(t, "attachment", "m-part-shapes", tc.selector, "-o", "-", "--json")
		if code != 0 || stdout != string(tc.want) {
			t.Fatalf("part fetch %q = (%d, %q, %q), want exact bytes", tc.selector, code, stdout, stderr)
		}
	}
	if got := strings.Join(g.attachmentRequestIDs, ","); got != "a-nameless,a-image" {
		t.Fatalf("attachment requests = %q, want external attachment requests only", got)
	}
}

func TestAttachmentListEmptyIsNormal(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)

	code, stdout, stderr := rig.run(t, "attachment", "m-plain", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("empty list = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var payload struct {
		Attachments []any `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Attachments == nil || len(payload.Attachments) != 0 {
		t.Fatalf("empty listing = %q (%v), want attachments: []", stdout, err)
	}
}

func TestAttachmentFetchWritesSanitizedNoClobberWithSHA256(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := rig.run(t, "attachment", "m-att", "0", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("fetch = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var payload struct {
		Path, Filename, SHA256 string
		Size                   int64
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(attachmentFixtureBytes("a-evil"))
	if payload.Path != filepath.Join(".", "evil\u202e.pdf") || payload.Filename != "evil\u202e.pdf" || payload.Size != int64(len(attachmentFixtureBytes("a-evil"))) || payload.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("payload = %+v", payload)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, payload.Filename))
	if err != nil || !bytes.Equal(onDisk, attachmentFixtureBytes("a-evil")) {
		t.Fatalf("saved bytes = %q, %v", onDisk, err)
	}

	code, stdout, stderr = rig.run(t, "attachment", "m-att", "0", "--json")
	if code != 1 || stderr != "" || !strings.Contains(stdout, "attachment_exists") {
		t.Fatalf("no-clobber = (%d, %q, %q), want attachment_exists", code, stdout, stderr)
	}
	if after, err := os.ReadFile(filepath.Join(dir, payload.Filename)); err != nil || !bytes.Equal(after, onDisk) {
		t.Fatalf("no-clobber refusal changed existing file = %q, %v", after, err)
	}
}

func TestAttachmentFetchOutputPathAndDirectory(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)
	directory := t.TempDir()

	code, stdout, stderr := rig.run(t, "attachment", "m-att", "report.pdf", "-o", directory, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("directory output = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var directoryPayload struct {
		Path, Filename string
	}
	if err := json.Unmarshal([]byte(stdout), &directoryPayload); err != nil {
		t.Fatal(err)
	}
	if directoryPayload.Path != filepath.Join(directory, "report.pdf") || directoryPayload.Filename != "report.pdf" {
		t.Fatalf("directory payload = %+v", directoryPayload)
	}
	if contents, err := os.ReadFile(directoryPayload.Path); err != nil || !bytes.Equal(contents, attachmentFixtureBytes("a-ok")) {
		t.Fatalf("directory output = %q, %v", contents, err)
	}

	file := filepath.Join(directory, "chosen.bin")
	code, stdout, stderr = rig.run(t, "attachment", "m-att", "1", "-o", file, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("file output = (%d, %q, %q), want success", code, stdout, stderr)
	}
	if contents, err := os.ReadFile(file); err != nil || !bytes.Equal(contents, attachmentFixtureBytes("a-ok")) {
		t.Fatalf("file output = %q, %v", contents, err)
	}
}

func TestAttachmentSelectorByFilenameAndUnknownSelector(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := rig.run(t, "attachment", "m-att", "report.pdf", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("filename selector = (%d, %q, %q), want success", code, stdout, stderr)
	}
	var saved struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(stdout), &saved); err != nil || saved.Filename != "report.pdf" {
		t.Fatalf("filename selector = %q (%v)", stdout, err)
	}

	code, stdout, stderr = rig.run(t, "attachment", "m-att", "nope.bin", "--json")
	if code != 1 || stderr != "" {
		t.Fatalf("unknown selector = (%d, %q, %q), want exit 1", code, stdout, stderr)
	}
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "attachment_not_found" || !strings.Contains(refused.Error.Message, "m-att") || !strings.Contains(refused.Error.Message, "report.pdf") {
		t.Fatalf("unknown selector envelope = %+v, want code, message ID, and available parts", refused)
	}
}

func TestAttachmentDashOStreamsRawBytes(t *testing.T) {
	g := newGmailTestServer(t)
	configureAttachmentMessage(g)
	rig := newReadRig(t, g)

	code, stdout, stderr := rig.run(t, "attachment", "m-att", "report.pdf", "-o", "-", "--json")
	if code != 0 {
		t.Fatalf("stream exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if stdout != string(attachmentFixtureBytes("a-ok")) {
		t.Fatalf("stdout = %q, want exact attachment bytes", stdout)
	}
	if !strings.Contains(stderr, "report.pdf") || !strings.Contains(stderr, "sha256=") {
		t.Fatalf("status line = %q, want filename and sha256 on stderr", stderr)
	}
}

func TestSendAttachDryRunReportsAttachmentsWithoutTouchingSendCustody(t *testing.T) {
	g := newGmailTestServer(t)
	configureSendMessages(g)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	content := []byte("%PDF-1.4 fixture")
	pdf := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(pdf, content, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := rig.run(t, "send", "--reply", "t1", "--body", "hi", "--attach", pdf, "--json")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr=%q", code, stderr)
	}
	var payload struct {
		Sendable    bool `json:"sendable"`
		Attachments []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
			MIMEType string `json:"mime_type"`
			SHA256   string `json:"sha256"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode: %v: %q", err, stdout)
	}
	want := sha256.Sum256(content)
	if !payload.Sendable || len(payload.Attachments) != 1 || payload.Attachments[0].Filename != "report.pdf" ||
		payload.Attachments[0].Size != int64(len(content)) || payload.Attachments[0].SHA256 != hex.EncodeToString(want[:]) ||
		payload.Attachments[0].MIMEType != "application/pdf" {
		t.Fatalf("attachments = %+v", payload.Attachments)
	}
	if got := rig.spawns(t); got != "" || len(g.sentBodies) != 0 {
		t.Fatalf("dry-run touched custody: spawns=%q sent=%v", got, g.sentBodies)
	}
}

func TestSendAttachRefusalsRenderAsEnvelopes(t *testing.T) {
	g := newGmailTestServer(t)
	configureSendMessages(g)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tooLarge := filepath.Join(dir, "too-large.bin")
	if err := os.WriteFile(tooLarge, bytes.Repeat([]byte("x"), send.MaxOutboundMessageBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, code, rule string
	}{
		{name: "unreadable", path: filepath.Join(dir, "missing.bin"), code: "attachment_unreadable", rule: "R-A1"},
		{name: "empty", path: empty, code: "attachment_empty", rule: "R-A2"},
		{name: "too large", path: tooLarge, code: "attachment_too_large", rule: "R-A3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := rig.run(t, "send", "--reply", "t1", "--body", "hi", "--attach", tc.path, "--json")
			if code != 1 {
				t.Fatalf("exit = %d, stdout=%q stderr=%q, want 1", code, stdout, stderr)
			}
			var payload map[string]map[string]any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["error"]["code"] != tc.code || payload["error"]["rule"] != tc.rule {
				t.Fatalf("refusal = %#v", payload)
			}
			if got := rig.spawns(t); got != "" || len(g.sentBodies) != 0 {
				t.Fatalf("refusal touched send custody: spawns=%q sent=%v", got, g.sentBodies)
			}
		})
	}
}
