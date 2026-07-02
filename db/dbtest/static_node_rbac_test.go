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
			ROW('v1.0.2', 'registry.example.com/neutree', jsonb_build_array(), NULL)::api.static_node_cluster_spec,
			ROW('Ready', 0, 0, FALSE, FALSE, 'v1.0.2', NULL, NULL)::api.static_node_cluster_status
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
			ROW($3::text, $1::text, 'head', NULL, NULL, jsonb_build_array())::api.static_node_spec,
			ROW('Ready', NULL, NULL, jsonb_build_array(), NULL, NULL)::api.static_node_status
		)
	`, "10.0.0.10", workspace, clusterName)
	if err != nil {
		t.Fatalf("failed to create static node: %v", err)
	}
}

func createUserWithPresetRole(t *testing.T, tx *sql.Tx, username, email, roleName string) string {
	t.Helper()
	ctx := context.Background()
	user := CreateTestUser(t, username, email, "password123")

	_, err := tx.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, NULL, TRUE, $3)::api.role_assignment_spec
		)
	`, username+"-"+roleName+"-role-assignment", user.ID, roleName)
	if err != nil {
		t.Fatalf("failed to assign preset role %s: %v", roleName, err)
	}

	return user.ID
}

func TestStaticNodeRBAC_UserWithoutStaticReadPermissionIsBlocked(t *testing.T) {
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
		if clusterCount != 0 {
			t.Fatalf("expected static node clusters to be hidden from users without static read permission, got %d", clusterCount)
		}

		var nodeCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.static_nodes
			WHERE (metadata).workspace = $1
		`, workspace).Scan(&nodeCount); err != nil {
			return err
		}
		if nodeCount != 0 {
			t.Fatalf("expected static nodes to be hidden from users without static read permission, got %d", nodeCount)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaticNodeRBAC_AdminRoleCanReadStaticResources(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	workspace := "static-node-rbac-admin-read"
	clusterName := "static-admin-a"
	createStaticNodeResources(t, tx1, workspace, clusterName)
	userID := createUserWithPresetRole(t, tx1, "static-admin-reader", "static-admin-reader@test.local", "admin")

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
			t.Fatalf("expected admin role to read one static node cluster, got %d", clusterCount)
		}

		var nodeCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.static_nodes
			WHERE (metadata).workspace = $1
		`, workspace).Scan(&nodeCount); err != nil {
			return err
		}
		if nodeCount != 1 {
			t.Fatalf("expected admin role to read one static node, got %d", nodeCount)
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
					ROW('v1.0.2', NULL, jsonb_build_array(), NULL)::api.static_node_cluster_spec,
					ROW('Provisioning', 0, 0, FALSE, FALSE, NULL, NULL, NULL)::api.static_node_cluster_status
				)
			`,
		},
		{
			name: "update cluster",
			sql: `
				UPDATE api.static_node_clusters
				SET status = ROW('Failed', 0, 0, FALSE, FALSE, NULL, NULL, NULL)::api.static_node_cluster_status
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
					ROW('static-write-a', '10.0.0.11', 'worker', NULL, NULL, jsonb_build_array())::api.static_node_spec,
					ROW('Pending', NULL, NULL, jsonb_build_array(), NULL, NULL)::api.static_node_status
				)
			`,
		},
		{
			name: "update node",
			sql: `
				UPDATE api.static_nodes
				SET status = ROW('Failed', NULL, NULL, jsonb_build_array(), NULL, NULL)::api.static_node_status
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

func TestStaticNodeRBAC_ServiceRoleCanManageInternalResources(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	workspace := "static-node-service-role"
	clusterName := "static-service-a"
	createStaticNodeResources(t, tx1, workspace, clusterName)

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	tx2, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin service role transaction: %v", err)
	}
	defer func() {
		_ = tx2.Rollback()
	}()

	if _, err = tx2.ExecContext(ctx, "SET LOCAL ROLE service_role"); err != nil {
		t.Fatalf("failed to set service_role: %v", err)
	}

	var clusterCount int
	if err = tx2.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api.static_node_clusters
		WHERE (metadata).workspace = $1
	`, workspace).Scan(&clusterCount); err != nil {
		t.Fatalf("failed to read static node clusters as service_role: %v", err)
	}
	if clusterCount != 1 {
		t.Fatalf("expected service_role to read one static node cluster, got %d", clusterCount)
	}

	var nodeCount int
	if err = tx2.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api.static_nodes
		WHERE (metadata).workspace = $1
	`, workspace).Scan(&nodeCount); err != nil {
		t.Fatalf("failed to read static nodes as service_role: %v", err)
	}
	if nodeCount != 1 {
		t.Fatalf("expected service_role to read one static node, got %d", nodeCount)
	}

	if _, err = tx2.ExecContext(ctx, `
		UPDATE api.static_node_clusters
		SET status = ROW('Provisioning', 1, 0, FALSE, FALSE, 'v1.0.2', NULL, 'warming')::api.static_node_cluster_status
		WHERE (metadata).workspace = $1
	`, workspace); err != nil {
		t.Fatalf("failed to update static node cluster as service_role: %v", err)
	}

	if _, err = tx2.ExecContext(ctx, `
		UPDATE api.static_nodes
		SET status = ROW('Reconciling', NULL, NULL, jsonb_build_array(), NULL, 'warming')::api.static_node_status
		WHERE (metadata).workspace = $1
	`, workspace); err != nil {
		t.Fatalf("failed to update static node as service_role: %v", err)
	}

	insertedCluster := "static-service-created"
	if _, err = tx2.ExecContext(ctx, `
		INSERT INTO api.static_node_clusters (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'StaticNodeCluster',
			ROW($1, NULL, $2, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW('v1.0.2', 'registry.example.com/neutree', jsonb_build_array(), NULL)::api.static_node_cluster_spec,
			ROW('Provisioning', 0, 0, FALSE, FALSE, NULL, NULL, NULL)::api.static_node_cluster_status
		)
	`, insertedCluster, workspace); err != nil {
		t.Fatalf("failed to insert static node cluster as service_role: %v", err)
	}

	if _, err = tx2.ExecContext(ctx, `
		INSERT INTO api.static_nodes (api_version, kind, metadata, spec, status)
		VALUES (
			'v1',
			'StaticNode',
			ROW('10.0.0.11', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::text, '10.0.0.11', 'worker', NULL, NULL, jsonb_build_array())::api.static_node_spec,
			ROW('Pending', NULL, NULL, jsonb_build_array(), NULL, NULL)::api.static_node_status
		)
	`, workspace, insertedCluster); err != nil {
		t.Fatalf("failed to insert static node as service_role: %v", err)
	}

	if _, err = tx2.ExecContext(ctx, `
		DELETE FROM api.static_nodes
		WHERE (metadata).workspace = $1 AND (spec).cluster = $2
	`, workspace, insertedCluster); err != nil {
		t.Fatalf("failed to delete static node as service_role: %v", err)
	}

	if _, err = tx2.ExecContext(ctx, `
		DELETE FROM api.static_node_clusters
		WHERE (metadata).workspace = $1 AND (metadata).name = $2
	`, workspace, insertedCluster); err != nil {
		t.Fatalf("failed to delete static node cluster as service_role: %v", err)
	}
}
