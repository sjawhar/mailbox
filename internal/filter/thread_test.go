package filter

import (
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/gmail"
)

func message(headers map[string]string) *gmail.Message {
	part := &gmail.MessagePart{}
	for name, value := range headers {
		part.Headers = append(part.Headers, gmail.Header{Name: name, Value: value})
	}
	return &gmail.Message{Payload: part}
}

func mustCompile(t *testing.T, name string, rules map[string]string) *Filter {
	t.Helper()
	f, err := Compile(name, rules)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRulesANDAcrossFieldsUnionAcrossMessages(t *testing.T) {
	f := mustCompile(t, "hiring", map[string]string{
		"subject": `(?i)red.?team`,
		"from":    `@jobs\.`,
	})
	thread := &gmail.Thread{ID: "t1", Messages: []*gmail.Message{
		message(map[string]string{"From": "x@jobs.example", "Subject": "hello"}),          // from matches, subject does not
		message(map[string]string{"From": "y@corp.example", "Subject": "Red Team intro"}), // subject matches, from does not
	}}
	if MatchesThread(f, thread) {
		t.Fatal("AND across rules must hold per message, not across messages")
	}
	thread.Messages = append(thread.Messages,
		message(map[string]string{"From": "z@jobs.example", "Subject": "red-team next steps"}))
	if !MatchesThread(f, thread) {
		t.Fatal("any single message satisfying every rule must match the thread")
	}
}

func TestDecodedEncodedWordHeadersMatch(t *testing.T) {
	f := mustCompile(t, "uml", map[string]string{"subject": `Grüße`})
	thread := &gmail.Thread{Messages: []*gmail.Message{
		message(map[string]string{"Subject": "=?UTF-8?Q?Gr=C3=BC=C3=9Fe?="}),
	}}
	if !MatchesThread(f, thread) {
		t.Fatal("matching input must be the decoded header value")
	}
}

func TestCcAndListMatchOnNonLatestMessage(t *testing.T) {
	f := mustCompile(t, "lists", map[string]string{"list": `dev\.example`, "cc": `carol@`})
	thread := &gmail.Thread{Messages: []*gmail.Message{
		message(map[string]string{"List-ID": "<dev.example>", "Cc": "carol@example.test", "From": "old@example.test"}),
		message(map[string]string{"From": "new@example.test"}), // latest message: no Cc, no List-ID
	}}
	// InternalDate zero on both; the point is evaluation walks EVERY message.
	if !MatchesThread(f, thread) {
		t.Fatal("cc and List-ID rules must match on a non-latest message (per-message hydration)")
	}
}

func TestOversizeHeaderIsNonMatchingAndUndisclosed(t *testing.T) {
	huge := strings.Repeat("a", MaxHeaderBytes+1)
	f := mustCompile(t, "big", map[string]string{"subject": `a`})
	thread := &gmail.Thread{Messages: []*gmail.Message{message(map[string]string{"Subject": huge})}}
	if MatchesThread(f, thread) {
		t.Fatal(">8 KiB header must be non-matching, never truncated-and-matched")
	}
	boundary := strings.Repeat("a", MaxHeaderBytes)
	thread = &gmail.Thread{Messages: []*gmail.Message{message(map[string]string{"Subject": boundary})}}
	if !MatchesThread(f, thread) {
		t.Fatal("exactly 8 KiB is within bounds")
	}
}

func TestMailTextRegexSyntaxIsInertHaystack(t *testing.T) {
	f := mustCompile(t, "lit", map[string]string{"subject": `^release$`})
	thread := &gmail.Thread{Messages: []*gmail.Message{
		message(map[string]string{"Subject": `release|.*`}), // regex syntax in MAIL content
	}}
	if MatchesThread(f, thread) {
		t.Fatal("mail text must be a literal haystack, never compiled")
	}
}

func TestAbsentHeaderEvaluatesAsEmpty(t *testing.T) {
	f := mustCompile(t, "nocc", map[string]string{"cc": `.+`})
	thread := &gmail.Thread{Messages: []*gmail.Message{message(map[string]string{"From": "a@b"})}}
	if MatchesThread(f, thread) {
		t.Fatal(`absent header is "" — a pattern requiring content must not match`)
	}
}

func TestFilterThreadsRetainsOrderAndNilPassthrough(t *testing.T) {
	f := mustCompile(t, "pick", map[string]string{"from": `keep`})
	threads := []*gmail.Thread{
		{ID: "a", Messages: []*gmail.Message{message(map[string]string{"From": "keep@x"})}},
		{ID: "b", Messages: []*gmail.Message{message(map[string]string{"From": "drop@x"})}},
		{ID: "c", Messages: []*gmail.Message{message(map[string]string{"From": "keep@y"})}},
	}
	got := FilterThreads(f, threads)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("FilterThreads() = %v, want [a c] in order", got)
	}
	if all := FilterThreads(nil, threads); len(all) != 3 {
		t.Fatalf("nil filter must pass threads through, got %d", len(all))
	}
}
