package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const attachedReplyGoldenSHA256 = "630c637cdd6d1e909842594a8b8b4a3e0454e18c3d254e97b3bbe475a0412986"

func assertAttachedReplyMIME(t *testing.T, raw []byte, wantName, wantType string, wantContent []byte) {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("outer Content-Type = %q (%v), want multipart/mixed", message.Header.Get("Content-Type"), err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	body, err := parts.NextPart()
	if err != nil {
		t.Fatalf("first mixed part: %v", err)
	}
	if innerType, _, err := mime.ParseMediaType(body.Header.Get("Content-Type")); err != nil || innerType != "multipart/alternative" {
		t.Fatalf("first mixed part Content-Type = %q (%v), want multipart/alternative", body.Header.Get("Content-Type"), err)
	}
	attachment, err := parts.NextPart()
	if err != nil {
		t.Fatalf("attachment mixed part: %v", err)
	}
	attachmentType, _, err := mime.ParseMediaType(attachment.Header.Get("Content-Type"))
	_, dispositionParams, dispositionErr := mime.ParseMediaType(attachment.Header.Get("Content-Disposition"))
	if err != nil || dispositionErr != nil || attachmentType != wantType || dispositionParams["filename"] != wantName {
		t.Fatalf("attachment headers = %v; content-type error = %v; disposition error = %v", attachment.Header, err, dispositionErr)
	}
	if encoding := attachment.Header.Get("Content-Transfer-Encoding"); encoding != "base64" {
		t.Fatalf("attachment Content-Transfer-Encoding = %q, want base64", encoding)
	}
	decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	if err != nil {
		t.Fatalf("decode attachment: %v", err)
	}
	if !bytes.Equal(decoded, wantContent) {
		t.Fatalf("attachment bytes = %x, want %x", decoded, wantContent)
	}
	if _, err := parts.NextPart(); err != io.EOF {
		t.Fatalf("unexpected trailing mixed part: %v", err)
	}
}

func TestCLIAttachSendCapturesNestedMIME(t *testing.T) {
	fixture := newSendFixture(t, false)
	content := []byte("%PDF-canary-attachment-bytes-1234567890")
	path := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runBinary(t, fixture.env, "send", "--reply", "t1", "--message", "m-t1", "--body", "hi", "--attach", path, "--json")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var preview struct {
		Attachments []struct {
			Filename string `json:"filename"`
			MIMEType string `json:"mime_type"`
			SHA256   string `json:"sha256"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatalf("decode dry-run JSON: %v: %q", err, stdout)
	}
	if len(preview.Attachments) != 1 || preview.Attachments[0].Filename != "report.pdf" || preview.Attachments[0].MIMEType != "application/pdf" || preview.Attachments[0].SHA256 != attachedReplyGoldenSHA256 {
		t.Fatalf("dry-run attachments = %+v, want report.pdf application/pdf checksum %s", preview.Attachments, attachedReplyGoldenSHA256)
	}
	assertNoSpawns(t, fixture.spawnFile)
	if sends := fixture.gmail.recordedSends(); len(sends) != 0 {
		t.Fatalf("dry-run sent = %#v, want none", sends)
	}

	code, stdout, stderr = runBinary(t, fixture.env, "send", "--reply", "t1", "--message", "m-t1", "--body", "hi", "--attach", path, "--send", "--json")
	if code != 0 {
		t.Fatalf("send exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	sends := fixture.gmail.recordedSends()
	if len(sends) != 1 {
		t.Fatalf("captured sends = %#v, want one", sends)
	}
	assertAttachedReplyMIME(t, sends[0].Raw, "report.pdf", "application/pdf", content)
	assertNoCanaryOnDisk(t, string(content), fixture.stubs, fixture.cache, filepath.Dir(buildMailbox(t)))
}

func mixedPartTypes(t *testing.T, raw []byte) []string {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("outer Content-Type = %q (%v), want multipart/mixed", message.Header.Get("Content-Type"), err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	var types []string
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			return types
		}
		if err != nil {
			t.Fatalf("read mixed part: %v", err)
		}
		partType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse mixed part Content-Type %q: %v", part.Header.Get("Content-Type"), err)
		}
		types = append(types, partType)
	}
}

func TestCLIForwardAttachKeepsOriginalEML(t *testing.T) {
	fixture := newSendFixture(t, false)
	path := filepath.Join(t.TempDir(), "extra.txt")
	if err := os.WriteFile(path, []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runBinary(t, fixture.env, "send", "--forward", "t1", "--message", "m-t1", "--to", "a@example.test", "--body", "fyi", "--attach", path, "--send", "--json")
	if code != 0 {
		t.Fatalf("forward send exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	sends := fixture.gmail.recordedSends()
	if len(sends) != 1 {
		t.Fatalf("captured sends = %#v, want one", sends)
	}
	if types := mixedPartTypes(t, sends[0].Raw); strings.Join(types, ",") != "multipart/alternative,message/rfc822,text/plain" {
		t.Fatalf("forward mixed parts = %v, want [multipart/alternative message/rfc822 text/plain]", types)
	}
}

func decodeAttachmentError(t *testing.T, stdout string) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode attachment error: %v: %q", err, stdout)
	}
	return payload.Error.Code
}

func TestCLIAttachmentDownloadFlow(t *testing.T) {
	fixture := newSendFixture(t, false)
	cwd := shortTempDir(t)

	code, stdout, stderr := runBinary(t, fixture.env, "attachment", "m-att", "--json")
	if code != 0 {
		t.Fatalf("list exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var listing struct {
		Attachments []struct {
			Index    int    `json:"index"`
			Filename string `json:"filename"`
			MIMEType string `json:"mime_type"`
			Size     int64  `json:"size"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stdout), &listing); err != nil {
		t.Fatalf("decode attachment listing: %v: %q", err, stdout)
	}
	if len(listing.Attachments) != 2 || listing.Attachments[0].Index != 0 || listing.Attachments[0].Filename != "evil\u202e.pdf" || listing.Attachments[0].MIMEType != "application/pdf" || listing.Attachments[0].Size != int64(len(fixtureAttachmentBytes("a-evil"))) || listing.Attachments[1].Index != 1 || listing.Attachments[1].Filename != "report.pdf" {
		t.Fatalf("attachment listing = %+v, want two canonical zero-based rows", listing.Attachments)
	}
	messageGets := callsTo(fixture.gmail, http.MethodGet, "/gmail/v1/users/me/messages/m-att")
	if len(messageGets) != 1 || messageGets[0].Query != "format=full" {
		t.Fatalf("attachment message calls = %+v, want one full-format read", messageGets)
	}

	code, stdout, stderr = runBinaryInDir(t, cwd, fixture.env, "attachment", "m-att", "0", "--json")
	if code != 0 {
		t.Fatalf("default index fetch exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	var defaultSaved struct {
		Path     string `json:"path"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		SHA256   string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(stdout), &defaultSaved); err != nil {
		t.Fatalf("decode default save: %v: %q", err, stdout)
	}
	evilContents := fixtureAttachmentBytes("a-evil")
	evilDigest := sha256.Sum256(evilContents)
	defaultPath := filepath.Join(cwd, "evil\u202e.pdf")
	onDisk, err := os.ReadFile(defaultPath)
	if err != nil || defaultSaved.Filename != "evil\u202e.pdf" || defaultSaved.Size != int64(len(evilContents)) || defaultSaved.SHA256 != hex.EncodeToString(evilDigest[:]) || !bytes.Equal(onDisk, evilContents) {
		t.Fatalf("default index save = %+v, contents=%q, err=%v", defaultSaved, onDisk, err)
	}

	code, stdout, stderr = runBinaryInDir(t, cwd, fixture.env, "attachment", "m-att", "0", "--json")
	if code != 1 || decodeAttachmentError(t, stdout) != "attachment_exists" {
		t.Fatalf("default no-clobber = exit %d stdout=%q stderr=%q, want attachment_exists", code, stdout, stderr)
	}
	if after, err := os.ReadFile(defaultPath); err != nil || !bytes.Equal(after, evilContents) {
		t.Fatalf("default no-clobber changed existing file: %q, %v", after, err)
	}

	filePath := filepath.Join(cwd, "chosen.bin")
	code, stdout, stderr = runBinary(t, fixture.env, "attachment", "m-att", "report.pdf", "-o", filePath, "--json")
	if code != 0 {
		t.Fatalf("filename selector explicit-file fetch exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if contents, err := os.ReadFile(filePath); err != nil || !bytes.Equal(contents, fixtureAttachmentBytes("a-ok")) {
		t.Fatalf("explicit file contents = %q, err=%v", contents, err)
	}

	directoryPath := filepath.Join(cwd, "downloads")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runBinary(t, fixture.env, "attachment", "m-att", "0", "-o", directoryPath, "--json")
	if code != 0 {
		t.Fatalf("index selector directory fetch exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if contents, err := os.ReadFile(filepath.Join(directoryPath, "evil\u202e.pdf")); err != nil || !bytes.Equal(contents, evilContents) {
		t.Fatalf("directory output contents = %q, err=%v", contents, err)
	}

	ciEnv := withEnvironment(fixture.env, map[string]string{"CI": "1"})
	code, stdout, stderr = runBinaryInDir(t, cwd, ciEnv, "attachment", "m-att", "report.pdf", "-o", "-")
	if code != 0 {
		t.Fatalf("stream exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if !bytes.Equal([]byte(stdout), fixtureAttachmentBytes("a-ok")) {
		t.Fatalf("stream stdout = %x, want attachment bytes %x", stdout, fixtureAttachmentBytes("a-ok"))
	}
	if !strings.Contains(stderr, "sha256=") {
		t.Fatalf("stream status = %q, want checksum on stderr", stderr)
	}

	code, stdout, stderr = runBinary(t, fixture.env, "attachment", "m-att", "nope", "--json")
	if code != 1 || decodeAttachmentError(t, stdout) != "attachment_not_found" || !strings.Contains(stdout, "m-att") {
		t.Fatalf("unknown selector = exit %d stdout=%q stderr=%q, want attachment_not_found naming m-att", code, stdout, stderr)
	}
}

func TestTUIAttachmentPickerSavesToSessionCWD(t *testing.T) {
	fixture := newSendFixture(t, false)
	binary := buildMailbox(t)
	cwd := shortTempDir(t)
	fixture.gmail.setListPages([][]string{{"t-att"}})

	session := newTmuxSession(t, fixture.env, "sh", "-c", "cd "+shellQuote(cwd)+" && exec "+shellQuote(binary))
	session.WaitFor("Mailbox — work inbox", 15*time.Second)
	session.WaitFor("Attachments: [1]", 15*time.Second)
	session.SendEnter()
	session.WaitFor("Thread", 10*time.Second)
	session.WaitFor("Attachments", 10*time.Second)
	session.SendKeys("a")
	session.WaitFor("Attachments", 5*time.Second)
	session.WaitFor("report.pdf", 5*time.Second)
	session.SendEnter()
	session.WaitFor("saved attachment", 15*time.Second)

	contents, err := os.ReadFile(filepath.Join(cwd, "evil\u202e.pdf"))
	if err != nil || !bytes.Equal(contents, fixtureAttachmentBytes("a-evil")) {
		t.Fatalf("picker save contents = %q, err=%v; want fixture bytes in session CWD", contents, err)
	}
}
