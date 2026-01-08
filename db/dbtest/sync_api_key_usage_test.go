package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestSyncApiKeyUsage(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("sync updates api key usage from daily usage", func(t *testing.T) {
		// Create a test user
		user := CreateTestUser(t, "testuser", "test@example.com", "testpassword")

		// Create an API key for the user
		var apiKeyID string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext("test")}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'test-workspace',
				p_name := 'test-key',
				p_quota := 1000
			)
		`).Scan(&apiKeyID)
		})

		if err != nil {
			t.Fatalf("failed to create API key: %v", err)
		}

		// Insert test daily usage data directly
		_, err = db.ExecContext(ctx, `
			INSERT INTO api.api_daily_usage (
				api_version,
				kind,
				metadata,
				spec,
				status
			) VALUES (
				'v1',
				'ApiDailyUsage',
				ROW('daily-usage-1', NULL, 'test-workspace', NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, CURRENT_DATE, 100, '{}'::jsonb)::api.api_daily_usage_spec,
				ROW(now())::api.api_daily_usage_status
			)
		`, apiKeyID)
		if err != nil {
			t.Fatalf("failed to insert daily usage: %v", err)
		}

		// Get initial usage value (should be 0 or NULL)
		var initialUsage sql.NullInt64
		err = db.QueryRowContext(ctx, `
			SELECT (status).usage FROM api.api_keys WHERE id = $1
		`, apiKeyID).Scan(&initialUsage)
		if err != nil {
			t.Fatalf("failed to get initial usage: %v", err)
		}

		// Call sync function
		var syncCount int
		err = db.QueryRowContext(ctx, "SELECT api.sync_api_key_usage()").Scan(&syncCount)
		if err != nil {
			t.Fatalf("failed to call sync_api_key_usage: %v", err)
		}

		// Should have synced 1 key
		if syncCount != 1 {
			t.Errorf("expected sync_count = 1, got %d", syncCount)
		}

		// Verify the API key usage is updated
		var updatedUsage int64
		var lastSyncAt time.Time
		err = db.QueryRowContext(ctx, `
			SELECT (status).usage, (status).last_sync_at
			FROM api.api_keys
			WHERE id = $1
		`, apiKeyID).Scan(&updatedUsage, &lastSyncAt)
		if err != nil {
			t.Fatalf("failed to get updated usage: %v", err)
		}

		if updatedUsage != 100 {
			t.Errorf("expected usage = 100, got %d", updatedUsage)
		}

		if lastSyncAt.IsZero() {
			t.Error("last_sync_at should be set")
		}

		// Cleanup
		_, err = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
		if err != nil {
			t.Fatalf("failed to cleanup API key: %v", err)
		}
	})

	t.Run("sync returns 0 when usage is already synced", func(t *testing.T) {
		// Create a test user
		user := CreateTestUser(t, "testuser2", "test2@example.com", "testpassword")

		// Create an API key
		var apiKeyID string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext("test")}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'test-workspace-2',
				p_name := 'test-key-2',
				p_quota := 1000
			)
		`).Scan(&apiKeyID)
		})

		if err != nil {
			t.Fatalf("failed to create API key: %v", err)
		}

		// Insert daily usage
		_, err = db.ExecContext(ctx, `
			INSERT INTO api.api_daily_usage (
				api_version,
				kind,
				metadata,
				spec,
				status
			) VALUES (
				'v1',
				'ApiDailyUsage',
				ROW('daily-usage-2', NULL, 'test-workspace-2', NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, CURRENT_DATE, 200, '{}'::jsonb)::api.api_daily_usage_spec,
				ROW(now())::api.api_daily_usage_status
			)
		`, apiKeyID)
		if err != nil {
			t.Fatalf("failed to insert daily usage: %v", err)
		}

		// First sync
		var syncCount int
		err = db.QueryRowContext(ctx, "SELECT api.sync_api_key_usage()").Scan(&syncCount)
		if err != nil {
			t.Fatalf("failed to call sync_api_key_usage: %v", err)
		}

		if syncCount != 1 {
			t.Errorf("expected first sync_count = 1, got %d", syncCount)
		}

		// Second sync (should return 0 as everything is already synced)
		err = db.QueryRowContext(ctx, "SELECT api.sync_api_key_usage()").Scan(&syncCount)
		if err != nil {
			t.Fatalf("failed to call sync_api_key_usage second time: %v", err)
		}

		if syncCount != 0 {
			t.Errorf("expected second sync_count = 0, got %d", syncCount)
		}

		// Cleanup
		_, err = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
		if err != nil {
			t.Fatalf("failed to cleanup API key: %v", err)
		}
	})

	t.Run("sync handles multiple daily usage records", func(t *testing.T) {
		// Create a test user
		user := CreateTestUser(t, "testuser3", "test3@example.com", "testpassword")

		// Create an API key
		var apiKeyID string
		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext("test")}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
			SELECT id FROM api.create_api_key(
				p_workspace := 'test-workspace-3',
				p_name := 'test-key-3',
				p_quota := 1000
			)
		`).Scan(&apiKeyID)
		})

		if err != nil {
			t.Fatalf("failed to create API key: %v", err)
		}

		// Insert multiple daily usage records
		for i := 0; i < 3; i++ {
			_, err = db.ExecContext(ctx, `
				INSERT INTO api.api_daily_usage (
					api_version,
					kind,
					metadata,
					spec,
					status
				) VALUES (
					'v1',
					'ApiDailyUsage',
					ROW($2, NULL, 'test-workspace-3', NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
					ROW($1::uuid, CURRENT_DATE - $3::integer, 50, '{}'::jsonb)::api.api_daily_usage_spec,
					ROW(now())::api.api_daily_usage_status
				)
			`, apiKeyID, fmt.Sprintf("daily-usage-%s-%d", apiKeyID, i), i)
			if err != nil {
				t.Fatalf("failed to insert daily usage: %v", err)
			}
		}

		// Sync
		var syncCount int
		err = db.QueryRowContext(ctx, "SELECT api.sync_api_key_usage()").Scan(&syncCount)
		if err != nil {
			t.Fatalf("failed to call sync_api_key_usage: %v", err)
		}

		if syncCount != 1 {
			t.Errorf("expected sync_count = 1, got %d", syncCount)
		}

		// Verify total usage (3 * 50 = 150)
		var totalUsage int64
		err = db.QueryRowContext(ctx, `
			SELECT (status).usage FROM api.api_keys WHERE id = $1
		`, apiKeyID).Scan(&totalUsage)
		if err != nil {
			t.Fatalf("failed to get total usage: %v", err)
		}

		if totalUsage != 150 {
			t.Errorf("expected total usage = 150, got %d", totalUsage)
		}

		// Cleanup
		_, err = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", apiKeyID)
		if err != nil {
			t.Fatalf("failed to cleanup API key: %v", err)
		}
	})
}
