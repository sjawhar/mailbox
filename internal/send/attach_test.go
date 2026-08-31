package send

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func writeAttachmentFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAttachmentsHappyPathHashesAndDetectsTypes(t *testing.T) {
	pdf := writeAttachmentFile(t, "report.pdf", []byte("%PDF-1.4 fixture"))
	atts, oversized, refusal := LoadAttachments([]string{pdf})
	if refusal != nil || oversized != nil {
		t.Fatalf("LoadAttachments = (%v, %v)", oversized, refusal)
	}
	want := sha256.Sum256([]byte("%PDF-1.4 fixture"))
	if atts[0].Filename != "report.pdf" || atts[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("attachment = %+v", atts[0])
	}
	if atts[0].MIMEType != "application/pdf" {
		t.Fatalf("MIMEType = %q, want application/pdf (mime.TypeByExtension)", atts[0].MIMEType)
	}
}

func TestLoadAttachmentsSniffsUnknownExtensionThenFallsBack(t *testing.T) {
	sniffed := writeAttachmentFile(t, "notes.unknownext", []byte("<html><body>x</body></html>"))
	atts, _, refusal := LoadAttachments([]string{sniffed})
	if refusal != nil || !strings.HasPrefix(atts[0].MIMEType, "text/html") {
		t.Fatalf("sniffed = %+v, %v; want text/html via http.DetectContentType", atts, refusal)
	}
	binary := writeAttachmentFile(t, "blob.unknownext", []byte{0x00, 0x01, 0x02, 0xff})
	atts, _, refusal = LoadAttachments([]string{binary})
	if refusal != nil || atts[0].MIMEType != "application/octet-stream" {
		t.Fatalf("binary = %+v, %v; want application/octet-stream", atts, refusal)
	}
}

func TestLoadAttachmentsRefusals(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.bin")
	if _, _, refusal := LoadAttachments([]string{missing}); refusal == nil ||
		refusal.Rule != "R-A1" || refusal.Code != "attachment_unreadable" || !strings.Contains(refusal.Message, missing) {
		t.Fatalf("unreadable refusal = %+v, want R-A1 naming %q", refusal, missing)
	}
	empty := writeAttachmentFile(t, "empty.bin", nil)
	if _, _, refusal := LoadAttachments([]string{empty}); refusal == nil || refusal.Rule != "R-A2" || refusal.Code != "attachment_empty" {
		t.Fatalf("empty refusal = %+v, want R-A2", refusal)
	}
}

func TestLoadAttachmentsControlByteNameIsR4(t *testing.T) {
	hostile := writeAttachmentFile(t, "evil", []byte("x"))
	renamed := filepath.Join(filepath.Dir(hostile), "cr\rlf.txt")
	if err := os.Rename(hostile, renamed); err != nil {
		t.Skipf("filesystem refuses control-byte names: %v", err)
	}
	if _, _, refusal := LoadAttachments([]string{renamed}); refusal == nil || refusal.Rule != "R4" || refusal.Code != "header_injection" {
		t.Fatalf("control-byte name refusal = %+v, want R4 header_injection", refusal)
	}
}

func TestLoadAttachmentsOversizeSweepKeepsCompleteAccounting(t *testing.T) {
	half := make([]byte, MaxOutboundMessageBytes/2+1)
	a := writeAttachmentFile(t, "a.bin", half)
	b := writeAttachmentFile(t, "b.bin", half)
	c := writeAttachmentFile(t, "c.bin", []byte("tail"))
	atts, oversized, refusal := LoadAttachments([]string{a, b, c})
	if refusal != nil || atts != nil {
		t.Fatalf("oversize sweep = (%v, %v), want metadata-only", atts, refusal)
	}
	if len(oversized) != 3 ||
		oversized[0].Filename != "a.bin" || oversized[0].RawSize != int64(len(half)) ||
		oversized[1].Filename != "b.bin" || oversized[1].RawSize != int64(len(half)) ||
		oversized[2].Filename != "c.bin" || oversized[2].RawSize != 4 {
		t.Fatalf("sweep metadata = %+v, want every attachment with its raw size", oversized)
	}
	sniffable := writeAttachmentFile(t, "page.unknownext", []byte("<html><body>oversize sweep</body></html>"))
	_, oversized, refusal = LoadAttachments([]string{a, b, sniffable})
	if refusal != nil || len(oversized) != 3 || !strings.HasPrefix(oversized[2].MIMEType, "text/html") {
		t.Fatalf("sweep sniffing = %+v (%v), want text/html for the drained file", oversized, refusal)
	}
}

