package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/mailbox/internal/toon/toontest"
)

type filterActionResult struct {
	Account   string                      `json:"account"`
	Action    string                      `json:"action"`
	Filter    string                      `json:"filter"`
	Matched   int                         `json:"matched"`
	Attempted int                         `json:"attempted"`
	Succeeded []string                    `json:"succeeded"`
	Failed    []filterActionFailureResult `json:"failed"`
	OK        bool                        `json:"ok"`
}

type filterActionFailureResult struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
}

func newBulkFilterRig(t *testing.T, g *gmailTestServer) *configRig {
	t.Helper()
	rig := newConfigRig(t, g, `default_account = "work"
[accounts.work]
read_credential_env = "CLI_READ"
write_credential_cmd = ["record-write"]

[filters.github]
from = "notifications@github\\.com"

[filters.hiring]
subject = "(?i)red.?team"
`)
	g.writeToken = "bulk-write-token-1234567890"
	rig.replaceCommand(t, "record-write", "#!/bin/sh\nprintf '%s %s\\n' \"$0\" \"$*\" >> "+rig.spawnLog+"\nprintf '%s\\n' bulk-write-token-1234567890\n")
	return rig
}

func runBulkFilterCommand(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeFilterActionJSON(t *testing.T, stdout string) filterActionResult {
	t.Helper()
	var result filterActionResult
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode filter action JSON %q: %v", stdout, err)
	}
	if err := assertOneJSON(decoder); err != nil {
		t.Fatalf("filter action JSON stdout purity: %v", err)
	}
	return result
}

func bulkFilterFixture(t *testing.T, pages [][]string, matched map[string]bool) (*gmailTestServer, *configRig) {
	t.Helper()
	g := newGmailTestServer(t)
	g.listPages = pages
	g.metadata = make(map[string]map[string]any)
	seen := make(map[string]struct{})
	for _, page := range pages {
		for _, id := range page {
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			g.metadata[id] = filterMetadataThread(id, matched[id])
		}
	}
	return g, newBulkFilterRig(t, g)
}

func filterMetadataThread(id string, matches bool) map[string]any {
	thread := metadataThread(id)
	if !matches {
		return thread
	}
	headers := thread["messages"].([]map[string]any)[0]["payload"].(map[string]any)["headers"].([]map[string]string)
	for index := range headers {
		if headers[index]["name"] == "From" {
			headers[index]["value"] = "notifications@github.com"
			return thread
		}
	}
	panic("test metadata omitted From header")
}

func threadIDs(count int) []string {
	ids := make([]string, count)
	for index := range ids {
		ids[index] = fmt.Sprintf("t-%03d", index)
	}
	return ids
}

func allMatching(ids []string) map[string]bool {
	matched := make(map[string]bool, len(ids))
	for _, id := range ids {
		matched[id] = true
	}
	return matched
}

