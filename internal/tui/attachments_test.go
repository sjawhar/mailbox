package tui

import (
	"github.com/sjawhar/mailbox/internal/gmail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentPickerDownloadsToCurrentDirectory(t *testing.T) {
	thread := attachmentThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	api.attachments[thread.Messages[0].ID+":attachment-1"] = []byte("report contents")
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	model, _ = update(t, model, key("a"))
	if model.view != attachmentPickerView {
		t.Fatalf("view after attachment key = %v, want attachment picker", model.view)
	}
	model, cmd := update(t, model, key("enter"))
	msg := runCmd(t, cmd)
	model, _ = update(t, model, msg)
	path := temporaryDirectory + "/report.pdf"
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "report contents"; got != want {
		t.Fatalf("downloaded attachment = %q, want %q", got, want)
	}
	if !strings.Contains(model.status, path) {
		t.Fatalf("status = %q, want saved path %q", model.status, path)
	}
}

func TestAttachmentCollisionSurfacesFileName(t *testing.T) {
	thread := attachmentThread()
	model, api := newTestApp([]*gmail.Thread{thread})
	api.attachments[thread.Messages[0].ID+":attachment-1"] = []byte("report contents")
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.WriteFile("report.pdf", []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	model, _ = update(t, model, key("a"))
	model, cmd := update(t, model, key("enter"))
	errMsg := runCmd(t, cmd)
	model, _ = update(t, model, errMsg)
	if !strings.Contains(model.status, "report.pdf") {
		t.Fatalf("status = %q, want collision file name", model.status)
	}
}

func TestAttachmentTraversalIsRejected(t *testing.T) {
	thread := attachmentThread()
	thread.Messages[0].Payload.Parts[1].Filename = "../escape.pdf"
	model, api := newTestApp([]*gmail.Thread{thread})
	api.attachments[thread.Messages[0].ID+":attachment-1"] = []byte("malicious contents")
	model.view = threadView
	model, _ = update(t, model, threadMsg{request: model.currentRequest(threadOperation), thread: thread})
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	outside := filepath.Join(filepath.Dir(temporaryDirectory), "escape.pdf")
	t.Cleanup(func() {
		if err := os.Remove(outside); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove traversal target: %v", err)
		}
	})

	model, _ = update(t, model, key("a"))
	model, command := update(t, model, key("enter"))
	message := runCmd(t, command)
	model, _ = update(t, model, message)
	if !strings.Contains(model.status, "unsafe attachment filename") {
		t.Fatalf("status = %q, want traversal rejection", model.status)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal target exists or could not be checked: %v", err)
	}
}
