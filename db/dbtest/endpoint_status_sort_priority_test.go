package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestEndpointStatusSortPriority pins the tiers endpoint lists sort by, ahead
// of the creation time: every endpoint that needs attention shares tier 0, so
// the newest of them is first; paused ones follow, deleting/deleted ones last.
func TestEndpointStatusSortPriority(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const ws = "status-sort-ws"

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.endpoints WHERE (metadata).workspace = $1", ws)
	})

	// minute orders creation time: a larger one is a newer endpoint.
	insert := func(name string, phase sql.NullString, minute int) {
		t.Helper()

		if _, err := db.ExecContext(ctx, `
			INSERT INTO api.endpoints (api_version, kind, spec, metadata)
			SELECT
				'v1',
				'Endpoint',
				ROW(
					'sort-cluster',
					ROW('sort-registry', 'sort-model', '', 'v1', '', NULL)::api.model_spec,
					ROW('vllm', 'v0.11.2')::api.endpoint_engine_spec,
					ROW('4', '2', NULL, '16')::api.resource_spec,
					ROW(1)::api.replica_spec,
					NULL, NULL, NULL
				)::api.endpoint_spec,
				ROW($1::text, NULL, $2::text, NULL, created, created, '{}'::json, '{}'::json)::api.metadata
			FROM (SELECT '2026-01-01T00:00:00Z'::timestamptz + make_interval(mins => $3) AS created) t`, name, ws, minute); err != nil {
			t.Fatalf("insert endpoint %s: %v", name, err)
		}

		if phase.Valid {
			if _, err := db.ExecContext(ctx, `
				UPDATE api.endpoints SET status.phase = $3
				WHERE (metadata).workspace = $1 AND (metadata).name = $2`, ws, name, phase.String); err != nil {
				t.Fatalf("set phase of %s: %v", name, err)
			}
		}
	}

	priority := func(name string) int {
		t.Helper()

		var p int
		if err := db.QueryRowContext(ctx, `
			SELECT status_sort_priority FROM api.endpoints
			WHERE (metadata).workspace = $1 AND (metadata).name = $2`, ws, name).Scan(&p); err != nil {
			t.Fatalf("read priority of %s: %v", name, err)
		}

		return p
	}

	cases := []struct {
		name  string
		phase sql.NullString
		want  int
	}{
		{"sort-unreported", sql.NullString{}, 0},
		{"sort-running", sql.NullString{String: "Running", Valid: true}, 0},
		{"sort-failed", sql.NullString{String: "Failed", Valid: true}, 0},
		{"sort-deploying", sql.NullString{String: "Deploying", Valid: true}, 0},
		{"sort-downloading", sql.NullString{String: "ModelDownloading", Valid: true}, 0},
		{"sort-pending", sql.NullString{String: "Pending", Valid: true}, 0},
		{"sort-paused", sql.NullString{String: "Paused", Valid: true}, 1},
		{"sort-deleting", sql.NullString{String: "Deleting", Valid: true}, 2},
		{"sort-deleted", sql.NullString{String: "Deleted", Valid: true}, 2},
	}

	for i, c := range cases {
		insert(c.name, c.phase, i)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := priority(c.name); got != c.want {
				t.Errorf("status_sort_priority = %d, want %d", got, c.want)
			}
		})
	}

	t.Run("list order", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, `
			SELECT (metadata).name FROM api.endpoints
			WHERE (metadata).workspace = $1
			ORDER BY status_sort_priority ASC, (metadata).creation_timestamp DESC`, ws)
		if err != nil {
			t.Fatalf("list endpoints: %v", err)
		}
		defer rows.Close()

		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, name)
		}

		want := []string{
			"sort-pending", "sort-downloading", "sort-deploying", "sort-failed", "sort-running", "sort-unreported",
			"sort-paused",
			"sort-deleted", "sort-deleting",
		}

		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})
}
