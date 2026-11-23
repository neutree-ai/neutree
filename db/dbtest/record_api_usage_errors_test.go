package dbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/lib/pq"
)

// Vector retries a usage delivery only on 5xx, so record_api_usage must answer
// 200 when retrying is useless and 503 when the write may still succeed.
func TestRecordApiUsageErrorStatus(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()
	waitForPostgREST(t)

	user := CreateTestUser(t, "usageerruser", "usageerr@example.com", "testpassword")

	var keyID string
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'usage-err-ws',
				p_name := 'usage-err-key',
				p_quota := 1000000
			)`).Scan(&keyID)
	})
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DROP TRIGGER IF EXISTS usage_err_test_fail ON api.api_usage_records")
		_, _ = db.ExecContext(ctx, "DROP FUNCTION IF EXISTS api.usage_err_test_fail()")
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_usage_records WHERE api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1", keyID)
		_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", keyID)
	})

	record := func(apiKeyID, requestID string) (int, map[string]interface{}) {
		t.Helper()

		body := fmt.Sprintf(`{"p_api_key_id":%q,"p_request_id":%q,"p_usage_amount":7,
			"p_endpoint_name":"usage-err-ep","p_endpoint_type":"endpoint","p_model_name":"m"}`, apiKeyID, requestID)
		status, raw := postgrestRequest(t, http.MethodPost, "/rpc/record_api_usage", body)

		var res map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}

		return status, res
	}

	rows := func(requestID string) int {
		t.Helper()

		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT count(*) FROM api.api_usage_records WHERE request_id = $1", requestID).Scan(&n); err != nil {
			t.Fatalf("count records: %v", err)
		}

		return n
	}

	t.Run("success", func(t *testing.T) {
		reqID := "usage-err-ok-" + keyID

		status, res := record(keyID, reqID)
		if status != http.StatusOK || res["success"] != true || res["duplicate"] != nil {
			t.Fatalf("want 200 success, got %d %v", status, res)
		}

		if n := rows(reqID); n != 1 {
			t.Fatalf("want 1 record, got %d", n)
		}
	})

	t.Run("duplicate request id is reported as already recorded", func(t *testing.T) {
		reqID := "usage-err-dup-" + keyID

		if status, res := record(keyID, reqID); status != http.StatusOK || res["success"] != true {
			t.Fatalf("first delivery: got %d %v", status, res)
		}

		status, res := record(keyID, reqID)
		if status != http.StatusOK || res["success"] != true || res["duplicate"] != true {
			t.Fatalf("want 200 success duplicate, got %d %v", status, res)
		}

		if n := rows(reqID); n != 1 {
			t.Fatalf("want 1 record, got %d", n)
		}
	})

	// Vector's usage heartbeat sends the nil key and relies on this answer.
	t.Run("unknown API key is a permanent 200 failure", func(t *testing.T) {
		reqID := "usage-err-nokey-" + keyID

		status, res := record("00000000-0000-0000-0000-000000000000", reqID)
		if status != http.StatusOK || res["success"] != false {
			t.Fatalf("want 200 success:false, got %d %v", status, res)
		}

		if n := rows(reqID); n != 0 {
			t.Fatalf("want 0 records, got %d", n)
		}
	})

	t.Run("missing usage amount is a permanent 200 failure", func(t *testing.T) {
		reqID := "usage-err-null-" + keyID
		body := fmt.Sprintf(`{"p_api_key_id":%q,"p_request_id":%q,"p_usage_amount":null}`, keyID, reqID)

		status, raw := postgrestRequest(t, http.MethodPost, "/rpc/record_api_usage", body)
		if status != http.StatusOK {
			t.Fatalf("want 200, got %d %s", status, raw)
		}

		if n := rows(reqID); n != 0 {
			t.Fatalf("want 0 records, got %d", n)
		}
	})

	t.Run("failed write returns 503 and a retry records it", func(t *testing.T) {
		reqID := "usage-err-fail-" + keyID

		// Stands in for a write lost to failover: the insert fails with a
		// connection-class error.
		if _, err := db.ExecContext(ctx, `
			CREATE FUNCTION api.usage_err_test_fail() RETURNS trigger AS $$
			BEGIN
				IF NEW.request_id LIKE 'usage-err-fail-%' THEN
					RAISE EXCEPTION 'simulated write failure' USING ERRCODE = '08006';
				END IF;
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql`); err != nil {
			t.Fatalf("create failing trigger function: %v", err)
		}

		if _, err := db.ExecContext(ctx, `
			CREATE TRIGGER usage_err_test_fail BEFORE INSERT ON api.api_usage_records
			FOR EACH ROW EXECUTE FUNCTION api.usage_err_test_fail()`); err != nil {
			t.Fatalf("create failing trigger: %v", err)
		}

		status, res := record(keyID, reqID)
		if status != http.StatusServiceUnavailable || res["code"] != "PT503" {
			t.Fatalf("want 503 PT503, got %d %v", status, res)
		}

		if n := rows(reqID); n != 0 {
			t.Fatalf("want 0 records after the failure, got %d", n)
		}

		if _, err := db.ExecContext(ctx, "DROP TRIGGER usage_err_test_fail ON api.api_usage_records"); err != nil {
			t.Fatalf("drop failing trigger: %v", err)
		}

		status, res = record(keyID, reqID)
		if status != http.StatusOK || res["success"] != true || res["duplicate"] != nil {
			t.Fatalf("retry: want 200 success, got %d %v", status, res)
		}

		if n := rows(reqID); n != 1 {
			t.Fatalf("want 1 record after the retry, got %d", n)
		}
	})

	t.Run("read-only transaction raises PT503", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()

		var raw []byte

		err = tx.QueryRowContext(ctx, `
			SELECT api.record_api_usage(p_api_key_id := $1, p_request_id := $2, p_usage_amount := 1)`,
			keyID, "usage-err-ro-"+keyID).Scan(&raw)

		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code != "PT503" || pqErr.Detail != "25006" {
			t.Fatalf("want PT503 with detail 25006, got %v (result %s)", err, raw)
		}
	})
}
