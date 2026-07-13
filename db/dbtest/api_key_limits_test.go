package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestApiKeyLimits covers the API Key limits that live on api_key.spec.limits
// (single object). Verifies create_api_key(p_limits),
// set_api_key_limits (owner-guarded), get_api_key_limits (config + period
// used/remaining) and get_api_key_remaining (the dynamic scalar the gateway
// quota plugin pulls), with usage drawn from the api_daily_usage ledger.
func TestApiKeyLimits(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "limitsuser", "limits@example.com", "testpassword")
	other := CreateTestUser(t, "limitsother", "limitsother@example.com", "testpassword")

	const limits = `{"token_quota":{"limit":300,"period":"monthly"},"rps":10,"rpm":600,"concurrency":8,"allowed_models":[{"model":"gpt-4","type":"internal","endpoint_name":"ep-a"}],"disabled":false}`

	// Create a key carrying limits in one call.
	var apiKeyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'limits-ws',
				p_name := 'limits-key',
				p_quota := 0,
				p_limits := $1::jsonb
			)`, limits).Scan(&apiKeyID)
	})
	if err != nil {
		t.Fatalf("create_api_key with limits: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", apiKeyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
	})

	t.Run("limits round-trip on spec", func(t *testing.T) {
		var lim, disabled, model, quota string
		if err := db.QueryRowContext(ctx, `
			SELECT (spec).limits #>> '{token_quota,limit}',
			       (spec).limits #>> '{disabled}',
			       (spec).limits #>> '{allowed_models,0,model}',
			       (spec).quota::text
			FROM api.api_keys WHERE id = $1`, apiKeyID).Scan(&lim, &disabled, &model, &quota); err != nil {
			t.Fatalf("read spec.limits: %v", err)
		}
		if lim != "300" || disabled != "false" || model != "gpt-4" {
			t.Fatalf("unexpected limits: limit=%s disabled=%s model=%s", lim, disabled, model)
		}
		// legacy spec.quota mirrors the enforced token_quota.limit (p_quota was 0)
		if quota != "300" {
			t.Fatalf("expected spec.quota mirrored to 300, got %s", quota)
		}
	})

	// Seed current-period usage = 100 for this key.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
		VALUES ('v1','ApiDailyUsage',
			ROW('limits-usage-1', NULL, 'limits-ws', NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, CURRENT_DATE, 100, '{}'::jsonb, NULL::jsonb)::api.api_daily_usage_spec,
			ROW(now())::api.api_daily_usage_status)`, apiKeyID); err != nil {
		t.Fatalf("insert daily usage: %v", err)
	}

	t.Run("get_api_key_remaining = limit - period usage (owner)", func(t *testing.T) {
		var rem sql.NullInt64
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT api.get_api_key_remaining($1)`, apiKeyID).Scan(&rem)
		})
		if err != nil {
			t.Fatalf("get_api_key_remaining: %v", err)
		}
		if !rem.Valid || rem.Int64 != 200 {
			t.Fatalf("expected remaining 200, got %v", rem)
		}
	})
	t.Run("get_api_key_remaining rejects a non-owner without usage-read", func(t *testing.T) {
		err := execWithContext(t, db, []SetContextFunc{setUserContext(other.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			var rem sql.NullInt64
			return tx.QueryRowContext(ctx, `SELECT api.get_api_key_remaining($1)`, apiKeyID).Scan(&rem)
		})
		if err == nil {
			t.Fatalf("expected a permission error for non-owner get_api_key_remaining")
		}
	})

	t.Run("get_api_key_limits exposes used/remaining to owner", func(t *testing.T) {
		var used, rem string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT j #>> '{token_quota,used}', j #>> '{token_quota,remaining}'
				FROM (SELECT api.get_api_key_limits($1) AS j) s`, apiKeyID).Scan(&used, &rem)
		})
		if err != nil {
			t.Fatalf("get_api_key_limits: %v", err)
		}
		if used != "100" || rem != "200" {
			t.Fatalf("expected used=100 remaining=200, got used=%s remaining=%s", used, rem)
		}
	})

	t.Run("get_api_key_limits is owner-scoped (other user sees null)", func(t *testing.T) {
		var got sql.NullString
		err := execWithContext(t, db, []SetContextFunc{setUserContext(other.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT api.get_api_key_limits($1)::text`, apiKeyID).Scan(&got)
		})
		if err != nil {
			t.Fatalf("get_api_key_limits (other): %v", err)
		}
		if got.Valid {
			t.Fatalf("expected NULL for non-owner, got %q", got.String)
		}
	})

	t.Run("get_api_key_limits denies an anonymous caller (no auth.uid)", func(t *testing.T) {
		// Without a JWT context auth.uid() is NULL; the owner check must still deny
		// (NULL-safe), not leak limits/usage.
		var got sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT api.get_api_key_limits($1)::text`, apiKeyID).Scan(&got); err != nil {
			t.Fatalf("get_api_key_limits (anon): %v", err)
		}
		if got.Valid {
			t.Fatalf("expected NULL for anonymous caller, got %q", got.String)
		}
	})

	t.Run("set_api_key_limits updates config for owner", func(t *testing.T) {
		const next = `{"token_quota":{"limit":500,"period":"daily"},"concurrency":3}`
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, next)
			return e
		})
		if err != nil {
			t.Fatalf("set_api_key_limits: %v", err)
		}
		var lim, period, quota string
		if err := db.QueryRowContext(ctx, `
			SELECT (spec).limits #>> '{token_quota,limit}', (spec).limits #>> '{token_quota,period}', (spec).quota::text
			FROM api.api_keys WHERE id = $1`, apiKeyID).Scan(&lim, &period, &quota); err != nil {
			t.Fatalf("read updated limits: %v", err)
		}
		if lim != "500" || period != "daily" {
			t.Fatalf("expected limit=500 period=daily, got limit=%s period=%s", lim, period)
		}
		if quota != "500" {
			t.Fatalf("expected spec.quota mirrored to 500, got %s", quota)
		}
	})

	t.Run("set_api_key_limits rejects non-owner", func(t *testing.T) {
		err := execWithContext(t, db, []SetContextFunc{setUserContext(other.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, '{}'::jsonb)`, apiKeyID)
			return e
		})
		if err == nil {
			t.Fatalf("expected error when non-owner sets limits")
		}
	})

	// A non-positive numeric limit must fail loudly (rather than be silently
	// dropped to "unlimited"). Covers create_api_key and set_api_key_limits.
	t.Run("create_api_key rejects non-positive numeric limits", func(t *testing.T) {
		bad := []string{
			`{"token_quota":{"limit":0,"period":"monthly"}}`,
			`{"token_quota":{"limit":-5,"period":"monthly"}}`,
			`{"rps":0}`,
			`{"rpm":-1}`,
			`{"concurrency":0}`,
			`{"rps":1.5}`,
		}
		for _, b := range bad {
			var id string
			err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `
					SELECT id FROM api.create_api_key(
						p_workspace := 'limits-ws',
						p_name := 'bad-key',
						p_quota := 0,
						p_limits := $1::jsonb
					)`, b).Scan(&id)
			})
			if err == nil {
				_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", id)
				t.Fatalf("expected create_api_key to reject limits %s", b)
			}
		}
	})

	t.Run("set_api_key_limits rejects non-positive numeric limits", func(t *testing.T) {
		bad := []string{
			`{"token_quota":{"limit":0,"period":"monthly"}}`,
			`{"rps":-2}`,
			`{"concurrency":0}`,
		}
		for _, b := range bad {
			err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, b)
				return e
			})
			if err == nil {
				t.Fatalf("expected set_api_key_limits to reject limits %s", b)
			}
		}
		// The rejected updates must not have changed the stored limits: the prior
		// subtest left token_quota.limit = 500.
		var lim string
		if err := db.QueryRowContext(ctx, `
			SELECT (spec).limits #>> '{token_quota,limit}'
			FROM api.api_keys WHERE id = $1`, apiKeyID).Scan(&lim); err != nil {
			t.Fatalf("read limits after rejected set: %v", err)
		}
		if lim != "500" {
			t.Fatalf("expected unchanged limit 500 after rejected sets, got %s", lim)
		}
	})

	t.Run("set_api_key_limits accepts an explicit empty allowed_models (deny-all)", func(t *testing.T) {
		// [] is a valid value (deny-all), not a non-positive numeric limit, so it
		// must pass validation.
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, '{"allowed_models":[]}'::jsonb)`, apiKeyID)
			return e
		})
		if err != nil {
			t.Fatalf("expected empty allowed_models to be accepted, got %v", err)
		}
	})

	t.Run("set_api_key_limits accepts endpoint-scoped and unpinned allowed_models entries", func(t *testing.T) {
		// A pinned entry (type + endpoint_name) and an unpinned one (model only,
		// = any endpoint of that model) are both valid post-NEU-540.
		const v = `{"allowed_models":[{"model":"gpt-4","type":"external","endpoint_name":"ee-a"},{"model":"llama"}]}`
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, v)
			return e
		})
		if err != nil {
			t.Fatalf("expected endpoint-scoped allowed_models to be accepted, got %v", err)
		}
	})

	t.Run("set_api_key_limits rejects malformed allowed_models entries", func(t *testing.T) {
		// The legacy bare-string shape and objects missing a non-empty model must
		// now fail validation (they can no longer reach the gateway as-is).
		cases := []string{
			`{"allowed_models":["gpt-4"]}`,                // legacy bare string
			`{"allowed_models":[{"type":"external"}]}`,    // no model
			`{"allowed_models":[{"model":""}]}`,           // empty model
			`{"allowed_models":[{"model":"x","type":5}]}`, // non-string type
		}
		for _, c := range cases {
			err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, c)
				return e
			})
			if err == nil {
				t.Fatalf("expected malformed allowed_models %q to be rejected, got nil", c)
			}
		}
	})
}
