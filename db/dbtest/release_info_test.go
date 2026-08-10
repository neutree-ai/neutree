package dbtest

import (
	"context"
	"os"
	"testing"
)

func TestReleaseInfoHasMinimalGlobalSchema(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	serviceTx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin service role transaction: %v", err)
	}
	defer func() {
		_ = serviceTx.Rollback()
	}()

	if _, err = serviceTx.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("set service_role: %v", err)
	}

	const releaseName = "v1.2.0"
	if _, err = serviceTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('["v1.1","v1.2"]'::jsonb)::api.release_info_spec
		)
	`, releaseName); err != nil {
		t.Fatalf("insert release info as service_role: %v", err)
	}

	var compatibleBaselinesJSON []byte
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT (spec).compatible_cluster_baselines
		FROM api.release_infos
		WHERE (metadata).name = $1
	`, releaseName).Scan(&compatibleBaselinesJSON); err != nil {
		t.Fatalf("read release info as service_role: %v", err)
	}
	if string(compatibleBaselinesJSON) != `["v1.1", "v1.2"]` && string(compatibleBaselinesJSON) != `["v1.1","v1.2"]` {
		t.Fatalf("expected persisted compatible cluster baselines, got %s", compatibleBaselinesJSON)
	}

	userTx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin api user transaction: %v", err)
	}
	defer func() {
		_ = userTx.Rollback()
	}()

	if _, err = userTx.ExecContext(ctx, "SET LOCAL ROLE api_user"); err != nil {
		t.Fatalf("set api_user: %v", err)
	}

	var visibleCount int
	if err = userTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM api.release_infos").Scan(&visibleCount); err != nil {
		t.Fatalf("list release infos as api_user: %v", err)
	}
	if visibleCount != 0 {
		t.Fatalf("expected api_user to see no internal release infos, got %d", visibleCount)
	}

	_, err = userTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.1', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('[]'::jsonb)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected api_user insert into release_infos to be blocked")
	}
}

func TestLegacyReleaseStateSchemaIsRemoved(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin service role transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("set service_role: %v", err)
	}

	for _, composite := range []struct {
		name   string
		fields []string
	}{
		{name: "api.release_info_spec", fields: []string{"channel", "build_identity", "cluster_versions"}},
		{name: "api.cluster_status", fields: []string{"release_info", "release_compatibility"}},
		{name: "api.static_node_cluster_spec", fields: []string{"components"}},
	} {
		for _, field := range composite.fields {
			var exists bool
			if err = tx.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM pg_attribute
					WHERE attrelid = to_regclass($1)
						AND attname = $2
						AND attnum > 0
						AND NOT attisdropped
				)
			`, composite.name, field).Scan(&exists); err != nil {
				t.Fatalf("check legacy %s.%s: %v", composite.name, field, err)
			}
			if exists {
				t.Fatalf("legacy %s.%s must be removed", composite.name, field)
			}
		}
	}

	var snapshotsExist bool
	if err = tx.QueryRowContext(ctx, `
		SELECT to_regclass('api.cluster_upgrade_snapshots') IS NOT NULL
	`).Scan(&snapshotsExist); err != nil {
		t.Fatalf("check legacy upgrade snapshots table: %v", err)
	}
	if snapshotsExist {
		t.Fatal("legacy cluster upgrade snapshots table must be removed")
	}
}

func TestReleaseInfoMigrationRoundTripCreatesOnlyFinalSchema(t *testing.T) {
	upMigration, err := os.ReadFile("../migrations/084_release_info_cluster_profiles.up.sql")
	if err != nil {
		t.Fatalf("read forward migration: %v", err)
	}

	downMigration, err := os.ReadFile("../migrations/084_release_info_cluster_profiles.down.sql")
	if err != nil {
		t.Fatalf("read rollback migration: %v", err)
	}

	adminDB := GetTestDB(t)
	ctx := context.Background()
	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin rollback transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err = tx.ExecContext(ctx, string(downMigration)); err != nil {
		t.Fatalf("restore pre-release-profile schema: %v", err)
	}
	if _, err = tx.ExecContext(ctx, string(upMigration)); err != nil {
		t.Fatalf("apply final release-profile migration: %v", err)
	}

	var engineCapabilitiesExist bool
	if err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_attribute
			WHERE attrelid = 'api.engine_version'::regclass
				AND attname = 'capabilities'
				AND attnum > 0
				AND NOT attisdropped
		)
	`).Scan(&engineCapabilitiesExist); err != nil {
		t.Fatalf("check engine_version.capabilities: %v", err)
	}
	if !engineCapabilitiesExist {
		t.Fatal("mainline engine capabilities must remain after release-profile migration")
	}

	const releaseName = "v1.2.0"
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('["v1.1","v1.2"]'::jsonb)::api.release_info_spec
		)
	`, releaseName); err != nil {
		t.Fatalf("insert final release info: %v", err)
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW('v1.2.0', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			'{"components":{"ray_runtime":{"image":"neutree/neutree-serve","tag":"v1.1.1"}}}'::jsonb
		)
	`); err != nil {
		t.Fatalf("insert final cluster profile: %v", err)
	}

	if _, err = tx.ExecContext(ctx, string(downMigration)); err != nil {
		t.Fatalf("roll back final release-profile migration: %v", err)
	}

	for _, tableName := range []string{"api.release_infos", "api.cluster_profiles"} {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, tableName).Scan(&exists); err != nil {
			t.Fatalf("check rollback table %s: %v", tableName, err)
		}
		if exists {
			t.Fatalf("rollback must remove %s", tableName)
		}
	}
}
