package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/render"
	"github.com/sjawhar/mailbox/internal/send"
	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

func TestResolveFormatMatrix(t *testing.T) {
	cases := []struct {
		json, text, agent, tty bool
		want                   Format
	}{
		{json: true, text: true, agent: true, tty: true, want: FormatJSON},
		{text: true, agent: true, tty: false, want: FormatText},
		{agent: true, tty: true, want: FormatTOON},
		{agent: false, tty: false, want: FormatTOON},
		{agent: false, tty: true, want: FormatText},
	}
	for _, c := range cases {
		if got := ResolveFormat(c.json, c.text, c.agent, c.tty); got != c.want {
			t.Fatalf("ResolveFormat(%v, %v, %v, %v) = %v, want %v", c.json, c.text, c.agent, c.tty, got, c.want)
		}
	}
}

func TestAgentEnvironmentPresenceNotValue(t *testing.T) {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE", "OPENCODE", "AGENT", "CI"} {
		t.Run(name, func(t *testing.T) {
			for _, v := range []string{"CLAUDECODE", "CLAUDE_CODE", "OPENCODE", "AGENT", "CI"} {
				if current, ok := os.LookupEnv(v); ok {
					t.Setenv(v, current)
				}
				os.Unsetenv(v)
			}
			t.Setenv(name, "")
			if !agentEnvironment() {
				t.Fatalf("%s present (empty) not detected", name)
			}
		})
	}
}

func TestToontestMirrorsMatchRealPayloads(t *testing.T) {
	real := []any{
		listingPayload{Account: "s1", Filter: "s2", Threads: []threadRow{{N: 1, ID: "s2", Subject: "s3", From: "s1", Date: "s2", Snippet: "s3", Unread: true, Labels: []string{"s1"}}}},
		readPayloadSample("s1", "s2", "s3"),
		statusSample("s1", "s2", "s3"),
		actionPayload{Account: "s1", Action: "s2", ThreadIDs: []string{"s3"}, OK: true},
		filterActionPayload{Account: "s1", Action: "s2", Filter: "s3", Matched: 1, Attempted: 1, Succeeded: []string{"s1"}, Failed: []filterActionFailure{{ID: "s2", Status: 7, Reason: "s3"}}, OK: true},
		attachmentListSample("s1", "s2", "s3"),
		attachmentSavePayload{Account: "s1", Path: "s2", Filename: "s3", Size: 7, SHA256: "s1"},
		draftsPayloadSample("s1", "s2", "s3"),
		openPayload{Account: "s1", ThreadID: "s2", MessageID: "s3", File: "s1"},
		errorEnvelopeSample("s1", "s2", "s3"),
		cliErrorPayloadSample("s1", "s2", "s3"),
		draftChangedPayloadSample("s1", "s2", "s3"),
		usageErrorPayloadSample("s1", "s2"),
		envelopePayloadSample("s1", "s2", "s3"),
	}
	mirrors := toontest.Shapes("s1", "s2", "s3")
	if len(real) != len(mirrors) {
		t.Fatalf("payload shape count drifted: cli %d, toontest %d — update toontest.Shapes", len(real), len(mirrors))
	}
	for index := range real {
		want, err := json.Marshal(real[index])
		if err != nil {
			t.Fatalf("marshal cli payload %d: %v", index, err)
		}
		got, err := json.Marshal(mirrors[index])
		if err != nil {
			t.Fatalf("marshal toontest payload %d: %v", index, err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("mirror %d diverged:\nreal:   %s\nmirror: %s", index, want, got)
		}
	}
	for index := range real {
		want := jsonLeafPaths(reflect.TypeOf(real[index]), "")
		got := jsonLeafPaths(reflect.TypeOf(mirrors[index]), "")
		if !maps.Equal(want, got) {
			t.Fatalf("payload %d JSON shape diverged — update toontest.Shapes:\nreal:   %v\nmirror: %v", index, want, got)
		}
	}
}

// jsonLeafPaths maps every JSON key path a type can marshal to its leaf kind.
// The sample-value comparison above cannot catch a NEW field added to a real
// payload (zero values vanish under omitempty), and the mirrors deliberately
// flatten exported nested types, so the contract is JSON SHAPE identity at the
// type level: same key paths, same leaf kinds, regardless of struct layout.
// This is what lets the TOON property suite mutate mirrors as a stand-in for
// the real private payload types without drift risk.
func jsonLeafPaths(t reflect.Type, prefix string) map[string]string {
	paths := map[string]string{}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				if field.Anonymous {
					// Embedded field without a tag: JSON promotes its
					// fields into the enclosing object.
					maps.Copy(paths, jsonLeafPaths(field.Type, prefix))
					continue
				}
				name = field.Name
			}
			maps.Copy(paths, jsonLeafPaths(field.Type, prefix+"."+name))
		}
	case reflect.Slice, reflect.Array:
		maps.Copy(paths, jsonLeafPaths(t.Elem(), prefix+"[]"))
	case reflect.Map:
		maps.Copy(paths, jsonLeafPaths(t.Elem(), prefix+"{}"))
	default:
		paths[prefix] = t.Kind().String()
	}
	return paths
}

