package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// NEU-447 regression tests.
//
// A single failed PostgREST RPC (sync_api_key_usage, fired by cron) must not
// poison the shared postgrest client so that every later, unrelated storage
// call keeps failing with the stale error until the process restarts.
//
// Before the fix this happened two ways, both exercised below:
//   - postgrest-go v0.0.11's executeHelper short-circuited on the sticky
//     Client.ClientError, so any .From(table).Select().Execute() replayed the
//     last RPC failure without issuing a request. Fixed by the v0.0.12 bump
//     (executeHelper now reads the per-call error).
//   - CallDatabaseFunction read s.postgrestClient.ClientError after Rpc(), so a
//     later successful RPC was still reported as failed. Fixed by switching to
//     RpcWithError (idiomatic error return, no sticky field).
//
// Both tests drive the real storage/client stack (New -> postgrest.NewClient)
// against an httptest server, so they fail on the pre-fix code and pass now.

func newTestStorage(t *testing.T, accessURL string) Storage {
	t.Helper()
	s, err := New(Options{
		AccessURL: accessURL,
		Scheme:    "public",
		JwtSecret: "test-secret",
	})
	require.NoError(t, err)
	return s
}

// dropConn aborts the request at the transport layer (no HTTP response), which
// is what the ticket's repro produces (dial/i-o timeout, disconnect) and what
// actually sets postgrest-go's sticky Client.ClientError — a 5xx would not,
// since RpcWithError doesn't inspect the status code.
func dropConn(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	require.True(t, ok, "test server must support hijacking")
	conn, _, err := hj.Hijack()
	require.NoError(t, err)
	_ = conn.Close()
}

// A failed RPC must not make the next (healthy) RPC report a stale failure.
func TestCallDatabaseFunction_FailureDoesNotPoisonSubsequentRPC(t *testing.T) {
	var rpcCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rpc/sync_api_key_usage" {
			// Fail only the first call; the server is healthy afterwards.
			if atomic.AddInt32(&rpcCalls, 1) == 1 {
				dropConn(t, w)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	s := newTestStorage(t, server.URL)

	// First call fails at the transport layer.
	require.Error(t, s.CallDatabaseFunction("sync_api_key_usage", map[string]interface{}{}, nil))

	// Second call hits the now-healthy server and must succeed — not replay the
	// first call's error from a sticky field.
	require.NoError(t, s.CallDatabaseFunction("sync_api_key_usage", map[string]interface{}{}, nil))
	require.Equal(t, int32(2), atomic.LoadInt32(&rpcCalls),
		"the second RPC must actually reach the server, not short-circuit on a stale error")
}

// A failed RPC must not poison unrelated List/Get/Update (Execute) queries.
func TestCallDatabaseFunction_FailureDoesNotPoisonListQueries(t *testing.T) {
	var listCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rpc/sync_api_key_usage":
			dropConn(t, w)
		case r.URL.Path == "/endpoints" && r.Method == http.MethodGet:
			atomic.AddInt32(&listCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := newTestStorage(t, server.URL)

	require.Error(t, s.CallDatabaseFunction("sync_api_key_usage", map[string]interface{}{}, nil))

	// A List on an unrelated table must still issue its own HTTP request and
	// succeed; the earlier RPC failure must not be replayed here (which, pre-fix,
	// also surfaced the wrong /rpc/sync_api_key_usage URL in the error).
	endpoints, err := s.ListEndpoint(ListOption{})
	require.NoError(t, err)
	require.Empty(t, endpoints)
	require.Equal(t, int32(1), atomic.LoadInt32(&listCalls),
		"the List must reach the server, not short-circuit on a stale error")
}

func TestReleaseInfoStorageUsesInternalTable(t *testing.T) {
	type capturedRequest struct {
		request *http.Request
		body    []byte
	}

	requests := make(chan capturedRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		requests <- capturedRequest{request: r.Clone(r.Context()), body: body}
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":7,"api_version":"v1","kind":"ReleaseInfo","metadata":{"name":"v1.2.0"},"spec":{"compatible_cluster_baselines":["v1.1","v1.2"]}}]`))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPatch:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := newTestStorage(t, server.URL)
	infos, err := s.ListReleaseInfo()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "v1.2.0", infos[0].GetName())
	assert.Equal(t, []string{"v1.1", "v1.2"}, infos[0].Spec.CompatibleClusterBaselines)

	info := &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: "v1.2.0"},
		Spec:       &v1.ReleaseInfoSpec{CompatibleClusterBaselines: []string{"v1.1", "v1.2"}},
	}
	require.NoError(t, s.CreateReleaseInfo(info))
	require.NoError(t, s.UpdateReleaseInfo("7", info))

	first := <-requests
	assert.Equal(t, http.MethodGet, first.request.Method)
	assert.Equal(t, "/release_infos", first.request.URL.Path)

	second := <-requests
	assert.Equal(t, http.MethodPost, second.request.Method)
	assert.Equal(t, "/release_infos", second.request.URL.Path)
	assertReleaseInfoPayloadIsMinimal(t, second.body)

	third := <-requests
	assert.Equal(t, http.MethodPatch, third.request.Method)
	assert.Equal(t, "/release_infos", third.request.URL.Path)
	assert.Equal(t, "eq.7", third.request.URL.Query().Get("id"))
	assertReleaseInfoPayloadIsMinimal(t, third.body)
}

func assertReleaseInfoPayloadIsMinimal(t *testing.T, body []byte) {
	t.Helper()

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.NotContains(t, payload, "status")

	spec, ok := payload["spec"].(map[string]interface{})
	require.True(t, ok, "release info payload must contain an object spec")
	assert.Equal(t, []interface{}{"v1.1", "v1.2"}, spec["compatible_cluster_baselines"])
	assert.NotContains(t, spec, "channel")
	assert.NotContains(t, spec, "build_identity")
	assert.NotContains(t, spec, "cluster_versions")
}

func TestClusterProfileStorageUsesInternalTable(t *testing.T) {
	requests := make(chan *http.Request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":7,"api_version":"v1","kind":"ClusterProfile","metadata":{"name":"v1.2.0-rc.1"},"spec":{"components":{"ray_runtime":{"image":"neutree/neutree-serve","tag":"v1.2.0-rc.1"}}}}]`))
		case http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPatch:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := newTestStorage(t, server.URL)
	profiles, err := s.ListClusterProfile(ListOption{Filters: []Filter{{Column: "id", Operator: "eq", Value: "7"}}})
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, "v1.2.0-rc.1", profiles[0].GetName())
	assert.Equal(t, "neutree/neutree-serve", profiles[0].Spec.Components.RayRuntime.Image)

	profile := &v1.ClusterProfile{Metadata: &v1.Metadata{Name: "v1.2.0-rc.1"}}
	require.NoError(t, s.CreateClusterProfile(profile))
	require.NoError(t, s.UpdateClusterProfile("7", profile))

	first := <-requests
	assert.Equal(t, http.MethodGet, first.Method)
	assert.Equal(t, "/cluster_profiles", first.URL.Path)
	assert.Equal(t, "eq.7", first.URL.Query().Get("id"))

	second := <-requests
	assert.Equal(t, http.MethodPost, second.Method)
	assert.Equal(t, "/cluster_profiles", second.URL.Path)

	third := <-requests
	assert.Equal(t, http.MethodPatch, third.Method)
	assert.Equal(t, "/cluster_profiles", third.URL.Path)
	assert.Equal(t, "eq.7", third.URL.Query().Get("id"))
}
