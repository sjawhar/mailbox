package send

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveRefusals(t *testing.T) {
	t.Run("R1 compose has no recipients", func(t *testing.T) {
		assertRefusal(t, Request{Mode: ModeCompose, Subject: "Status", Body: "update"}, "R1", "empty_recipients")
	})

	t.Run("R2 derived reply removes every self recipient", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode: ModeReply,
			Body: "update",
			Self: "user@example.com",
			Target: &TargetHeaders{
				ReplyTo: "USER@Example.COM",
				From:    "USER@Example.COM",
				To:      "USER@Example.COM",
				Cc:      "USER@Example.COM",
			},
		}, "R2", "self_only_recipients")
	})

	t.Run("R2 explicit reply removes self", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:   ModeReply,
			To:     []string{"USER@Example.COM"},
			Body:   "update",
			Self:   "user@example.com",
			Target: &TargetHeaders{From: "alice@example.com"},
		}, "R2", "self_only_recipients")
	})

	t.Run("R2 retains external explicit recipient", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode:   ModeReply,
			To:     []string{"USER@Example.COM", "teammate@example.com"},
			Body:   "update",
			Self:   "user@example.com",
			Target: &TargetHeaders{From: "alice@example.com"},
		})
		if refusal != nil {
			t.Fatalf("Resolve() refusal = %v", refusal)
		}
		want := []Recipient{{Address: "teammate@example.com", Provenance: ProvenanceExplicit}}
		if !reflect.DeepEqual(env.To, want) {
			t.Fatalf("Resolve() To = %#v, want %#v", env.To, want)
		}
	})

	t.Run("R3 rejects bare malformed address", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"not an address"},
			Subject: "Status",
			Body:    "update",
		}, "R3", "invalid_address")
	})

	t.Run("R3 rejects malformed address list", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"a@b, malformed <"},
			Subject: "Status",
			Body:    "update",
		}, "R3", "invalid_address")
	})

	t.Run("R4 rejects subject CRLF", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"a@example.com"},
			Subject: "Status\r\nBcc: evil@example.com",
			Body:    "update",
		}, "R4", "header_injection")
	})

	t.Run("R4 rejects recipient LF", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"a@example.com\n"},
			Subject: "Status",
			Body:    "update",
		}, "R4", "header_injection")
	})

	t.Run("R4 rejects derived subject control byte", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:   ModeReply,
			Body:   "update",
			Target: &TargetHeaders{From: "alice@example.com", Subject: "Status\a"},
		}, "R4", "header_injection")
	})

	t.Run("R5 rejects empty body", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"a@example.com"},
			Subject: "Status",
		}, "R5", "empty_body")
	})

	t.Run("R5 rejects whitespace-only body", func(t *testing.T) {
		assertRefusal(t, Request{
			Mode:    ModeCompose,
			To:      []string{"a@example.com"},
			Subject: "Status",
			Body:    " \n\t",
		}, "R5", "empty_body")
	})

	t.Run("R6 rejects divergent Reply-To", func(t *testing.T) {
		req := Request{
			Mode: ModeReply,
			Body: "update",
			Target: &TargetHeaders{
				ReplyTo: "attacker@evil.test",
				From:    "Alice <alice@corp.test>",
			},
		}
		_, refusal := Resolve(req)
		if refusal == nil {
			t.Fatal("Resolve() refusal = nil, want R6")
		}
		if refusal.Rule != "R6" || refusal.Code != "needs_explicit_recipient" {
			t.Fatalf("Resolve() refusal = %#v, want R6 needs_explicit_recipient", refusal)
		}
		wantReplyTo := []Recipient{{Address: "attacker@evil.test", Provenance: ProvenanceReplyTo}}
		wantFrom := []Recipient{{Address: "alice@corp.test", Display: "Alice", Provenance: ProvenanceFrom}}
		if !reflect.DeepEqual(refusal.ReplyTo, wantReplyTo) || !reflect.DeepEqual(refusal.From, wantFrom) {
			t.Fatalf("Resolve() candidates = Reply-To %#v, From %#v; want Reply-To %#v, From %#v", refusal.ReplyTo, refusal.From, wantReplyTo, wantFrom)
		}
	})

	t.Run("R6 explicit To bypasses divergent Reply-To", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			To:   []string{"bob@example.com"},
			Body: "update",
			Target: &TargetHeaders{
				ReplyTo: "attacker@evil.test",
				From:    "alice@corp.test",
			},
		})
		if refusal != nil {
			t.Fatalf("Resolve() refusal = %v", refusal)
		}
		want := []Recipient{{Address: "bob@example.com", Provenance: ProvenanceExplicit}}
		if !reflect.DeepEqual(env.To, want) {
			t.Fatalf("Resolve() To = %#v, want %#v", env.To, want)
		}
	})

	t.Run("R6 permits equivalent Reply-To and From", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			Body: "update",
			Target: &TargetHeaders{
				ReplyTo: "ALICE@EXAMPLE.COM",
				From:    "Alice Example <alice@example.com>",
			},
		})
		if refusal != nil {
			t.Fatalf("Resolve() refusal = %v", refusal)
		}
		want := []Recipient{{Address: "ALICE@EXAMPLE.COM", Provenance: ProvenanceReplyTo}}
		if !reflect.DeepEqual(env.To, want) {
			t.Fatalf("Resolve() To = %#v, want %#v", env.To, want)
		}
	})
}

