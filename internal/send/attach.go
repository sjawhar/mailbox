package send

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sjawhar/mailbox/internal/render"
)

// MaxOutboundMessageBytes caps the final RFC 5322/MIME message handed to Gmail.
const MaxOutboundMessageBytes = 25_000_000

// Attachment is one outbound MIME part. Filename is canonical and header-safe.
type Attachment struct {
	Filename string
	MIMEType string
	SHA256   string
	Content  []byte
}

// AttachmentMeta is report-only accounting for the oversize refusal path.
type AttachmentMeta struct {
	Filename string
	MIMEType string
	RawSize  int64
}

// LoadAttachments reads every requested path once. Before the cumulative raw
// budget trips, the read bytes supply both the hash and the eventual MIME part.
// After it trips, every remaining path is streamed to collect exact metadata
// without retaining its body. A caller uses the complete sweep to build R-A3.
func LoadAttachments(paths []string) (attachments []Attachment, oversized []AttachmentMeta, refusal *Refusal) {
	budget := int64(MaxOutboundMessageBytes)
	attachments = make([]Attachment, 0, len(paths))
	sweep := make([]AttachmentMeta, 0, len(paths))
	tripped := false

	for index, path := range paths {
		if _, nameRefusal := attachmentFilename(path, index); nameRefusal != nil {
			return nil, nil, nameRefusal
		}
		if tripped {
			meta, drainRefusal := drainMeta(path, index)
			if drainRefusal != nil {
				return nil, nil, drainRefusal
			}
			sweep = append(sweep, meta)
			continue
		}

		file, err := os.Open(path)
		if err != nil {
			return nil, nil, refusalUnreadable(path, err)
		}
		content, err := io.ReadAll(io.LimitReader(file, budget+1))
		if err != nil {
			_ = file.Close()
			return nil, nil, refusalUnreadable(path, err)
		}
		if int64(len(content)) > budget {
			meta, drainRefusal := drainOpenAttachment(path, index, file, content)
			_ = file.Close()
			if drainRefusal != nil {
				return nil, nil, drainRefusal
			}
			for _, attachment := range attachments {
				sweep = append(sweep, AttachmentMeta{
					Filename: attachment.Filename,
					MIMEType: attachment.MIMEType,
					RawSize:  int64(len(attachment.Content)),
				})
			}
			sweep = append(sweep, meta)
			attachments = nil
			tripped = true
			continue
		}
		_ = file.Close()
		budget -= int64(len(content))
		attachment, attachmentRefusal := NewCarriedAttachment(filepath.Base(path), index, content)
		if attachmentRefusal != nil {
			return nil, nil, attachmentRefusal
		}
		attachments = append(attachments, attachment)
	}

	if tripped {
		return nil, sweep, nil
	}
	return attachments, nil, nil
}

// drainOpenAttachment completes the budget-tripping file on the descriptor
// already used for its buffered prefix. It never reopens the path, preventing a
// concurrent path replacement from changing the refusal accounting.
func drainOpenAttachment(path string, index int, file *os.File, prefix []byte) (AttachmentMeta, *Refusal) {
	filename, filenameRefusal := attachmentFilename(path, index)
	if filenameRefusal != nil {
		return AttachmentMeta{}, filenameRefusal
	}
	head := prefix
	if len(head) > 512 {
		head = head[:512]
	}
	topped := 0
	if len(head) < 512 {
		top := make([]byte, 512-len(head))
		n, err := io.ReadFull(file, top)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return AttachmentMeta{}, refusalUnreadable(path, err)
		}
		topped = n
		head = append(append(make([]byte, 0, 512), head...), top[:n]...)
	}
	rest, err := io.Copy(io.Discard, file)
	if err != nil {
		return AttachmentMeta{}, refusalUnreadable(path, err)
	}
	return AttachmentMeta{
		Filename: filename,
		MIMEType: detectMIMEType(filename, head),
		RawSize:  int64(len(prefix)) + int64(topped) + rest,
	}, nil
}

// drainMeta opens an as-yet-unread path once, preserving its first 512 bytes
// for the same MIME detection chain used by the outbound builder while the rest
// is counted and discarded.
func drainMeta(path string, index int) (AttachmentMeta, *Refusal) {
	filename, filenameRefusal := attachmentFilename(path, index)
	if filenameRefusal != nil {
		return AttachmentMeta{}, filenameRefusal
	}
	file, err := os.Open(path)
	if err != nil {
		return AttachmentMeta{}, refusalUnreadable(path, err)
	}
	defer file.Close()

	head := make([]byte, 512)
	headLen, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return AttachmentMeta{}, refusalUnreadable(path, err)
	}
	rest, err := io.Copy(io.Discard, file)
	if err != nil {
		return AttachmentMeta{}, refusalUnreadable(path, err)
	}
	total := int64(headLen) + rest
	if total == 0 {
		return AttachmentMeta{}, refusal("R-A2", "attachment_empty", fmt.Sprintf("R-A2 attachment_empty: attachment %s is empty", render.SanitizeTerminal(path)))
	}
	return AttachmentMeta{Filename: filename, MIMEType: detectMIMEType(filename, head[:headLen]), RawSize: total}, nil
}

