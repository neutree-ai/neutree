package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_ListActors_BuildsFilterQueryParams(t *testing.T) {
	var capturedQuery map[string][]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v0/actors", r.URL.Path)
		capturedQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":true,"msg":"","data":{"result":{"total":0,"num_after_truncation":0,"num_filtered":0,"result":[]}}}`))
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	resp, err := c.ListActors([]ActorFilter{
		{Key: "class_name", Predicate: "=", Value: "ServeReplica:default_test:deploy"},
		{Key: "state", Predicate: "=", Value: "DEAD"},
	}, true, 100)

	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, []string{"class_name", "state"}, capturedQuery["filter_keys"])
	assert.Equal(t, []string{"=", "="}, capturedQuery["filter_predicates"])
	assert.Equal(t, []string{"ServeReplica:default_test:deploy", "DEAD"}, capturedQuery["filter_values"])
	assert.Equal(t, []string{"true"}, capturedQuery["detail"])
	assert.Equal(t, []string{"100"}, capturedQuery["limit"])
}

func TestClient_ListActors_ParsesResponse(t *testing.T) {
	body := `{
		"result": true,
		"msg": "",
		"data": {
			"result": {
				"total": 2,
				"num_after_truncation": 2,
				"num_filtered": 1,
				"result": [
					{
						"actor_id": "abc123",
						"class_name": "ServeReplica:default_test:deploy",
						"state": "DEAD",
						"name": "SERVE_REPLICA::default_test#deploy#xyz789",
						"node_id": "node-1",
						"pid": 1234,
						"death_cause": {"actor_died_error_context": {"error_message": "init failed"}}
					}
				]
			}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	resp, err := c.ListActors(nil, true, 100)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Result)
	assert.Equal(t, 2, resp.Data.Result.Total)
	assert.Equal(t, 1, resp.Data.Result.NumFiltered)
	require.Len(t, resp.Data.Result.Result, 1)

	a := resp.Data.Result.Result[0]
	assert.Equal(t, "abc123", a.ActorID)
	assert.Equal(t, "ServeReplica:default_test:deploy", a.ClassName)
	assert.Equal(t, "DEAD", a.State)
	assert.Equal(t, "SERVE_REPLICA::default_test#deploy#xyz789", a.Name)
	assert.Equal(t, "node-1", a.NodeID)
	assert.Equal(t, 1234, a.PID)
	require.NotNil(t, a.DeathCause)
}

func TestClient_ListActors_OmitsDetailWhenFalse(t *testing.T) {
	var capturedQuery map[string][]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"result":{"result":[]}}}`))
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	_, err := c.ListActors(nil, false, 0)

	require.NoError(t, err)
	_, hasDetail := capturedQuery["detail"]
	assert.False(t, hasDetail, "detail should not appear in query when false")
	_, hasLimit := capturedQuery["limit"]
	assert.False(t, hasLimit, "limit should not appear in query when 0")
}

func TestClient_ListActors_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server boom`))
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	resp, err := c.ListActors(nil, false, 0)

	assert.Error(t, err)
	assert.Nil(t, resp)
}