func TestDeriveEnvelopeAllowsEmptyBody(t *testing.T) {
	envelope, refusal := DeriveEnvelope(Request{
		Mode: ModeReply,
		Self: "me@example.test",
		Target: &TargetHeaders{
			From:      "Sender <sender@example.test>",
			To:        "Me <me@example.test>",
			Subject:   "Target subject",
			MessageID: "<target@example.test>",
		},
	})
	if refusal != nil {
		t.Fatalf("DeriveEnvelope() refusal = %v", refusal)
	}
	if envelope.Body != "" {
		t.Fatalf("DeriveEnvelope() body = %q, want empty", envelope.Body)
	}
	if envelope.Subject != "Re: Target subject" {
		t.Fatalf("DeriveEnvelope() subject = %q, want reply subject", envelope.Subject)
	}
	wantRecipients := []Recipient{{Address: "sender@example.test", Display: "Sender", Provenance: ProvenanceFrom}}
	if !reflect.DeepEqual(envelope.To, wantRecipients) {
		t.Fatalf("DeriveEnvelope() To = %#v, want %#v", envelope.To, wantRecipients)
	}
}

func TestResolveReplyDerivation(t *testing.T) {
	t.Run("reply all removes self and preserves source provenance", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			Body: "update",
			Self: "user@example.com",
			Target: &TargetHeaders{
				From: "Alice <alice@example.com>",
				To:   "USER@example.com, Bob <bob@example.com>, Duplicated <dupe@EXAMPLE.com>",
				Cc:   "bob@example.com, Dupe <DUPE@example.COM>, Carol <carol@example.com>",
			},
		})
		if refusal != nil {
			t.Fatalf("Resolve() refusal = %v", refusal)
		}
		wantTo := []Recipient{{Address: "alice@example.com", Display: "Alice", Provenance: ProvenanceFrom}}
		wantCC := []Recipient{
			{Address: "bob@example.com", Display: "Bob", Provenance: ProvenanceTo},
			{Address: "dupe@EXAMPLE.com", Display: "Duplicated", Provenance: ProvenanceTo},
			{Address: "carol@example.com", Display: "Carol", Provenance: ProvenanceCC},
		}
		if !reflect.DeepEqual(env.To, wantTo) || !reflect.DeepEqual(env.Cc, wantCC) {
			t.Fatalf("Resolve() recipients = To %#v, Cc %#v; want To %#v, Cc %#v", env.To, env.Cc, wantTo, wantCC)
		}
	})

	t.Run("explicit reply recipients replace all derived recipients", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			To:   []string{"chosen@example.com"},
			Body: "update",
			Target: &TargetHeaders{
				From: "alice@example.com",
				To:   "user@example.com",
				Cc:   "ignored@example.com",
			},
		})
		if refusal != nil {
			t.Fatalf("Resolve() refusal = %v", refusal)
		}
		want := []Recipient{{Address: "chosen@example.com", Provenance: ProvenanceExplicit}}
		if !reflect.DeepEqual(env.To, want) || len(env.Cc) != 0 {
			t.Fatalf("Resolve() recipients = To %#v, Cc %#v; want To %#v, empty Cc", env.To, env.Cc, want)
		}
	})
}

