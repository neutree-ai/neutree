package dbtest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEndpointCacheDependencyGeneration(t *testing.T) {
	db := GetTestDB(t)
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck
	insert := func(table, name string, spec string) int {
		t.Helper()
		payload := fmt.Sprintf(`{"api_version":"v1","kind":"%s","metadata":{"name":"%s","workspace":"default"},"spec":%s}`, map[string]string{"image_registries": "ImageRegistry", "model_registries": "ModelRegistry", "clusters": "Cluster", "endpoints": "Endpoint"}[table], name, spec)
		var id int
		err := tx.QueryRow(`INSERT INTO api.`+table+` (api_version,kind,metadata,spec) SELECT api_version,kind,metadata,spec FROM json_populate_record(NULL::api.`+table+`,$1) RETURNING id`, payload).Scan(&id)
		require.NoError(t, err)
		return id
	}
	insert("image_registries", "cache-test-images", `{"url":"https://registry.example","repository":"neutree"}`)
	insert("model_registries", "cache-test-models", `{"type":"hugging-face","url":"https://huggingface.co"}`)
	clusterID := insert("clusters", "cache-test-cluster", `{"type":"kubernetes","image_registry":"cache-test-images","version":"v1.2.0","config":{"kubernetes_config":{"kubeconfig":"dGVzdA==","router":{"access_mode":"NodePort","replicas":1,"resources":{"cpu":"1","memory":"1Gi"}}}},"zcache":{"enabled":true,"l1_size_gib":1,"target_nodes":["worker-a","worker-b"]}}`)
	endpointID := insert("endpoints", "cache-test-endpoint", `{"cluster":"cache-test-cluster","model":{"registry":"cache-test-models","name":"qwen","version":"main","task":"text-generation"},"engine":{"engine":"vllm","version":"v0.24.0"},"resources":{"cpu":"1","memory":"1Gi"},"replicas":{"num":0},"zcache":{"enabled":true,"timeout_seconds":2}}`)
	checkBlocked := func() {
		t.Helper()
		_, err = tx.Exec(`SAVEPOINT guard_test`)
		require.NoError(t, err)
		_, err = tx.Exec(`UPDATE api.clusters SET spec.zcache='{"enabled":false,"l1_size_gib":1,"target_nodes":["worker-a","worker-b"]}' WHERE id=$1`, clusterID)
		require.ErrorContains(t, err, "still use ZCache")
		_, err = tx.Exec(`ROLLBACK TO SAVEPOINT guard_test`)
		require.NoError(t, err)
	}
	checkBlocked() // Paused desired instances still depend on cache.
	_, err = tx.Exec(`UPDATE api.endpoints SET spec.zcache='{"enabled":false}' WHERE id=$1`, endpointID)
	require.NoError(t, err)
	checkBlocked() // Saving off does not release running old Pods.
	_, err = tx.Exec(`UPDATE api.endpoints SET status.zcache='{"generation":1,"in_use":false}' WHERE id=$1`, endpointID)
	require.NoError(t, err)
	checkBlocked() // An observation from before this edit cannot release it.
	var observed []byte
	require.NoError(t, tx.QueryRow(`SELECT (status).zcache FROM api.endpoints WHERE id=$1`, endpointID).Scan(&observed))
	var state map[string]interface{}
	require.NoError(t, json.Unmarshal(observed, &state))
	require.Equal(t, float64(2), state["generation"])
	_, err = tx.Exec(`UPDATE api.endpoints SET status.zcache='{"generation":2,"in_use":false}' WHERE id=$1`, endpointID)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE api.clusters SET spec.zcache='{"enabled":false,"l1_size_gib":1,"target_nodes":["worker-a","worker-b"]}' WHERE id=$1`, clusterID)
	require.NoError(t, err)
}
