package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/send"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

func TestSendReplyRequiresPinnedMessage(t *testing.T) {
	g := newGmailTestServer(t)
	code, stdout, stderr := runCLI(t, g, "send", "--send", "--reply=t1", "--body=x")
	if code != 2 || !strings.Contains(stderr, "run the dry-run first") {
		t.Fatalf("send without a reply pin = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
}

const cliSendToken = "clisendtoken1234567890"

type sendRig struct {
	configPath string
	helperPath string
	spawnLog   string
}

func newSendRig(t *testing.T, g *gmailTestServer, accountConfig string) *sendRig {
	t.Helper()
	dir := t.TempDir()
	rig := &sendRig{
		configPath: filepath.Join(dir, "config.toml"),
		helperPath: filepath.Join(dir, "send-helper"),
		spawnLog:   filepath.Join(dir, "send-spawns"),
	}
	config := "default_account = \"work\"\n[accounts.work]\nread_credential_env = \"CLI_READ\"\n" + accountConfig
	if err := os.WriteFile(rig.configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	rig.setHelper(t, fmt.Sprintf("#!/bin/sh\nprintf 'spawn\\n' >> %q\nprintf '%%s\\n' %q\n", rig.spawnLog, cliSendToken))
	t.Setenv("MAILBOX_CONFIG", rig.configPath)
	t.Setenv("MAILBOX_GMAIL_BASE_URL", g.server.URL)
	t.Setenv("MAILBOX_CACHE_DIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "test-token")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	g.writeToken = cliSendToken
	return rig
}

func (r *sendRig) setHelper(t *testing.T, script string) {
	t.Helper()
	if err := os.WriteFile(r.helperPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (r *sendRig) spawns(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.spawnLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (r *sendRig) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runConfiguredSend(args...)
}

func nonInteractiveSendSource() string {
	return "send_credential_cmd = [\"send-helper\"]\nsend_interactive = false\n"
}

func interactiveSendSource() string {
	return "send_credential_cmd = [\"send-helper\"]\nsend_interactive = true\n"
}

func runConfiguredSend(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func configureSendMessages(g *gmailTestServer) {
	older := sendFixtureMessage(
		"m-t1",
		"t1",
		"1780000000000",
		[]map[string]string{
			{"name": "From", "value": "Alice <alice@example.test>"},
			{"name": "To", "value": "user@example.com, Bob <bob@example.test>"},
			{"name": "Cc", "value": "Carol <carol@example.test>"},
			{"name": "Subject", "value": "Re: Quarterly"},
			{"name": "Message-ID", "value": "<orig-1@example.test>"},
			{"name": "References", "value": "<root@example.test>"},
		},
	)
	newest := sendFixtureMessage(
		"m-t1-new",
		"t1",
		"1780000001000",
		[]map[string]string{
			{"name": "From", "value": "Newest <newest@example.test>"},
			{"name": "To", "value": "user@example.com"},
			{"name": "Subject", "value": "New subject"},
			{"name": "Message-ID", "value": "<new@example.test>"},
		},
	)
	g.messages = map[string]map[string]any{
		"m-t1":     older,
		"m-t1-new": newest,
		"m-other":  sendFixtureMessage("m-other", "other-thread", "1780000002000", nil),
	}
	g.rawMessages = map[string][]byte{
		"m-t1":     []byte("From: Alice <alice@example.test>\r\nSubject: Quarterly\r\n\r\ncomplete original"),
		"m-t1-new": []byte("From: Newest <newest@example.test>\r\nSubject: New subject\r\n\r\nnew original"),
	}
	g.thread = map[string]any{"id": "t1", "messages": []map[string]any{older, newest}}
}

func configureSelfOnlyMessage(g *gmailTestServer) {
	self := sendFixtureMessage(
		"m-t1",
		"t1",
		"1780000000000",
		[]map[string]string{
			{"name": "From", "value": "user@example.com"},
			{"name": "To", "value": "user@example.com"},
			{"name": "Cc", "value": "user@example.com"},
			{"name": "Subject", "value": "Quarterly"},
			{"name": "Message-ID", "value": "<orig-1@example.test>"},
		},
	)
	g.messages = map[string]map[string]any{"m-t1": self}
	g.thread = map[string]any{"id": "t1", "messages": []map[string]any{self}}
}

func configureDivergentReplyToMessage(g *gmailTestServer) {
	message := sendFixtureMessage(
		"m-t1",
		"t1",
		"1780000000000",
		[]map[string]string{
			{"name": "From", "value": "Alice <alice@example.test>"},
			{"name": "Reply-To", "value": "List <list@example.test>"},
			{"name": "To", "value": "user@example.com"},
			{"name": "Subject", "value": "Quarterly"},
			{"name": "Message-ID", "value": "<orig-1@example.test>"},
		},
	)
	g.messages = map[string]map[string]any{"m-t1": message}
	g.thread = map[string]any{"id": "t1", "messages": []map[string]any{message}}
}

func sendFixtureMessage(id, threadID, internalDate string, headers []map[string]string) map[string]any {
	return map[string]any{
		"id":           id,
		"threadId":     threadID,
		"internalDate": internalDate,
		"payload": map[string]any{
			"headers": headers,
		},
	}
}

func targetHeaders() *send.TargetHeaders {
	return &send.TargetHeaders{
		From:       "Alice <alice@example.test>",
		To:         "user@example.com, Bob <bob@example.test>",
		Cc:         "Carol <carol@example.test>",
		Subject:    "Re: Quarterly",
		MessageID:  "<orig-1@example.test>",
		References: "<root@example.test>",
	}
}

func TestSendPinningAndPreviews(t *testing.T) {
	t.Run("message outside the thread is refused in every format", func(t *testing.T) {
		for _, format := range []string{"--json", "", "--text"} {
			t.Run(strings.TrimPrefix(format, "--"), func(t *testing.T) {
				g := newGmailTestServer(t)
				configureSendMessages(g)
				rig := newSendRig(t, g, nonInteractiveSendSource())
				args := []string{"send", "--reply=t1", "--message=m-other", "--body=x"}
				if format != "" {
					args = append(args, format)
				}
				code, stdout, stderr := runConfiguredSend(args...)
				if code != 1 {
					t.Fatalf("not-in-thread exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
				}
				switch format {
				case "--json":
					var payload map[string]map[string]any
					if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
						t.Fatalf("decode JSON refusal: %v", err)
					}
					if payload["error"]["code"] != "message_not_in_thread" {
						t.Fatalf("refusal = %#v", payload)
					}
				case "--text":
					if !strings.Contains(stderr, "m-other") || !strings.Contains(stderr, "t1") {
						t.Fatalf("text refusal = %q, want both message and thread ids", stderr)
					}
				default:
					if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil || !strings.Contains(stdout, "message_not_in_thread") {
						t.Fatalf("TOON refusal = %q, decode error = %v", stdout, err)
					}
				}
				if rig.spawns(t) != "" || len(g.sentBodies) != 0 {
					t.Fatalf("not-in-thread touched send custody: spawns=%q sent=%v", rig.spawns(t), g.sentBodies)
				}
			})
		}
	})

	t.Run("pinned older message determines the envelope", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		newSendRig(t, g, nonInteractiveSendSource())
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("pinned preview = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["message"] != "m-t1" || payload["subject"] != "Re: Quarterly" {
			t.Fatalf("pinned payload = %#v", payload)
		}
		to, ok := payload["to"].([]any)
		if !ok || len(to) == 0 || to[0].(map[string]any)["address"] != "alice@example.test" {
			t.Fatalf("pinned recipients = %#v", payload["to"])
		}
	})

	t.Run("unpinned preview selects and prints the newest message", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		newSendRig(t, g, nonInteractiveSendSource())
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--body=x", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("preview = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["message"] != "m-t1-new" {
			t.Fatalf("preview message = %#v, want newest m-t1-new", payload["message"])
		}
	})

	t.Run("compose sends without a message pin", func(t *testing.T) {
		g := newGmailTestServer(t)
		newSendRig(t, g, nonInteractiveSendSource())
		code, stdout, stderr := runConfiguredSend("send", "--to=dest@example.test", "--subject=Hello", "--body=x", "--send", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("compose send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		if len(g.sentBodies) != 1 {
			t.Fatalf("compose sends = %v, want one", g.sentBodies)
		}
	})
}

func TestSendDryRunDoesNotTouchSendCredential(t *testing.T) {
	g := newGmailTestServer(t)
	configureSendMessages(g)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("dry-run = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if got := rig.spawns(t); got != "" {
		t.Fatalf("dry-run spawned send helper: %q", got)
	}
	if len(g.sentBodies) != 0 {
		t.Fatalf("dry-run sent requests: %v", g.sentBodies)
	}
}

func TestSendHappyPathPostsResolvedMIME(t *testing.T) {
	g := newGmailTestServer(t)
	configureSendMessages(g)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if got := rig.spawns(t); strings.Count(got, "spawn\n") != 1 {
		t.Fatalf("send helper spawns = %q, want one", got)
	}
	if len(g.sentBodies) != 1 {
		t.Fatalf("send requests = %v, want one", g.sentBodies)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sendable"] != true || payload["sent"].(map[string]any)["id"] != "sent-1" {
		t.Fatalf("send payload = %#v", payload)
	}
	if g.sentBodies[0]["threadId"] != "t1" {
		t.Fatalf("send threadId = %#v, want t1", g.sentBodies[0]["threadId"])
	}
	gotRaw, err := base64.RawURLEncoding.DecodeString(g.sentBodies[0]["raw"].(string))
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(gotRaw))
	if err != nil {
		t.Fatalf("parse sent MIME: %v", err)
	}
	for name, want := range map[string]string{
		"To":          `"Alice" <alice@example.test>`,
		"Cc":          `"Bob" <bob@example.test>, "Carol" <carol@example.test>`,
		"Subject":     "Re: Quarterly",
		"In-Reply-To": "<orig-1@example.test>",
		"References":  "<root@example.test> <orig-1@example.test>",
	} {
		if got := message.Header.Get(name); got != want {
			t.Fatalf("%s header = %q, want %q", name, got, want)
		}
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q (%v), want multipart/alternative with a boundary", message.Header.Get("Content-Type"), err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	for _, want := range []struct {
		contentType string
		body        string
	}{
		{"text/plain; charset=UTF-8", "x"},
		{"text/html; charset=UTF-8", "<p>x</p>\n"},
	} {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatalf("read alternative part: %v", err)
		}
		if part.Header.Get("Content-Type") != want.contentType || part.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("alternative part headers = %#v", part.Header)
		}
		encoded, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(encoded)))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(decoded); got != want.body {
			t.Fatalf("decoded alternative part = %q, want %q", got, want.body)
		}
	}
	if part, err := parts.NextPart(); err != io.EOF || part != nil {
		t.Fatalf("NextPart() after alternative leaves = %v, %v; want EOF", part, err)
	}
}

func TestSendRefusalsRenderThroughEveryFormat(t *testing.T) {
	cases := []struct {
		name      string
		rule      string
		configure func(*gmailTestServer)
		args      []string
	}{
		{
			name: "R1 empty recipients",
			rule: "R1",
			args: []string{"send", "--subject=s", "--body=x"},
		},
		{
			name:      "R2 self-only reply",
			rule:      "R2",
			configure: configureSelfOnlyMessage,
			args:      []string{"send", "--reply=t1", "--message=m-t1", "--body=x"},
		},
		{
			name: "R3 invalid address",
			rule: "R3",
			args: []string{"send", "--to=not an address", "--subject=s", "--body=x"},
		},
		{
			name: "R4 header injection",
			rule: "R4",
			args: []string{"send", "--to=dest@example.test", "--subject=bad\nsubject", "--body=x"},
		},
		{
			name: "R5 empty body",
			rule: "R5",
			args: []string{"send", "--to=dest@example.test", "--subject=s", "--body= \t"},
		},
		{
			name:      "R6 divergent reply-to",
			rule:      "R6",
			configure: configureDivergentReplyToMessage,
			args:      []string{"send", "--reply=t1", "--message=m-t1", "--body=x"},
		},
	}
	formats := []string{"--json", "", "--text"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range formats {
				t.Run(strings.TrimPrefix(format, "--"), func(t *testing.T) {
					g := newGmailTestServer(t)
					if tc.configure != nil {
						tc.configure(g)
					}
					rig := newSendRig(t, g, nonInteractiveSendSource())
					args := append([]string(nil), tc.args...)
					if format != "" {
						args = append(args, format)
					}
					code, stdout, stderr := runConfiguredSend(args...)
					if code != 1 {
						t.Fatalf("refusal exit = %d, stdout=%q, stderr=%q", code, stdout, stderr)
					}
					switch format {
					case "--json":
						var payload map[string]map[string]any
						if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
							t.Fatalf("decode JSON refusal: %v", err)
						}
						if payload["error"]["rule"] != tc.rule {
							t.Fatalf("JSON refusal = %#v", payload)
						}
					case "--text":
						if !strings.Contains(stderr, "("+tc.rule+")") {
							t.Fatalf("text refusal = %q, want %s", stderr, tc.rule)
						}
					default:
						if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil || !strings.Contains(stdout, tc.rule) {
							t.Fatalf("TOON refusal = %q, decode error = %v", stdout, err)
						}
					}
					if got := rig.spawns(t); got != "" || len(g.sentBodies) != 0 {
						t.Fatalf("refusal touched send custody: spawns=%q sent=%v", got, g.sentBodies)
					}
				})
			}
		})
	}
}

