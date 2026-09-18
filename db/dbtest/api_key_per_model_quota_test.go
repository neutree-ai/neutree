package dbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// seedDetailedUsage writes the single api_daily_usage row a key gets for one day
// (api_daily_usage_key_date_idx is unique on key+date), carrying every
// "<endpoint_type>|<endpoint_name>|<model>" dimension in its
// detailed_dimensional_usage -- the shape sync_api_key_usage accumulates
// (054_usage_statistics_enhance.up.sql:131). total_usage is their sum, as the
// real aggregation keeps the two in step.
func seedDetailedUsage(t *testing.T, db *sql.DB, name, workspace, apiKeyID string, byDimension map[string]int) {
	t.Helper()

	detail := map[string]map[string]int{}
	total := 0

	for k, v := range byDimension {
		detail[k] = map[string]int{"total": v, "prompt": v, "completion": 0}
		total += v
	}

	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("encode detailed usage: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
		VALUES ('v1','ApiDailyUsage',
			ROW($1, NULL, $2, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($3::uuid, CURRENT_DATE, $4, '{}'::jsonb, $5::jsonb)::api.api_daily_usage_spec,
			ROW(now())::api.api_daily_usage_status)`,
		name, workspace, apiKeyID, total, string(encoded)); err != nil {
		t.Fatalf("insert detailed daily usage: %v", err)
	}
}

// TestApiKeyPerModelQuota covers per-model token quotas: the limit hangs off an
// allowed_models entry, and as soon as any entry carries one the key's overall
// token_quota stops being enforced. Usage is read from the ledger's
// detailed_dimensional_usage, keyed by the same (type, endpoint, model) triple the
// allowlist entries are scoped to.
func TestApiKeyPerModelQuota(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "pmquotauser", "pmquota@example.com", "testpassword")

	// paid-model is capped per (external, ep-ext); free-model has no limit at all.
	// The overall token_quota is deliberately present and tiny: it must be ignored
	// entirely while any entry carries a limit.
	const limits = `{
		"token_quota": {"limit": 1, "period": "monthly"},
		"allowed_models": [
			{"model": "free-model"},
			{"model": "paid-model", "type": "external", "endpoint_name": "ep-ext", "token_limit": 1000}
		]
	}`

	var apiKeyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'pmquota-ws',
				p_name := 'pmquota-key',
				p_quota := 0,
				p_limits := $1::jsonb
			)`, limits).Scan(&apiKeyID)
	})
	if err != nil {
		t.Fatalf("create_api_key with per-model limits: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", apiKeyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
	})

	// 300 tokens on the capped entry, plus usage on dimensions that must NOT count
	// against it: the same model on a different endpoint, and a different model.
	seedDetailedUsage(t, db, "pmquota-u1", "pmquota-ws", apiKeyID, map[string]int{
		"external|ep-ext|paid-model":   300,
		"external|ep-other|paid-model": 700,
		"internal|ep-int|free-model":   900,
	})

	remaining := func(t *testing.T, model, epType, epName interface{}) sql.NullInt64 {
		t.Helper()

		var rem sql.NullInt64
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				`SELECT api.get_api_key_remaining($1, $2, $3, $4)`, apiKeyID, model, epType, epName).Scan(&rem)
		})
		if err != nil {
			t.Fatalf("get_api_key_remaining: %v", err)
		}

		return rem
	}

	t.Run("pinned entry counts only its own dimension", func(t *testing.T) {
		// 1000 - 300. The 700 on ep-other and the 900 on free-model are other
		// dimensions and must not be charged here.
		if got := remaining(t, "paid-model", "external", "ep-ext"); !got.Valid || got.Int64 != 700 {
			t.Fatalf("expected remaining 700, got %v", got)
		}
	})

	t.Run("a model with no entry limit is unlimited", func(t *testing.T) {
		if got := remaining(t, "free-model", "internal", "ep-int"); got.Valid {
			t.Fatalf("expected NULL (unlimited) for an unlimited entry, got %v", got.Int64)
		}
	})

	t.Run("the pinned entry does not match a different endpoint", func(t *testing.T) {
		// The entry pins endpoint_name=ep-ext, so a request that hit ep-other
		// matches no limited entry: unlimited here, enforced by the access plugin
		// elsewhere if the model is not permitted at all.
		if got := remaining(t, "paid-model", "external", "ep-other"); got.Valid {
			t.Fatalf("expected NULL for a non-matching endpoint, got %v", got.Int64)
		}
	})

	t.Run("overall token_quota is not enforced while an entry carries a limit", func(t *testing.T) {
		// token_quota.limit is 1 and total usage is 1900; had the overall quota
		// still applied, every one of these would be deeply negative.
		if got := remaining(t, "free-model", "internal", "ep-int"); got.Valid {
			t.Fatalf("overall quota leaked into per-model mode: got %v", got.Int64)
		}
	})

	t.Run("no model on the call is unlimited (fail-open for an old gateway)", func(t *testing.T) {
		var rem sql.NullInt64
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT api.get_api_key_remaining($1)`, apiKeyID).Scan(&rem)
		})
		if err != nil {
			t.Fatalf("get_api_key_remaining (legacy 1-arg call): %v", err)
		}
		if rem.Valid {
			t.Fatalf("expected NULL when no model is supplied, got %v", rem.Int64)
		}
	})

	t.Run("get_api_key_limits reports per-entry used/remaining and granularity", func(t *testing.T) {
		var granularity, used, rem string
		var freeUsed sql.NullString
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT j ->> 'quota_granularity',
				       j #>> '{allowed_models,1,used}',
				       j #>> '{allowed_models,1,remaining}',
				       j #>> '{allowed_models,0,used}'
				FROM (SELECT api.get_api_key_limits($1) AS j) s`, apiKeyID).
				Scan(&granularity, &used, &rem, &freeUsed)
		})
		if err != nil {
			t.Fatalf("get_api_key_limits: %v", err)
		}
		if granularity != "per_model" {
			t.Fatalf("expected quota_granularity=per_model, got %s", granularity)
		}
		if used != "300" || rem != "700" {
			t.Fatalf("expected used=300 remaining=700, got used=%s remaining=%s", used, rem)
		}
		// An entry without a limit gets no used/remaining: there is nothing to
		// count against, and reporting a number would imply a cap.
		if freeUsed.Valid {
			t.Fatalf("expected no used on an unlimited entry, got %s", freeUsed.String)
		}
	})

	t.Run("clearing every entry limit falls back to the overall quota", func(t *testing.T) {
		const next = `{
			"token_quota": {"limit": 5000, "period": "monthly"},
			"allowed_models": [
				{"model": "free-model"},
				{"model": "paid-model", "type": "external", "endpoint_name": "ep-ext"}
			]
		}`
		if err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `SELECT api.set_api_key_limits($1, $2::jsonb)`, apiKeyID, next)
			return e
		}); err != nil {
			t.Fatalf("set_api_key_limits: %v", err)
		}
		// Back to one pool: 5000 - (300 + 700 + 900).
		if got := remaining(t, "paid-model", "external", "ep-ext"); !got.Valid || got.Int64 != 3100 {
			t.Fatalf("expected remaining 3100 after falling back to the overall quota, got %v", got)
		}

		var granularity string
		if err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				`SELECT api.get_api_key_limits($1) ->> 'quota_granularity'`, apiKeyID).Scan(&granularity)
		}); err != nil {
			t.Fatalf("get_api_key_limits after fallback: %v", err)
		}
		if granularity != "overall" {
			t.Fatalf("expected quota_granularity=overall after clearing entry limits, got %s", granularity)
		}
	})
}

