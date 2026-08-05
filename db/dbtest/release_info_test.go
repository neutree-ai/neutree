package dbtest

import (
	"context"
	"encoding/json"
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
			ROW('Stable', 'v1.2.0', '[{"version":"v1.2.0"}]'::jsonb, '["v1.1","v1.2"]'::jsonb)::api.release_info_spec,
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

	var (
		channel                 string
		buildIdentity           string
		clusterVersionsJSON     []byte
		compatibleBaselinesJSON []byte
		revision                string
	)
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT
			(spec).channel,
			(spec).build_identity,
			(spec).cluster_versions,
			(spec).compatible_cluster_baselines,
			(status).revision
		FROM api.release_infos
		WHERE (metadata).name = $1
	`, releaseName).Scan(&channel, &buildIdentity, &clusterVersionsJSON, &compatibleBaselinesJSON, &revision); err != nil {
		t.Fatalf("read release info as service_role: %v", err)
	}
	if channel != "Stable" {
		t.Fatalf("expected persisted release info channel, got %q", channel)
	}
	if buildIdentity != "v1.2.0" {
		t.Fatalf("expected persisted release info build identity, got %q", buildIdentity)
	}
	var clusterVersions []struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal(clusterVersionsJSON, &clusterVersions); err != nil {
		t.Fatalf("decode persisted release info cluster versions: %v", err)
	}
	if len(clusterVersions) != 1 || clusterVersions[0].Version != "v1.2.0" {
		t.Fatalf("expected persisted release info cluster version v1.2.0, got %#v", clusterVersions)
	}
	var compatibleBaselines []string
	if err = json.Unmarshal(compatibleBaselinesJSON, &compatibleBaselines); err != nil {
		t.Fatalf("decode persisted compatible cluster baselines: %v", err)
	}
	if len(compatibleBaselines) != 2 || compatibleBaselines[0] != "v1.1" || compatibleBaselines[1] != "v1.2" {
		t.Fatalf("expected persisted compatible cluster baselines [v1.1 v1.2], got %#v", compatibleBaselines)
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
			ROW('Stable', 'v1.2.1', '[]'::jsonb, '[]'::jsonb)::api.release_info_spec,
			ROW('revision-c')::api.release_info_status
		)
	`)
	if err == nil {
		t.Fatal("expected api_user insert into release_infos to be blocked")
	}
}

func TestReleaseInfoAllowsNilStatusDuringTransition(t *testing.T) {
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

	const releaseName = "v1.2.0-statusless"
	if _, err = serviceTx.ExecContext(ctx, `
		INSERT INTO api.release_infos (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ReleaseInfo',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('Stable', 'v1.2.0', '[{"version":"v1.2.0"}]'::jsonb, '["v1.2"]'::jsonb)::api.release_info_spec
		)
	`, releaseName); err != nil {
		t.Fatalf("insert statusless release info as service_role: %v", err)
	}

	var statusIsNull bool
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT status IS NULL
		FROM api.release_infos
		WHERE (metadata).name = $1
	`, releaseName).Scan(&statusIsNull); err != nil {
		t.Fatalf("read statusless release info as service_role: %v", err)
	}
	if !statusIsNull {
		t.Fatal("expected transition release info status to be NULL")
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