func TestSendR2BlocksTransmission(t *testing.T) {
	g := newGmailTestServer(t)
	configureSelfOnlyMessage(g)
	rig := newSendRig(t, g, nonInteractiveSendSource())
	for _, sendFlag := range []bool{false, true} {
		args := []string{"send", "--reply=t1", "--message=m-t1", "--body=x", "--json"}
		if sendFlag {
			args = append(args, "--send")
		}
		code, stdout, stderr := runConfiguredSend(args...)
		if code != 1 || stderr != "" {
			t.Fatalf("R2 send=%t = %d, stdout=%q, stderr=%q", sendFlag, code, stdout, stderr)
		}
		var payload map[string]map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["error"]["rule"] != "R2" {
			t.Fatalf("R2 payload = %#v", payload)
		}
	}
	if got := rig.spawns(t); got != "" || len(g.sentBodies) != 0 {
		t.Fatalf("R2 touched send custody: spawns=%q sent=%v", got, g.sentBodies)
	}
}

func TestSendScopeWarning(t *testing.T) {
	t.Run("broad minted scope is surfaced", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		rig := newSendRig(t, g, nonInteractiveSendSource())
		rig.setHelper(t, fmt.Sprintf("#!/bin/sh\nprintf 'spawn\\n' >> %q\nprintf '%%s\\n' '{\"access_token\":\"%s\",\"expiry\":\"2050-01-01T00:00:00Z\",\"scope\":\"gmail.send gmail.readonly\"}'\n", rig.spawnLog, cliSendToken))
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
		if code != 0 || !strings.Contains(stderr, "granted scope: gmail.send gmail.readonly") {
			t.Fatalf("broad scope send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(payload["warning"].(string), "accounts.work.send_credential_cmd") {
			t.Fatalf("scope warning = %#v", payload["warning"])
		}
	})

	t.Run("broad scope has a text warning line", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		rig := newSendRig(t, g, nonInteractiveSendSource())
		rig.setHelper(t, fmt.Sprintf("#!/bin/sh\nprintf 'spawn\\n' >> %q\nprintf '%%s\\n' '{\"access_token\":\"%s\",\"expiry\":\"2050-01-01T00:00:00Z\",\"scope\":\"gmail.send gmail.readonly\"}'\n", rig.spawnLog, cliSendToken))
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--text")
		if code != 0 || !strings.Contains(stderr, "granted scope: gmail.send gmail.readonly") {
			t.Fatalf("text scope send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "warning: granted scope exceeds gmail.send; de-scope the credential behind accounts.work.send_credential_cmd when ready") {
			t.Fatalf("text scope warning = %q", stdout)
		}
	})

	t.Run("exact minted scope has no warning", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		rig := newSendRig(t, g, nonInteractiveSendSource())
		rig.setHelper(t, fmt.Sprintf("#!/bin/sh\nprintf 'spawn\\n' >> %q\nprintf '%%s\\n' '{\"access_token\":\"%s\",\"expiry\":\"2050-01-01T00:00:00Z\",\"scope\":\"https://www.googleapis.com/auth/gmail.send\"}'\n", rig.spawnLog, cliSendToken))
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
		if code != 0 || !strings.Contains(stderr, "granted scope: https://www.googleapis.com/auth/gmail.send") {
			t.Fatalf("exact scope send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["warning"]; ok {
			t.Fatalf("exact scope warning = %#v", payload["warning"])
		}
	})
}

