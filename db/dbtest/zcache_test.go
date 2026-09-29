package dbtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZCacheAdmissionAndStatusRoundTrip(t *testing.T) {
	waitForPostgREST(t)
	// Use the same PostgREST resource endpoint as the UI and CLI. Tests never
	// connect to the live control plane; POSTGREST_URL selects the isolated stack.
	base := map[string]interface{}{
		"api_version": "v1", "kind": "Cluster", "metadata": map[string]interface{}{"name": "zcache-contract", "workspace": "default"},
		"spec": map[string]interface{}{"type": "kubernetes", "image_registry": "test-registry", "version": "v1.2.0", "config": map[string]interface{}{"kubernetes_config": map[string]interface{}{"kubeconfig": "dGVzdA==", "router": map[string]interface{}{"access_mode": "NodePort", "replicas": 1, "resources": map[string]interface{}{"cpu": "1", "memory": "1Gi"}}}}},
	}
	// Registry FK validation applies at the same boundary as normal cluster edits.
	code, body := postgrestRequest(t, http.MethodPost, "/image_registries", `{"api_version":"v1","kind":"ImageRegistry","metadata":{"name":"test-registry","workspace":"default"},"spec":{"url":"https://registry.example","repository":"neutree"}}`)
	require.Contains(t, []int{201, 409}, code, body)
	t.Cleanup(func() { postgrestRequest(t, http.MethodDelete, "/clusters?metadata->>name=eq.zcache-contract", "") })
	spec := base["spec"].(map[string]interface{})
	for _, test := range []struct {
		name   string
		config string
	}{
		{"disabled malformed capacity", `{"enabled":false,"l1_size_gib":"broken"}`},
		{"invalid node name", `{"enabled":true,"l1_size_gib":1,"target_nodes":["worker..a"]}`},
		{"string capacity", `{"enabled":true,"l1_size_gib":"8","target_nodes":["worker-a"]}`},
		{"fractional", `{"enabled":true,"l1_size_gib":1.5,"target_nodes":["worker-a"]}`},
		{"negative", `{"enabled":true,"l1_size_gib":-1,"target_nodes":["worker-a"]}`},
		{"empty selection", `{"enabled":true,"l1_size_gib":1,"target_nodes":[]}`},
		{"duplicates", `{"enabled":true,"l1_size_gib":1,"target_nodes":["worker-a","worker-a"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec["zcache"] = json.RawMessage(test.config)
			data, err := json.Marshal(base)
			require.NoError(t, err)
			code, body := postgrestRequest(t, http.MethodPost, "/clusters", string(data))
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, "zcache")
		})
	}
	spec["zcache"] = json.RawMessage(`{"enabled":true,"l1_size_gib":8,"target_nodes":["worker-a","worker-b"]}`)
	data, err := json.Marshal(base)
	require.NoError(t, err)
	code, body = postgrestRequest(t, http.MethodPost, "/clusters", string(data))
	require.Equal(t, http.StatusCreated, code, body)
	// Patch desired configuration, then persist controller status. Whole composite
	// writes must retain zcache rather than silently dropping the new attribute.
	spec["zcache"] = json.RawMessage(`{"enabled":true,"l1_size_gib":1,"target_nodes":["worker-a"]}`)
	data, err = json.Marshal(map[string]interface{}{"spec": spec})
	require.NoError(t, err)
	code, body = postgrestRequest(t, http.MethodPatch, "/clusters?metadata->>name=eq.zcache-contract", string(data))
	require.Equal(t, http.StatusOK, code, body)
	code, body = postgrestRequest(t, http.MethodPatch, "/clusters?metadata->>name=eq.zcache-contract", `{"status":{"phase":"Running","zcache":{"phase":"Reconciling","nodes":[{"name":"worker-a","runtime":"Ready"}],"change":{"operation_id":"op-test","phase":"Running"}}}}`)
	require.Equal(t, http.StatusOK, code, body)
	code, body = postgrestRequest(t, http.MethodGet, "/clusters?metadata->>name=eq.zcache-contract", "")
	require.Equal(t, http.StatusOK, code, body)
	var rows []struct {
		Spec struct {
			ZCache struct {
				L1    int      `json:"l1_size_gib"`
				Nodes []string `json:"target_nodes"`
			} `json:"zcache"`
		} `json:"spec"`
		Status struct {
			ZCache json.RawMessage `json:"zcache"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Spec.ZCache.L1)
	require.Equal(t, []string{"worker-a"}, rows[0].Spec.ZCache.Nodes)
	require.Contains(t, string(rows[0].Status.ZCache), "op-test", fmt.Sprint(rows))
}
