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

// TestQuotaDimensions covers per-dimension quota overlays (NEUTREE-GENERAL-9):
// a dimension policy is an independent overlay (it does not trip the sum
// hierarchy) and only constrains get_api_key_remaining when the request targets
// that dimension; its usage is sourced from detailed_dimensional_usage.
func TestQuotaDimensions(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const ws = "quota-dim-ws"
	const model = "quota-dim-model"
	user := CreateTestUser(t, "quotadimuser", "quotadim@example.com", "testpassword")

	var key string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(p_workspace := $1, p_name := $2, p_quota := 1000)`,
			ws, "quota-dim-k").Scan(&key)
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.quota_policies WHERE workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", key)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", key)
	})

	remaining := func(model, endpoint, etype interface{}) sql.NullInt64 {
		var r sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT api.get_api_key_remaining($1, $2, $3, $4)`, key, model, endpoint, etype).Scan(&r); err != nil {
			t.Fatalf("get_api_key_remaining: %v", err)
		}
		return r
	}

	// A model-dimension overlay on the api_key, limit 100. No agnostic policy.
	if _, err := db.ExecContext(ctx,
		`SELECT api.set_quota_policy('api_key','monthly',100,NULL,NULL,$1,'model',$2)`, key, model); err != nil {
		t.Fatalf("set model-dimension policy: %v", err)
	}

	t.Run("dimension overlay only applies when request matches", func(t *testing.T) {
		// No request dimension -> overlay ignored -> unconstrained (NULL).
		if r := remaining(nil, nil, nil); r.Valid {
			t.Fatalf("expected NULL (unconstrained) without request dimension, got %v", r.Int64)
		}
		// Matching model -> overlay applies, 100 - 0 usage = 100.
		if r := remaining(model, nil, nil); !r.Valid || r.Int64 != 100 {
			t.Fatalf("expected remaining 100 for matching model, got %v", r)
		}
		// Different model -> overlay does not apply.
		if r := remaining("other-model", nil, nil); r.Valid {
			t.Fatalf("expected NULL for non-matching model, got %v", r.Int64)
		}
	})

	t.Run("dimension usage comes from detailed_dimensional_usage", func(t *testing.T) {
		// 30 tokens on endpoint|ep1|model this period.
		if _, err := db.ExecContext(ctx, `
			INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
			VALUES ('v1','ApiDailyUsage',
				ROW('quota-dim-du', NULL, $2::text, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, CURRENT_DATE, 30::bigint, '{}'::jsonb,
					jsonb_build_object('endpoint|ep1|' || $3::text,
						jsonb_build_object('total', 30, 'prompt', 20, 'completion', 10)))::api.api_daily_usage_spec,
				ROW(now())::api.api_daily_usage_status)`, key, ws, model); err != nil {
			t.Fatalf("insert daily usage: %v", err)
		}
		// 100 - 30 = 70 for the matching model.
		if r := remaining(model, nil, nil); !r.Valid || r.Int64 != 70 {
			t.Fatalf("expected remaining 70 after 30 used, got %v", r)
		}
	})

	t.Run("dimension overlay skips the sum hierarchy", func(t *testing.T) {
		// An agnostic api_key quota of 50 exists; a model overlay of 700 must NOT
		// trip 10052 (overlays are independent), unlike an agnostic one would.
		if _, err := db.ExecContext(ctx,
			`SELECT api.set_quota_policy('user','monthly',50,$1,$2,NULL)`, ws, user.ID); err != nil {
			t.Fatalf("set user policy: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`SELECT api.set_quota_policy('api_key','monthly',700,NULL,NULL,$1,'model',$2)`, key, model); err != nil {
			t.Fatalf("model overlay of 700 should be allowed regardless of user quota: %v", err)
		}
	})
}
