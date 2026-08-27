package gmail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type receivedBatchPart struct {
	contentID string
	request   *http.Request
	body      []byte
}

type batchResponsePart struct {
	index   int
	status  int
	headers http.Header
	body    string
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func readBatchRequest(t *testing.T, r *http.Request) []receivedBatchPart {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", r.Method)
	}
	if r.URL.Path != "/batch/gmail/v1" {
		t.Fatalf("path = %q, want /batch/gmail/v1", r.URL.Path)
	}
	if r.Header.Get("Authorization") == "" {
		t.Fatal("Authorization is empty")
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse batch Content-Type: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("batch media type = %q, want multipart/mixed", mediaType)
	}

	reader := multipart.NewReader(r.Body, params["boundary"])
	var parts []receivedBatchPart
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read batch part: %v", err)
		}
		if got := part.Header.Get("Content-Type"); got != "application/http" {
			t.Fatalf("part Content-Type = %q, want application/http", got)
		}
		request, err := http.ReadRequest(bufio.NewReader(part))
		if err != nil {
			t.Fatalf("read embedded request: %v", err)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read embedded body: %v", err)
		}
		parts = append(parts, receivedBatchPart{
			contentID: part.Header.Get("Content-ID"),
			request:   request,
			body:      body,
		})
	}
	return parts
}

func writeBatchResponse(t *testing.T, w http.ResponseWriter, parts []batchResponsePart) {
	t.Helper()
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	w.WriteHeader(http.StatusOK)

	for _, response := range parts {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", "application/http")
		headers.Set("Content-ID", fmt.Sprintf("<response-item%d>", response.index))
		part, err := writer.CreatePart(headers)
		if err != nil {
			t.Fatalf("create batch response part: %v", err)
		}
		if _, err := fmt.Fprintf(part, "HTTP/1.1 %d %s\r\n", response.status, http.StatusText(response.status)); err != nil {
			t.Fatalf("write batch response status: %v", err)
		}
		for key, values := range response.headers {
			for _, value := range values {
				if _, err := fmt.Fprintf(part, "%s: %s\r\n", key, value); err != nil {
					t.Fatalf("write batch response header: %v", err)
				}
			}
		}
		if _, err := fmt.Fprintf(part, "Content-Type: application/json\r\n\r\n%s", response.body); err != nil {
			t.Fatalf("write batch response body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close batch response: %v", err)
	}
}

func metadataResponse(index int, id string) batchResponsePart {
	return batchResponsePart{index: index, status: http.StatusOK, body: fmt.Sprintf(`{"id":%q}`, id)}
}

func requireMetadataPart(t *testing.T, part receivedBatchPart, index int, id string) {
	t.Helper()
	if got, want := part.contentID, fmt.Sprintf("<item%d>", index); got != want {
		t.Fatalf("part Content-ID = %q, want %q", got, want)
	}
	if got := part.request.Method; got != http.MethodGet {
		t.Fatalf("inner method = %q, want GET", got)
	}
	if got, want := part.request.URL.Path, "/gmail/v1/users/me/threads/"+id; got != want {
		t.Fatalf("inner path = %q, want %q", got, want)
	}
	query := part.request.URL.Query()
	if got := query.Get("format"); got != "metadata" {
		t.Fatalf("inner format = %q, want metadata", got)
	}
	if got, want := query["metadataHeaders"], []string{"From", "To", "Subject", "Date"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("metadataHeaders = %v, want %v", got, want)
	}
}

func TestBatchMetadataRequestShape(t *testing.T) {
	ids := []string{"t0", "t1", "t2"}
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		for index, id := range ids {
			requireMetadataPart(t, parts[index], index, id)
		}
		writeBatchResponse(t, w, []batchResponsePart{
			metadataResponse(0, "t0"),
			metadataResponse(1, "t1"),
			metadataResponse(2, "t2"),
		})
	}, "token")

	threads, err := client.GetThreadsMetadata(context.Background(), ids)
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	if requests != 1 {
		t.Fatalf("batch POSTs = %d, want 1", requests)
	}
	if len(threads) != len(ids) {
		t.Fatalf("threads = %d, want %d", len(threads), len(ids))
	}
}

