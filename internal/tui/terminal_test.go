package tui

import (
	"github.com/sjawhar/mailbox/internal/auth"
	"github.com/sjawhar/mailbox/internal/gmail"
	"github.com/sjawhar/mailbox/internal/render"
	"strings"
	"testing"
	"time"
)

func TestRenderThreadDocumentSanitizesMailText(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	thread := &render.RenderedThread{
		Subject: "subject" + payload,
		Messages: []render.RenderedMessage{{
			From:     "from" + payload,
			To:       "to" + payload,
			Date:     time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
			Markdown: "body " + payload + "\n[alt " + payload + "](https://example.test/" + payload + ")",
			Attachments: []render.Attachment{{
				N:        1,
				Filename: "report" + payload + ".pdf",
				MimeType: "application/pdf",
				Size:     1,
			}},
		}},
	}

	document, err := renderThreadDocument(thread, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document, "clipboard") || strings.Contains(document, "\x1b]52") {
		t.Fatalf("renderThreadDocument() leaked terminal control payload: %q", document)
	}
	for _, want := range []string{"subject", "from", "to", "body", "alt", "report.pdf"} {
		if !strings.Contains(document, want) {
			t.Errorf("renderThreadDocument() = %q, want %q retained", document, want)
		}
	}
}

func TestRenderThreadDocumentElidesLinkTableQueries(t *testing.T) {
	fullURL := "https://example.test/private/path?email_token=secret"
	thread := &render.RenderedThread{
		Messages: []render.RenderedMessage{{
			From:     "Sender <sender@example.test>",
			To:       "Receiver <receiver@example.test>",
			Date:     time.Date(2026, 8, 29, 10, 40, 0, 0, time.UTC),
			Markdown: "Open [link][1]\n\n[1]: " + fullURL + "\n",
			Links:    []render.Link{{N: 1, URL: fullURL}},
		}},
	}

	document, err := renderThreadDocument(thread, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document, "email_token=secret") {
		t.Fatalf("reader link table leaked query parameters: %q", document)
	}
	if !strings.Contains(document, "https://example.test/private/path…") {
		t.Fatalf("reader link table = %q, want query-elided URL", document)
	}
}

func TestInboxViewSanitizesMailText(t *testing.T) {
	payload := "\x1b]52;c;clipboard\a"
	thread := threadFixture(1, "<p>body</p>")
	thread.Messages[0].Payload.Headers[0].Value = "from" + payload
	thread.Messages[0].Payload.Headers[2].Value = "subject" + payload
	thread.Messages[0].LabelIDs = []string{"label"}
	inbox := newInboxModel()
	inbox.setRows([]*gmail.Thread{thread})

	view := inbox.View(auth.AccountWork, 80, 10, labelNames([]gmail.Label{{ID: "label", Name: "label" + payload, Type: "user"}}), false)
	if strings.Contains(view, "clipboard") || strings.Contains(view, "\x1b]52") {
		t.Fatalf("inbox.View() leaked terminal control payload: %q", view)
	}
	for _, want := range []string{"from", "subject", "label"} {
		if !strings.Contains(view, want) {
			t.Errorf("inbox.View() = %q, want %q retained", view, want)
		}
	}
}

func TestOpenedStatusSanitizesTarget(t *testing.T) {
	model, _ := newTestApp(testThreads(1))
	payload := "https://example.test/\x1b]52;c;clipboard\a"
	model, _ = update(t, model, openedMsg{request: model.currentRequest(openOperation), target: payload})
	if strings.Contains(model.status, "clipboard") || strings.Contains(model.status, "\x1b]52") {
		t.Fatalf("opened status leaked terminal control payload: %q", model.status)
	}
	if !strings.Contains(model.status, "handed to opener:") {
		t.Fatalf("opened status = %q, want handoff wording", model.status)
	}
}
