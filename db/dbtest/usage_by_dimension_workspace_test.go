package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestGetUsageByDimensionWorkspaceFilter is a regression test for NEU-463.
//
// get_usage_by_dimension used to derive each usage row's workspace by joining
// the dimension's endpoint_name against api.endpoints / api.external_endpoints.
// endpoint names are unique only within a workspace (unique index is
// workspace+name), so the name-only join (1) bled usage from same-named
// endpoints across workspaces and (2) dropped historical usage once an endpoint
// was deleted. Migration 063 sources the workspace from the daily-usage row's
// metadata.workspace instead, removing the endpoint joins.
//
// The scenario below reproduces the exact repro from the ticket: one user owns
// API keys in two workspaces, both keys report usage under the SAME dimension
// name, and a same-named endpoint exists in both workspaces.
func TestGetUsageByDimensionWorkspaceFilter(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const (
		wsA          = "neu-463-ws-a"
		wsB          = "neu-463-ws-b"
		dimension    = "neu-463-gpt" // same dimension/endpoint name used in both workspaces
		usageA int64 = 100
		usageB int64 = 200
	)

	// One user owns API keys in two different workspaces.
	user := CreateTestUser(t, "neu463user", "neu463@example.com", "testpassword")

	createKey := func(workspace, name string) string {
		var id string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT id FROM api.create_api_key(
					p_workspace := $1,
					p_name := $2,
					p_quota := 1000
				)`, workspace, name).Scan(&id)
		})
		if err != nil {
			t.Fatalf("failed to create API key %s/%s: %v", workspace, name, err)
		}
		return id
	}

	keyA := createKey(wsA, "neu-463-key-a")
	keyB := createKey(wsB, "neu-463-key-b")

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id IN ($1, $2)", keyA, keyB)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.endpoints WHERE (metadata).name = $1", dimension)
	})

	// Daily usage: both keys report usage under the SAME dimension name.
	// metadata.workspace mirrors the owning key's workspace, as written by
	// aggregate_usage_records at aggregation time.
	insertDaily := func(keyID, workspace string, amount int64) {
		_, err := db.ExecContext(ctx, `
			INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
			VALUES (
				'v1',
				'ApiDailyUsage',
				ROW('neu-463-du-' || $2, NULL, $2, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, CURRENT_DATE, $3, jsonb_build_object($4::text, $3), NULL::jsonb)::api.api_daily_usage_spec,
				ROW(now())::api.api_daily_usage_status
			)`, keyID, workspace, amount, dimension)
		if err != nil {
			t.Fatalf("failed to insert daily usage for %s: %v", workspace, err)
		}
	}
	insertDaily(keyA, wsA, usageA)
	insertDaily(keyB, wsB, usageB)

	// Same-named endpoint in BOTH workspaces (the condition that broke the
	// name-only join). The endpoint rows are intentionally irrelevant to the
	// fixed query — it no longer joins them.
	insertEndpoint := func(workspace string) {
		_, err := db.ExecContext(ctx, `
			INSERT INTO api.endpoints (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Endpoint',
				ROW(
					'neu-463-cluster',
					ROW('neu-463-registry', 'neu-463-model', '', 'v1', '')::api.model_spec,
					ROW('vllm', 'v0.11.2')::api.endpoint_engine_spec,
					ROW('4', '2', NULL, '16')::api.resource_spec,
					ROW(1)::api.replica_spec,
					NULL,
					NULL,
					NULL
				)::api.endpoint_spec,
				ROW($1, NULL, $2, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata
			)`, dimension, workspace)
		if err != nil {
			t.Fatalf("failed to insert endpoint in %s: %v", workspace, err)
		}
	}
	insertEndpoint(wsA)
	insertEndpoint(wsB)

	type usageRow struct {
		workspace string
		endpoint  string
		usage     int64
	}

	// queryDim runs get_usage_by_dimension within the user's auth context
	// (the RPC scopes to auth.uid()). A nil pWorkspace passes SQL NULL.
	queryDim := func(pWorkspace *string) []usageRow {
		var rows []usageRow
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID)}, func(tx *sql.Tx) error {
			r, err := tx.QueryContext(ctx, `
				SELECT workspace, endpoint_name, usage
				FROM api.get_usage_by_dimension(CURRENT_DATE - 1, CURRENT_DATE + 1, NULL, NULL, $1)
				ORDER BY workspace, usage`, pWorkspace)
			if err != nil {
				return err
			}
			defer r.Close()
			for r.Next() {
				var x usageRow
				if err := r.Scan(&x.workspace, &x.endpoint, &x.usage); err != nil {
					return err
				}
				rows = append(rows, x)
			}
			return r.Err()
		})
		if err != nil {
			t.Fatalf("failed to query get_usage_by_dimension: %v", err)
		}
		return rows
	}
	ws := func(s string) *string { return &s }

	t.Run("workspace filter returns only that workspace's usage", func(t *testing.T) {
		got := queryDim(ws(wsA))
		if len(got) != 1 || got[0].workspace != wsA || got[0].usage != usageA {
			t.Fatalf("expected exactly [%s/%d], got %+v", wsA, usageA, got)
		}

		got = queryDim(ws(wsB))
		if len(got) != 1 || got[0].workspace != wsB || got[0].usage != usageB {
			t.Fatalf("expected exactly [%s/%d], got %+v", wsB, usageB, got)
		}
	})

	t.Run("no cross-workspace bleed for same-named endpoints", func(t *testing.T) {
		got := queryDim(nil)
		// Exactly two rows. The pre-fix name-only join matched the same-named
		// endpoint in both workspaces and produced four rows (each usage row
		// duplicated into the other workspace).
		if len(got) != 2 ||
			got[0].workspace != wsA || got[0].usage != usageA ||
			got[1].workspace != wsB || got[1].usage != usageB {
			t.Fatalf("expected [%s/%d, %s/%d], got %+v", wsA, usageA, wsB, usageB, got)
		}
	})

	t.Run("history retained after endpoint deletion", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "DELETE FROM api.endpoints WHERE (metadata).name = $1", dimension); err != nil {
			t.Fatalf("failed to delete endpoints: %v", err)
		}
		got := queryDim(ws(wsA))
		// Pre-fix the workspace could not be resolved once the endpoint was
		// gone, so the workspace filter dropped the row entirely (0 rows).
		if len(got) != 1 || got[0].workspace != wsA || got[0].usage != usageA {
			t.Fatalf("expected [%s/%d] to survive endpoint deletion, got %+v", wsA, usageA, got)
		}
	})
}
