package send

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
)

// BuildMIME assembles the outbound RFC 5322 message with CRLF line endings.
func BuildMIME(env *Envelope, original []byte, boundary string) ([]byte, error) {
	if err := validateEnvelopeForMIME(env, original); err != nil {
		return nil, err
	}
	if len(env.Attachments) == 0 {
		if env.Mode == ModeForward {
			return buildForwardMIME(env, original, boundary, "")
		}
		return buildAlternativeMIME(env, boundary)
	}
	message, _, err := buildMixedMIME(env, original, boundary, "")
	return message, err
}

func validateEnvelopeForMIME(env *Envelope, original []byte) error {
	if env == nil {
		return errors.New("send: MIME envelope is required")
	}
	if err := validateHeaderValues(env); err != nil {
		return err
	}
	if env.Mode == ModeForward {
		if original == nil {
			return errors.New("send: forward original is required")
		}
	} else if original != nil {
		return errors.New("send: original is only valid for forwards")
	}
	return nil
}

func buildAlternativeMIME(env *Envelope, boundary string) ([]byte, error) {
	var out bytes.Buffer
	if err := writeRecipientHeaders(&out, env); err != nil {
		return nil, err
	}
	writeHeader(&out, "Subject", env.Subject)
	if env.Mode == ModeReply {
		if env.InReplyTo != "" {
			writeHeader(&out, "In-Reply-To", env.InReplyTo)
		}
		if len(env.References) > 0 {
			writeHeader(&out, "References", strings.Join(env.References, " "))
		}
	}
	writeHeader(&out, "MIME-Version", "1.0")
	body, contentType, err := alternativeBody(env.Body, boundary)
	if err != nil {
		return nil, err
	}
	writeHeader(&out, "Content-Type", contentType)
	out.WriteString("\r\n")
	out.Write(body)
	return out.Bytes(), nil
}

// alternativeBody renders markdown as the multipart/alternative pair shared
// by compose, reply, and the forward comment part.
func alternativeBody(markdown, boundary string) ([]byte, string, error) {
	html, err := RenderHTML(markdown)
	if err != nil {
		return nil, "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if boundary != "" {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, "", fmt.Errorf("send: multipart boundary: %w", err)
		}
	}
	plainPart, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("send: create text/plain part: %w", err)
	}
	if err := writeBase64(plainPart, []byte(markdown)); err != nil {
		return nil, "", fmt.Errorf("send: encode text/plain part: %w", err)
	}
	htmlPart, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=UTF-8"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("send: create text/html part: %w", err)
	}
	if err := writeBase64(htmlPart, []byte(html)); err != nil {
		return nil, "", fmt.Errorf("send: encode text/html part: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("send: close multipart alternative: %w", err)
	}
	return body.Bytes(), `multipart/alternative; boundary="` + writer.Boundary() + `"`, nil
}

func buildForwardMIME(env *Envelope, original []byte, boundary, alternativeBoundary string) ([]byte, error) {
	message, _, err := buildMixedMIME(env, original, boundary, alternativeBoundary)
	return message, err
}

