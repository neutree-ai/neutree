package dbtest

import (
	"database/sql"
	"strconv"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertServedModelsEndpoint(t *testing.T, db *sql.DB, name, workspace, status string, deleted bool) int {
	t.Helper()

	var id int
	require.NoError(t, db.QueryRow(`
		INSERT INTO api.endpoints (api_version, kind, metadata, spec, status)
		VALUES ('v1', 'Endpoint',
			jsonb_populate_record(NULL::api.metadata, jsonb_build_object(
				'name', $1::text, 'workspace', $2::text,
				'deletion_timestamp', CASE WHEN $4 THEN now() ELSE NULL END)),
			jsonb_populate_record(NULL::api.endpoint_spec, '{
				"cluster":"served-model-cluster",
				"model":{"registry":"served-registry","name":"deployment-model","version":"v1"},
				"engine":{"engine":"vllm","version":"v0.11.2"},
				"resources":{"cpu":"1","memory":"1"},
				"replicas":{"num":1}
			}'::jsonb),
			jsonb_populate_record(NULL::api.endpoint_status, $3::jsonb))
		RETURNING id`, name, workspace, status, deleted).Scan(&id))
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM api.endpoints WHERE id = $1", id)
	})

	return id
}

func TestEndpointServedModelsRoundTrip(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)
	id := insertServedModelsEndpoint(t, db, "served-model-roundtrip", "default", `{"phase":"Running"}`, false)

	for _, models := range [][]string{{"z-first-alias", "a-second-alias"}, {}} {
		require.NoError(t, s.UpdateEndpoint(strconv.Itoa(id), &v1.Endpoint{
			Status: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServedModels: models},
		}))
		got, err := s.GetEndpoint(strconv.Itoa(id))
		require.NoError(t, err)
		require.NotNil(t, got.Status)
		assert.Equal(t, models, got.Status.ServedModels, "PostgREST must preserve model order and explicit empty lists")
	}
}

func TestWorkspaceModelsUsesFirstServedModel(t *testing.T) {
	db := GetTestDB(t)
	const workspace = "served-model-options"

	cases := []struct {
		name      string
		workspace string
		status    string
		deleted   bool
	}{
		{"aliases", workspace, `{"served_models":["z-client-name","a-alias"]}`, false},
		{"not-observed", workspace, `{}`, false},
		{"empty", workspace, `{"served_models":[]}`, false},
		{"other-workspace", "served-model-other", `{"served_models":["other"]}`, false},
		{"deleted", workspace, `{"served_models":["deleted"]}`, true},
	}
	for _, tc := range cases {
		insertServedModelsEndpoint(t, db, tc.name, tc.workspace, tc.status, tc.deleted)
	}

	rows, err := db.Query(`SELECT model, source, endpoint_name, source_label FROM api.get_workspace_models($1)`, workspace)
	require.NoError(t, err)
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var model, source, endpoint, label string
		require.NoError(t, rows.Scan(&model, &source, &endpoint, &label))
		assert.Equal(t, "endpoint", source)
		assert.Equal(t, "self-hosted", label)
		got[endpoint] = model
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{"aliases": "z-client-name"}, got)
}
