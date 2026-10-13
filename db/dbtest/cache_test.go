package dbtest

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClusterCacheEnvelope(t *testing.T) {
	db := GetTestDB(t)
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(t.Context(), `CREATE TEMP TABLE cache_admission (spec api.cluster_spec, status api.cluster_status);
 CREATE TRIGGER validate_cache BEFORE INSERT OR UPDATE OF spec ON cache_admission FOR EACH ROW EXECUTE FUNCTION api.validate_cluster_cache();`)
	require.NoError(t, err)
	for _, config := range []string{`{}`, `{"provider":""}`, `{"provider":"example","options":[]}`, `{"provider":"example","options":{},"extra":1}`} {
		_, err = conn.ExecContext(t.Context(), `INSERT INTO cache_admission (spec) SELECT json_populate_record(NULL::api.cluster_spec, json_build_object('cache',$1::json))`, config)
		require.Error(t, err)
	}
	_, err = conn.ExecContext(t.Context(), `INSERT INTO cache_admission SELECT json_populate_record(NULL::api.cluster_spec,'{"cache":{"provider":"example","options":{"capacity":8}}}'::json), json_populate_record(NULL::api.cluster_status,'{"cache":{"provider":"example","details":{"operation":"op-1"}}}'::json)`)
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), `UPDATE cache_admission SET spec=json_populate_record(spec,'{"cache":{"provider":"other","options":{}}}'::json)`)
	require.ErrorContains(t, err, "cleanup")
	_, err = conn.ExecContext(t.Context(), `UPDATE cache_admission SET spec=json_populate_record(spec,'{"cache":null}'::json)`)
	require.NoError(t, err)
	var owner, operation string
	require.NoError(t, conn.QueryRowContext(t.Context(), `SELECT (status).cache->>'provider', (status).cache#>>'{details,operation}' FROM cache_admission`).Scan(&owner, &operation))
	require.Equal(t, "example", owner)
	require.Equal(t, "op-1", operation)
}
