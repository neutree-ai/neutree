package zcache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientValidateAndRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/zcache/validate" {
			var request ValidateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.LMCache.Mode != "l1" || request.LMCache.L1SizeGB != 8 {
				t.Fatalf("unexpected request: %+v", request)
			}
			_ = json.NewEncoder(w).Encode(ValidateResponse{Valid: true})
			return
		}
		if r.URL.Path == "/api/v1/zcache/runtime" {
			_ = json.NewEncoder(w).Encode(RuntimeResponse{Ready: true, Nodes: []string{"gpu-1"}, Endpoint: &RuntimeEndpoint{Address: "zcache-1", Port: 7500}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	c := NewClient(server.URL, server.Client())
	validation, err := c.Validate(context.Background(), ValidateRequest{LMCache: RuntimeRequest{Mode: "l1", L1SizeGB: 8}})
	if err != nil || !validation.Valid {
		t.Fatalf("validate: %+v %v", validation, err)
	}
	runtime, err := c.Runtime(context.Background())
	if err != nil || !runtime.Ready || runtime.Endpoint.Address != "zcache-1" {
		t.Fatalf("runtime: %+v %v", runtime, err)
	}
}
