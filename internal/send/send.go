package send

// NewTargetHeaders returns the target's decoded, unfolded header values.
func NewTargetHeaders(message interface{ Header(string) string }) *TargetHeaders {
	return &TargetHeaders{
		From:       message.Header("From"),
		ReplyTo:    message.Header("Reply-To"),
		To:         message.Header("To"),
		Cc:         message.Header("Cc"),
		Subject:    message.Header("Subject"),
		MessageID:  message.Header("Message-ID"),
		References: message.Header("References"),
	}
}

// Finalize builds the outbound MIME message and checks its exact size.
func Finalize(envelope *Envelope, original []byte, boundary string) (outbound []byte, refusal *Refusal, err error) {
	outbound, err = BuildMIME(envelope, original, boundary)
	if err != nil {
		return nil, nil, err
	}
	return outbound, OutboundSizeRefusal(outbound, envelope.Attachments), nil
}
