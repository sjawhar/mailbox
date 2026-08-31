package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func fixturePost(t *testing.T, url, bearer string, body []byte) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", bearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, payload
}

func fixtureGet(t *testing.T, url, bearer string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", bearer)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, payload
}

func TestFixtureRecordsEveryCallAndFailsUnknownEndpoints(t *testing.T) {
	g := newFakeGmail(t)
	response, _ := fixtureGet(t, g.server.URL+"/gmail/v1/users/me/unknown-endpoint", "Bearer probe-tok")
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unknown endpoint status = %d, want 500 (default failure)", response.StatusCode)
	}
	unknown := g.unknownCalls()
	if len(unknown) != 1 || unknown[0].Path != "/gmail/v1/users/me/unknown-endpoint" || unknown[0].Bearer != "Bearer probe-tok" {
		t.Fatalf("unknownCalls = %+v, want the probe with its bearer", unknown)
	}
	all := g.recordedCalls()
	if len(all) == 0 || all[len(all)-1].Method != http.MethodGet {
		t.Fatalf("recordedCalls = %+v, want the probe recorded", all)
	}
}

func TestFixtureDraftLifecycleRotationAndDeleteLever(t *testing.T) {
	g := newFakeGmail(t)
	create := []byte(fmt.Sprintf(`{"message":{"raw":%q,"threadId":"t1"}}`,
		base64.RawURLEncoding.EncodeToString([]byte("To: a@example.test\r\nSubject: s\r\n\r\nbody"))))
	response, payload := fixturePost(t, g.server.URL+"/gmail/v1/users/me/drafts", "Bearer w-tok", create)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("drafts.create = %d: %s", response.StatusCode, payload)
	}
	var created struct {
		ID      string `json:"id"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(payload, &created); err != nil || created.ID == "" || created.Message.ID == "" {
		t.Fatalf("create payload = %s (%v)", payload, err)
	}

	response, payload = fixtureGet(t, g.server.URL+"/gmail/v1/users/me/drafts/"+created.ID+"?format=full", "Bearer r-tok")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(payload), created.Message.ID) {
		t.Fatalf("drafts.get = %d: %s", response.StatusCode, payload)
	}

	response, _ = fixturePost(t, g.server.URL+"/gmail/v1/users/me/drafts/"+created.ID+"/update", "Bearer test-lever", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rotation lever = %d", response.StatusCode)
	}
	_, payload = fixtureGet(t, g.server.URL+"/gmail/v1/users/me/drafts/"+created.ID+"?format=full", "Bearer r-tok")
	if !strings.Contains(string(payload), created.Message.ID+"r") {
		t.Fatalf("rotated draft = %s, want message id %sr", payload, created.Message.ID)
	}

	request, _ := http.NewRequest(http.MethodDelete, g.server.URL+"/gmail/v1/users/me/drafts/"+created.ID, nil)
	request.Header.Set("Authorization", "Bearer w-tok")
	deleteResponse, err := http.DefaultClient.Do(request)
	if err != nil || deleteResponse.StatusCode >= 300 {
		t.Fatalf("drafts.delete = %v %v", deleteResponse, err)
	}
	deleteResponse.Body.Close()
	if response, _ := fixtureGet(t, g.server.URL+"/gmail/v1/users/me/drafts/"+created.ID, "Bearer r-tok"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted draft still served: %d", response.StatusCode)
	}
}

func TestFixtureDraftsSendIsHard500(t *testing.T) {
	g := newFakeGmail(t)
	response, _ := fixturePost(t, g.server.URL+"/gmail/v1/users/me/drafts/send", "Bearer any", []byte(`{}`))
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("drafts.send = %d, want hard 500 (prohibition pin)", response.StatusCode)
	}
	if calls := callsUnderFixture(g, "/gmail/v1/users/me/drafts/send"); len(calls) != 1 {
		t.Fatalf("drafts.send calls recorded = %d, want 1", len(calls))
	}
}

func TestFixtureAttachmentBytesAndSendLevers(t *testing.T) {
	g := newFakeGmail(t)
	response, payload := fixtureGet(t, g.server.URL+"/gmail/v1/users/me/messages/m-att/attachments/a-ok", "Bearer r-tok")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("attachments.get = %d", response.StatusCode)
	}
	var attachment struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(payload, &attachment); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(attachment.Data)
	if err != nil || !bytes.Equal(decoded, fixtureAttachmentBytes("a-ok")) {
		t.Fatalf("attachment bytes = %q (%v)", decoded, err)
	}

	sendBody := []byte(fmt.Sprintf(`{"raw":%q,"threadId":"t1"}`, base64.RawURLEncoding.EncodeToString([]byte("MIME"))))
	g.armSendGarbage()
	response, payload = fixturePost(t, g.server.URL+"/gmail/v1/users/me/messages/send", "Bearer s-tok", sendBody)
	if response.StatusCode != http.StatusOK || json.Valid(payload) {
		t.Fatalf("armed garbage send = %d %q, want 200 + non-JSON once", response.StatusCode, payload)
	}
	response, payload = fixturePost(t, g.server.URL+"/gmail/v1/users/me/messages/send", "Bearer s-tok", sendBody)
	if response.StatusCode != http.StatusOK || !json.Valid(payload) {
		t.Fatalf("lever must be one-shot: second send = %d %q", response.StatusCode, payload)
	}

	g.armSendStatus(http.StatusInternalServerError)
	response, _ = fixturePost(t, g.server.URL+"/gmail/v1/users/me/messages/send", "Bearer s-tok", sendBody)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("armed 5xx send = %d, want 500 once", response.StatusCode)
	}
}

func callsUnderFixture(g *fakeGmail, prefix string) []recordedCall {
	var out []recordedCall
	for _, call := range g.recordedCalls() {
		if strings.HasPrefix(call.Path, prefix) {
			out = append(out, call)
		}
	}
	return out
}