func TestBatchResponseOutOfOrder(t *testing.T) {
	ids := []string{"t0", "t1", "t2"}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		writeBatchResponse(t, w, []batchResponsePart{
			metadataResponse(2, "t2"),
			metadataResponse(1, "t1"),
			metadataResponse(0, "t0"),
		})
	}, "token")

	threads, err := client.GetThreadsMetadata(context.Background(), ids)
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	for index, id := range ids {
		if threads[index].ID != id {
			t.Fatalf("threads[%d].ID = %q, want %q", index, threads[index].ID, id)
		}
	}
}

func TestBatchChunksAt100(t *testing.T) {
	ids := make([]string, 150)
	for index := range ids {
		ids[index] = fmt.Sprintf("t%d", index)
	}

	var partCounts []int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := readBatchRequest(t, r)
		partCounts = append(partCounts, len(parts))
		responses := make([]batchResponsePart, len(parts))
		for index, part := range parts {
			id := part.request.URL.Path[strings.LastIndex(part.request.URL.Path, "/")+1:]
			responses[index] = metadataResponse(index, id)
		}
		writeBatchResponse(t, w, responses)
	}, "token")

	threads, err := client.GetThreadsMetadata(context.Background(), ids)
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	if got, want := partCounts, []int{100, 50}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("batch part counts = %v, want %v", got, want)
	}
	if len(threads) != len(ids) {
		t.Fatalf("threads = %d, want %d", len(threads), len(ids))
	}
}

func TestBatchPartError(t *testing.T) {
	ids := []string{"t-ok", "t-missing"}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		writeBatchResponse(t, w, []batchResponsePart{
			metadataResponse(0, "t-ok"),
			{index: 1, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
		})
	}, "token")

	_, err := client.GetThreadsMetadata(context.Background(), ids)
	if err == nil || !strings.Contains(err.Error(), "t-missing") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("GetThreadsMetadata error = %v, want failing id and status", err)
	}
}

func TestModifyThreadsSingleDirect(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/gmail/v1/users/me/threads/t1/modify", "token")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if got, want := string(body), `{"addLabelIds":[],"removeLabelIds":["INBOX"]}`; got != want {
			t.Fatalf("request body = %s, want %s", got, want)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{})
	}, "token")

	if err := client.ModifyThreads(context.Background(), []string{"t1"}, nil, []string{"INBOX"}); err != nil {
		t.Fatalf("ModifyThreads: %v", err)
	}
}

func TestModifyThreadsBatch(t *testing.T) {
	ids := []string{"t0", "t1"}
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		for index, id := range ids {
			if got, want := parts[index].request.Method, http.MethodPost; got != want {
				t.Fatalf("inner method = %q, want %q", got, want)
			}
			if got, want := parts[index].request.URL.Path, "/gmail/v1/users/me/threads/"+id+"/modify"; got != want {
				t.Fatalf("inner path = %q, want %q", got, want)
			}
			if got, want := parts[index].request.Header.Get("Content-Type"), "application/json"; got != want {
				t.Fatalf("inner Content-Type = %q, want %q", got, want)
			}
			if got, want := string(parts[index].body), `{"addLabelIds":["STARRED"],"removeLabelIds":[]}`; got != want {
				t.Fatalf("inner body = %s, want %s", got, want)
			}
		}
		writeBatchResponse(t, w, []batchResponsePart{
			{index: 0, status: http.StatusNoContent},
			{index: 1, status: http.StatusNoContent},
		})
	}, "token")

	if err := client.ModifyThreads(context.Background(), ids, []string{"STARRED"}, nil); err != nil {
		t.Fatalf("ModifyThreads: %v", err)
	}
}