func readPayloadSample(s1, s2, s3 string) readPayload {
	return readPayload{
		Account: s1,
		RenderedThread: &render.RenderedThread{
			ID:           s2,
			Subject:      s3,
			Participants: []string{s1},
			Messages: []render.RenderedMessage{{
				ID:          s2,
				From:        s3,
				To:          s1,
				Date:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				Markdown:    s2,
				Links:       []render.Link{{N: 1, Text: s3, URL: s1}},
				Attachments: []render.Attachment{{N: 1, Filename: s2, MimeType: s3, Size: 7}},
			}},
		},
	}
}

func statusSample(s1, s2, s3 string) statusOutput {
	return statusOutput{
		Config: s1,
		Accounts: []statusAccount{{
			Name:    s2,
			Default: true,
			Read:    statusSource{Kind: s3, Argv0: s1, Interactive: true, Label: s2},
			Write:   statusSource{Kind: s3, Argv0: s1, Interactive: true, Label: s2},
			Route:   s3,
			Cache:   statusCache{Path: s1, Exists: true, Valid: true, Expiry: s2},
			Profile: statusProfile{Email: s3},
			Pinned:  true,
			Error:   s1,
		}},
		OK: true,
	}
}

func attachmentListSample(s1, s2, s3 string) attachmentListPayload {
	return attachmentListPayload{
		Account:     s1,
		Message:     s2,
		Attachments: []attachmentRow{{Index: 7, Filename: s3, MIMEType: s1, Size: 7}},
	}
}

func draftsPayloadSample(s1, s2, s3 string) draftsPayload {
	return draftsPayload{
		Account: s1,
		Drafts: []draftRow{{
			DraftID:  s2,
			ThreadID: s3,
			To:       s1,
			Subject:  s2,
			Updated:  s3,
		}},
	}
}

func errorEnvelopeSample(s1, s2, s3 string) errorEnvelope {
	output := errorEnvelope{}
	output.Error.Code = s1
	output.Error.Account = s2
	output.Error.ConfigKey = s3
	output.Error.Config = s1
	return output
}

func cliErrorPayloadSample(s1, s2, s3 string) cliErrorPayload {
	output := cliErrorPayload{}
	output.Error.Code = s1
	output.Error.Account = s2
	output.Error.Message = s3
	return output
}

func draftChangedPayloadSample(s1, s2, s3 string) draftChangedPayload {
	output := draftChangedPayload{}
	output.Error.Code = s1
	output.Error.Account = s2
	output.Error.Message = s3
	output.Error.Pinned = s1
	output.Error.Current = s2
	output.Error.Fresh = envelopePayloadSample(s1, s2, s3)
	return output
}

func usageErrorPayloadSample(s1, s2 string) usageErrorPayload {
	output := usageErrorPayload{}
	output.Error.Code = s1
	output.Error.Message = s2
	return output
}

func envelopePayloadSample(s1, s2, s3 string) send.EnvelopePayload {
	return send.EnvelopePayload{
		Account:    s1,
		Mode:       s2,
		ThreadID:   s3,
		Message:    s1,
		To:         []send.RecipientPayload{{Address: s1, Name: s2, Provenance: s3}},
		Cc:         []send.RecipientPayload{{Address: s2, Name: s3, Provenance: s1}},
		Bcc:        []send.RecipientPayload{{Address: s3, Name: s1, Provenance: s2}},
		Subject:    s1,
		BodyBytes:  7,
		InReplyTo:  s2,
		References: []string{s1, s3},
		Forward:    &send.ForwardPayload{OriginalBytes: 7, Disclosure: s2},
		Sendable:   true,
		Sent:       &send.SentPayload{ID: s1, ThreadID: s2},
		Scope:      s3,
		Warning:    s1,
		Attachments: []send.AttachmentPayload{{
			Filename: s1,
			Size:     7,
			MIMEType: s2,
			SHA256:   s3,
		}},
		DraftID: s3,
	}
}

func TestNeedsCredentialDefaultsToTOON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cc := &cmdCtx{stdout: &stdout, stderr: &stderr}
	code := cc.needsCredential(&auth.NeedsCredentialError{
		Account:    "work",
		Class:      auth.ClassRead,
		ConfigKey:  "accounts.work.read_credential_env",
		ConfigPath: "/config.toml",
		Reason:     auth.ReasonEnvUnset,
	})
	if code != 1 || stderr.Len() != 0 || !bytes.HasPrefix(stdout.Bytes(), []byte("error:")) {
		t.Fatalf("needs credential = (%d, %q, %q), want TOON envelope", code, stdout.String(), stderr.String())
	}
	if _, err := toontest.Decode(strings.TrimSuffix(stdout.String(), "\n")); err != nil {
		t.Fatalf("decode TOON envelope %q: %v", stdout.String(), err)
	}
}

func TestMintStaysStrictJSONUnderAgentAndText(t *testing.T) {
	g := newGmailTestServer(t)
	t.Setenv("AGENT", "1")
	t.Setenv("MAILBOX_TOKEN", "")
	t.Setenv("MAILBOX_TOKEN_URL", g.tokenURL(t, "minted-token"))
	t.Setenv("PROBE_VAR", `{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--text", "__mint", "--env", "PROBE_VAR"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("__mint = (%d, %q, %q), want strict JSON success", code, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var output map[string]json.RawMessage
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("decode __mint JSON %q: %v", stdout.String(), err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("__mint stdout has trailing content: %v", err)
	}
	if string(output["access_token"]) != `"minted-token"` {
		t.Fatalf("__mint access token = %s, want minted-token", output["access_token"])
	}
}
