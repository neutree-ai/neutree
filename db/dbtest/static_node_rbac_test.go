package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func createStaticNodeResources(t *testing.T, tx *sql.Tx, workspace, clusterName string) {
	t.Helper()
	ctx := context.Background()

	_, err := tx.ExecContext(ctx, `
		INSERT INTO api.static_node_clusters (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'StaticNodeCluster',
			ROW($1, NULL, $2, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			jsonb_build_object('version', 'v1.0.2', 'nodes', jsonb_build_array()),
			jsonb_build_object('phase', 'Ready')
		)
	`, clusterName, workspace)
	if err != nil {
		t.Fatalf("failed to create static node cluster: %v", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO api.static_nodes (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'StaticNode',
			ROW($1, NULL, $2, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			jsonb_build_object('cluster', $3::text, 'ip', $1, 'role', 'head'),
			jsonb_build_object('phase', 'Ready')
		)
	`, "10.0.0.10", workspace, clusterName)
	if err != nil {
		t.Fatalf("failed to create static node: %v", err)
	}
}

func TestStaticNodeRBAC_ReadOnlyForClusterReaders(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	workspace := "static-node-rbac-read"
	clusterName := "static-a"
	createStaticNodeResources(t, tx1, workspace, clusterName)
	userID := createUserWithPermissions(t, tx1, "static-reader", "static-reader@test.local", []string{"cluster:read"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		var clusterCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.static_node_clusters
			WHERE (metadata).workspace = $1
		`, workspace).Scan(&clusterCount); err != nil {
			return err
		}
		if clusterCount != 1 {
			t.Fatalf("expected to read one static node cluster, got %d", clusterCount)
		}

		var nodeCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.static_nodes
			WHERE (metadata).workspace = $1
		`, workspace).Scan(&nodeCount); err != nil {
			return err
		}
		if nodeCount != 1 {
			t.Fatalf("expected to read one static node, got %d", nodeCount)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaticNodeRBAC_DirectUserWritesAreBlocked(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	workspace := "static-node-rbac-write"
	clusterName := "static-write-a"
	createStaticNodeResources(t, tx1, workspace, clusterName)
	userID := createUserWithPermissions(
		t,
		tx1,
		"static-writer",
		"static-writer@test.local",
		[]string{"cluster:create", "cluster:update", "cluster:delete"},
	)
	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	blockedWrites := []struct {
		name string
		sql  string
	}{
		{
			name: "insert cluster",
			sql: `
				INSERT INTO api.static_node_clusters (api_version, kind, metadata, spec, status)
				VALUES (
					'v1',
					'StaticNodeCluster',
					ROW('blocked-static', NULL, 'default', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
					jsonb_build_object('version', 'v1.0.2'),
					jsonb_build_object('phase', 'Provisioning')
				)
			`,
		},
		{
			name: "update cluster",
			sql: `
				UPDATE api.static_node_clusters
				SET status = jsonb_build_object('phase', 'Failed')
				WHERE (metadata).workspace = 'static-node-rbac-write'
			`,
		},
		{
			name: "delete cluster",
			sql: `
				DELETE FROM api.static_node_clusters
				WHERE (metadata).workspace = 'static-node-rbac-write'
			`,
		},
		{
			name: "insert node",
			sql: `
				INSERT INTO api.static_nodes (api_version, kind, metadata, spec, status)
				VALUES (
					'v1',
					'StaticNode',
					ROW('10.0.0.11', NULL, 'default', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
					jsonb_build_object('cluster', 'static-write-a', 'ip', '10.0.0.11', 'role', 'worker'),
					jsonb_build_object('phase', 'Pending')
				)
			`,
		},
		{
			name: "update node",
			sql: `
				UPDATE api.static_nodes
				SET status = jsonb_build_object('phase', 'Failed')
				WHERE (metadata).workspace = 'static-node-rbac-write'
			`,
		},
		{
			name: "delete node",
			sql: `
				DELETE FROM api.static_nodes
				WHERE (metadata).workspace = 'static-node-rbac-write'
			`,
		},
	}

	for _, write := range blockedWrites {
		t.Run(write.name, func(t *testing.T) {
			err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
				result, execErr := tx.ExecContext(ctx, write.sql)
				if execErr != nil {
					return nil
				}

				rowsAffected, rowsErr := result.RowsAffected()
				if rowsErr != nil || rowsAffected == 0 {
					return nil
				}

				return fmt.Errorf("direct static node write affected %d rows", rowsAffected)
			})
			if err != nil {
				t.Fatalf("expected direct static node write to be blocked by RLS: %v", err)
			}
		})
	}
}