func TestTrashThreadsBatch(t *testing.T) {
	ids := []string{"t0", "t1", "t2"}
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		parts := readBatchRequest(t, r)
		if len(parts) != len(ids) {
			t.Fatalf("batch parts = %d, want %d", len(parts), len(ids))
		}
		for index, id := range ids {
			if got, want := parts[index].contentID, fmt.Sprintf("<item%d>", index); got != want {
				t.Fatalf("part Content-ID = %q, want %q", got, want)
			}
			if got := parts[index].request.Method; got != http.MethodPost {
				t.Fatalf("inner method = %q, want POST", got)
			}
			if got, want := parts[index].request.URL.Path, "/gmail/v1/users/me/threads/"+id+"/trash"; got != want {
				t.Fatalf("inner path = %q, want %q", got, want)
			}
			if len(parts[index].body) != 0 {
				t.Fatalf("inner body = %q, want empty", parts[index].body)
			}
		}
		writeBatchResponse(t, w, []batchResponsePart{
			{index: 0, status: http.StatusNoContent},
			{index: 1, status: http.StatusNoContent},
			{index: 2, status: http.StatusNoContent},
		})
	}, "token")

	if err := client.TrashThreads(context.Background(), ids); err != nil {
		t.Fatalf("TrashThreads: %v", err)
	}
	if requests != 1 {
		t.Fatalf("batch POSTs = %d, want 1", requests)
	}
}

func TestBatchRateLimitRetriesOnlyFailedParts(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		switch requestCount {
		case 1:
			if len(parts) != 2 {
				t.Fatalf("first batch parts = %d, want 2", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t0")
			requireMetadataPart(t, parts[1], 1, "t1")
			writeBatchResponse(t, w, []batchResponsePart{
				metadataResponse(0, "t0"),
				{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
			})
		case 2:
			if len(parts) != 1 {
				t.Fatalf("retry batch parts = %d, want 1", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t1")
			writeBatchResponse(t, w, []batchResponsePart{metadataResponse(0, "t1")})
		default:
			t.Fatalf("unexpected batch request %d", requestCount)
		}
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration { return 0 }

	threads, err := client.GetThreadsMetadata(context.Background(), []string{"t0", "t1"})
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("batch requests = %d, want 2", requestCount)
	}
	if len(slept) != 1 || slept[0] != 250*time.Millisecond {
		t.Fatalf("retry sleeps = %v, want [250ms]", slept)
	}
	if len(threads) != 2 || threads[0].ID != "t0" || threads[1].ID != "t1" {
		t.Fatalf("threads = %#v, want [t0 t1]", threads)
	}
}

func TestBatchRetriesQuotaForbidden(t *testing.T) {
	for _, reason := range []string{"rateLimitExceeded", "userRateLimitExceeded"} {
		t.Run(reason, func(t *testing.T) {
			var requestCount int
			var slept []time.Duration
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				parts := readBatchRequest(t, r)
				if len(parts) != 1 {
					t.Fatalf("batch parts = %d, want 1", len(parts))
				}
				if requestCount == 1 {
					writeBatchResponse(t, w, []batchResponsePart{{
						index:  0,
						status: http.StatusForbidden,
						body:   fmt.Sprintf(`{"error":{"code":403,"message":"Quota exceeded","errors":[{"reason":%q}]}}`, reason),
					}})
					return
				}
				writeBatchResponse(t, w, []batchResponsePart{metadataResponse(0, "t1")})
			}, "token")
			client.sleep = func(ctx context.Context, delay time.Duration) error {
				slept = append(slept, delay)
				return nil
			}
			client.jitter = func(time.Duration) time.Duration { return 0 }

			threads, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
			if err != nil {
				t.Fatalf("GetThreadsMetadata: %v", err)
			}
			if len(threads) != 1 || threads[0].ID != "t1" {
				t.Fatalf("threads = %#v, want t1", threads)
			}
			if requestCount != 2 {
				t.Fatalf("batch requests = %d, want 2", requestCount)
			}
			if len(slept) != 1 || slept[0] != 250*time.Millisecond {
				t.Fatalf("retry sleeps = %v, want [250ms]", slept)
			}
		})
	}
}

func TestBatchRetriesAnyTooManyRequests(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		headers   http.Header
		wantSleep time.Duration
	}{
		{
			name:      "user rate limit reason",
			body:      `{"error":{"code":429,"message":"User rate limit exceeded","errors":[{"reason":"userRateLimitExceeded"}]}}`,
			headers:   http.Header{"Retry-After": {"5"}},
			wantSleep: time.Second,
		},
		{
			name:      "missing reason",
			body:      `{"error":{"code":429,"message":"Too Many Requests"}}`,
			wantSleep: 250 * time.Millisecond,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount int
			var slept []time.Duration
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				parts := readBatchRequest(t, r)
				if len(parts) != 1 {
					t.Fatalf("batch parts = %d, want 1", len(parts))
				}
				if requestCount == 1 {
					writeBatchResponse(t, w, []batchResponsePart{{
						index:   0,
						status:  http.StatusTooManyRequests,
						headers: test.headers,
						body:    test.body,
					}})
					return
				}
				writeBatchResponse(t, w, []batchResponsePart{metadataResponse(0, "t1")})
			}, "token")
			client.sleep = func(ctx context.Context, delay time.Duration) error {
				slept = append(slept, delay)
				return nil
			}
			client.jitter = func(time.Duration) time.Duration { return 0 }

			threads, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
			if err != nil {
				t.Fatalf("GetThreadsMetadata: %v", err)
			}
			if len(threads) != 1 || threads[0].ID != "t1" {
				t.Fatalf("threads = %#v, want t1", threads)
			}
			if requestCount != 2 {
				t.Fatalf("batch requests = %d, want 2", requestCount)
			}
			if got := slept; len(got) != 1 || got[0] != test.wantSleep {
				t.Fatalf("retry sleeps = %v, want [%s]", got, test.wantSleep)
			}
		})
	}
}

