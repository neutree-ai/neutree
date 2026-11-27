package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/proxy"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func TestShutdownWaitsForStreamAndVector(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ":live\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	posted := make(chan struct{})
	vector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(posted) }))
	defer vector.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := proxy.ParseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &state.Store{}
	if err = store.Apply(event.Event{Kind: state.ConfigReplace, Value: state.ConfigSnapshot{Kong: upstream.URL}}); err != nil {
		t.Fatal(err)
	}
	s := proxy.NewServer(opts, store, state.NewRuntime(1))
	s.Sink = proxy.NewVectorSink(vector.URL, 4)
	ctx, signal := context.WithCancel(context.Background())
	defer signal()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, s) }()
	resp, err := http.Post("http://"+listener.Addr().String()+"/workspace/ws/endpoint/ep/v1/chat/completions", "application/json", strings.NewReader(`{"model":"v","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	signal()
	deadline := time.Now().Add(time.Second)
	for !s.Draining.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	health, err := http.Get("http://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	accepted := httptest.NewRecorder()
	s.ServeHTTP(accepted, httptest.NewRequest("GET", "/v1/models", nil))
	close(release)
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("accepted request during drain: %d", accepted.Code)
	}
	tail, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(tail), "[DONE]") || health.StatusCode != 503 {
		t.Fatal(string(tail), err, health.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked")
	}
	select {
	case <-posted:
	default:
		t.Fatal("exited before Vector delivery")
	}
}
