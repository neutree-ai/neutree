package dbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// Vector retries a usage delivery whose response it did not see, so the same
// request id can reach record_api_usage twice. The second call must not add
// usage and must report success, so Vector does not retry it again.
func TestRecordApiUsageIsIdempotent(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "usageidemuser", "usageidem@example.com", "testpassword")

	var keyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'usage-idem-ws',
				p_name := 'usage-idem-key',
				p_quota := 1000000
			)`).Scan(&keyID)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_usage_records WHERE api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", keyID)
	})

	requestID := "usage-idem-" + keyID

	record := func(amount int) map[string]interface{} {
		t.Helper()

		var raw []byte
		if err := db.QueryRowContext(ctx, `
			SELECT api.record_api_usage(
				p_api_key_id := $1,
				p_request_id := $2,
				p_usage_amount := $3,
				p_endpoint_name := 'usage-idem-ep',
				p_endpoint_type := 'endpoint',
				p_model_name := 'm',
				p_prompt_tokens := $3,
				p_completion_tokens := 0
			)`, keyID, requestID, amount).Scan(&raw); err != nil {
			t.Fatalf("record_api_usage raised: %v", err)
		}

		var res map[string]interface{}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}

		return res
	}

	if res := record(10); res["success"] != true {
		t.Fatalf("first delivery not recorded: %v", res)
	}

	if res := record(10); res["success"] != true || res["duplicate"] != true {
		t.Fatalf("redelivery not reported as an already-recorded duplicate: %v", res)
	}

	var rows int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM api.api_usage_records WHERE request_id = $1", requestID).Scan(&rows); err != nil {
		t.Fatalf("count records: %v", err)
	}

	if rows != 1 {
		t.Fatalf("expected 1 usage record, got %d", rows)
	}

	if _, err := db.ExecContext(ctx,
		"SELECT api.aggregate_usage_records(now() + interval '1 second')"); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	// Redelivery after aggregation is still rejected by the request id.
	if res := record(10); res["duplicate"] != true {
		t.Fatalf("redelivery after aggregation recorded again: %v", res)
	}

	var usage sql.NullInt64
	if err := db.QueryRowContext(ctx,
		"SELECT sum((spec).total_usage) FROM api.api_daily_usage WHERE (spec).api_key_id = $1", keyID).Scan(&usage); err != nil {
		t.Fatalf("read daily usage: %v", err)
	}

	if !usage.Valid || usage.Int64 != 10 {
		t.Fatalf("expected daily usage 10, got %v", usage)
	}
}