func TestSingleRequestRetriesAnyTooManyRequests(t *testing.T) {
	tests := []struct {
		name      string
		body      map[string]any
		headers   http.Header
		wantSleep time.Duration
	}{
		{
			name:      "user rate limit reason",
			body:      googleError(http.StatusTooManyRequests, "userRateLimitExceeded", "User rate limit exceeded"),
			headers:   http.Header{"Retry-After": {"5"}},
			wantSleep: time.Second,
		},
		{
			name: "missing reason",
			body: map[string]any{
				"error": map[string]any{
					"code":    http.StatusTooManyRequests,
					"message": "Too Many Requests",
				},
			},
			wantSleep: 250 * time.Millisecond,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestCount int
			var slept []time.Duration
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/profile", "token")
				if requestCount == 1 {
					for key, values := range test.headers {
						w.Header()[key] = values
					}
					writeJSON(t, w, http.StatusTooManyRequests, test.body)
					return
				}
				writeJSON(t, w, http.StatusOK, map[string]string{"emailAddress": "sami@example.com"})
			}, "token")
			client.sleep = func(ctx context.Context, delay time.Duration) error {
				slept = append(slept, delay)
				return nil
			}
			client.jitter = func(time.Duration) time.Duration { return 0 }

			profile, err := client.GetProfile(context.Background())
			if err != nil {
				t.Fatalf("GetProfile: %v", err)
			}
			if profile.EmailAddress != "sami@example.com" {
				t.Fatalf("EmailAddress = %q, want sami@example.com", profile.EmailAddress)
			}
			if requestCount != 2 {
				t.Fatalf("requests = %d, want 2", requestCount)
			}
			if got := slept; len(got) != 1 || got[0] != test.wantSleep {
				t.Fatalf("retry sleeps = %v, want [%s]", got, test.wantSleep)
			}
		})
	}
}