func TestArchiveFilterActsAcrossAllPagesWithOneUnlock(t *testing.T) {
	pages := [][]string{{"t1", "t2"}, {"t3", "t4"}, {"t5", "t6"}}
	g, rig := bulkFilterFixture(t, pages, map[string]bool{"t1": true, "t3": true, "t4": true, "t5": true, "t6": true})

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("archive --filter = (%d, %q), want success", code, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	want := []string{"t1", "t3", "t4", "t5", "t6"}
	if result.Matched != len(want) || result.Attempted != len(want) || !result.OK || !sameStrings(result.Succeeded, want) || len(result.Failed) != 0 {
		t.Fatalf("archive receipt = %#v, want %d successful ids %v", result, len(want), want)
	}
	if got := rig.recordedSpawns(t); strings.Count(got, "record-write") != 1 {
		t.Fatalf("write credential helper spawns = %q, want one", got)
	}
	if !sameStrings(flattenWriteIDs(g), want) {
		t.Fatalf("write ids = %v, want %v", flattenWriteIDs(g), want)
	}
}

func TestBulkFilterUnknownNameErrorsBeforeUnlock(t *testing.T) {
	g, rig := bulkFilterFixture(t, [][]string{{"t1"}}, map[string]bool{"t1": true})

	code, _, stderr := runBulkFilterCommand(t, "archive", "--filter", "nope", "--json")
	if code != 1 || !strings.Contains(stderr, `unknown filter "nope"`) || !strings.Contains(stderr, "github, hiring") {
		t.Fatalf("archive unknown filter = (%d, %q), want runtime error listing filters", code, stderr)
	}
	if got := rig.recordedSpawns(t); got != "" {
		t.Fatalf("unknown filter spent a write unlock: %q", got)
	}
	if len(g.batchWriteIDs) != 0 {
		t.Fatalf("unknown filter sent writes: %v", g.batchWriteIDs)
	}
}

func TestDuplicateIDAcrossPagesDedupsEverywhere(t *testing.T) {
	pages := [][]string{{"t1", "t2"}, {"t3"}, {"t1", "t4"}}
	g, _ := bulkFilterFixture(t, pages, allMatching([]string{"t1", "t2", "t3", "t4"}))

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("archive duplicate pages = (%d, %q), want success", code, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	want := []string{"t1", "t2", "t3", "t4"}
	if !sameStrings(result.Succeeded, want) || result.Attempted != len(want) {
		t.Fatalf("receipt = %#v, want one result per first-seen id %v", result, want)
	}
	if got := countBatchRequests(g, "GET", "t1"); got != 1 {
		t.Fatalf("t1 hydration requests = %d, want one", got)
	}
	if got := countBatchRequests(g, "POST", "t1"); got != 1 {
		t.Fatalf("t1 write requests = %d, want one", got)
	}
}

func TestPartialBatchFailureReportsReceiptsInAllFormatsNonzero(t *testing.T) {
	ids := threadIDs(250)
	formats := []struct {
		name string
		args []string
	}{
		{name: "json", args: []string{"--json"}},
		{name: "toon", args: nil},
		{name: "text", args: []string{"--text"}},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			g, _ := bulkFilterFixture(t, [][]string{ids}, allMatching(ids))
			g.batchItemResponses = map[string][]scriptedResponse{
				"t-149": {{status: 403, reason: "insufficientPermissions"}},
			}
			args := append([]string{"archive", "--filter", "github"}, format.args...)
			code, stdout, stderr := runBulkFilterCommand(t, args...)
			if code != 1 || !strings.Contains(stderr, "insufficientPermissions") {
				t.Fatalf("archive partial failure = (%d, %q), want receipt failure", code, stderr)
			}
			switch format.name {
			case "json":
				result := decodeFilterActionJSON(t, stdout)
				assertPartialFilterFailure(t, result)
			case "toon":
				value, err := toontest.Decode(strings.TrimSuffix(stdout, "\n"))
				if err != nil {
					t.Fatalf("decode TOON %q: %v", stdout, err)
				}
				if got := toonNumber(t, value, "matched"); got != "250" {
					t.Fatalf("TOON matched = %q, want 250", got)
				}
				if got := toonNumber(t, value, "attempted"); got != "250" {
					t.Fatalf("TOON attempted = %q, want 250", got)
				}
				if got := len(toonField(t, value, "succeeded").Arr); got != 249 {
					t.Fatalf("TOON succeeded count = %d, want 249", got)
				}
				failed := toonField(t, value, "failed")
				if failed.Kind != toontest.Array || len(failed.Arr) != 1 || toonString(t, failed.Arr[0], "id") != "t-149" {
					t.Fatalf("TOON failed = %#v, want t-149 receipt", failed)
				}
			case "text":
				if !strings.Contains(stdout, "archived 249 of 250 matched thread(s) (filter: github)") || !strings.Contains(stdout, "failed: t-149 (403 insufficientPermissions)") {
					t.Fatalf("text receipt = %q, want counts and failed id", stdout)
				}
			}
		})
	}
}

