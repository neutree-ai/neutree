package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// TestUsageBreakdownAggregation covers migration 095: aggregate_usage_records
// carries the cache / reasoning / cost breakdown into api_daily_usage, and
// get_usage_by_dimension returns it, NULL where it was never recorded.
func TestUsageBreakdownAggregation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "neu784user", "neu784@example.com", "testpassword")

	var keyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'neu-784-ws',
				p_name := 'neu-784-key',
				p_quota := 1000000
			)`).Scan(&keyID)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", keyID)
	})

	requestSeq := 0
	// record inserts one raw usage record through the same RPC the gateway
	// pipeline calls. Breakdown arguments may be nil for "not reported".
	record := func(model string, prompt, completion int64, cacheRead, cacheCreation, reasoning *int64, cost *float64) {
		requestSeq++
		_, err := db.ExecContext(ctx, `
			SELECT api.record_api_usage(
				p_api_key_id := $1,
				p_request_id := $2,
				p_usage_amount := $3,
				p_endpoint_name := 'neu-784-ep',
				p_endpoint_type := 'endpoint',
				p_model_name := $4,
				p_prompt_tokens := $5,
				p_completion_tokens := $6,
				p_cache_read_tokens := $7,
				p_cache_creation_tokens := $8,
				p_reasoning_tokens := $9,
				p_cost_usd := $10
			)`, keyID, fmt.Sprintf("neu-784-%s-%d", keyID, requestSeq), prompt+completion, model,
			prompt, completion, cacheRead, cacheCreation, reasoning, cost)
		if err != nil {
			t.Fatalf("failed to record usage: %v", err)
		}
	}

	aggregate := func() int {
		var n int
		// A cutoff slightly in the future so records inserted just now qualify.
		if err := db.QueryRowContext(ctx,
			"SELECT api.aggregate_usage_records(now() + interval '1 second')").Scan(&n); err != nil {
			t.Fatalf("failed to aggregate: %v", err)
		}
		return n
	}

	type bucket struct {
		usage, prompt, completion           sql.NullInt64
		cacheRead, cacheCreation, reasoning sql.NullInt64
		cost                                sql.NullFloat64
	}

	queryBucket := func(model string) bucket {
		var b bucket
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID)}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT usage, prompt_tokens, completion_tokens,
				       cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost_usd
				FROM api.get_usage_by_dimension(CURRENT_DATE - 1, CURRENT_DATE + 1, $1, NULL, NULL)
				WHERE model_name = $2`, keyID, model).Scan(
				&b.usage, &b.prompt, &b.completion,
				&b.cacheRead, &b.cacheCreation, &b.reasoning, &b.cost)
		})
		if err != nil {
			t.Fatalf("failed to query bucket %s: %v", model, err)
		}
		return b
	}

	i64 := func(v int64) *int64 { return &v }
	f64 := func(v float64) *float64 { return &v }

	record("m-full", 100, 50, i64(40), i64(10), i64(20), f64(0.25))
	record("m-full", 200, 30, i64(60), nil, i64(5), f64(0.5))
	record("m-full", 10, 10, nil, nil, nil, nil)
	record("m-none", 7, 3, nil, nil, nil, nil)

	if n := aggregate(); n < 4 {
		t.Fatalf("expected at least 4 aggregated records, got %d", n)
	}

	t.Run("breakdown is summed into the daily bucket", func(t *testing.T) {
		b := queryBucket("m-full")
		assertInt(t, "usage", b.usage, 400)
		assertInt(t, "prompt", b.prompt, 310)
		assertInt(t, "completion", b.completion, 90)
		assertInt(t, "cache_read", b.cacheRead, 100)
		assertInt(t, "cache_creation", b.cacheCreation, 10)
		assertInt(t, "reasoning", b.reasoning, 25)
		if !b.cost.Valid || b.cost.Float64 != 0.75 {
			t.Errorf("cost_usd: expected 0.75, got %+v", b.cost)
		}
	})

	t.Run("breakdown never reported stays NULL", func(t *testing.T) {
		b := queryBucket("m-none")
		assertInt(t, "usage", b.usage, 10)
		if b.cacheRead.Valid || b.cacheCreation.Valid || b.reasoning.Valid || b.cost.Valid {
			t.Errorf("expected NULL breakdown, got %+v", b)
		}
	})

	t.Run("re-running aggregation does not double count", func(t *testing.T) {
		aggregate()
		b := queryBucket("m-full")
		assertInt(t, "usage", b.usage, 400)
		assertInt(t, "cache_read", b.cacheRead, 100)
	})

	t.Run("concurrent aggregation runs count each record once", func(t *testing.T) {
		record("m-full", 1, 1, i64(1), nil, nil, nil)

		tx1, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx1: %v", err)
		}
		defer func() { _ = tx1.Rollback() }()

		var first int
		if err := tx1.QueryRowContext(ctx,
			"SELECT api.aggregate_usage_records(now() + interval '1 second')").Scan(&first); err != nil {
			t.Fatalf("tx1 aggregate: %v", err)
		}

		// The second run must wait for the first to commit instead of reading
		// the not-yet-aggregated record alongside it.
		second := make(chan int, 1)
		secondErr := make(chan error, 1)
		go func() {
			var n int
			if err := db.QueryRowContext(ctx,
				"SELECT api.aggregate_usage_records(now() + interval '1 second')").Scan(&n); err != nil {
				secondErr <- err
				return
			}
			second <- n
		}()

		select {
		case n := <-second:
			t.Fatalf("second run finished (%d records) while the first still held the lock", n)
		case err := <-secondErr:
			t.Fatalf("second run failed: %v", err)
		case <-time.After(500 * time.Millisecond):
		}

		if err := tx1.Commit(); err != nil {
			t.Fatalf("commit tx1: %v", err)
		}

		select {
		case <-second:
		case err := <-secondErr:
			t.Fatalf("second run failed: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("second run did not finish after the first committed")
		}

		b := queryBucket("m-full")
		assertInt(t, "usage", b.usage, 402)
		assertInt(t, "cache_read", b.cacheRead, 101)
	})

	t.Run("cleanup never deletes unaggregated records", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `
			INSERT INTO api.api_usage_records (api_key_id, request_id, usage_amount, created_at, is_aggregated)
			VALUES ($1, $2, 1, now() - interval '1 day', false),
			       ($1, $3, 1, now() - interval '1 day', true)`,
			keyID, "neu-784-pending-"+keyID, "neu-784-done-"+keyID)
		if err != nil {
			t.Fatalf("failed to insert old records: %v", err)
		}

		if _, err := db.ExecContext(ctx,
			"SELECT api.cleanup_aggregated_records('1 hour'::interval, 100000)"); err != nil {
			t.Fatalf("failed to cleanup: %v", err)
		}

		var pending, done int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE request_id = $1),
			       count(*) FILTER (WHERE request_id = $2)
			FROM api.api_usage_records`,
			"neu-784-pending-"+keyID, "neu-784-done-"+keyID).Scan(&pending, &done); err != nil {
			t.Fatalf("failed to count records: %v", err)
		}

		if pending != 1 {
			t.Errorf("unaggregated record was deleted")
		}
		if done != 0 {
			t.Errorf("aggregated record past retention was not deleted")
		}
	})
}

// TestUsageBreakdownLegacyBucket checks that a daily bucket aggregated before
// migration 095 (no breakdown keys) still reads, with NULL breakdown columns.
func TestUsageBreakdownLegacyBucket(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "neu784legacy", "neu784legacy@example.com", "testpassword")

	var keyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'neu-784-legacy-ws',
				p_name := 'neu-784-legacy-key',
				p_quota := 1000
			)`).Scan(&keyID)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", keyID)
	})

	_, err = db.ExecContext(ctx, `
		INSERT INTO api.api_daily_usage (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'ApiDailyUsage',
			ROW('neu-784-legacy-du', NULL, 'neu-784-legacy-ws', NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, CURRENT_DATE, 30, '{"legacy-ep": 30}'::jsonb,
			    '{"endpoint|legacy-ep|legacy-model": {"total": 30, "prompt": 20, "completion": 10}}'::jsonb
			)::api.api_daily_usage_spec,
			ROW(now())::api.api_daily_usage_status
		)`, keyID)
	if err != nil {
		t.Fatalf("failed to insert legacy daily usage: %v", err)
	}

	var (
		usage, prompt, completion           sql.NullInt64
		cacheRead, cacheCreation, reasoning sql.NullInt64
		cost                                sql.NullFloat64
	)
	err = execWithContext(t, db, []SetContextFunc{setUserContext(user.ID)}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT usage, prompt_tokens, completion_tokens,
			       cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost_usd
			FROM api.get_usage_by_dimension(CURRENT_DATE - 1, CURRENT_DATE + 1, $1, NULL, NULL)`,
			keyID).Scan(&usage, &prompt, &completion, &cacheRead, &cacheCreation, &reasoning, &cost)
	})
	if err != nil {
		t.Fatalf("failed to query legacy bucket: %v", err)
	}

	assertInt(t, "usage", usage, 30)
	assertInt(t, "prompt", prompt, 20)
	assertInt(t, "completion", completion, 10)
	if cacheRead.Valid || cacheCreation.Valid || reasoning.Valid || cost.Valid {
		t.Errorf("expected NULL breakdown for a legacy bucket, got %v %v %v %v", cacheRead, cacheCreation, reasoning, cost)
	}
}

func assertInt(t *testing.T, name string, got sql.NullInt64, want int64) {
	t.Helper()

	if !got.Valid || got.Int64 != want {
		t.Errorf("%s: expected %d, got %+v", name, want, got)
	}
}