func TestSendNeedsCredentialAndBatchInteractiveSources(t *testing.T) {
	t.Run("MAILBOX_TOKEN cannot provide a send credential", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		g.readToken = "decoy"
		rig := newSendRig(t, g, "")
		t.Setenv("MAILBOX_TOKEN", "decoy")
		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
		if code != 1 || stderr != "" {
			t.Fatalf("missing send source = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["error"]["code"] != "needs_send_credential" {
			t.Fatalf("missing send source payload = %#v", payload)
		}
		if rig.spawns(t) != "" || len(g.sentBodies) != 0 {
			t.Fatalf("MAILBOX_TOKEN route touched send custody: spawns=%q sent=%v", rig.spawns(t), g.sentBodies)
		}
	})

	t.Run("interactive send source transmits in batch without inheriting pipe stdin", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		rig := newSendRig(t, g, interactiveSendSource())
		stdinRecord := filepath.Join(filepath.Dir(rig.helperPath), "send-stdin")
		rig.setHelper(t, fmt.Sprintf(`#!/bin/sh
printf 'spawn\n' >> %q
if [ -t 0 ]; then
  printf 'tty\n' > %q
elif IFS= read -r value; then
  printf 'not-tty:read:%%s\n' "$value" > %q
else
  printf 'not-tty:eof\n' > %q
fi
printf '%%s\n' %q
`, rig.spawnLog, stdinRecord, stdinRecord, stdinRecord, cliSendToken))
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		originalStdin := os.Stdin
		os.Stdin = reader
		t.Cleanup(func() {
			os.Stdin = originalStdin
			_ = reader.Close()
			_ = writer.Close()
		})
		if _, err := writer.Write([]byte("caller pipe must not reach helper\n")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("interactive batch send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatal(err)
		}
		if sent, ok := payload["sent"].(map[string]any); !ok || sent["id"] != "sent-1" {
			t.Fatalf("interactive payload = %#v", payload)
		}
		if got := rig.spawns(t); strings.Count(got, "spawn\n") != 1 || len(g.sentBodies) != 1 {
			t.Fatalf("interactive batch custody = spawns:%q sent:%v", got, g.sentBodies)
		}
		data, err := os.ReadFile(stdinRecord)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), "not-tty:eof\n"; got != want {
			t.Fatalf("interactive send helper stdin = %q, want %q", got, want)
		}
	})
}

