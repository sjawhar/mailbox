package filter

import "github.com/sjawhar/mailbox/internal/gmail"

// MatchesThread reports whether any message of the thread satisfies every
// rule of the filter. Matching input is the decoded, unfolded header value
// (gmail.Message.Header). A value longer than MaxHeaderBytes is non-matching.
// An absent header evaluates as "".
func MatchesThread(f *Filter, thread *gmail.Thread) bool {
	if f == nil || thread == nil {
		return false
	}
	for _, message := range thread.Messages {
		if message == nil {
			continue
		}
		if matchesMessage(f, message) {
			return true
		}
	}
	return false
}

func matchesMessage(f *Filter, message *gmail.Message) bool {
	for _, rule := range f.Rules {
		value := message.Header(headerByField[rule.Field])
		if len(value) > MaxHeaderBytes {
			return false
		}
		if !rule.pattern.MatchString(value) {
			return false
		}
	}
	return true
}

// FilterThreads returns the threads matching f, retaining input order.
// A nil filter selects everything.
func FilterThreads(f *Filter, threads []*gmail.Thread) []*gmail.Thread {
	if f == nil {
		return threads
	}
	matched := make([]*gmail.Thread, 0, len(threads))
	for _, thread := range threads {
		if MatchesThread(f, thread) {
			matched = append(matched, thread)
		}
	}
	return matched
}
