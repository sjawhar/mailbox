package send

import "testing"

func draftTestRequest(body string) Request {
	return Request{To: []string{"a@example.test"}, Subject: "s", Body: body, Self: "work@example.test"}
}

func TestResolveDraftComposeRulesApply(t *testing.T) {
	if _, refusal := ResolveDraft(draftTestRequest(""), DraftThreading{}); refusal == nil || refusal.Rule != "R5" {
		t.Fatalf("empty body = %+v, want R5", refusal)
	}
	if _, refusal := ResolveDraft(Request{To: []string{"not-an-address"}, Subject: "s", Body: "b"}, DraftThreading{}); refusal == nil || refusal.Rule != "R3" {
		t.Fatalf("invalid address = %+v, want R3", refusal)
	}
	if _, refusal := ResolveDraft(Request{To: []string{"a@example.test"}, Subject: "s\r\nX: y", Body: "b"}, DraftThreading{}); refusal == nil || refusal.Rule != "R4" {
		t.Fatalf("CRLF subject = %+v, want R4", refusal)
	}
	if _, refusal := ResolveDraft(Request{Subject: "s", Body: "b"}, DraftThreading{}); refusal == nil || refusal.Rule != "R1" {
		t.Fatalf("no recipients = %+v, want R1", refusal)
	}
}

func TestResolveDraftThreadingValidation(t *testing.T) {
	env, refusal := ResolveDraft(draftTestRequest("b"), DraftThreading{
		ThreadID:   "t1",
		InReplyTo:  "<m-t1@example.test>",
		References: "<m-t0@example.test> <m-t1@example.test>",
	})
	if refusal != nil {
		t.Fatalf("threading refusal = %+v", refusal)
	}
	if env.Mode != ModeReply || env.ThreadID != "t1" || env.InReplyTo != "<m-t1@example.test>" {
		t.Fatalf("envelope = %+v, want reply mode with carried threading", env)
	}
	if len(env.References) != 2 || env.References[1] != "<m-t1@example.test>" {
		t.Fatalf("references = %v, want the two carried ids without re-appending", env.References)
	}
}

func TestResolveDraftThreadingHostileRows(t *testing.T) {
	for _, hostile := range []DraftThreading{
		{ThreadID: "t1\r\nBcc: x@example.test"},
		{InReplyTo: "<ok@example.test>", References: "<a@b>\r\n<c@d>"},
		{InReplyTo: "<crlf\r@example.test>"},
	} {
		if _, refusal := ResolveDraft(draftTestRequest("b"), hostile); refusal == nil || refusal.Rule != "R4" {
			t.Fatalf("hostile threading %+v = %+v, want R4", hostile, refusal)
		}
	}
	// A merely-invalid (non-hostile) msg-id drops silently, like reply derivation:
	env, refusal := ResolveDraft(draftTestRequest("b"), DraftThreading{ThreadID: "t1", InReplyTo: "not-a-msg-id", References: "junk <ok@example.test>"})
	if refusal != nil || env.Mode != ModeCompose || env.InReplyTo != "" || len(env.References) != 0 {
		t.Fatalf("invalid msg-ids: env=%+v refusal=%+v; want compose mode, dropped ids, no references without In-Reply-To", env, refusal)
	}
}

func TestResolveDraftExplicitSetsCannotFireR2R6(t *testing.T) {
	env, refusal := ResolveDraft(Request{To: []string{"work@example.test"}, Subject: "s", Body: "b", Self: "work@example.test"}, DraftThreading{})
	if refusal != nil || len(env.To) != 1 {
		t.Fatalf("self-addressed explicit draft = %+v, %+v; explicit sets never self-subtract (compose semantics)", env, refusal)
	}
}
