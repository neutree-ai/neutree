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
				p_quota := 1000,
				p_display_name := 'Owner display',
				p_description := 'Owner description'
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

	t.Run("owner can read own quota summary without usage-read", func(t *testing.T) {
		var count int
		err := execWithContext(t, db, []SetContextFunc{setUserContext(owner.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT count(*) FROM api.get_api_keys_usage_summary($1)
				WHERE api_key_id = $2
			`, workspace, ownerKeyID).Scan(&count)
		})
		if err != nil {
			t.Fatalf("owner quota summary: %v", err)
		}
		if count != 1 {
			t.Fatalf("owner quota summary rows=%d, want 1", count)
		}
	})

	t.Run("usage rows include API key display identity", func(t *testing.T) {
		var displayName, description string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(owner.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT api_key_display_name, api_key_description
				FROM api.get_usage_by_dimension(
					CURRENT_DATE - 1, CURRENT_DATE + 1, $1, NULL, $2
				) LIMIT 1
			`, ownerKeyID, workspace).Scan(&displayName, &description)
		})
		if err != nil {
			t.Fatalf("usage display identity: %v", err)
		}
		if displayName != "Owner display" || description != "Owner description" {
			t.Fatalf("usage identity=(%q,%q), want display name and description", displayName, description)
		}
	})

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

// dimFilter narrows get_usage_by_dimension; an empty field leaves its
// parameter NULL.
type dimFilter struct {
	workspace, team, orgUnit, user, sa string
	direct                             bool
}

func nullable(s string) any {
	if s == "" {
		return nil
	}

	return s
}

// dimensionUsage sums get_usage_by_dimension per key as userID, after prelude.
func dimensionUsage(t *testing.T, db *sql.DB, userID string, prelude func(tx *sql.Tx), f dimFilter) map[string]int64 {
	t.Helper()

	out := map[string]int64{}
	if err := asUser(t, db, userID, prelude, func(tx *sql.Tx) error {
		rows, err := tx.Query(`
			SELECT api_key_id::text, usage FROM api.get_usage_by_dimension(
			    p_start_date => '1970-01-01', p_end_date => '2999-12-31',
			    p_workspace => $1, p_team => $2, p_org_unit => $3,
			    p_user_id => $4, p_service_account_id => $5, p_direct => $6)`,
			nullable(f.workspace), nullable(f.team), nullable(f.orgUnit),
			nullable(f.user), nullable(f.sa), f.direct)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var key string
			var n int64
			if err := rows.Scan(&key, &n); err != nil {
				return err
			}
			out[key] += n
		}

		return rows.Err()
	}); err != nil {
		t.Fatalf("get_usage_by_dimension as %s failed: %v", userID, err)
	}

	return out
}

// A team-admin reads a member's own key from the usage recorded while the
// member was in the Team; another member reads none of it.
func TestGetUsageByDimension_TeamAdmin(t *testing.T) {
	db := GetTestDB(t)
	const ws = "ud-team-ws"

	team := newOrgName("ud-team")
	teamID := manualTeam(t, db, team)
	admin := rawUser(t, db, "ud-admin")
	member := rawUser(t, db, "ud-member")
	peer := rawUser(t, db, "ud-peer")

	bindTeam(t, db, admin, "team-admin", team)
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, teamID, peer)

	key := createTestAPIKey(t, member, ws, 0)
	recordUsage(t, key, "m1", 10, 0, nil)
	aggregateUsage(t)

	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, teamID, member)
	recordUsage(t, key, "m1", 20, 5, nil)
	aggregateUsage(t)

	if got := dimensionUsage(t, db, admin, nil, dimFilter{workspace: ws}); len(got) != 1 || got[key] != 25 {
		t.Errorf("team-admin: %v, want the member's key with 25 (usage after joining)", got)
	}

	if got := dimensionUsage(t, db, peer, nil, dimFilter{workspace: ws}); len(got) != 0 {
		t.Errorf("another member: %v, want nothing", got)
	}

	if got := dimensionUsage(t, db, member, nil, dimFilter{workspace: ws}); got[key] != 35 {
		t.Errorf("owner: %v, want 35", got)
	}

	var name sql.NullString
	if err := asUser(t, db, admin, nil, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT api_key_name FROM api.get_usage_by_dimension('1970-01-01', '2999-12-31', $1) LIMIT 1`, key).Scan(&name)
	}); err != nil || !name.Valid || name.String == "" {
		t.Errorf("team-admin key name = %v, %v; want the key's name", name, err)
	}
}

// Usage of a deleted key stays visible to whoever read the key directly.
func TestGetUsageByDimension_DeletedKey(t *testing.T) {
	db := GetTestDB(t)
	const ws = "ud-deleted-ws"

	owner := rawUser(t, db, "ud-del-owner")
	stranger := rawUser(t, db, "ud-del-stranger")
	reader := rawUser(t, db, "ud-del-reader")
	bindGlobal(t, db, reader, customRole(t, db, "workspace:usage-read"))

	key := createTestAPIKey(t, owner, ws, 0)
	recordUsage(t, key, "m1", 10, 2, nil)
	aggregateUsage(t)

	mustExec(t, db, `DELETE FROM api.api_keys WHERE id = $1`, key)

	var daily int
	if err := db.QueryRow(`SELECT count(*) FROM api.api_daily_usage WHERE (spec).api_key_id = $1`, key).Scan(&daily); err != nil || daily != 0 {
		t.Fatalf("api_daily_usage rows after delete = %d, %v; want 0", daily, err)
	}

	for who, want := range map[string]int64{owner: 12, reader: 12, stranger: 0} {
		if got := dimensionUsage(t, db, who, nil, dimFilter{workspace: ws}); got[key] != want {
			t.Errorf("%s: %v, want %d", who, got, want)
		}
	}

	var name sql.NullString
	if err := asUser(t, db, owner, nil, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT api_key_name FROM api.get_usage_by_dimension('1970-01-01', '2999-12-31', $1) LIMIT 1`, key).Scan(&name)
	}); err != nil || name.Valid {
		t.Errorf("deleted key name = %v, %v; want NULL", name, err)
	}
}