// TestApiKeyPerModelQuotaWildcard covers an entry that pins neither type nor
// endpoint_name: its usage is the sum of every detail key for that model, not a
// single lookup.
func TestApiKeyPerModelQuotaWildcard(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "pmwilduser", "pmwild@example.com", "testpassword")

	const limits = `{"allowed_models":[{"model":"shared-model","token_limit":1000}]}`

	var apiKeyID string
	if err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'pmwild-ws',
				p_name := 'pmwild-key',
				p_quota := 0,
				p_limits := $1::jsonb
			)`, limits).Scan(&apiKeyID)
	}); err != nil {
		t.Fatalf("create_api_key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", apiKeyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
	})

	seedDetailedUsage(t, db, "pmwild-u1", "pmwild-ws", apiKeyID, map[string]int{
		// Same model reached through two different endpoints, one internal and one
		// external: an unpinned entry caps their sum.
		"internal|ep-a|shared-model": 100,
		"external|ep-b|shared-model": 250,
		// A different model must not be charged against it.
		"internal|ep-a|another-model": 900,
	})

	var rem sql.NullInt64
	if err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT api.get_api_key_remaining($1, 'shared-model', 'internal', 'ep-a')`, apiKeyID).Scan(&rem)
	}); err != nil {
		t.Fatalf("get_api_key_remaining: %v", err)
	}
	// 1000 - (100 + 250); the 900 on another-model is not counted.
	if !rem.Valid || rem.Int64 != 650 {
		t.Fatalf("expected remaining 650, got %v", rem)
	}
}