func TestSendFormatsAndForward(t *testing.T) {
	t.Run("dry-run formats", func(t *testing.T) {
		for _, format := range []string{"", "--json", "--text"} {
			t.Run(strings.TrimPrefix(format, "--"), func(t *testing.T) {
				g := newGmailTestServer(t)
				configureSendMessages(g)
				newSendRig(t, g, nonInteractiveSendSource())
				args := []string{"send", "--reply=t1", "--message=m-t1", "--body=x"}
				if format != "" {
					args = append(args, format)
				}
				code, stdout, stderr := runConfiguredSend(args...)
				if code != 0 || stderr != "" {
					t.Fatalf("format preview = %d, stdout=%q, stderr=%q", code, stdout, stderr)
				}
				switch format {
				case "--json":
					var payload map[string]any
					if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
						t.Fatal(err)
					}
				case "--text":
					if !strings.Contains(stdout, "to  alice@example.test") {
						t.Fatalf("text preview omitted fixed address column: %q", stdout)
					}
				default:
					if _, err := toontest.Decode(strings.TrimSuffix(stdout, "\n")); err != nil {
						t.Fatalf("TOON preview = %q, decode error = %v", stdout, err)
					}
				}
			})
		}
	})

	t.Run("forward attaches the unmodified raw original without a thread id", func(t *testing.T) {
		g := newGmailTestServer(t)
		configureSendMessages(g)
		newSendRig(t, g, nonInteractiveSendSource())
		code, stdout, stderr := runConfiguredSend("send", "--forward=t1", "--to=dest@example.test", "--message=m-t1", "--body=fyi", "--send", "--text")
		if code != 0 || stderr != "" {
			t.Fatalf("forward send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "attaches the complete original ("+fmt.Sprint(len(g.rawMessages["m-t1"]))+" bytes)") {
			t.Fatalf("forward disclosure = %q", stdout)
		}
		if len(g.sentBodies) != 1 {
			t.Fatalf("forward sends = %v", g.sentBodies)
		}
		if _, ok := g.sentBodies[0]["threadId"]; ok {
			t.Fatalf("forward payload unexpectedly has threadId: %#v", g.sentBodies[0])
		}
		raw, err := base64.RawURLEncoding.DecodeString(g.sentBodies[0]["raw"].(string))
		if err != nil {
			t.Fatal(err)
		}
		message, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/mixed" {
			t.Fatalf("forward content type = %q, error = %v", message.Header.Get("Content-Type"), err)
		}
		parts := multipart.NewReader(message.Body, params["boundary"])
		if _, err := parts.NextPart(); err != nil {
			t.Fatalf("read forward body part: %v", err)
		}
		originalPart, err := parts.NextPart()
		if err != nil {
			t.Fatalf("read original part: %v", err)
		}
		if originalPart.Header.Get("Content-Type") != "message/rfc822" || originalPart.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("original part headers = %#v", originalPart.Header)
		}
		encoded, err := io.ReadAll(originalPart)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(encoded), "\r\n", ""))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, g.rawMessages["m-t1"]) {
			t.Fatalf("forwarded original = %q, want %q", decoded, g.rawMessages["m-t1"])
		}
	})
}