func TestResolveSubjectAndThreading(t *testing.T) {
	t.Run("reply deduplicates Re prefixes", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode:   ModeReply,
			Body:   "update",
			Target: &TargetHeaders{From: "alice@example.com", Subject: "Re: RE: re: Standup"},
		})
		if refusal != nil || env.Subject != "Re: Standup" {
			t.Fatalf("Resolve() = %#v, %v; want subject Re: Standup", env, refusal)
		}
	})

	t.Run("forward deduplicates Fwd prefixes", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode:   ModeForward,
			To:     []string{"bob@example.com"},
			Body:   "update",
			Target: &TargetHeaders{Subject: "Fwd: FWD: Fwd: Report"},
		})
		if refusal != nil || env.Subject != "Fwd: Report" {
			t.Fatalf("Resolve() = %#v, %v; want subject Fwd: Report", env, refusal)
		}
	})

	t.Run("forward retains Re prefix", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode:   ModeForward,
			To:     []string{"bob@example.com"},
			Body:   "update",
			Target: &TargetHeaders{Subject: "Re: X"},
		})
		if refusal != nil || env.Subject != "Fwd: Re: X" {
			t.Fatalf("Resolve() = %#v, %v; want subject Fwd: Re: X", env, refusal)
		}
	})

	t.Run("valid Message-ID appends grammar-valid References", func(t *testing.T) {
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			Body: "update",
			Target: &TargetHeaders{
				From:       "alice@example.com",
				MessageID:  "<message@example.com>",
				References: "<root@example.com> <parent@example.com>",
			},
		})
		want := []string{"<root@example.com>", "<parent@example.com>", "<message@example.com>"}
		if refusal != nil || env.InReplyTo != "<message@example.com>" || !reflect.DeepEqual(env.References, want) {
			t.Fatalf("Resolve() = %#v, %v; want threading %#v", env, refusal, want)
		}
	})

	t.Run("missing Message-ID omits threading headers", func(t *testing.T) {
		env, refusal := Resolve(Request{Mode: ModeReply, Body: "update", Target: &TargetHeaders{From: "alice@example.com", References: "<root@example.com>"}})
		if refusal != nil || env.InReplyTo != "" || len(env.References) != 0 {
			t.Fatalf("Resolve() = %#v, %v; want omitted threading", env, refusal)
		}
	})

	t.Run("invalid Message-ID omits threading headers", func(t *testing.T) {
		env, refusal := Resolve(Request{Mode: ModeReply, Body: "update", Target: &TargetHeaders{From: "alice@example.com", MessageID: "<x y@example.com>", References: "<root@example.com>"}})
		if refusal != nil || env.InReplyTo != "" || len(env.References) != 0 {
			t.Fatalf("Resolve() = %#v, %v; want omitted threading", env, refusal)
		}
	})

	t.Run("invalid Reference tokens are omitted", func(t *testing.T) {
		tooLong := "<" + strings.Repeat("a", 995) + "@b>"
		env, refusal := Resolve(Request{
			Mode: ModeReply,
			Body: "update",
			Target: &TargetHeaders{
				From:       "alice@example.com",
				MessageID:  "<message@example.com>",
				References: "<ok@example.com> junk <ok2@example.com> " + tooLong,
			},
		})
		want := []string{"<ok@example.com>", "<ok2@example.com>", "<message@example.com>"}
		if refusal != nil || !reflect.DeepEqual(env.References, want) {
			t.Fatalf("Resolve() References = %#v, refusal = %v; want %#v", env.References, refusal, want)
		}
	})
}

func TestValidMsgID(t *testing.T) {
	validAtLimit := "<" + strings.Repeat("a", 994) + "@b>"
	cases := []struct {
		token string
		want  bool
	}{
		{"<a@b>", true},
		{"<a@b@c>", true},
		{validAtLimit, true},
		{"<a b@c>", false},
		{"<a@b", false},
		{"a@b>", false},
		{"<@b>", false},
		{"<a@>", false},
		{"<" + strings.Repeat("a", 995) + "@b>", false},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			if got := ValidMsgID(tc.token); got != tc.want {
				t.Fatalf("ValidMsgID(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeCompose, "compose"},
		{ModeReply, "reply"},
		{ModeForward, "forward"},
		{Mode(99), ""},
	}
	for _, tc := range cases {
		if got := tc.mode.String(); got != tc.want {
			t.Fatalf("Mode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func assertRefusal(t *testing.T, req Request, wantRule, wantCode string) {
	t.Helper()
	_, refusal := Resolve(req)
	if refusal == nil {
		t.Fatalf("Resolve() refusal = nil, want %s", wantRule)
	}
	if refusal.Rule != wantRule || refusal.Code != wantCode {
		t.Fatalf("Resolve() refusal = %#v, want rule %s code %s", refusal, wantRule, wantCode)
	}
	if !strings.Contains(refusal.Message, wantRule) || refusal.Error() != refusal.Message {
		t.Fatalf("Resolve() refusal message = %q, Error() = %q; want message naming %s", refusal.Message, refusal.Error(), wantRule)
	}
}

func TestReplyToOwnMessagePromotesOriginalToRecipients(t *testing.T) {
	env, ref := Resolve(Request{
		Mode: ModeReply,
		Self: "me@example.test",
		Body: "following up",
		Target: &TargetHeaders{
			From:    "Me <me@example.test>",
			To:      "Ada <ada@example.test>, me@example.test",
			Cc:      "Grace <grace@example.test>",
			Subject: "Getting set up",
		},
	})
	if ref != nil {
		t.Fatalf("refusal = %+v, want none", ref)
	}
	if len(env.To) != 1 || env.To[0].Address != "ada@example.test" || env.To[0].Provenance != ProvenanceTo {
		t.Fatalf("To = %+v, want promoted original-To recipient ada@example.test", env.To)
	}
	if len(env.Cc) != 1 || env.Cc[0].Address != "grace@example.test" || env.Cc[0].Provenance != ProvenanceCC {
		t.Fatalf("Cc = %+v, want grace@example.test retained as CC", env.Cc)
	}
}

func TestReplyToOwnMessageWithSelfOnlyToStaysUnpromoted(t *testing.T) {
	env, ref := Resolve(Request{
		Mode: ModeReply,
		Self: "me@example.test",
		Body: "note",
		Target: &TargetHeaders{
			From:    "Me <me@example.test>",
			To:      "me@example.test",
			Cc:      "Grace <grace@example.test>",
			Subject: "Note to self",
		},
	})
	if ref != nil {
		t.Fatalf("refusal = %+v, want none", ref)
	}
	if len(env.To) != 0 || len(env.Cc) != 1 {
		t.Fatalf("To,Cc = %+v,%+v; want empty To and grace in CC", env.To, env.Cc)
	}
}
