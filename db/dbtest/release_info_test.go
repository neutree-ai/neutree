package dbtest

import (
	"context"
	"database/sql"
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