func TestBulkFilterInsufficientScopeReceiptsKeepPayloadAndProvisioningHint(t *testing.T) {
	g, _ := bulkFilterFixture(t, [][]string{{"t1"}}, allMatching([]string{"t1"}))
	g.forbidden = true

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 1 {
		t.Fatalf("archive insufficient-scope receipt = (%d, %q, %q), want exit 1", code, stdout, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	if result.Matched != 1 || result.Attempted != 1 || result.OK ||
		len(result.Succeeded) != 0 ||
		len(result.Failed) != 1 ||
		result.Failed[0] != (filterActionFailureResult{ID: "t1", Status: 403, Reason: "insufficientPermissions"}) {
		t.Fatalf("insufficient-scope receipt payload = %#v, want failed t1 receipt", result)
	}
	if !strings.Contains(stderr, "provision:") || !strings.Contains(stderr, "gmail.modify") {
		t.Fatalf("insufficient-scope receipt stderr = %q, want write provisioning hint", stderr)
	}
}

func TestBulkFilterTransportAbortStillRendersPartialReceipts(t *testing.T) {
	ids := threadIDs(150)
	g, _ := bulkFilterFixture(t, [][]string{ids}, allMatching(ids))
	g.batchRequestStatus = []int{0, 401, 401}
	t.Setenv("MAILBOX_TOKEN", "test-token")

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 1 || !strings.Contains(stderr, "still unauthorized") {
		t.Fatalf("archive transport abort = (%d, %q), want transport fatal", code, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	if result.Matched != 150 || result.Attempted != 100 || len(result.Succeeded) != 100 || len(result.Failed) != 0 || result.OK {
		t.Fatalf("transport receipt = %#v, want first-chunk partial successes", result)
	}
	if g.batchWriteCalls != 3 {
		t.Fatalf("batch write calls = %d, want successful chunk plus two 401 attempts", g.batchWriteCalls)
	}
}

func TestManualRetrySucceedsIdempotently(t *testing.T) {
	ids := threadIDs(250)
	g, _ := bulkFilterFixture(t, [][]string{ids}, allMatching(ids))
	g.batchItemResponses = map[string][]scriptedResponse{
		"t-149": {{status: 403, reason: "insufficientPermissions"}},
	}
	if code, _, _ := runBulkFilterCommand(t, "archive", "--filter", "github", "--json"); code != 1 {
		t.Fatalf("initial partial archive = %d, want 1", code)
	}

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("retry archive = (%d, %q), want success", code, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	if result.Matched != 250 || result.Attempted != 250 || len(result.Succeeded) != 250 || len(result.Failed) != 0 || !result.OK || !containsString(result.Succeeded, "t-149") {
		t.Fatalf("retry receipt = %#v, want every id including the prior failure", result)
	}
}

func Test429OnLaterListingPageWritesNothing(t *testing.T) {
	pages := [][]string{{"t1"}, {"t2"}, {"t3"}}
	g, _ := bulkFilterFixture(t, pages, allMatching([]string{"t1", "t2", "t3"}))
	g.listPageStatus = map[int]int{2: 429}

	code, _, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 1 || !strings.Contains(stderr, "429") {
		t.Fatalf("later listing rate limit = (%d, %q), want failure", code, stderr)
	}
	if len(g.batchWriteIDs) != 0 {
		t.Fatalf("listing failure sent writes: %v", g.batchWriteIDs)
	}
}

func Test429InLaterBatchChunkRetriesPendingOnly(t *testing.T) {
	ids := threadIDs(150)
	g, _ := bulkFilterFixture(t, [][]string{ids}, allMatching(ids))
	g.batchItemResponses = map[string][]scriptedResponse{
		"t-120": {{status: 429, retryAfter: "1", reason: "rateLimitExceeded"}},
	}

	code, stdout, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("batch rate limit = (%d, %q), want success", code, stderr)
	}
	result := decodeFilterActionJSON(t, stdout)
	if result.Matched != 150 || result.Attempted != 150 || len(result.Succeeded) != 150 || len(result.Failed) != 0 || !result.OK {
		t.Fatalf("rate-limit receipt = %#v, want all successful", result)
	}
	if g.batchWriteCalls != 3 || len(g.batchWriteIDs) != 3 {
		t.Fatalf("batch writes = %d %#v, want chunk 1, chunk 2, retry", g.batchWriteCalls, g.batchWriteIDs)
	}
	if got := g.batchWriteIDs[2]; !sameStrings(got, []string{"t-120"}) {
		t.Fatalf("retry write ids = %v, want only t-120", got)
	}
}

func TestZeroMatchesExitsZeroWithEmptyArrays(t *testing.T) {
	formats := []struct {
		name string
		args []string
	}{
		{name: "json", args: []string{"--json"}},
		{name: "toon", args: nil},
		{name: "text", args: []string{"--text"}},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			g, _ := bulkFilterFixture(t, [][]string{{"t1"}}, map[string]bool{})
			args := append([]string{"archive", "--filter", "github"}, format.args...)
			code, stdout, stderr := runBulkFilterCommand(t, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("zero-match archive = (%d, %q), want success", code, stderr)
			}
			if len(g.batchWriteIDs) != 0 {
				t.Fatalf("zero-match archive sent writes: %v", g.batchWriteIDs)
			}
			switch format.name {
			case "json":
				result := decodeFilterActionJSON(t, stdout)
				if result.Matched != 0 || result.Attempted != 0 || len(result.Succeeded) != 0 || len(result.Failed) != 0 || !result.OK {
					t.Fatalf("zero-match JSON = %#v", result)
				}
				assertEmptyJSONField(t, stdout, "succeeded")
				assertEmptyJSONField(t, stdout, "failed")
			case "toon":
				value, err := toontest.Decode(strings.TrimSuffix(stdout, "\n"))
				if err != nil {
					t.Fatalf("decode zero-match TOON %q: %v", stdout, err)
				}
				if got := toonField(t, value, "succeeded"); got.Kind != toontest.Array || len(got.Arr) != 0 {
					t.Fatalf("TOON succeeded = %#v, want empty array", got)
				}
				if got := toonField(t, value, "failed"); got.Kind != toontest.Array || len(got.Arr) != 0 {
					t.Fatalf("TOON failed = %#v, want empty array", got)
				}
			case "text":
				if !strings.Contains(stdout, "matched 0 thread(s) (filter: github)") {
					t.Fatalf("zero-match text = %q", stdout)
				}
			}
		})
	}
}

func TestFilterAndIDsAreMutuallyExclusive(t *testing.T) {
	g, rig := bulkFilterFixture(t, [][]string{{"t1"}}, map[string]bool{"t1": true})

	code, _, stderr := runBulkFilterCommand(t, "archive", "--filter", "github", "t1", "--json")
	if code != 2 || !strings.Contains(stderr, "--filter and thread ids are mutually exclusive") {
		t.Fatalf("archive --filter with id = (%d, %q), want usage refusal", code, stderr)
	}
	if got := rig.recordedSpawns(t); got != "" || len(g.batchWriteIDs) != 0 {
		t.Fatalf("usage refusal spent write access: spawns=%q writes=%v", got, g.batchWriteIDs)
	}
}

func TestBulkFilterSupportsArchiveTrashMarkAndLabel(t *testing.T) {
	cases := []struct {
		name string
		args []string
		path string
		body string
	}{
		{name: "archive", args: []string{"archive", "--filter", "github", "--json"}, path: "/modify", body: `"removeLabelIds":["INBOX"]`},
		{name: "trash", args: []string{"trash", "--filter", "github", "--json"}, path: "/trash"},
		{name: "mark read", args: []string{"mark", "read", "--filter", "github", "--json"}, path: "/modify", body: `"removeLabelIds":["UNREAD"]`},
		{name: "mark unread", args: []string{"mark", "unread", "--filter", "github", "--json"}, path: "/modify", body: `"addLabelIds":["UNREAD"]`},
		{name: "label add", args: []string{"label", "add", "Newsletters", "--filter", "github", "--json"}, path: "/modify", body: `"addLabelIds":["Label_7"]`},
		{name: "label rm", args: []string{"label", "rm", "Newsletters", "--filter", "github", "--json"}, path: "/modify", body: `"removeLabelIds":["Label_7"]`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			g, _ := bulkFilterFixture(t, [][]string{{"t1", "t2"}}, map[string]bool{"t1": true, "t2": true})
			code, stdout, stderr := runBulkFilterCommand(t, testCase.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s = (%d, %q), want success", testCase.name, code, stderr)
			}
			result := decodeFilterActionJSON(t, stdout)
			if result.Matched != 2 || result.Attempted != 2 || !sameStrings(result.Succeeded, []string{"t1", "t2"}) || !result.OK {
				t.Fatalf("%s receipt = %#v", testCase.name, result)
			}
			if len(g.batchWriteIDs) != 1 || !sameStrings(g.batchWriteIDs[0], []string{"t1", "t2"}) {
				t.Fatalf("%s writes = %v, want t1 and t2", testCase.name, g.batchWriteIDs)
			}
			request := lastBatchWriteRequest(t, g)
			if !strings.Contains(request, testCase.path) || (testCase.body != "" && !strings.Contains(request, testCase.body)) {
				t.Fatalf("%s batch request = %q, want path %q and body %q", testCase.name, request, testCase.path, testCase.body)
			}
		})
	}
}

func assertPartialFilterFailure(t *testing.T, result filterActionResult) {
	t.Helper()
	if result.Matched != 250 || result.Attempted != 250 || len(result.Succeeded) != 249 || result.OK {
		t.Fatalf("partial receipt = %#v, want 249 successful of 250", result)
	}
	if len(result.Failed) != 1 || result.Failed[0] != (filterActionFailureResult{ID: "t-149", Status: 403, Reason: "insufficientPermissions"}) {
		t.Fatalf("failed receipt = %#v, want t-149 insufficientPermissions", result.Failed)
	}
	if !containsString(result.Succeeded, "t-249") {
		t.Fatalf("partial receipt omitted later chunk success: %#v", result.Succeeded)
	}
}

func flattenWriteIDs(g *gmailTestServer) []string {
	var ids []string
	for _, batch := range g.batchWriteIDs {
		ids = append(ids, batch...)
	}
	return ids
}

func countBatchRequests(g *gmailTestServer, method, id string) int {
	needle := method + " /gmail/v1/users/me/threads/" + id
	count := 0
	for _, request := range g.batchRequests {
		if strings.HasPrefix(request, needle) {
			count++
		}
	}
	return count
}

func lastBatchWriteRequest(t *testing.T, g *gmailTestServer) string {
	t.Helper()
	for index := len(g.batchRequests) - 1; index >= 0; index-- {
		if strings.HasPrefix(g.batchRequests[index], "POST ") {
			return g.batchRequests[index]
		}
	}
	t.Fatal("no batch write request recorded")
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func toonField(t *testing.T, value toontest.Value, key string) toontest.Value {
	t.Helper()
	if value.Kind != toontest.Object {
		t.Fatalf("TOON value kind = %v, want object", value.Kind)
	}
	for _, field := range value.Obj {
		if field.Key == key {
			return field.Val
		}
	}
	t.Fatalf("TOON object missing field %q: %#v", key, value)
	return toontest.Value{}
}

func toonNumber(t *testing.T, value toontest.Value, key string) string {
	t.Helper()
	field := toonField(t, value, key)
	if field.Kind != toontest.Number {
		t.Fatalf("TOON %s kind = %v, want number", key, field.Kind)
	}
	return field.Num
}

func toonString(t *testing.T, value toontest.Value, key string) string {
	t.Helper()
	field := toonField(t, value, key)
	if field.Kind != toontest.String {
		t.Fatalf("TOON %s kind = %v, want string", key, field.Kind)
	}
	return field.Str
}
