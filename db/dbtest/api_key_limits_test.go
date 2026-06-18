package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// TestApiKeyLimits covers the API Key limits convergence (migration 069): limits
// live on api_key.spec.limits (single object). Verifies create_api_key(p_limits),
// set_api_key_limits (owner-guarded), get_api_key_limits (config + period
// used/remaining) and get_api_key_remaining (the dynamic scalar the gateway
// quota plugin pulls), with usage drawn from the api_daily_usage ledger.
func TestApiKeyLimits(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "limitsuser", "limits@example.com", "testpassword")
	other := CreateTestUser(t, "limitsother", "limitsother@example.com", "testpassword")

	const limits = `{"token_quota":{"limit":300,"period":"monthly"},"rps":10,"rpm":600,"concurrency":8,"allowed_models":["gpt-4"],"disabled":false}`

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
		var lim, disabled, model string
		if err := db.QueryRowContext(ctx, `
			SELECT (spec).limits #>> '{token_quota,limit}',
			       (spec).limits #>> '{disabled}',
			       (spec).limits #>> '{allowed_models,0}'
			FROM api.api_keys WHERE id = $1`, apiKeyID).Scan(&lim, &disabled, &model); err != nil {
			t.Fatalf("read spec.limits: %v", err)
		}
		if lim != "300" || disabled != "false" || model != "gpt-4" {
			t.Fatalf("unexpected limits: limit=%s disabled=%s model=%s", lim, disabled, model)
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

	t.Run("get_api_key_remaining = limit - period usage", func(t *testing.T) {
		var rem sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT api.get_api_key_remaining($1)`, apiKeyID).Scan(&rem); err != nil {
			t.Fatalf("get_api_key_remaining: %v", err)
		}
		if !rem.Valid || rem.Int64 != 200 {
			t.Fatalf("expected remaining 200, got %v", rem)
		}
	})

	t.Run("get_api_key_limits exposes used/remaining to owner", func(t *testing.T) {
		var used, rem string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT api.get_api_key_limits($1) #>> '{token_quota,used}',
				       api.get_api_key_limits($1) #>> '{token_quota,remaining}'`, apiKeyID).Scan(&used, &rem)
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

	t.Run("set_api_key_limits updates config for owner", func(t *testing.T) {
		const next = `{"token_quota":{"limit":500,"period":"daily"},"concurrency":3}`
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, next)
			return e
		})
		if err != nil {
			t.Fatalf("set_api_key_limits: %v", err)
		}
		var lim, period string
		if err := db.QueryRowContext(ctx, `
			SELECT (spec).limits #>> '{token_quota,limit}', (spec).limits #>> '{token_quota,period}'
			FROM api.api_keys WHERE id = $1`, apiKeyID).Scan(&lim, &period); err != nil {
			t.Fatalf("read updated limits: %v", err)
		}
		if lim != "500" || period != "daily" {
			t.Fatalf("expected limit=500 period=daily, got limit=%s period=%s", lim, period)
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
}