func runConfiguredSendWithInput(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	cfg, err := auth.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cc := &cmdCtx{
		stdin:  strings.NewReader(input),
		stdout: &stdout,
		stderr: &stderr,
		cfg:    cfg,
	}
	return runSend(cc, args), stdout.String(), stderr.String()
}

func TestSendBodyReadsStdinIntoMIME(t *testing.T) {
	g := newGmailTestServer(t)
	newSendRig(t, g, nonInteractiveSendSource())
	code, stdout, stderr := runConfiguredSendWithInput(t, "from stdin", "--to=dest@example.test", "--subject=Hello", "--body=-", "--send", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("stdin send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if len(g.sentBodies) != 1 {
		t.Fatalf("stdin send requests = %v", g.sentBodies)
	}
	raw, err := base64.RawURLEncoding.DecodeString(g.sentBodies[0]["raw"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ZnJvbSBzdGRpbg==") {
		t.Fatalf("stdin body is absent from MIME: %q", raw)
	}
}

func TestSendRetriesOnceAfterAnExpiredSendToken(t *testing.T) {
	g := newGmailTestServer(t)
	configureSendMessages(g)
	g.sendStatus = 401
	rig := newSendRig(t, g, nonInteractiveSendSource())
	code, stdout, stderr := runConfiguredSend("send", "--reply=t1", "--message=m-t1", "--body=x", "--send", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("retry send = %d, stdout=%q, stderr=%q", code, stdout, stderr)
	}
	if len(g.sentBodies) != 2 {
		t.Fatalf("send attempts = %d, want 2", len(g.sentBodies))
	}
	if got := strings.Count(rig.spawns(t), "spawn\n"); got != 2 {
		t.Fatalf("send helper spawns = %d, want 2", got)
	}
}

func TestSendGrammar(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "reply and forward conflict",
			args: []string{"send", "--reply=t1", "--forward=t1", "--body=x"},
			want: "mutually exclusive",
		},
		{
			name: "derived subject cannot be provided",
			args: []string{"send", "--reply=t1", "--subject=s", "--body=x"},
			want: "only valid for compose",
		},
		{
			name: "compose requires a subject",
			args: []string{"send", "--to=dest@example.test", "--body=x"},
			want: "compose requires --subject",
		},
		{
			name: "body is required",
			args: []string{"send", "--to=dest@example.test", "--subject=s"},
			want: "send requires --body",
		},
		{
			name: "compose forbids message",
			args: []string{"send", "--to=dest@example.test", "--subject=s", "--body=x", "--message=m1"},
			want: "only valid with --reply or --forward",
		},
		{
			name: "positional arguments are refused",
			args: []string{"send", "--to=dest@example.test", "--subject=s", "--body=x", "extra"},
			want: "requires 0 argument",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGmailTestServer(t)
			newSendRig(t, g, nonInteractiveSendSource())
			code, stdout, stderr := runConfiguredSend(tc.args...)
			if code != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%s = %d, stdout=%q, stderr=%q", tc.name, code, stdout, stderr)
			}
			doc, err := toontest.Decode(strings.TrimSuffix(stdout, "\n"))
			if err != nil {
				t.Fatalf("decode TOON usage envelope %q: %v", stdout, err)
			}
			errObj := toonField(t, doc, "error")
			if got := toonString(t, errObj, "code"); got != "usage" {
				t.Fatalf("error.code = %q, want usage", got)
			}
			if got := toonString(t, errObj, "message"); !strings.Contains(got, tc.want) {
				t.Fatalf("error.message = %q, want containing %q", got, tc.want)
			}
		})
	}
}

