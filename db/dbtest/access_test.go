package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestAccessPolicyResolution exercises the access-control policies
// (NEUTREE-GENERAL-9, 1.1 track): per-level rules (rate_limit, concurrency)
// and the most-restrictive resolution the AI gateway consumes
// (api.get_api_key_access), plus set/delete.
//
// set_access_policy is SECURITY INVOKER; running on the superuser test
// connection bypasses access_policies RLS (exercised separately by gateway/UI),
// isolating the upsert + resolver logic. Unlike quota there is no hierarchy
// trigger: rules never sum, so a child rule never conflicts with a parent.
func TestAccessPolicyResolution(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const ws = "access-test-ws"
	user1 := CreateTestUser(t, "accessuser1", "access1@example.com", "testpassword")

	var k1 string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user1.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := $1, p_name := $2, p_quota := 1000)`, ws, "access-k1").Scan(&k1)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.access_policies WHERE workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", k1)
	})

	setAccess := func(level, ruleType, spec string, wsArg, userArg, keyArg interface{}) (int64, error) {
		var id int64
		err := db.QueryRowContext(ctx,
			`SELECT (api.set_access_policy($1, $2, $3::jsonb, $4, $5, $6)).id`,
			level, ruleType, spec, wsArg, userArg, keyArg).Scan(&id)
		return id, err
	}
	// minimum rate_limit for a given window across all levels (what the gateway enforces).
	minRate := func(keyID, window string) sql.NullInt64 {
		var v sql.NullInt64
		if err := db.QueryRowContext(ctx, `
			SELECT min((e->>'limit')::bigint)
			FROM jsonb_array_elements(api.get_api_key_access($1)->'rate_limits') e
			WHERE e->>'window' = $2`, keyID, window).Scan(&v); err != nil {
			t.Fatalf("minRate(%s,%s): %v", keyID, window, err)
		}
		return v
	}
	concurrency := func(keyID string) sql.NullInt64 {
		var v sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT (api.get_api_key_access($1)->>'concurrency')::bigint`, keyID).Scan(&v); err != nil {
			t.Fatalf("concurrency(%s): %v", keyID, err)
		}
		return v
	}

	// Three levels set the same window; the gateway must see the most restrictive.
	if _, err := setAccess("workspace", "rate_limit", `{"limit":1000,"window":"minute"}`, ws, nil, nil); err != nil {
		t.Fatalf("workspace rate_limit: %v", err)
	}
	if _, err := setAccess("user", "rate_limit", `{"limit":600,"window":"minute"}`, ws, user1.ID, nil); err != nil {
		t.Fatalf("user rate_limit: %v", err)
	}
	if _, err := setAccess("api_key", "rate_limit", `{"limit":300,"window":"minute"}`, nil, nil, k1); err != nil {
		t.Fatalf("api_key rate_limit: %v", err)
	}

	t.Run("rate_limit resolves to most restrictive across levels", func(t *testing.T) {
		if got := minRate(k1, "minute"); !got.Valid || got.Int64 != 300 {
			t.Fatalf("expected effective minute limit 300, got %v", got)
		}
	})

	t.Run("a scope holds one rate_limit per window (RPS and RPM together)", func(t *testing.T) {
		// Same api_key already has a per-minute cap; a per-second cap coexists.
		if _, err := setAccess("api_key", "rate_limit", `{"limit":10,"window":"second"}`, nil, nil, k1); err != nil {
			t.Fatalf("api_key second rate_limit: %v", err)
		}
		if got := minRate(k1, "second"); !got.Valid || got.Int64 != 10 {
			t.Fatalf("expected effective second limit 10, got %v", got)
		}
		// the per-minute cap is unaffected by adding the per-second one.
		if got := minRate(k1, "minute"); !got.Valid || got.Int64 != 300 {
			t.Fatalf("expected minute limit still 300, got %v", got)
		}
	})

	t.Run("upserting the same window overwrites in place", func(t *testing.T) {
		if _, err := setAccess("api_key", "rate_limit", `{"limit":250,"window":"minute"}`, nil, nil, k1); err != nil {
			t.Fatalf("api_key minute rate_limit upsert: %v", err)
		}
		if got := minRate(k1, "minute"); !got.Valid || got.Int64 != 250 {
			t.Fatalf("expected minute limit 250 after upsert, got %v", got)
		}
	})

	t.Run("concurrency resolves to minimum", func(t *testing.T) {
		if _, err := setAccess("workspace", "concurrency", `{"max":20}`, ws, nil, nil); err != nil {
			t.Fatalf("workspace concurrency: %v", err)
		}
		if _, err := setAccess("api_key", "concurrency", `{"max":8}`, nil, nil, k1); err != nil {
			t.Fatalf("api_key concurrency: %v", err)
		}
		if got := concurrency(k1); !got.Valid || got.Int64 != 8 {
			t.Fatalf("expected effective concurrency 8, got %v", got)
		}
	})

	t.Run("invalid rule_spec is rejected by CHECK", func(t *testing.T) {
		if _, err := setAccess("api_key", "rate_limit", `{"limit":0,"window":"minute"}`, nil, nil, k1); err == nil {
			t.Fatalf("expected CHECK violation for limit=0")
		}
		if _, err := setAccess("api_key", "rate_limit", `{"limit":5,"window":"fortnight"}`, nil, nil, k1); err == nil {
			t.Fatalf("expected CHECK violation for bad window")
		}
	})

	t.Run("delete removes a policy", func(t *testing.T) {
		id, err := setAccess("user", "concurrency", `{"max":4}`, ws, user1.ID, nil)
		if err != nil {
			t.Fatalf("user concurrency: %v", err)
		}
		var deleted int64
		if err := db.QueryRowContext(ctx, `SELECT api.delete_access_policy($1)`, id).Scan(&deleted); err != nil {
			t.Fatalf("delete_access_policy: %v", err)
		}
		if deleted != id {
			t.Fatalf("expected delete to return %d, got %d", id, deleted)
		}
		// user concurrency gone -> effective concurrency back to min(workspace 20, api_key 8) = 8
		if got := concurrency(k1); !got.Valid || got.Int64 != 8 {
			t.Fatalf("expected concurrency 8 after delete, got %v", got)
		}
	})
}

