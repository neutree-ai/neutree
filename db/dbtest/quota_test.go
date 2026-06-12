package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestQuotaHierarchyAndRemaining exercises the three-tier token quota
// (NEUTREE-GENERAL-9): the per-period hierarchy invariant enforced by
// api.validate_quota_hierarchy (sum of children <= parent) and the
// minimum-remaining computation the AI gateway consumes
// (api.get_api_key_remaining), plus api.delete_quota_policy.
//
// set_quota_policy is SECURITY INVOKER; running the writes on the superuser
// test connection bypasses the quota_policies RLS (which is exercised
// separately by the gateway/UI), so this test isolates the trigger + RPC logic.
func TestQuotaHierarchyAndRemaining(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const ws = "quota-test-ws"
	user1 := CreateTestUser(t, "quotauser1", "quota1@example.com", "testpassword")
	user2 := CreateTestUser(t, "quotauser2", "quota2@example.com", "testpassword")

	createKey := func(owner *TestUser, name string) string {
		var id string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(owner.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT id FROM api.create_api_key(
					p_workspace := $1, p_name := $2, p_quota := 1000)`, ws, name).Scan(&id)
		})
		if err != nil {
			t.Fatalf("failed to create API key %s: %v", name, err)
		}
		return id
	}
	k1 := createKey(user1, "quota-k1")
	k2 := createKey(user1, "quota-k2")

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.quota_policies WHERE workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id IN ($1, $2)", k1, k2)
	})

	// setPolicy upserts a policy on the superuser connection (RLS bypassed; the
	// hierarchy trigger still fires). nil args become SQL NULL.
	setPolicy := func(level, period string, limit int64, wsArg, userArg, keyArg interface{}) error {
		_, err := db.ExecContext(ctx,
			`SELECT api.set_quota_policy($1, $2, $3, $4, $5, $6)`,
			level, period, limit, wsArg, userArg, keyArg)
		return err
	}
	remaining := func(keyID string) sql.NullInt64 {
		var r sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT api.get_api_key_remaining($1)`, keyID).Scan(&r); err != nil {
			t.Fatalf("get_api_key_remaining(%s): %v", keyID, err)
		}
		return r
	}
	wantErrCode := func(t *testing.T, err error, code string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("expected error code %s, got %v", code, err)
		}
	}

	// workspace=1000, user1=600 are valid.
	if err := setPolicy("workspace", "monthly", 1000, ws, nil, nil); err != nil {
		t.Fatalf("workspace policy: %v", err)
	}
	if err := setPolicy("user", "monthly", 600, ws, user1.ID, nil); err != nil {
		t.Fatalf("user1 policy: %v", err)
	}

	t.Run("user total exceeding workspace is rejected (10050)", func(t *testing.T) {
		// user1=600 + user2=500 = 1100 > workspace 1000.
		wantErrCode(t, setPolicy("user", "monthly", 500, ws, user2.ID, nil), "10050")
	})

	t.Run("api key total exceeding user is rejected (10052)", func(t *testing.T) {
		wantErrCode(t, setPolicy("api_key", "monthly", 700, nil, nil, k1), "10052")
	})

	t.Run("valid api key policy auto-fills workspace", func(t *testing.T) {
		if err := setPolicy("api_key", "monthly", 500, nil, nil, k1); err != nil {
			t.Fatalf("k1=500: %v", err)
		}
		var got string
		if err := db.QueryRowContext(ctx,
			`SELECT workspace FROM api.quota_policies WHERE level='api_key' AND api_key_id=$1`, k1).Scan(&got); err != nil {
			t.Fatalf("read k1 policy: %v", err)
		}
		if got != ws {
			t.Fatalf("expected workspace %q auto-filled, got %q", ws, got)
		}
		// k1(500) + k2(200) = 700 > user1 600.
		wantErrCode(t, setPolicy("api_key", "monthly", 200, nil, nil, k2), "10052")
	})

	t.Run("lowering a parent below its children is rejected (10051/10053)", func(t *testing.T) {
		// user1 -> 400 < sum of its api keys (500).
		wantErrCode(t, setPolicy("user", "monthly", 400, ws, user1.ID, nil), "10051")
		// workspace -> 100 < sum of its users (600).
		wantErrCode(t, setPolicy("workspace", "monthly", 100, ws, nil, nil), "10053")
	})

	t.Run("get_api_key_remaining is the min across levels", func(t *testing.T) {
		// k1: min(workspace 1000, user 600, key 500) = 500.
		if r := remaining(k1); !r.Valid || r.Int64 != 500 {
			t.Fatalf("expected k1 remaining 500, got %v", r)
		}
		// k2: no key policy -> min(workspace 1000, user 600) = 600.
		if r := remaining(k2); !r.Valid || r.Int64 != 600 {
			t.Fatalf("expected k2 remaining 600, got %v", r)
		}
	})

	t.Run("delete_quota_policy relaxes the key", func(t *testing.T) {
		var deleted sql.NullInt64
		if err := db.QueryRowContext(ctx, `
			SELECT api.delete_quota_policy(
				(SELECT id FROM api.quota_policies WHERE level='api_key' AND api_key_id=$1))`, k1).Scan(&deleted); err != nil {
			t.Fatalf("delete_quota_policy: %v", err)
		}
		if !deleted.Valid {
			t.Fatalf("expected a deleted policy id")
		}
		// k1 now bounded only by user(600) / workspace(1000) -> 600.
		if r := remaining(k1); !r.Valid || r.Int64 != 600 {
			t.Fatalf("expected k1 remaining 600 after delete, got %v", r)
		}
	})
}
