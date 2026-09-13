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

func TestClientLatestOperationLoadsNodeDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/zcache/operations":
			_ = json.NewEncoder(w).Encode(OperationsResponse{Operations: []OperationResponse{{ID: "op-1"}}})
		case "/api/v1/zcache/operations/op-1":
			_ = json.NewEncoder(w).Encode(OperationResponse{
				ID: "op-1", Phase: "Failed", Operation: OperationMetadata{Kind: "update_nodes"},
				Nodes: []OperationNode{{NodeName: "gpu-1", Phase: "Succeeded"}, {NodeName: "gpu-2", Phase: "Failed", Reason: "OOM"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	operation, err := NewClient(server.URL, server.Client()).LatestOperation(context.Background())
	if err != nil {
		t.Fatalf("latest operation: %v", err)
	}
	if operation.ID != "op-1" || len(operation.Nodes) != 2 || operation.Nodes[1].Reason != "OOM" {
		t.Fatalf("unexpected operation: %+v", operation)
	}
}
