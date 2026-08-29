package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

const completeClusterProfileSpec = `{
  "components": {
    "ssh": {
      "ray_runtime": {"image": "neutree/neutree-serve", "tag": "v1.2.0-rc.1"},
      "node_agent": {"image": "neutree/neutree-node-agent", "tag": "v1.2.0-rc.1"},
      "node_exporter": {"image": "quay.io/prometheus/node-exporter", "tag": "v1.8.2"},
      "vmagent": {"image": "victoriametrics/vmagent", "tag": "v1.115.0"}
    },
    "kubernetes": {
      "kubernetes_runtime": {"image": "neutree/neutree-runtime", "tag": "v1.2.0-rc.1"},
      "router": {"image": "neutree/router", "tag": "v1.2.0-rc.1"},
      "node_agent": {"image": "neutree/neutree-node-agent", "tag": "v1.2.0-rc.1"},
      "node_exporter": {"image": "quay.io/prometheus/node-exporter", "tag": "v1.8.2"},
      "vmagent": {"image": "victoriametrics/vmagent", "tag": "v1.115.0"},
      "kube_state_metrics": {"image": "registry.k8s.io/kube-state-metrics/kube-state-metrics", "tag": "v2.15.0"}
    }
  }
}`

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
	if _, err = insertClusterProfile(ctx, serviceTx, profileName, completeClusterProfileSpec); err != nil {
		t.Fatalf("insert cluster profile as service_role: %v", err)
	}

	var runtimeTag string
	if err = serviceTx.QueryRowContext(ctx, `
		SELECT spec->'components'->'ssh'->'ray_runtime'->>'tag'
		FROM api.cluster_profiles
		WHERE (metadata).name = $1
	`, profileName).Scan(&runtimeTag); err != nil {
		t.Fatalf("read cluster profile as service_role: %v", err)
	}
	if runtimeTag != "v1.2.0-rc.1" {
		t.Fatalf("expected persisted SSH component tag, got %q", runtimeTag)
	}

	if err = serviceTx.Commit(); err != nil {
		t.Fatalf("commit service role transaction: %v", err)
	}

	duplicateTx := beginServiceRoleTx(t, adminDB, ctx, "duplicate")
	defer func() {
		_ = duplicateTx.Rollback()
	}()
	_, err = insertClusterProfile(ctx, duplicateTx, profileName, completeClusterProfileSpec)
	if err == nil {
		t.Fatal("expected duplicate cluster profile version to be rejected")
	}

	workspaceTx := beginServiceRoleTx(t, adminDB, ctx, "workspace")
	defer func() {
		_ = workspaceTx.Rollback()
	}()
	_, err = workspaceTx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW('v1.2.0-rc.4', NULL, 'workspace-a', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			$1::jsonb
		)
	`, completeClusterProfileSpec)
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

	_, err = insertClusterProfile(ctx, userTx, "v1.2.0-rc.5", completeClusterProfileSpec)
	if err == nil {
		t.Fatal("expected api_user insert into cluster_profiles to be blocked")
	}
}

func beginServiceRoleTx(t *testing.T, adminDB *sql.DB, ctx context.Context, name string) *sql.Tx {
	t.Helper()
	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin %s service role transaction: %v", name, err)
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("set %s service role: %v", name, err)
	}
	return tx
}

func insertClusterProfile(ctx context.Context, tx *sql.Tx, name, spec string) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		INSERT INTO api.cluster_profiles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'ClusterProfile',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			$2::jsonb
		)
	`, name, spec)
}