func buildMixedMIME(env *Envelope, original []byte, boundary, alternativeBoundary string) ([]byte, string, error) {
	var out bytes.Buffer
	writer := multipart.NewWriter(&out)
	if boundary != "" {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, "", fmt.Errorf("send: multipart boundary: %w", err)
		}
	}

	if err := writeRecipientHeaders(&out, env); err != nil {
		return nil, "", err
	}
	writeHeader(&out, "Subject", env.Subject)
	if env.Mode == ModeReply {
		if env.InReplyTo != "" {
			writeHeader(&out, "In-Reply-To", env.InReplyTo)
		}
		if len(env.References) > 0 {
			writeHeader(&out, "References", strings.Join(env.References, " "))
		}
	}
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", `multipart/mixed; boundary="`+writer.Boundary()+`"`)
	out.WriteString("\r\n")

	bodyContent, bodyContentType, err := alternativeBody(env.Body, alternativeBoundary)
	if err != nil {
		return nil, "", err
	}
	bodyPart, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {bodyContentType}})
	if err != nil {
		return nil, "", fmt.Errorf("send: create alternative MIME part: %w", err)
	}
	if _, err := bodyPart.Write(bodyContent); err != nil {
		return nil, "", fmt.Errorf("send: write alternative MIME part: %w", err)
	}
	if original != nil {
		originalPart, err := writer.CreatePart(originalPartHeader())
		if err != nil {
			return nil, "", fmt.Errorf("send: create original MIME part: %w", err)
		}
		if err := writeBase64(originalPart, original); err != nil {
			return nil, "", fmt.Errorf("send: encode original MIME part: %w", err)
		}
	}
	for _, attachment := range env.Attachments {
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {attachment.MIMEType},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, "", fmt.Errorf("send: create attachment part: %w", err)
		}
		if err := writeBase64(part, attachment.Content); err != nil {
			return nil, "", fmt.Errorf("send: encode attachment part: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("send: close multipart MIME: %w", err)
	}
	return out.Bytes(), writer.Boundary(), nil
}

func buildMixedBase(env *Envelope, original []byte, boundary string) ([]byte, string, error) {
	if err := validateEnvelopeForMIME(env, original); err != nil {
		return nil, "", err
	}
	return buildMixedMIME(env, original, boundary, "")
}

func writeRecipientHeaders(out *bytes.Buffer, env *Envelope) error {
	for _, header := range []struct {
		name       string
		recipients []Recipient
	}{
		{name: "To", recipients: env.To},
		{name: "Cc", recipients: env.Cc},
		{name: "Bcc", recipients: env.Bcc},
	} {
		if len(header.recipients) == 0 {
			continue
		}
		value, err := formatRecipients(header.recipients)
		if err != nil {
			return err
		}
		writeHeader(out, header.name, value)
	}
	return nil
}

func formatRecipients(recipients []Recipient) (string, error) {
	values := make([]string, len(recipients))
	for i, recipient := range recipients {
		if recipient.Address == "" {
			return "", errors.New("send: recipient address is empty")
		}
		values[i] = (&mail.Address{Name: recipient.Display, Address: recipient.Address}).String()
	}
	return strings.Join(values, ", "), nil
}

func writeHeader(out *bytes.Buffer, name, value string) {
	out.WriteString(name)
	out.WriteString(": ")
	out.WriteString(value)
	out.WriteString("\r\n")
}

func originalPartHeader() textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Type":              {"message/rfc822"},
		"Content-Disposition":       {`attachment; filename="original.eml"`},
		"Content-Transfer-Encoding": {"base64"},
	}
}

func writeBase64(out io.Writer, value []byte) error {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(value)))
	base64.StdEncoding.Encode(encoded, value)
	for len(encoded) > 76 {
		if _, err := out.Write(encoded[:76]); err != nil {
			return err
		}
		if _, err := io.WriteString(out, "\r\n"); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	_, err := out.Write(encoded)
	return err
}

func validateHeaderValues(env *Envelope) error {
	for _, value := range []string{env.Subject, env.InReplyTo} {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("send: header value contains CR or LF")
		}
	}
	for _, reference := range env.References {
		if strings.ContainsAny(reference, "\r\n") {
			return errors.New("send: header value contains CR or LF")
		}
	}
	for _, recipients := range [][]Recipient{env.To, env.Cc, env.Bcc} {
		for _, recipient := range recipients {
			if strings.ContainsAny(recipient.Address, "\r\n") || strings.ContainsAny(recipient.Display, "\r\n") {
				return errors.New("send: header value contains CR or LF")
			}
		}
	}
	for _, attachment := range env.Attachments {
		if strings.ContainsAny(attachment.Filename, "\r\n") || strings.ContainsAny(attachment.MIMEType, "\r\n") {
			return errors.New("send: header value contains CR or LF")
		}
	}
	return nil
}
