package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateDraftPostsWrappedRawWithWriteCredentials(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/gmail/v1/users/me/drafts", "write-token")
		var body struct {
			Message struct {
				Raw      string `json:"raw"`
				ThreadID string `json:"threadId"`
			} `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(body.Message.Raw)
		if err != nil || string(raw) != "MIME" || body.Message.ThreadID != "t1" {
			t.Fatalf("draft body = %+v (%v)", body, err)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"id": "d1", "message": map[string]any{"id": "m-d1", "threadId": "t1"}})
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-token"}}, Write: &fakeCreds{tokens: []string{"write-token"}}, Account: "work"})
	draft, err := client.CreateDraft(context.Background(), []byte("MIME"), "t1")
	if err != nil || draft.ID != "d1" || draft.Message.ID != "m-d1" {
		t.Fatalf("CreateDraft = %+v, %v", draft, err)
	}
}

func TestCreateDraftOmitsEmptyThreadID(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/gmail/v1/users/me/drafts", "write-token")
		var body struct {
			Message map[string]json.RawMessage `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body.Message["threadId"]; ok {
			t.Fatalf("draft body included empty threadId: %s", body.Message["threadId"])
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"id": "d1", "message": map[string]any{"id": "m-d1"}})
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-token"}}, Write: &fakeCreds{tokens: []string{"write-token"}}, Account: "work"})
	if _, err := client.CreateDraft(context.Background(), []byte("MIME"), ""); err != nil {
		t.Fatal(err)
	}
}

func TestDraftReadsRideReadCredentials(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gmail/v1/users/me/drafts" && r.Method == http.MethodGet:
			requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/drafts", "read-token")
			if r.URL.Query().Get("maxResults") != "25" {
				t.Fatalf("maxResults = %q", r.URL.Query().Get("maxResults"))
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"drafts": []map[string]any{{"id": "d1", "message": map[string]any{"id": "m-d1", "threadId": "t1"}}}})
		case r.URL.Path == "/gmail/v1/users/me/drafts/d1" && r.Method == http.MethodGet:
			requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/drafts/d1", "read-token")
			if r.URL.Query().Get("format") != "full" {
				t.Fatalf("format = %q", r.URL.Query().Get("format"))
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "d1", "message": map[string]any{"id": "m-d1", "threadId": "t1"}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-token"}}, Write: &fakeCreds{tokens: []string{"write-token"}}, Account: "work"})
	drafts, err := client.ListDrafts(context.Background(), 25)
	if err != nil || len(drafts) != 1 || drafts[0].ID != "d1" || drafts[0].Message.ID != "m-d1" {
		t.Fatalf("ListDrafts = %+v, %v", drafts, err)
	}
	draft, err := client.GetDraft(context.Background(), "d1", "full")
	if err != nil || draft.ID != "d1" || draft.Message.ID != "m-d1" {
		t.Fatalf("GetDraft = %+v, %v", draft, err)
	}
}

func TestGetDraftOmitsEmptyFormat(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/drafts/d1", "read-token")
		if _, present := r.URL.Query()["format"]; present {
			t.Fatalf("query included empty format: %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"id": "d1", "message": map[string]any{"id": "m-d1"}})
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-token"}}, Account: "work"})
	if _, err := client.GetDraft(context.Background(), "d1", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGetDraftsMetadataUsesReadBatchAndPreservesInputOrder(t *testing.T) {
	ids := []string{"d0", "d1", "d2"}
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		for index, id := range ids {
			part := parts[index]
			if got, want := part.request.Method, http.MethodGet; got != want {
				t.Fatalf("part %d method = %q, want %q", index, got, want)
			}
			if got, want := part.request.URL.Path, "/gmail/v1/users/me/drafts/"+id; got != want {
				t.Fatalf("part %d path = %q, want %q", index, got, want)
			}
			if got := part.request.URL.Query().Get("format"); got != "metadata" {
				t.Fatalf("part %d format = %q, want metadata", index, got)
			}
		}
		writeBatchResponse(t, w, []batchResponsePart{
			{index: 2, status: http.StatusOK, body: `{"id":"d2","message":{"id":"m2","threadId":"t2"}}`},
			{index: 1, status: http.StatusOK, body: `{"id":"d1","message":{"id":"m1","threadId":"t1"}}`},
			{index: 0, status: http.StatusOK, body: `{"id":"d0","message":{"id":"m0","threadId":"t0"}}`},
		})
	}, "token")

	drafts, err := client.GetDraftsMetadata(context.Background(), ids)
	if err != nil {
		t.Fatalf("GetDraftsMetadata: %v", err)
	}
	if requests != 1 {
		t.Fatalf("batch POSTs = %d, want 1", requests)
	}
	for index, id := range ids {
		if drafts[index].ID != id {
			t.Fatalf("drafts[%d].ID = %q, want %q", index, drafts[index].ID, id)
		}
	}
}

func TestDeleteDraftUsesWriteCredentialsAndDeleteMethod(t *testing.T) {
	client := newTestClientWithConfig(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodDelete, "/gmail/v1/users/me/drafts/d1", "write-token")
		w.WriteHeader(http.StatusNoContent)
	}, ClientConfig{Read: &fakeCreds{tokens: []string{"read-token"}}, Write: &fakeCreds{tokens: []string{"write-token"}}, Account: "work"})
	if err := client.DeleteDraft(context.Background(), "d1"); err != nil {
		t.Fatal(err)
	}
}