func attachmentFilename(path string, index int) (string, *Refusal) {
	filename, hadControl := render.CanonicalFilename(filepath.Base(path), index)
	if hadControl {
		return "", refusal("R4", "header_injection", "R4 header_injection: attachment filename contains control bytes")
	}
	return filename, nil
}

func refusalUnreadable(path string, err error) *Refusal {
	return refusal("R-A1", "attachment_unreadable", fmt.Sprintf("R-A1 attachment_unreadable: cannot read attachment %s: %v", path, err))
}

// NewCarriedAttachment applies the same filename and type rules to a carried
// draft attachment as LoadAttachments applies to a local path.
func NewCarriedAttachment(name string, index int, content []byte) (Attachment, *Refusal) {
	if len(content) == 0 {
		return Attachment{}, refusal("R-A2", "attachment_empty", fmt.Sprintf("R-A2 attachment_empty: attachment %s is empty", render.SanitizeTerminal(name)))
	}
	filename, filenameRefusal := attachmentFilename(name, index)
	if filenameRefusal != nil {
		return Attachment{}, filenameRefusal
	}
	digest := sha256.Sum256(content)
	return Attachment{
		Filename: filename,
		MIMEType: detectMIMEType(filename, content),
		SHA256:   hex.EncodeToString(digest[:]),
		Content:  content,
	}, nil
}

func detectMIMEType(name string, content []byte) string {
	if byExtension := mime.TypeByExtension(filepath.Ext(name)); byExtension != "" {
		return byExtension
	}
	if len(content) > 512 {
		content = content[:512]
	}
	return http.DetectContentType(content)
}

// OutboundSizeRefusal measures the exact MIME bytes that will be sent.
func OutboundSizeRefusal(message []byte, attachments []Attachment) *Refusal {
	return outboundSizeRefusal(message, attachments, MaxOutboundMessageBytes)
}

func outboundSizeRefusal(message []byte, attachments []Attachment, limit int) *Refusal {
	if len(message) <= limit {
		return nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "R-A3 attachment_too_large: final message is %d bytes; the cap is %d bytes", len(message), limit)
	for _, attachment := range attachments {
		fmt.Fprintf(&out, "\n  %s: %d bytes", attachment.Filename, len(attachment.Content))
	}
	return refusal("R-A3", "attachment_too_large", out.String())
}

// OversizeRefusal uses the real mixed-message base plus exact per-part bytes to
// report an R-A3 refusal without retaining oversized attachment contents.
func OversizeRefusal(env *Envelope, original []byte, sweep []AttachmentMeta) (*Refusal, error) {
	final, err := oversizeFinalSize(env, original, sweep, "")
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "R-A3 attachment_too_large: final message is %d bytes; the cap is %d bytes", final, MaxOutboundMessageBytes)
	for _, attachment := range env.Attachments {
		fmt.Fprintf(&out, "\n  %s: %d bytes", attachment.Filename, len(attachment.Content))
	}
	for _, meta := range sweep {
		fmt.Fprintf(&out, "\n  %s: %d bytes", meta.Filename, meta.RawSize)
	}
	return refusal("R-A3", "attachment_too_large", out.String()), nil
}

func oversizeFinalSize(env *Envelope, original []byte, sweep []AttachmentMeta, boundary string) (int, error) {
	base, usedBoundary, err := buildMixedBase(env, original, boundary)
	if err != nil {
		return 0, err
	}
	total := len(base)
	for _, meta := range sweep {
		total += oversizePartSize(len(usedBoundary), meta)
	}
	return total, nil
}

func oversizePartSize(boundaryLen int, meta AttachmentMeta) int {
	headers := len("Content-Disposition: ") + len(mime.FormatMediaType("attachment", map[string]string{"filename": meta.Filename})) + 2 +
		len("Content-Transfer-Encoding: base64") + 2 +
		len("Content-Type: ") + len(meta.MIMEType) + 2
	return boundaryLen + 6 + headers + 2 + int(encodedBase64Len(meta.RawSize))
}

func encodedBase64Len(raw int64) int64 {
	encoded := 4 * ((raw + 2) / 3)
	if encoded == 0 {
		return 0
	}
	return encoded + 2*((encoded-1)/76)
}
