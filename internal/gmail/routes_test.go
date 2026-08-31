package gmail

import (
	"net/http"
	"strings"
	"testing"
)

func TestRouteMapIsCompleteAndExact(t *testing.T) {
	want := map[operation]route{
		opThreadsList:     {http.MethodGet, "/gmail/v1/users/me/threads", classRead, "gmail.readonly"},
		opThreadsGet:      {http.MethodGet, "/gmail/v1/users/me/threads/%s", classRead, "gmail.readonly"},
		opThreadsModify:   {http.MethodPost, "/gmail/v1/users/me/threads/%s/modify", classWrite, "gmail.modify"},
		opThreadsTrash:    {http.MethodPost, "/gmail/v1/users/me/threads/%s/trash", classWrite, "gmail.modify"},
		opMessagesGet:     {http.MethodGet, "/gmail/v1/users/me/messages/%s", classRead, "gmail.readonly"},
		opMessagesGetFull: {http.MethodGet, "/gmail/v1/users/me/messages/%s", classRead, "gmail.readonly"},
		opAttachmentsGet:  {http.MethodGet, "/gmail/v1/users/me/messages/%s/attachments/%s", classRead, "gmail.readonly"},
		opLabelsList:      {http.MethodGet, "/gmail/v1/users/me/labels", classRead, "gmail.readonly"},
		opProfileGet:      {http.MethodGet, "/gmail/v1/users/me/profile", classRead, "gmail.readonly"},
		opMessagesSend:    {http.MethodPost, "/gmail/v1/users/me/messages/send", classSend, "gmail.send"},
		opDraftsList:      {http.MethodGet, "/gmail/v1/users/me/drafts", classRead, "gmail.readonly"},
		opDraftsGet:       {http.MethodGet, "/gmail/v1/users/me/drafts/%s", classRead, "gmail.readonly"},
		opDraftsCreate:    {http.MethodPost, "/gmail/v1/users/me/drafts", classWrite, "gmail.modify"},
		opDraftsDelete:    {http.MethodDelete, "/gmail/v1/users/me/drafts/%s", classWrite, "gmail.modify"},
	}
	if int(operationCount) != len(want) {
		t.Fatalf("operation count = %d, want %d — the allowlist and this test move together", operationCount, len(want))
	}
	for op, wantRoute := range want {
		if routes[op] != wantRoute {
			t.Fatalf("routes[%d] = %+v, want %+v", op, routes[op], wantRoute)
		}
	}
}

func TestNoDraftsSendOperationExists(t *testing.T) {
	sendClassOps := 0
	for op := operation(0); op < operationCount; op++ {
		if strings.Contains(routes[op].template, "drafts/send") {
			t.Fatalf("routes[%d] reaches drafts.send: %q — prohibited by construction", op, routes[op].template)
		}
		if routes[op].class == classSend {
			sendClassOps++
			if routes[op].template != "/gmail/v1/users/me/messages/send" {
				t.Fatalf("send-class operation %d transmits via %q, want messages.send only", op, routes[op].template)
			}
		}
	}
	if sendClassOps != 1 {
		t.Fatalf("send-class operations = %d, want exactly one (messages.send)", sendClassOps)
	}
}
