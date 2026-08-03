package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type recordedRequest struct {
	method string
	path   string
	query  string
	prefer string
	body   string
}

// aliasTestServer answers every model_aliases request with an empty-ish payload
// and records what it was asked, so the tests below can assert the request the
// storage layer builds rather than a round-trip through a real database.
func aliasTestServer(t *testing.T, payload string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()

	var seen []recordedRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			prefer: r.Header.Get("Prefer"),
			body:   string(body),
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))

	t.Cleanup(server.Close)

	return server, &seen
}

// CreateModelAlias must issue a plain insert. If it ever asked PostgREST to
// resolve duplicates (Prefer: resolution=merge-duplicates) the unique index on
// (model_registry_id, alias_normalized) would stop rejecting a taken alias and
// silently overwrite the existing row instead — which is the opposite of what
// the table exists for.
func TestCreateModelAlias_IsAPlainInsert(t *testing.T) {
	server, seen := aliasTestServer(t, `[]`)
	s := newTestStorage(t, server.URL)

	require.NoError(t, s.CreateModelAlias(&v1.ModelAlias{
		ModelRegistryID: 7,
		ModelName:       "qwen3",
		ModelVersion:    "v1",
		Alias:           "Qwen3",
		AliasNormalized: v1.NormalizeModelAlias("Qwen3"),
	}))

	require.Len(t, *seen, 1)
	got := (*seen)[0]

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/model_aliases", got.path)
	assert.NotContains(t, got.prefer, "resolution=merge-duplicates",
		"a duplicate alias must come back as an error, not silently replace the existing row")

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(got.body, "[")), &sent))
	assert.Equal(t, "Qwen3", sent["alias"])
	assert.Equal(t, "qwen3", sent["alias_normalized"])
	assert.NotContains(t, sent, "workspace",
		"workspace is derived by the database; sending it would imply the client picks it")
}

func TestModelAliasCRUDRequests(t *testing.T) {
	t.Run("list filters by registry", func(t *testing.T) {
		server, seen := aliasTestServer(t, `[{"id":1,"model_registry_id":7,"workspace":"default","alias":"Qwen3"}]`)
		s := newTestStorage(t, server.URL)

		got, err := s.ListModelAlias(ListOption{
			Filters: []Filter{{Column: "model_registry_id", Operator: "eq", Value: "7"}},
		})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "Qwen3", got[0].Alias)
		assert.Equal(t, "default", got[0].Workspace)

		require.Len(t, *seen, 1)
		assert.Equal(t, http.MethodGet, (*seen)[0].method)
		assert.Contains(t, (*seen)[0].query, "model_registry_id=eq.7")
	})

	t.Run("get by id", func(t *testing.T) {
		server, seen := aliasTestServer(t, `[{"id":3,"alias":"Qwen3"}]`)
		s := newTestStorage(t, server.URL)

		got, err := s.GetModelAlias("3")
		require.NoError(t, err)
		assert.Equal(t, 3, got.ID)
		assert.Contains(t, (*seen)[0].query, "id=eq.3")
	})

	t.Run("get missing returns ErrResourceNotFound", func(t *testing.T) {
		server, _ := aliasTestServer(t, `[]`)
		s := newTestStorage(t, server.URL)

		_, err := s.GetModelAlias("404")
		assert.ErrorIs(t, err, ErrResourceNotFound)
	})

	t.Run("update patches one row", func(t *testing.T) {
		server, seen := aliasTestServer(t, `[]`)
		s := newTestStorage(t, server.URL)

		require.NoError(t, s.UpdateModelAlias("3", &v1.ModelAlias{ModelName: "qwen3", ModelVersion: "v2"}))

		require.Len(t, *seen, 1)
		assert.Equal(t, http.MethodPatch, (*seen)[0].method)
		assert.Contains(t, (*seen)[0].query, "id=eq.3")
		assert.Contains(t, (*seen)[0].body, `"model_version":"v2"`)
	})

	t.Run("delete removes one row", func(t *testing.T) {
		server, seen := aliasTestServer(t, `[]`)
		s := newTestStorage(t, server.URL)

		require.NoError(t, s.DeleteModelAlias("3"))

		require.Len(t, *seen, 1)
		assert.Equal(t, http.MethodDelete, (*seen)[0].method)
		assert.Contains(t, (*seen)[0].query, "id=eq.3")
	})
}
