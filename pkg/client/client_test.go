package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClientAcceptsAPIV1BaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clusters" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"metadata":{"name":"test","workspace":"default"}}]`))
	}))
	defer server.Close()

	c := NewClient(server.URL + "/api/v1")
	if c.Generic == nil {
		t.Fatal("generic service is nil")
	}

	items, err := c.Generic.List("Cluster", "default")
	if err != nil {
		t.Fatalf("list clusters: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(items))
	}
}
