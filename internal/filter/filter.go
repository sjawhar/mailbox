// Package filter compiles and evaluates the named header filters defined in
// mailbox configuration. Regexes are Go RE2, compiled at config load only;
// compiled rules are used exclusively as pattern.MatchString(headerValue) —
// there is no path that compiles a regexp from mail content.
package filter

import (
	"fmt"
	"regexp"
	"sort"
)

type Field string

const (
	FieldFrom    Field = "from"
	FieldTo      Field = "to"
	FieldCc      Field = "cc"
	FieldSubject Field = "subject"
	FieldList    Field = "list"
)

// MaxHeaderBytes bounds a decoded header value for matching. Values over the
// bound are non-matching: never truncated (a truncated prefix could match a
// rule the full value would not) and never echoed into diagnostics.
const MaxHeaderBytes = 8192

var headerByField = map[Field]string{
	FieldFrom:    "From",
	FieldTo:      "To",
	FieldCc:      "Cc",
	FieldSubject: "Subject",
	FieldList:    "List-ID",
}

// fieldOrder is the deterministic rule order inside one filter.
var fieldOrder = []Field{FieldCc, FieldFrom, FieldList, FieldSubject, FieldTo}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// HeaderName returns the mail header a field matches against.
func HeaderName(f Field) string { return headerByField[f] }

type Rule struct {
	Field   Field
	pattern *regexp.Regexp
}

// Filter is one compiled named filter. All rules must match one message
// (AND); a thread matches when any of its messages matches (union).
type Filter struct {
	Name  string
	Rules []Rule
}

// Compile validates a filter name and rule table and compiles its patterns.
// Error text names the filter and field and never includes mail content.
func Compile(name string, rules map[string]string) (*Filter, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("filters.%s: invalid filter name (want [a-z0-9][a-z0-9-]*)", name)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("filters.%s: at least one rule is required", name)
	}
	fields := make([]string, 0, len(rules))
	for field := range rules {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if _, known := headerByField[Field(field)]; !known {
			return nil, fmt.Errorf("filters.%s: unknown field %q (fields: cc, from, list, subject, to)", name, field)
		}
	}
	compiled := &Filter{Name: name, Rules: make([]Rule, 0, len(rules))}
	for _, field := range fieldOrder {
		pattern, present := rules[string(field)]
		if !present {
			continue
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("filters.%s.%s: invalid regexp: %v", name, field, err)
		}
		compiled.Rules = append(compiled.Rules, Rule{Field: field, pattern: expression})
	}
	return compiled, nil
}