func TestSendBodyFileAndStdinSources(t *testing.T) {
	g := newGmailTestServer(t)
	newSendRig(t, g, nonInteractiveSendSource())
	bodyPath := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(bodyPath, []byte("drafted in a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("body-file reads the file", func(t *testing.T) {
		code, stdout, stderr := runConfiguredSendWithInput(t, "", "--to=a@example.test", "--subject=s", "--body-file="+bodyPath, "--text")
		if code != 0 {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "body: 18 bytes") {
			t.Fatalf("stdout = %q, want file-sized body", stdout)
		}
	})

	t.Run("body-file dash reads stdin", func(t *testing.T) {
		code, stdout, stderr := runConfiguredSendWithInput(t, "from stdin", "--to=a@example.test", "--subject=s", "--body-file=-", "--text")
		if code != 0 {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "body: 10 bytes") {
			t.Fatalf("stdout = %q, want stdin-sized body", stdout)
		}
	})

	t.Run("body and body-file are mutually exclusive", func(t *testing.T) {
		code, _, stderr := runConfiguredSendWithInput(t, "", "--to=a@example.test", "--subject=s", "--body=x", "--body-file="+bodyPath)
		if code != 2 || !strings.Contains(stderr, "mutually exclusive") {
			t.Fatalf("exit=%d stderr=%q, want usage refusal", code, stderr)
		}
	})

	t.Run("missing body-file is a runtime error naming the flag", func(t *testing.T) {
		code, _, stderr := runConfiguredSendWithInput(t, "", "--to=a@example.test", "--subject=s", "--body-file="+filepath.Join(t.TempDir(), "absent"))
		if code == 0 || !strings.Contains(stderr, "--body-file") {
			t.Fatalf("exit=%d stderr=%q, want failure naming --body-file", code, stderr)
		}
	})
}
