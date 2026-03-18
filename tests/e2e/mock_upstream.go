package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

// RecordedRequest stores key parts of an HTTP request received by MockUpstream.
type RecordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    string
}

// MockUpstream is a lightweight HTTP server that records incoming requests
// and returns configurable OpenAI-compatible responses.
type MockUpstream struct {
	server   *http.Server
	listener net.Listener

	mu       sync.Mutex
	requests []RecordedRequest
}

// StartMockUpstream creates and starts a MockUpstream on a random port.
func StartMockUpstream() *MockUpstream {
	m := &MockUpstream{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", m.handleChatCompletions)
	mux.HandleFunc("/v1/embeddings", m.handleEmbeddings)

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		panic(fmt.Sprintf("mock upstream: failed to listen: %v", err))
	}

	m.listener = listener
	m.server = &http.Server{Handler: mux}

	go m.server.Serve(listener) //nolint:errcheck

	return m
}

// Port returns the listening port.
func (m *MockUpstream) Port() int {
	return m.listener.Addr().(*net.TCPAddr).Port
}

// ExternalHost returns the hostname that Docker containers (Kong) should use
// to reach this server. Configurable via E2E_MOCK_UPSTREAM_HOST env var;
// defaults to "host.docker.internal" (works on macOS Docker Desktop).
func (m *MockUpstream) ExternalHost() string {
	return Cfg.MockUpstreamHost
}

// ExternalURL returns the full URL reachable from Docker containers.
func (m *MockUpstream) ExternalURL() string {
	return fmt.Sprintf("http://%s:%d", m.ExternalHost(), m.Port())
}

// Stop shuts down the server.
func (m *MockUpstream) Stop() {
	if m.server != nil {
		m.server.Close()
	}
}

// Requests returns a copy of all recorded requests.
func (m *MockUpstream) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := make([]RecordedRequest, len(m.requests))
	copy(cp, m.requests)

	return cp
}

// LastRequest returns the most recent recorded request, or nil.
func (m *MockUpstream) LastRequest() *RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.requests) == 0 {
		return nil
	}

	r := m.requests[len(m.requests)-1]

	return &r
}

// ClearRequests removes all recorded requests.
func (m *MockUpstream) ClearRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = nil
}

// recordAndReadBody records the request and returns the raw body bytes.
func (m *MockUpstream) recordAndReadBody(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = append(m.requests, RecordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: r.Header.Clone(),
		Body:    string(body),
	})

	return body
}

func (m *MockUpstream) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body := m.recordAndReadBody(r)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req map[string]any
	json.Unmarshal(body, &req) //nolint:errcheck

	model, _ := req["model"].(string)
	if model == "" {
		model = "mock-model"
	}

	stream, _ := req["stream"].(bool)
	if stream {
		m.writeChatStream(w, model)
		return
	}

	m.writeChatJSON(w, model)
}

func (m *MockUpstream) writeChatJSON(w http.ResponseWriter, model string) {
	resp := map[string]any{
		"id":      "chatcmpl-mock-001",
		"object":  "chat.completion",
		"model":   model,
		"created": 1700000000,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hello from mock upstream!",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (m *MockUpstream) writeChatStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	chunks := []map[string]any{
		// chunk 1: role
		{
			"id": "chatcmpl-mock-stream", "object": "chat.completion.chunk",
			"model": model, "created": 1700000000,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"role": "assistant"}},
			},
		},
		// chunk 2: content
		{
			"id": "chatcmpl-mock-stream", "object": "chat.completion.chunk",
			"model": model, "created": 1700000000,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"content": "Hello from mock!"}},
			},
		},
		// chunk 3: finish + usage
		{
			"id": "chatcmpl-mock-stream", "object": "chat.completion.chunk",
			"model": model, "created": 1700000000,
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
			},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14,
			},
		},
	}

	for _, chunk := range chunks {
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (m *MockUpstream) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	m.recordAndReadBody(r)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	resp := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": []float64{0.1, 0.2, 0.3},
			},
		},
		"model": "mock-embedding",
		"usage": map[string]any{
			"prompt_tokens": 5,
			"total_tokens":  5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
