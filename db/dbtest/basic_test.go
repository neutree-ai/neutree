package dbtest

import (
	"context"
	"testing"
)

func TestDatabaseSetup(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("schemas exist", func(t *testing.T) {
		var exists bool

		// Check api schema
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = 'api')").Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check api schema: %v", err)
		}
		if !exists {
			t.Error("api schema does not exist")
		}

		// Check auth schema
		err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = 'auth')").Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check auth schema: %v", err)
		}
		if !exists {
			t.Error("auth schema does not exist")
		}
	})

	t.Run("extensions exist", func(t *testing.T) {
		var exists bool

		// Check pgcrypto extension
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto')").Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check pgcrypto extension: %v", err)
		}
		if !exists {
			t.Error("pgcrypto extension does not exist")
		}
	})

	t.Run("basic tables exist", func(t *testing.T) {
		tables := []string{"workspaces", "endpoints", "clusters", "roles"}

		for _, table := range tables {
			var exists bool
			query := "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = 'api' AND table_name = $1)"
			err := db.QueryRowContext(ctx, query, table).Scan(&exists)
			if err != nil {
				t.Fatalf("failed to check table %s: %v", table, err)
			}
			if !exists {
				t.Errorf("table api.%s does not exist", table)
			}
		}
	})
}
