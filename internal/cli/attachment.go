package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
)

func runAttachment(cc *cmdCtx, args []string) int {
	cf := cc.flags("attachment")
	outputPath := cf.fs.String("o", "", "output file, directory, or - for stdout")
	pos, next, done, code := cc.parse(cf, args)
	if done || code != 0 {
		return code
	}
	if err := requireArity(pos, 1, 2, "attachment"); err != nil {
		return next.failUsage(err)
	}
	account, source, client, code := next.start()
	if code != 0 {
		return code
	}
	ctx := context.Background()
	messageID := pos[0]
	message, err := client.GetMessageFull(ctx, messageID)
	if err != nil {
		if gmail.IsNotFound(err) {
			return next.commandError(account, source, auth.ClassRead, "attachment_not_found",
				fmt.Sprintf("message %s not found (message ids appear in 'read' output)", messageID))
		}
		return next.runtimeError(account, source, err)
	}
	content, err := render.ExtractContent(message)
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	rows := canonicalRows(content.Attachments)
	if len(pos) == 1 {
		return next.attachmentList(account, source, messageID, rows)
	}
	index, found := selectAttachment(rows, content.Attachments, pos[1])
	if !found {
		return next.commandError(account, source, auth.ClassRead, "attachment_not_found",
			fmt.Sprintf("message %s has no attachment %q; available: %s", messageID, pos[1], strings.Join(rowNames(rows), ", ")))
	}
	contents, err := render.ResolveAttachmentBytes(ctx, client, content.Attachments[index])
	if err != nil {
		return next.runtimeError(account, source, err)
	}
	sum := sha256.Sum256(contents)
	digest := hex.EncodeToString(sum[:])
	if *outputPath == "-" {
		if _, err := next.stdout.Write(contents); err != nil {
			return next.runtimeError(account, source, wrapError("write attachment to stdout", err))
		}
		fmt.Fprintf(next.stderr, "%s: %d bytes, sha256=%s\n", render.SanitizeTerminal(rows[index].Filename), len(contents), digest)
		next.emitCredentialDiagnostic(source, auth.ClassRead)
		return 0
	}
	dir, base := resolveOutput(*outputPath, rows[index].Filename)
	if err := render.SaveAttachment(dir, base, contents); err != nil {
		if errors.Is(err, os.ErrExist) {
			return next.commandError(account, source, auth.ClassRead, "attachment_exists",
				fmt.Sprintf("%s exists — pass -o to choose a different destination", filepath.Join(dir, base)))
		}
		return next.runtimeError(account, source, wrapError("write attachment", err))
	}
	return next.attachmentSaved(account, source, filepath.Join(dir, base), base, int64(len(contents)), digest)
}

type attachmentRow struct {
	Index    int    `json:"index"`
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

type attachmentListPayload struct {
	Account     string          `json:"account"`
	Message     string          `json:"message"`
	Attachments []attachmentRow `json:"attachments"`
}

type attachmentSavePayload struct {
	Account  string `json:"account"`
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func canonicalRows(attachments []render.Attachment) []attachmentRow {
	rows := make([]attachmentRow, len(attachments))
	for index, attachment := range attachments {
		filename, _ := render.CanonicalFilename(attachment.Filename, index)
		rows[index] = attachmentRow{
			Index:    index,
			Filename: filename,
			MIMEType: attachment.MimeType,
			Size:     attachment.Size,
		}
	}
	return rows
}

func rowNames(rows []attachmentRow) []string {
	names := make([]string, len(rows))
	for index, row := range rows {
		names[index] = row.Filename
	}
	return names
}

func selectAttachment(rows []attachmentRow, attachments []render.Attachment, selector string) (int, bool) {
	for index, row := range rows {
		if selector == row.Filename {
			return index, true
		}
	}
	for index, attachment := range attachments {
		if selector == attachment.Filename {
			return index, true
		}
	}
	index, err := strconv.Atoi(selector)
	if err != nil || index < 0 || index >= len(rows) {
		return 0, false
	}
	return index, true
}

func resolveOutput(output, canonical string) (dir, base string) {
	if output == "" {
		return ".", canonical
	}
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		return output, canonical
	}
	return filepath.Dir(output), filepath.Base(output)
}

func (cc *cmdCtx) attachmentList(account string, source *auth.Source, messageID string, attachments []attachmentRow) int {
	switch cc.format() {
	case FormatText:
		fmt.Fprintln(cc.stdout, "index\tfilename\tmime\tsize")
		for _, attachment := range attachments {
			fmt.Fprintf(cc.stdout, "%d\t%s\t%s\t%d\n", attachment.Index, render.SanitizeTerminal(attachment.Filename), render.SanitizeTerminal(attachment.MIMEType), attachment.Size)
		}
	default:
		if attachments == nil {
			attachments = []attachmentRow{}
		}
		output := attachmentListPayload{Account: account, Message: messageID, Attachments: attachments}
		if err := cc.writeMachine(output); err != nil {
			return cc.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}

func (cc *cmdCtx) attachmentSaved(account string, source *auth.Source, path, filename string, size int64, sha256 string) int {
	switch cc.format() {
	case FormatText:
		fmt.Fprintf(cc.stdout, "saved %s (%d bytes) sha256=%s\n", render.SanitizeTerminal(path), size, sha256)
	default:
		output := attachmentSavePayload{Account: account, Path: path, Filename: filename, Size: size, SHA256: sha256}
		if err := cc.writeMachine(output); err != nil {
			return cc.runtimeError(account, source, wrapError("write JSON", err))
		}
	}
	cc.emitCredentialDiagnostic(source, auth.ClassRead)
	return 0
}