func TestLoadAttachmentsOversizeSweepStillRefusesUnreadable(t *testing.T) {
	half := make([]byte, MaxOutboundMessageBytes/2+1)
	a := writeAttachmentFile(t, "a.bin", half)
	b := writeAttachmentFile(t, "b.bin", half)
	missing := filepath.Join(t.TempDir(), "missing.bin")
	_, _, refusal := LoadAttachments([]string{a, b, missing})
	if refusal == nil || refusal.Rule != "R-A1" || !strings.Contains(refusal.Message, missing) {
		t.Fatalf("unreadable path after the budget trip = %+v, want R-A1 naming %q", refusal, missing)
	}
}

func realShapeEnvelope(t *testing.T, shape string) (*Envelope, []byte) {
	t.Helper()
	att, refusal := NewCarriedAttachment("report.pdf", 0, []byte("%PDF-1.4 shaped fixture body"))
	if refusal != nil {
		t.Fatal(refusal)
	}
	switch shape {
	case "compose":
		return &Envelope{Mode: ModeCompose, To: []Recipient{{Address: "a@example.test"}}, Subject: "s", Body: "hi", Attachments: []Attachment{att}}, nil
	case "forward":
		return &Envelope{Mode: ModeForward, To: []Recipient{{Address: "a@example.test"}}, Subject: "Fwd: s", Body: "fyi", Attachments: []Attachment{att}}, []byte("From: o@example.test\r\n\r\noriginal body")
	case "resumed-draft":
		return &Envelope{Mode: ModeReply, To: []Recipient{{Address: "a@example.test"}}, Subject: "Re: s", Body: "resumed", InReplyTo: "<m-t1@example.test>", References: []string{"<m-t1@example.test>"}, ThreadID: "t1", Attachments: []Attachment{att}}, nil
	}
	t.Fatalf("unknown shape %q", shape)
	return nil, nil
}

func TestOutboundSizeBoundaryOnRealMIMEPerShape(t *testing.T) {
	for _, shape := range []string{"compose", "forward", "resumed-draft"} {
		env, original := realShapeEnvelope(t, shape)
		raw, err := BuildMIME(env, original, "")
		if err != nil {
			t.Fatalf("%s: BuildMIME: %v", shape, err)
		}
		if refusal := outboundSizeRefusal(raw, env.Attachments, len(raw)); refusal != nil {
			t.Fatalf("%s exact-at-limit refused: %+v", shape, refusal)
		}
		refusal := outboundSizeRefusal(raw, env.Attachments, len(raw)-1)
		if refusal == nil || refusal.Rule != "R-A3" || refusal.Code != "attachment_too_large" {
			t.Fatalf("%s one-byte-over = %+v, want R-A3", shape, refusal)
		}
		for _, want := range []string{"report.pdf", strconv.Itoa(len(raw)), strconv.Itoa(len(raw) - 1)} {
			if !strings.Contains(refusal.Message, want) {
				t.Fatalf("%s R-A3 message %q missing %q (must list every attachment, the final size, and the cap)", shape, refusal.Message, want)
			}
		}
	}
}