func TestBatchRateLimitExhaustionReturnsMappedError(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		if len(parts) != 1 {
			t.Fatalf("batch parts = %d, want 1", len(parts))
		}
		writeBatchResponse(t, w, []batchResponsePart{{
			index:  0,
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`,
		}})
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration { return 0 }

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rateLimitExceeded") {
		t.Fatalf("GetThreadsMetadata error = %v, want mapped rate-limit error", err)
	}
	if requestCount != 4 {
		t.Fatalf("batch requests = %d, want 4", requestCount)
	}
	if got, want := slept, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("retry sleeps = %v, want %v", got, want)
	}
}

func TestBatchDoesNotRetryOtherFourXX(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		if len(parts) != 1 {
			t.Fatalf("batch parts = %d, want 1", len(parts))
		}
		writeBatchResponse(t, w, []batchResponsePart{{
			index:  0,
			status: http.StatusForbidden,
			body:   `{"error":{"code":403,"message":"Forbidden","errors":[{"reason":"insufficientPermissions"}]}}`,
		}})
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("GetThreadsMetadata error = %v, want mapped 403 error", err)
	}
	if requestCount != 1 {
		t.Fatalf("batch requests = %d, want 1", requestCount)
	}
	if len(slept) != 0 {
		t.Fatalf("retry sleeps = %v, want none", slept)
	}
}

func TestSingleRequestRateLimitRetries(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		requireRequest(t, r, http.MethodGet, "/gmail/v1/users/me/profile", "token")
		if requestCount == 1 {
			writeJSON(t, w, http.StatusTooManyRequests, googleError(http.StatusTooManyRequests, "rateLimitExceeded", "Rate Limit Exceeded"))
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"emailAddress": "sami@example.com"})
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration { return 0 }

	profile, err := client.GetProfile(context.Background())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.EmailAddress != "sami@example.com" {
		t.Fatalf("EmailAddress = %q, want sami@example.com", profile.EmailAddress)
	}
	if requestCount != 2 {
		t.Fatalf("requests = %d, want 2", requestCount)
	}
	if len(slept) != 1 || slept[0] != 250*time.Millisecond {
		t.Fatalf("retry sleeps = %v, want [250ms]", slept)
	}
}

func TestBatchQuotaRetriesShareUnauthorizedBudget(t *testing.T) {
	var requestCount int
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		if len(parts) != 1 {
			t.Fatalf("batch parts = %d, want 1", len(parts))
		}
		switch requestCount {
		case 1, 3:
			writeBatchResponse(t, w, []batchResponsePart{{
				index:  0,
				status: http.StatusTooManyRequests,
				body:   `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`,
			}})
		case 2, 4:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}, "old", "new")
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(time.Duration) time.Duration { return 0 }

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
	if err == nil || !strings.Contains(err.Error(), "still unauthorized") {
		t.Fatalf("GetThreadsMetadata error = %v, want second-401 failure", err)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
	if requestCount != 4 {
		t.Fatalf("batch requests = %d, want 4", requestCount)
	}
}

func TestBatchMixedFailureRetriesRateLimitedPartBeforeReturning(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		switch requestCount {
		case 1:
			if len(parts) != 2 {
				t.Fatalf("first batch parts = %d, want 2", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t-missing")
			requireMetadataPart(t, parts[1], 1, "t-limited")
			writeBatchResponse(t, w, []batchResponsePart{
				{index: 0, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
				{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
			})
		case 2, 3, 4:
			if len(parts) != 1 {
				t.Fatalf("retry batch parts = %d, want 1", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t-limited")
			writeBatchResponse(t, w, []batchResponsePart{{
				index:  0,
				status: http.StatusTooManyRequests,
				body:   `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`,
			}})
		default:
			t.Fatalf("unexpected batch request %d", requestCount)
		}
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration { return 0 }

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t-missing", "t-limited"})
	if err == nil {
		t.Fatal("GetThreadsMetadata error = nil, want aggregated terminal failures")
	}
	for _, want := range []string{"t-missing", "404", "notFound", "t-limited", "429", "rateLimitExceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("GetThreadsMetadata error = %v, missing %q", err, want)
		}
	}
	if requestCount != 4 {
		t.Fatalf("batch requests = %d, want 4", requestCount)
	}
	if got, want := slept, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("retry sleeps = %v, want %v", got, want)
	}
}

func TestBatchSubsetRetryStillUnauthorizedPreservesSystemError(t *testing.T) {
	var requestCount int
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		switch requestCount {
		case 1:
			if len(parts) != 2 {
				t.Fatalf("first batch parts = %d, want 2", len(parts))
			}
			writeBatchResponse(t, w, []batchResponsePart{
				{index: 0, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
				{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
			})
		case 2, 3:
			if len(parts) != 1 {
				t.Fatalf("retry batch parts = %d, want 1", len(parts))
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected batch request %d", requestCount)
		}
	}, "old", "new")
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(time.Duration) time.Duration { return 0 }

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t-missing", "t-limited"})
	if err == nil {
		t.Fatal("GetThreadsMetadata error = nil, want still-unauthorized failure")
	}
	if !errors.Is(err, stillUnauthorizedError()) {
		t.Fatalf("GetThreadsMetadata error = %v, want the still-unauthorized failure", err)
	}
	if !strings.HasPrefix(err.Error(), stillUnauthorizedError().Error()) {
		t.Fatalf("GetThreadsMetadata error = %v, want still-unauthorized failure to be dominant", err)
	}
	if !strings.Contains(err.Error(), "t-missing") {
		t.Fatalf("GetThreadsMetadata error = %v, want prior terminal failure context", err)
	}
	var batchFailures *batchFailuresError
	if errors.As(err, &batchFailures) {
		t.Fatalf("GetThreadsMetadata error = %v, want system error rather than batch failure aggregate", err)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
	if requestCount != 3 {
		t.Fatalf("batch requests = %d, want 3", requestCount)
	}
}

func TestBatchSubsetRetryTransportErrorPreservesSystemError(t *testing.T) {
	var serverRequests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		serverRequests++
		parts := readBatchRequest(t, r)
		if len(parts) != 2 {
			t.Fatalf("first batch parts = %d, want 2", len(parts))
		}
		writeBatchResponse(t, w, []batchResponsePart{
			{index: 0, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
			{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
		})
	}, "token")
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(time.Duration) time.Duration { return 0 }

	transportErr := errors.New("subset retry transport failed")
	baseTransport := client.HTTP.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	var transportRequests int
	client.HTTP.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		transportRequests++
		if transportRequests == 2 {
			return nil, transportErr
		}
		return baseTransport.RoundTrip(r)
	})

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t-missing", "t-limited"})
	if err == nil {
		t.Fatal("GetThreadsMetadata error = nil, want transport failure")
	}
	if !errors.Is(err, transportErr) {
		t.Fatalf("GetThreadsMetadata error = %v, want wrapped transport error", err)
	}
	if !strings.Contains(err.Error(), "t-missing") {
		t.Fatalf("GetThreadsMetadata error = %v, want prior terminal failure context", err)
	}
	var batchFailures *batchFailuresError
	if errors.As(err, &batchFailures) {
		t.Fatalf("GetThreadsMetadata error = %v, want system error rather than batch failure aggregate", err)
	}
	if transportRequests != 2 {
		t.Fatalf("transport requests = %d, want 2", transportRequests)
	}
	if serverRequests != 1 {
		t.Fatalf("server requests = %d, want 1", serverRequests)
	}
}

func TestBatchSubsetRetryCredentialErrorPreservesSystemError(t *testing.T) {
	var requestCount int
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		if len(parts) != 2 {
			t.Fatalf("first batch parts = %d, want 2", len(parts))
		}
		writeBatchResponse(t, w, []batchResponsePart{
			{index: 0, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
			{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
		})
	}, "token")
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.jitter = func(time.Duration) time.Duration { return 0 }
	credentialErr := errors.New("credential resolution failed")
	creds.mu.Lock()
	creds.accessTokenErrAt = 2
	creds.accessTokenErr = credentialErr
	creds.mu.Unlock()

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t-missing", "t-limited"})
	if err == nil {
		t.Fatal("GetThreadsMetadata error = nil, want credential failure")
	}
	if !errors.Is(err, credentialErr) {
		t.Fatalf("GetThreadsMetadata error = %v, want wrapped credential error", err)
	}
	if !strings.Contains(err.Error(), "t-missing") {
		t.Fatalf("GetThreadsMetadata error = %v, want prior terminal failure context", err)
	}
	var batchFailures *batchFailuresError
	if errors.As(err, &batchFailures) {
		t.Fatalf("GetThreadsMetadata error = %v, want system error rather than batch failure aggregate", err)
	}
	if requestCount != 1 {
		t.Fatalf("batch requests = %d, want 1", requestCount)
	}
}

func TestBatchMixedFailureAggregatesOuterRetryFailure(t *testing.T) {
	var requestCount int
	var slept []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		parts := readBatchRequest(t, r)
		switch requestCount {
		case 1:
			if len(parts) != 2 {
				t.Fatalf("first batch parts = %d, want 2", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t-missing")
			requireMetadataPart(t, parts[1], 1, "t-limited")
			writeBatchResponse(t, w, []batchResponsePart{
				{index: 0, status: http.StatusNotFound, body: `{"error":{"code":404,"message":"Not Found","errors":[{"reason":"notFound"}]}}`},
				{index: 1, status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`},
			})
		case 2:
			if len(parts) != 1 {
				t.Fatalf("retry batch parts = %d, want 1", len(parts))
			}
			requireMetadataPart(t, parts[0], 0, "t-limited")
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected batch request %d", requestCount)
		}
	}, "token")
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		slept = append(slept, delay)
		return nil
	}
	client.jitter = func(time.Duration) time.Duration { return 0 }

	_, err := client.GetThreadsMetadata(context.Background(), []string{"t-missing", "t-limited"})
	if err == nil {
		t.Fatal("GetThreadsMetadata error = nil, want aggregated failures")
	}
	for _, want := range []string{"t-missing", "404", "notFound", "t-limited", "500"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("GetThreadsMetadata error = %v, missing %q", err, want)
		}
	}
	if requestCount != 2 {
		t.Fatalf("batch requests = %d, want 2", requestCount)
	}
	if got, want := slept, []time.Duration{250 * time.Millisecond}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("retry sleeps = %v, want %v", got, want)
	}
}

