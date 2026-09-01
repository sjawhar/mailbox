package e2e

import (
	"net/http"
	"testing"
	"time"
)

type capturedSend struct {
	Auth     string
	Raw      []byte
	ThreadID string
}

type recordedCall struct {
	Method string
	Path   string
	Query  string
	Bearer string
}

func (g *fakeGmail) recordModified(request *http.Request, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writeAuths = append(g.writeAuths, request.Header.Get("Authorization"))
	g.modified = append(g.modified, id)
}

func (g *fakeGmail) recordTrashed(request *http.Request, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writeAuths = append(g.writeAuths, request.Header.Get("Authorization"))
	g.trashed = append(g.trashed, id)
}

func (g *fakeGmail) recordBatchRequest(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.batchRequests = append(g.batchRequests, request.Method+" "+request.URL.RequestURI())
}

func (g *fakeGmail) recordReadAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.readAuths = append(g.readAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordWriteAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writeAuths = append(g.writeAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordSendAuth(request *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sendAuths = append(g.sendAuths, request.Header.Get("Authorization"))
}

func (g *fakeGmail) recordSend(request *http.Request, raw []byte, threadID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, capturedSend{Auth: request.Header.Get("Authorization"), Raw: append([]byte(nil), raw...), ThreadID: threadID})
}
func (g *fakeGmail) record(call recordedCall) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
}

func (g *fakeGmail) armSendGarbage() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sendGarbage = true
	g.sendStatus = 0
}

func (g *fakeGmail) armSendStatus(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sendGarbage = false
	g.sendStatus = status
}

func (g *fakeGmail) setDraftSubject(id, subject string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	draft, ok := g.drafts[id]
	if !ok {
		panic("fixture draft not found: " + id)
	}
	draft.Subject = subject
}

func (g *fakeGmail) recordedCalls() []recordedCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedCall(nil), g.calls...)
}

func (g *fakeGmail) unknownCalls() []recordedCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedCall(nil), g.unknown...)
}

func (g *fakeGmail) recordedReadAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.readAuths...)
}

func (g *fakeGmail) recordedWriteAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writeAuths...)
}

func (g *fakeGmail) waitForWriteAuths(t *testing.T, count int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		auths := g.recordedWriteAuths()
		if len(auths) >= count {
			return auths
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d write requests; got %v", count, g.recordedWriteAuths())
	return nil
}

func (g *fakeGmail) recordedSendAuths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.sendAuths...)
}

func (g *fakeGmail) recordedSends() []capturedSend {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]capturedSend(nil), g.sent...)
}

func (g *fakeGmail) recordedBatchRequests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.batchRequests...)
}

func (g *fakeGmail) recordedModified() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.modified...)
}

func (g *fakeGmail) recordedTrashed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.trashed...)
}
