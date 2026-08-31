package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if got, want := stdout, "index\tfilename\tmime\tsize\n0\tevil\u202e.pdf\tapplication/pdf\t22\n1\treport.pdf\tapplication/pdf\t20\n"; got != want {
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