func TestBatchOuterUnauthorizedRetriesWholeBatch(t *testing.T) {
	var authorization []string
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization = append(authorization, r.Header.Get("Authorization"))
		parts := readBatchRequest(t, r)
		if len(parts) != 1 {
			t.Fatalf("batch parts = %d, want 1", len(parts))
		}
		if len(authorization) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeBatchResponse(t, w, []batchResponsePart{metadataResponse(0, "t1")})
	}, "old", "new")

	threads, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "t1" {
		t.Fatalf("threads = %#v, want t1", threads)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
	if got, want := authorization, []string{"Bearer old", "Bearer new"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Authorization values = %v, want %v", got, want)
	}
}

func TestBatchPartUnauthorizedRetriesWholeBatch(t *testing.T) {
	var authorization []string
	client, creds := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization = append(authorization, r.Header.Get("Authorization"))
		parts := readBatchRequest(t, r)
		if len(parts) != 1 {
			t.Fatalf("batch parts = %d, want 1", len(parts))
		}
		if len(authorization) == 1 {
			writeBatchResponse(t, w, []batchResponsePart{{
				index:  0,
				status: http.StatusUnauthorized,
				body:   `{"error":{"code":401,"message":"Unauthorized","errors":[{"reason":"authError"}]}}`,
			}})
			return
		}
		writeBatchResponse(t, w, []batchResponsePart{metadataResponse(0, "t1")})
	}, "old", "new")

	threads, err := client.GetThreadsMetadata(context.Background(), []string{"t1"})
	if err != nil {
		t.Fatalf("GetThreadsMetadata: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "t1" {
		t.Fatalf("threads = %#v, want t1", threads)
	}
	creds.mu.Lock()
	invalidated := creds.invalidated
	creds.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", invalidated)
	}
	if got, want := authorization, []string{"Bearer old", "Bearer new"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Authorization values = %v, want %v", got, want)
	}
}