// TestAccessAllowlistAndDay exercises the model/endpoint allowlist resolution
// (intersection across levels; null = unrestricted, [] = deny all) and the
// 'day' rate-limit window added in migration 073.
func TestAccessAllowlistAndDay(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	const ws = "access-allow-ws"
	user1 := CreateTestUser(t, "accessallow1", "accessallow1@example.com", "testpassword")

	var k1 string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user1.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := $1, p_name := $2, p_quota := 1000)`, ws, "allow-k1").Scan(&k1)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.access_policies WHERE workspace = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", k1)
	})

	setAccess := func(level, ruleType, spec string, wsArg, userArg, keyArg interface{}) error {
		_, err := db.ExecContext(ctx,
			`SELECT api.set_access_policy($1, $2, $3::jsonb, $4, $5, $6)`,
			level, ruleType, spec, wsArg, userArg, keyArg)
		return err
	}
	// allowed_models as a text scalar: "null" when unrestricted, else a json array.
	allowedModels := func() string {
		var s string
		if err := db.QueryRowContext(ctx,
			`SELECT coalesce(api.get_api_key_access($1)->'allowed_models', 'null'::jsonb)::text`, k1).Scan(&s); err != nil {
			t.Fatalf("allowed_models: %v", err)
		}
		return s
	}
	hasModel := func(m string) bool {
		var b bool
		if err := db.QueryRowContext(ctx,
			`SELECT coalesce(api.get_api_key_access($1)->'allowed_models' ? $2, false)`, k1, m).Scan(&b); err != nil {
			t.Fatalf("hasModel: %v", err)
		}
		return b
	}
	modelsLen := func() int {
		var n sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT jsonb_array_length(api.get_api_key_access($1)->'allowed_models')`, k1).Scan(&n); err != nil {
			t.Fatalf("modelsLen: %v", err)
		}
		if !n.Valid {
			return -1
		}
		return int(n.Int64)
	}

	t.Run("no allowlist -> unrestricted (null)", func(t *testing.T) {
		if got := allowedModels(); got != "null" {
			t.Fatalf("expected null, got %s", got)
		}
	})

	t.Run("model allowlist intersects across levels", func(t *testing.T) {
		if err := setAccess("workspace", "model_allowlist", `{"models":["a","b","c"]}`, ws, nil, nil); err != nil {
			t.Fatalf("ws model_allowlist: %v", err)
		}
		if err := setAccess("api_key", "model_allowlist", `{"models":["b","c","d"]}`, nil, nil, k1); err != nil {
			t.Fatalf("key model_allowlist: %v", err)
		}
		// intersection {a,b,c} ∩ {b,c,d} = {b,c}
		if modelsLen() != 2 || !hasModel("b") || !hasModel("c") || hasModel("a") || hasModel("d") {
			t.Fatalf("expected {b,c}, got %s", allowedModels())
		}
	})

	t.Run("empty intersection -> deny all ([])", func(t *testing.T) {
		// override api_key allowlist to a disjoint set -> {a,b,c} ∩ {z} = {}
		if err := setAccess("api_key", "model_allowlist", `{"models":["z"]}`, nil, nil, k1); err != nil {
			t.Fatalf("key model_allowlist: %v", err)
		}
		if modelsLen() != 0 {
			t.Fatalf("expected empty array (deny all), got %s", allowedModels())
		}
	})

	t.Run("endpoint allowlist intersects by (type,name)", func(t *testing.T) {
		if err := setAccess("workspace", "endpoint_allowlist",
			`{"endpoints":[{"type":"endpoint","name":"x"},{"type":"external_endpoint","name":"y"}]}`, ws, nil, nil); err != nil {
			t.Fatalf("ws endpoint_allowlist: %v", err)
		}
		if err := setAccess("user", "endpoint_allowlist",
			`{"endpoints":[{"type":"endpoint","name":"x"}]}`, ws, user1.ID, nil); err != nil {
			t.Fatalf("user endpoint_allowlist: %v", err)
		}
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT jsonb_array_length(api.get_api_key_access($1)->'allowed_endpoints')`, k1).Scan(&n); err != nil {
			t.Fatalf("endpoints len: %v", err)
		}
		if n != 1 {
			t.Fatalf("expected 1 endpoint in intersection, got %d", n)
		}
		var ok bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM jsonb_array_elements(api.get_api_key_access($1)->'allowed_endpoints') e
				WHERE e->>'type'='endpoint' AND e->>'name'='x')`, k1).Scan(&ok); err != nil {
			t.Fatalf("endpoint check: %v", err)
		}
		if !ok {
			t.Fatalf("expected endpoint:x in intersection")
		}
	})

	t.Run("day window rate_limit accepted and resolved", func(t *testing.T) {
		if _, err := db.ExecContext(ctx,
			`SELECT api.set_access_policy('api_key','rate_limit','{"limit":100,"window":"day"}'::jsonb, NULL, NULL, $1)`, k1); err != nil {
			t.Fatalf("day rate_limit: %v", err)
		}
		var lim sql.NullInt64
		if err := db.QueryRowContext(ctx, `
			SELECT (e->>'limit')::bigint FROM jsonb_array_elements(api.get_api_key_access($1)->'rate_limits') e
			WHERE e->>'window'='day'`, k1).Scan(&lim); err != nil {
			t.Fatalf("day resolve: %v", err)
		}
		if !lim.Valid || lim.Int64 != 100 {
			t.Fatalf("expected day limit 100, got %v", lim)
		}
	})
}
