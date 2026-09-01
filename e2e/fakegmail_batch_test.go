package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

func (g *fakeGmail) serveBatch(w http.ResponseWriter, request *http.Request) {
	mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" || params["boundary"] == "" {
		http.Error(w, "invalid batch request", http.StatusBadRequest)
		return
	}
	reader := multipart.NewReader(request.Body, params["boundary"])
	var responses []batchHTTPResponse
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "invalid batch request", http.StatusBadRequest)
			return
		}
		if part.Header.Get("Content-Type") != "application/http" {
			http.Error(w, "invalid batch request part", http.StatusBadRequest)
			return
		}
		inner, err := http.ReadRequest(bufio.NewReader(part))
		if err != nil {
			http.Error(w, "invalid batch HTTP request", http.StatusBadRequest)
			return
		}
		if _, err := io.Copy(io.Discard, inner.Body); err != nil {
			http.Error(w, "invalid batch HTTP request body", http.StatusBadRequest)
			return
		}
		inner.Body.Close()
		g.recordBatchRequest(inner)
		responses = append(responses, g.batchResponse(request, inner))
	}

	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	for index, response := range responses {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Type", "application/http")
		headers.Set("Content-ID", fmt.Sprintf("<response-item%d>", index))
		part, err := writer.CreatePart(headers)
		if err != nil {
			panic(err)
		}
		if _, err := fmt.Fprintf(part, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", response.status, http.StatusText(response.status), len(response.body), response.body); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
}

type batchHTTPResponse struct {
	status int
	body   string
}

func (g *fakeGmail) batchResponse(outer, inner *http.Request) batchHTTPResponse {
	const threadPrefix = "/gmail/v1/users/me/threads/"
	const draftPrefix = "/gmail/v1/users/me/drafts/"
	threadPath := strings.TrimPrefix(inner.URL.Path, threadPrefix)
	switch {
	case inner.Method == http.MethodGet && strings.HasPrefix(inner.URL.Path, draftPrefix):
		g.recordReadAuth(outer)
		id := strings.TrimPrefix(inner.URL.Path, draftPrefix)
		response, found, err := g.draftResponse(id, inner.URL.Query())
		if !found {
			return batchHTTPResponse{status: http.StatusNotFound, body: `{"error":{"message":"not found"}}`}
		}
		if err != nil {
			return batchHTTPResponse{status: http.StatusInternalServerError, body: `{"error":{"message":"invalid draft MIME"}}`}
		}
		body, err := json.Marshal(response)
		if err != nil {
			panic(err)
		}
		return batchHTTPResponse{status: http.StatusOK, body: string(body)}
	case inner.Method == http.MethodGet && strings.HasPrefix(inner.URL.Path, threadPrefix):
		g.recordReadAuth(outer)
		thread, found := g.threadResponse(threadPath, inner.URL.Query())
		if !found {
			return batchHTTPResponse{status: http.StatusNotFound, body: `{"error":{"message":"not found"}}`}
		}
		return batchHTTPResponse{status: http.StatusOK, body: thread}
	case inner.Method == http.MethodPost && strings.HasSuffix(threadPath, "/modify"):
		g.recordModified(outer, strings.TrimSuffix(threadPath, "/modify"))
		return batchHTTPResponse{status: http.StatusOK, body: `{}`}
	case inner.Method == http.MethodPost && strings.HasSuffix(threadPath, "/trash"):
		g.recordTrashed(outer, strings.TrimSuffix(threadPath, "/trash"))
		return batchHTTPResponse{status: http.StatusOK, body: `{}`}
	default:
		return batchHTTPResponse{status: http.StatusNotFound, body: `{"error":{"message":"not found"}}`}
	}
}
