package storage

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
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
