package dbtest

import (
	"context"
	"strings"
	"testing"
)

func TestModelRegistryValidation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("model registry url required - error code 10035", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert model registry without URL
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', NULL, NULL)::api.model_registry_spec,
				ROW('test-registry', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: spec.url is required")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10035"`) {
			t.Fatalf("expected error code 10035, got: %v", err)
		}

		if !strings.Contains(err.Error(), "spec.url is required") {
			t.Fatalf("expected error message 'spec.url is required', got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10035: %v", err)
	})

	t.Run("model registry url empty string - error code 10035", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert model registry with empty URL
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', '', NULL)::api.model_registry_spec,
				ROW('test-registry-empty', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: spec.url is required")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10035"`) {
			t.Fatalf("expected error code 10035, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with empty URL: %v", err)
	})

	t.Run("model registry url valid - success", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Insert model registry with valid URL
		var registryID int
		err = tx.QueryRowContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', 'https://registry.example.com', NULL)::api.model_registry_spec,
				ROW('test-registry-valid', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
			RETURNING id
		`).Scan(&registryID)

		if err != nil {
			t.Fatalf("failed to insert valid model registry: %v", err)
		}

		t.Logf("successfully inserted model registry with ID: %d", registryID)
	})
}