// TestApiKeyPerModelQuotaValidation covers validate_api_key_limits' two new rules:
// token_limit is two-state (absent = unlimited, present = positive integer), and
// entries of a model that carries a limit must not overlap.
func TestApiKeyPerModelQuotaValidation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "pmvaliduser", "pmvalid@example.com", "testpassword")

	create := func(t *testing.T, limits string) (string, error) {
		t.Helper()

		var id string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT id FROM api.create_api_key(
					p_workspace := 'pmvalid-ws',
					p_name := 'pmvalid-key',
					p_quota := 0,
					p_limits := $1::jsonb
				)`, limits).Scan(&id)
		})
		if err == nil {
			_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", id)
		}

		return id, err
	}

	t.Run("rejects a non-positive or fractional token_limit", func(t *testing.T) {
		for _, bad := range []string{
			`{"allowed_models":[{"model":"m","token_limit":0}]}`,
			`{"allowed_models":[{"model":"m","token_limit":-1}]}`,
			`{"allowed_models":[{"model":"m","token_limit":1.5}]}`,
			`{"allowed_models":[{"model":"m","token_limit":"100"}]}`,
		} {
			if _, err := create(t, bad); err == nil {
				t.Fatalf("expected rejection of %s", bad)
			}
		}
	})

	t.Run("rejects overlapping entries for a limited model", func(t *testing.T) {
		for _, bad := range []string{
			// A bare entry is a wildcard, so it overlaps the pinned one.
			`{"allowed_models":[{"model":"m","token_limit":100},{"model":"m","type":"external"}]}`,
			// Pinning only `type` on both still leaves endpoint_name wildcard on both.
			`{"allowed_models":[{"model":"m","type":"external","token_limit":100},{"model":"m","type":"external","endpoint_name":"ep"}]}`,
			// Exact duplicates overlap too.
			`{"allowed_models":[{"model":"m","type":"external","endpoint_name":"ep","token_limit":100},{"model":"m","type":"external","endpoint_name":"ep"}]}`,
		} {
			if _, err := create(t, bad); err == nil {
				t.Fatalf("expected rejection of overlapping entries: %s", bad)
			}
		}
	})

	t.Run("accepts disjoint entries for a limited model", func(t *testing.T) {
		for _, ok := range []string{
			// Different endpoints of the same model, both pinned: a partition.
			`{"allowed_models":[{"model":"m","type":"external","endpoint_name":"ep-a","token_limit":100},{"model":"m","type":"external","endpoint_name":"ep-b","token_limit":200}]}`,
			// Different IE/EE sides of the same model name -- the case per-model
			// quotas exist for.
			`{"allowed_models":[{"model":"m","type":"internal"},{"model":"m","type":"external","token_limit":200}]}`,
			// Overlap across DIFFERENT models is not overlap.
			`{"allowed_models":[{"model":"m","token_limit":100},{"model":"n"},{"model":"n","type":"external"}]}`,
		} {
			if _, err := create(t, ok); err != nil {
				t.Fatalf("expected %s to be accepted, got %v", ok, err)
			}
		}
	})

	t.Run("leaves overlapping entries alone when no entry is limited", func(t *testing.T) {
		// 078 migrated legacy name-only entries into this shape, so overlap must
		// stay legal for keys that carry no per-model quota; otherwise existing keys
		// would start failing on their next edit.
		const legacy = `{"allowed_models":[{"model":"m"},{"model":"m","type":"external"},{"model":"m","type":"external","endpoint_name":"ep"}]}`
		if _, err := create(t, legacy); err != nil {
			t.Fatalf("expected legacy overlapping allowlist to remain valid, got %v", err)
		}
	})
}
