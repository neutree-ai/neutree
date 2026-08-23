package dbtest

import (
	"context"
	"database/sql"
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
	if _, err = insertReleaseInfo(ctx, serviceTx, releaseName, "v1.2.0", `["v1.1","v1.2"]`); err != nil {
		t.Fatalf("insert release info as service_role: %v", err)
	}

	var defaultClusterVersion string
	var compatibleBaselinesJSON []byte
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT (spec).default_cluster_version, (spec).compatible_cluster_baselines
		FROM api.release_infos
		WHERE (metadata).name = $1
	`, releaseName).Scan(&defaultClusterVersion, &compatibleBaselinesJSON); err != nil {
		t.Fatalf("read release info as service_role: %v", err)
	}
	if defaultClusterVersion != "v1.2.0" {
		t.Fatalf("expected persisted default cluster version, got %q", defaultClusterVersion)
	}
	if string(compatibleBaselinesJSON) != `["v1.1", "v1.2"]` && string(compatibleBaselinesJSON) != `["v1.1","v1.2"]` {
		t.Fatalf("expected persisted compatible cluster baselines, got %s", compatibleBaselinesJSON)
	}
	if err = serviceTx.Commit(); err != nil {
		t.Fatalf("commit service role transaction: %v", err)
	}

	duplicateTx := beginServiceRoleTx(t, adminDB, ctx, "duplicate release info")
	defer func() {
		_ = duplicateTx.Rollback()
	}()
	if _, err = insertReleaseInfo(ctx, duplicateTx, releaseName, "v1.2.0", `["v1.1","v1.2"]`); err == nil {
		t.Fatal("expected duplicate release info name to be rejected")
	}

	missingDefaultTx := beginServiceRoleTx(t, adminDB, ctx, "missing default cluster version")
	defer func() {
		_ = missingDefaultTx.Rollback()
	}()
	if _, err = insertReleaseInfo(ctx, missingDefaultTx, "v1.2.1", "", `[]`); err == nil {
		t.Fatal("expected an empty default cluster version to be rejected")
	}

	blankDefaultTx := beginServiceRoleTx(t, adminDB, ctx, "blank default cluster version")
	defer func() {
		_ = blankDefaultTx.Rollback()
	}()
	_, err = blankDefaultTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.15', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('   ', '[]'::jsonb)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected blank default cluster version to be rejected")
	}

	invalidBaselinesTx := beginServiceRoleTx(t, adminDB, ctx, "invalid compatible baselines")
	defer func() {
		_ = invalidBaselinesTx.Rollback()
	}()
	_, err = invalidBaselinesTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.16', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('v1.2.16', '{"minor":"v1.2"}'::jsonb)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected non-array compatible cluster baselines to be rejected")
	}

	missingBaselinesTx := beginServiceRoleTx(t, adminDB, ctx, "missing compatible baselines")
	defer func() {
		_ = missingBaselinesTx.Rollback()
	}()
	_, err = missingBaselinesTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.17', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('v1.2.17', NULL)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected missing compatible cluster baselines to be rejected")
	}

	workspaceTx := beginServiceRoleTx(t, adminDB, ctx, "workspace-scoped release info")
	defer func() {
		_ = workspaceTx.Rollback()
	}()
	_, err = workspaceTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.2', NULL, 'workspace-a', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('v1.2.2', '[]'::jsonb)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected workspace-scoped release info to be rejected")
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
			ROW('v1.2.1', '[]'::jsonb)::api.release_info_spec
		)
	`)
	if err == nil {
		t.Fatal("expected api_user insert into release_infos to be blocked")
	}
}

func TestReleaseInfoMigrationRoundTripCreatesOnlyFinalSchema(t *testing.T) {
	upMigration, err := os.ReadFile("../migrations/090_release_info_cluster_profiles.up.sql")
	if err != nil {
		t.Fatalf("read forward migration: %v", err)
	}

	downMigration, err := os.ReadFile("../migrations/090_release_info_cluster_profiles.down.sql")
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
			ROW('v1.2.0', '["v1.1","v1.2"]'::jsonb)::api.release_info_spec
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
			$1::jsonb
		)
	`, completeClusterProfileSpec); err != nil {
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

func insertReleaseInfo(ctx context.Context, tx *sql.Tx, name, defaultClusterVersion, compatibleBaselines string) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULLIF($2, ''), $3::jsonb)::api.release_info_spec
		)
	`, name, defaultClusterVersion, compatibleBaselines)
}
