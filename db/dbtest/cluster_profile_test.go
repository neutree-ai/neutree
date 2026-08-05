package dbtest

import (
	"context"
	"testing"
)

func TestClusterProfileIsGlobalInternalState(t *testing.T) {
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

	const profileName = "v1.2.0-rc.1"
	if _, err = serviceTx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			'{"components":{"ray_runtime":{"image":"neutree/neutree-serve","tag":"v1.2.0-rc.1"}}}'::jsonb
		)
	`, profileName); err != nil {
		t.Fatalf("insert cluster profile as service_role: %v", err)
	}

	var runtimeTag string
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT spec->'components'->'ray_runtime'->>'tag'
		FROM api.cluster_profiles
		WHERE (metadata).name = $1
	`, profileName).Scan(&runtimeTag); err != nil {
		t.Fatalf("read cluster profile as service_role: %v", err)
	}
	if runtimeTag != "v1.2.0-rc.1" {
		t.Fatalf("expected persisted cluster profile component tag, got %q", runtimeTag)
	}

	if err = serviceTx.Commit(); err != nil {
		t.Fatalf("commit service role transaction: %v", err)
	}

	duplicateTx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin duplicate service role transaction: %v", err)
	}
	defer func() {
		_ = duplicateTx.Rollback()
	}()
	if _, err = duplicateTx.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("set duplicate service role: %v", err)
	}
	_, err = duplicateTx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			'{"components":{}}'::jsonb
		)
	`, profileName)
	if err == nil {
		t.Fatal("expected duplicate global cluster profile name to be rejected")
	}

	workspaceTx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin workspace service role transaction: %v", err)
	}
	defer func() {
		_ = workspaceTx.Rollback()
	}()
	if _, err = workspaceTx.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("set workspace service role: %v", err)
	}
	_, err = workspaceTx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW('v1.2.0-rc.2', NULL, 'workspace-a', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			'{"components":{}}'::jsonb
		)
	`)
	if err == nil {
		t.Fatal("expected workspace-scoped cluster profile to be rejected")
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
	if err = userTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM api.cluster_profiles").Scan(&visibleCount); err != nil {
		t.Fatalf("list cluster profiles as api_user: %v", err)
	}
	if visibleCount != 0 {
		t.Fatalf("expected api_user to see no internal cluster profiles, got %d", visibleCount)
	}

	_, err = userTx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW('v1.2.0-rc.3', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			'{"components":{}}'::jsonb
		)
	`)
	if err == nil {
		t.Fatal("expected api_user insert into cluster_profiles to be blocked")
	}
}
