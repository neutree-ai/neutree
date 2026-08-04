package dbtest

import (
	"context"
	"testing"
)

func TestReleaseInfoIsGlobalInternalState(t *testing.T) {
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
		INSERT INTO api.release_infos (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('Stable', 'v1.2.0', '[{"version":"v1.2.0"}]'::jsonb)::api.release_info_spec,
			ROW('revision-a')::api.release_info_status
		)
	`, releaseName); err != nil {
		t.Fatalf("insert release info as service_role: %v", err)
	}

	if _, err = serviceTx.ExecContext(ctx, `
		UPDATE api.release_infos
		SET status = ROW('revision-b')::api.release_info_status
		WHERE (metadata).name = $1
	`, releaseName); err != nil {
		t.Fatalf("update release info as service_role: %v", err)
	}

	var revision string
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT (status).revision
		FROM api.release_infos
		WHERE (metadata).name = $1
	`, releaseName).Scan(&revision); err != nil {
		t.Fatalf("read release info as service_role: %v", err)
	}
	if revision != "revision-b" {
		t.Fatalf("expected updated release info revision, got %q", revision)
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
		INSERT INTO api.release_infos (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW('v1.2.1', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('Stable', 'v1.2.1', '[]'::jsonb)::api.release_info_spec,
			ROW('revision-c')::api.release_info_status
		)
	`)
	if err == nil {
		t.Fatal("expected api_user insert into release_infos to be blocked")
	}
}

func TestClusterUpgradeSnapshotIsGlobalInternalState(t *testing.T) {
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

	var clusterID int
	if err = serviceTx.QueryRowContext(ctx, `
		INSERT INTO api.clusters (api_version, kind, spec, metadata)
		VALUES (
			'v1',
			'Cluster',
			ROW(
				'kubernetes',
				'{"kubernetes_config":{"kubeconfig":"test-kubeconfig","router":{"access_mode":"LoadBalancer","replicas":1,"resources":{"cpu":"1","memory":"1Gi"}}}}'::jsonb,
				'test-image-registry',
				'v1.1.0',
				NULL::json
			)::api.cluster_spec,
			ROW('snapshot-cluster', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
		)
		RETURNING id
	`).Scan(&clusterID); err != nil {
		t.Fatalf("insert cluster as service_role: %v", err)
	}

	if _, err = serviceTx.ExecContext(ctx, `
		INSERT INTO api.cluster_upgrade_snapshots (
			cluster_id,
			source_cluster_version,
			target_cluster_version,
			target_release_info,
			allowed_edge,
			components
		) VALUES ($1, 'v1.1.0', 'v1.2.0', $2::jsonb, $3::jsonb, $4::jsonb)
	`, clusterID,
		`{"baseline":"v1.2.0","revision":"revision-2"}`,
		`{"from":"v1.1.0","to":"v1.2.0"}`,
		`{"ray_runtime":"neutree/neutree-serve:v1.1.1"}`,
	); err != nil {
		t.Fatalf("insert upgrade snapshot as service_role: %v", err)
	}

	var runtimeImage string
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT components->>'ray_runtime'
		FROM api.cluster_upgrade_snapshots
		WHERE cluster_id = $1
	`, clusterID).Scan(&runtimeImage); err != nil {
		t.Fatalf("read upgrade snapshot as service_role: %v", err)
	}
	if runtimeImage != "neutree/neutree-serve:v1.1.1" {
		t.Fatalf("expected persisted snapshot component, got %q", runtimeImage)
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
	if err = userTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM api.cluster_upgrade_snapshots").Scan(&visibleCount); err != nil {
		t.Fatalf("list upgrade snapshots as api_user: %v", err)
	}
	if visibleCount != 0 {
		t.Fatalf("expected api_user to see no internal upgrade snapshots, got %d", visibleCount)
	}
}
