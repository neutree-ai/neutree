package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestGetUsageByDimensionPermissionScope verifies that api.get_usage_by_dimension
// returns own keys PLUS every key in any workspace where the caller holds
// workspace:usage-read. In the community edition has_permission ignores the
// workspace argument and degrades to a global check, so the permission is
// exercised here via a global role assignment.
func TestGetUsageByDimensionPermissionScope(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const workspace = "usage-scope-ws"

	// owner: creates an API key in the workspace and accrues usage on it.
	owner := CreateTestUser(t, "usage-owner", "usage-owner@example.com", "password123")

	var ownerKeyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(owner.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := $1,
				p_name := 'owner-key',
				p_quota := 1000
			)
		`, workspace).Scan(&ownerKeyID)
	})
	if err != nil {
		t.Fatalf("failed to create owner API key: %v", err)
	}

	// Seed one daily-usage row for the owner's key (old-data path: only
	// dimensional_usage populated), so the RPC has a row to surface.
	_, err = db.ExecContext(ctx, `
		INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
		VALUES (
			'v1', 'ApiDailyUsage',
			ROW('usage-scope-daily', NULL, $2, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, CURRENT_DATE, 100, '{"endpoint-x": 100}'::jsonb, NULL::jsonb)::api.api_daily_usage_spec,
			ROW(now())::api.api_daily_usage_status
		)
	`, ownerKeyID, workspace)
	if err != nil {
		t.Fatalf("failed to insert daily usage: %v", err)
	}

	// countOwnerRows returns how many get_usage_by_dimension rows for the
	// owner's key are visible to the given caller.
	countOwnerRows := func(t *testing.T, userID string) int {
		t.Helper()
		var n int
		qErr := execWithContext(t, db, []SetContextFunc{setUserContext(userID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT count(*) FROM api.get_usage_by_dimension(
					CURRENT_DATE - 1, CURRENT_DATE + 1, NULL, NULL, NULL
				) WHERE api_key_id = $1
			`, ownerKeyID).Scan(&n)
		})
		if qErr != nil {
			t.Fatalf("failed to call get_usage_by_dimension: %v", qErr)
		}
		return n
	}

	t.Run("user without usage-read cannot see other users' key usage", func(t *testing.T) {
		var outsiderID string
		if cErr := execWithContext(t, db, nil, func(tx *sql.Tx) error {
			outsiderID = createUserWithPermissions(t, tx, "usage-outsider", "usage-outsider@example.com", []string{"workspace:read"})
			return nil
		}); cErr != nil {
			t.Fatalf("failed to create outsider user: %v", cErr)
		}

		if n := countOwnerRows(t, outsiderID); n != 0 {
			t.Errorf("expected 0 rows for user without workspace:usage-read, got %d", n)
		}
	})

	t.Run("user with usage-read sees other users' key usage", func(t *testing.T) {
		var readerID string
		if cErr := execWithContext(t, db, nil, func(tx *sql.Tx) error {
			readerID = createUserWithPermissions(t, tx, "usage-reader", "usage-reader@example.com", []string{"workspace:usage-read"})
			return nil
		}); cErr != nil {
			t.Fatalf("failed to create reader user: %v", cErr)
		}

		if n := countOwnerRows(t, readerID); n < 1 {
			t.Errorf("expected >=1 row for user with workspace:usage-read, got %d", n)
		}
	})
}