func TestOutboundSizeRefusalWiredCap(t *testing.T) {
	if MaxOutboundMessageBytes != 25_000_000 {
		t.Fatalf("cap = %d, want 25000000 (spec §2)", MaxOutboundMessageBytes)
	}
	if refusal := OutboundSizeRefusal(make([]byte, MaxOutboundMessageBytes+1), nil); refusal == nil || refusal.Code != "attachment_too_large" {
		t.Fatalf("over-cap = %+v, want R-A3 through the exported gate", refusal)
	}
	if refusal := OutboundSizeRefusal(make([]byte, MaxOutboundMessageBytes), nil); refusal != nil {
		t.Fatalf("exact-cap refused through the exported gate: %+v", refusal)
	}
}

func TestOversizeAccountingMatchesBuilder(t *testing.T) {
	for _, sizes := range [][]int{{1}, {2}, {3}, {4}, {56}, {57}, {58}, {75}, {76}, {77}, {100}, {8192}, {57, 100}, {3, 76, 8192}} {
		attachments := make([]Attachment, 0, len(sizes))
		sweep := make([]AttachmentMeta, 0, len(sizes))
		for index, size := range sizes {
			content := make([]byte, size)
			for i := range content {
				content[i] = 'x'
			}
			att, refusal := NewCarriedAttachment("data-"+strconv.Itoa(index)+".bin", index, content)
			if refusal != nil {
				t.Fatal(refusal)
			}
			attachments = append(attachments, att)
			sweep = append(sweep, AttachmentMeta{Filename: att.Filename, MIMEType: att.MIMEType, RawSize: int64(size)})
		}
		env := &Envelope{Mode: ModeCompose, To: []Recipient{{Address: "a@example.test"}}, Subject: "s", Body: "hi"}
		withParts := *env
		withParts.Attachments = attachments
		real, err := BuildMIME(&withParts, nil, "fixedboundary")
		if err != nil {
			t.Fatal(err)
		}
		accounted, err := oversizeFinalSize(env, nil, sweep, "fixedboundary")
		if err != nil {
			t.Fatal(err)
		}
		if len(real) != accounted {
			t.Fatalf("sizes %v: builder %d != accounting %d", sizes, len(real), accounted)
		}
	}
}

func TestOversizeRefusalReportsEveryAttachmentAndAccountedFinalSize(t *testing.T) {
	env := &Envelope{Mode: ModeCompose, To: []Recipient{{Address: "a@example.test"}}, Subject: "s", Body: "hi"}
	sweep := []AttachmentMeta{
		{Filename: "a.bin", MIMEType: "application/octet-stream", RawSize: MaxOutboundMessageBytes/2 + 1},
		{Filename: "b.bin", MIMEType: "application/octet-stream", RawSize: MaxOutboundMessageBytes/2 + 1},
	}
	refusal, err := OversizeRefusal(env, nil, sweep)
	if err != nil || refusal == nil || refusal.Rule != "R-A3" || refusal.Code != "attachment_too_large" {
		t.Fatalf("refusal = %+v, %v", refusal, err)
	}
	for _, want := range []string{
		"a.bin", "b.bin",
		strconv.FormatInt(MaxOutboundMessageBytes/2+1, 10),
		strconv.Itoa(MaxOutboundMessageBytes),
	} {
		if !strings.Contains(refusal.Message, want) {
			t.Fatalf("R-A3 message %q missing %q", refusal.Message, want)
		}
	}
	final := regexp.MustCompile(`final message is (\d+) bytes`).FindStringSubmatch(refusal.Message)
	if final == nil {
		t.Fatalf("R-A3 message %q lacks the accounted final size", refusal.Message)
	}
	accounted, _ := strconv.Atoi(final[1])
	expected, err := oversizeFinalSize(env, nil, sweep, "")
	if err != nil || accounted != expected || accounted <= MaxOutboundMessageBytes {
		t.Fatalf("accounted final = %d, want %d (> cap)", accounted, expected)
	}
}
