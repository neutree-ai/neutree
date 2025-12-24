package dbtest

import (
	"context"
	"strings"
	"testing"
)

func TestClusterDeletionProtection(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("prevent cluster deletion when endpoint exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		workspace := "default"

		// Create image registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.image_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ImageRegistry',
				ROW('https://registry.example.com', 'my-repo', '{}'::json)::api.image_registry_spec,
				ROW('test-image-registry', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create image registry: %v", err)
		}

		// Create cluster
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('ssh', '{"ssh_config": {"provider":{"head_ip":"192.168.1.1"},"auth":{"ssh_user":"test"}}}'::jsonb, 'test-image-registry', '')::api.cluster_spec,
				ROW('test-cluster', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create cluster: %v", err)
		}

		// Create model registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('bentoml', 'file://localhost/tmp', NULL)::api.model_registry_spec,
				ROW('test-model-registry', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create model registry: %v", err)
		}

		// Create endpoint
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.endpoints (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Endpoint',
				ROW('test-cluster',
					ROW('test-model-registry', 'test-model', NULL, 'v1', NULL)::api.model_spec,
					NULL::api.endpoint_engine_spec,
					NULL::api.resource_spec,
					ROW(1)::api.replica_spec,
					'{}'::jsonb,
					'{}'::jsonb,
					'{}'::jsonb
				)::api.endpoint_spec,
				ROW('test-endpoint', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create endpoint: %v", err)
		}

		// Try to delete cluster
		_, err = tx.ExecContext(ctx, `
			UPDATE api.clusters
			SET metadata = ROW('test-cluster', NULL, $1, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE (metadata).name = 'test-cluster' AND (metadata).workspace = $1
		`, workspace)

		if err == nil {
			t.Fatal("expected error when deleting cluster with endpoint, but deletion succeeded")
		}

		if !strings.Contains(err.Error(), "cannot delete cluster") {
			t.Fatalf("expected 'cannot delete cluster' error, got: %v", err)
		}

		if !strings.Contains(err.Error(), "endpoint(s) still reference this cluster") {
			t.Fatalf("expected endpoint reference message, got: %v", err)
		}

		if !strings.Contains(err.Error(), "1 endpoint(s)") {
			t.Fatalf("expected '1 endpoint(s)' in error message, got: %v", err)
		}

		t.Logf("successfully prevented cluster deletion: %v", err)
	})

	t.Run("allow cluster deletion when no endpoint exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		workspace := "default"

		// Create image registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.image_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ImageRegistry',
				ROW('https://registry.example.com', 'my-repo', '{}'::json)::api.image_registry_spec,
				ROW('test-image-registry-2', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create image registry: %v", err)
		}

		// Create cluster without endpoint
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('ssh', '{"ssh_config": {"provider":{"head_ip":"192.168.1.1"},"auth":{"ssh_user":"test"}}}'::jsonb, 'test-image-registry-2', '')::api.cluster_spec,
				ROW('test-cluster-2', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create cluster: %v", err)
		}

		// Delete cluster should succeed
		_, err = tx.ExecContext(ctx, `
			UPDATE api.clusters
			SET metadata = ROW('test-cluster-2', NULL, $1, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE (metadata).name = 'test-cluster-2' AND (metadata).workspace = $1
		`, workspace)

		if err != nil {
			t.Fatalf("expected cluster deletion to succeed, got error: %v", err)
		}

		t.Log("successfully allowed cluster deletion when no endpoint exists")
	})
}

func TestImageRegistryDeletionProtection(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("prevent image_registry deletion when cluster exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		workspace := "default"

		// Create image registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.image_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ImageRegistry',
				ROW('https://registry.example.com', 'my-repo', '{}'::json)::api.image_registry_spec,
				ROW('test-image-registry-3', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create image registry: %v", err)
		}

		// Create cluster
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('ssh', '{"ssh_config": {"provider":{"head_ip":"192.168.1.1"},"auth":{"ssh_user":"test"}}}'::jsonb, 'test-image-registry-3', '')::api.cluster_spec,
				ROW('test-cluster-3', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create cluster: %v", err)
		}

		// Try to delete image registry
		_, err = tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata = ROW('test-image-registry-3', NULL, $1, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE (metadata).name = 'test-image-registry-3' AND (metadata).workspace = $1
		`, workspace)

		if err == nil {
			t.Fatal("expected error when deleting image_registry with cluster, but deletion succeeded")
		}

		if !strings.Contains(err.Error(), "cannot delete image_registry") {
			t.Fatalf("expected 'cannot delete image_registry' error, got: %v", err)
		}

		if !strings.Contains(err.Error(), "cluster(s) still reference this image registry") {
			t.Fatalf("expected cluster reference message, got: %v", err)
		}

		if !strings.Contains(err.Error(), "1 cluster(s)") {
			t.Fatalf("expected '1 cluster(s)' in error message, got: %v", err)
		}

		t.Logf("successfully prevented image_registry deletion: %v", err)
	})
}

func TestModelRegistryDeletionProtection(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("prevent model_registry deletion when endpoint exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		workspace := "default"

		// Create image registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.image_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ImageRegistry',
				ROW('https://registry.example.com', 'my-repo', '{}'::json)::api.image_registry_spec,
				ROW('test-image-registry-4', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create image registry: %v", err)
		}

		// Create cluster
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('ssh', '{"ssh_config": {"provider":{"head_ip":"192.168.1.1"},"auth":{"ssh_user":"test"}}}'::jsonb, 'test-image-registry-4', '')::api.cluster_spec,
				ROW('test-cluster-4', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create cluster: %v", err)
		}

		// Create model registry
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('bentoml', 'file://localhost/tmp', NULL)::api.model_registry_spec,
				ROW('test-model-registry-2', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create model registry: %v", err)
		}

		// Create endpoint
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.endpoints (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Endpoint',
				ROW('test-cluster-4',
					ROW('test-model-registry-2', 'test-model', NULL, 'v1', NULL)::api.model_spec,
					NULL::api.endpoint_engine_spec,
					NULL::api.resource_spec,
					ROW(1)::api.replica_spec,
					'{}'::jsonb,
					'{}'::jsonb,
					'{}'::jsonb
				)::api.endpoint_spec,
				ROW('test-endpoint-2', NULL, $1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`, workspace)
		if err != nil {
			t.Fatalf("failed to create endpoint: %v", err)
		}

		// Try to delete model registry
		_, err = tx.ExecContext(ctx, `
			UPDATE api.model_registries
			SET metadata = ROW('test-model-registry-2', NULL, $1, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE (metadata).name = 'test-model-registry-2' AND (metadata).workspace = $1
		`, workspace)

		if err == nil {
			t.Fatal("expected error when deleting model_registry with endpoint, but deletion succeeded")
		}

		if !strings.Contains(err.Error(), "cannot delete model_registry") {
			t.Fatalf("expected 'cannot delete model_registry' error, got: %v", err)
		}

		if !strings.Contains(err.Error(), "endpoint(s) still reference this model registry") {
			t.Fatalf("expected endpoint reference message, got: %v", err)
		}

		if !strings.Contains(err.Error(), "1 endpoint(s)") {
			t.Fatalf("expected '1 endpoint(s)' in error message, got: %v", err)
		}

		t.Logf("successfully prevented model_registry deletion: %v", err)
	})
}

func TestRoleDeletionProtection(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("prevent role deletion when role_assignment exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Create user
		user := CreateTestUser(t, "test-role-user", "test-role@example.com", "password123")

		// Create role
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.roles (api_version, kind, metadata, spec)
			VALUES (
				'v1',
				'Role',
				ROW('test-role-2', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW(NULL, ARRAY['endpoint:read']::api.permission_action[])::api.role_spec
			)
		`)
		if err != nil {
			t.Fatalf("failed to create role: %v", err)
		}

		// Create global role_assignment
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES (
				'v1',
				'RoleAssignment',
				ROW('test-assignment-2', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, NULL, TRUE, 'test-role-2')::api.role_assignment_spec
			)
		`, user.ID)
		if err != nil {
			t.Fatalf("failed to create role assignment: %v", err)
		}

		// Try to delete role
		_, err = tx.ExecContext(ctx, `
			UPDATE api.roles
			SET metadata = ROW('test-role-2', NULL, NULL, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE (metadata).name = 'test-role-2'
		`)

		if err == nil {
			t.Fatal("expected error when deleting role with role_assignment, but deletion succeeded")
		}

		if !strings.Contains(err.Error(), "cannot delete role") {
			t.Fatalf("expected 'cannot delete role' error, got: %v", err)
		}

		if !strings.Contains(err.Error(), "role assignment(s) still reference this role") {
			t.Fatalf("expected role assignment reference message, got: %v", err)
		}

		if !strings.Contains(err.Error(), "1 role assignment(s)") {
			t.Fatalf("expected '1 role assignment(s)' in error message, got: %v", err)
		}

		t.Logf("successfully prevented role deletion: %v", err)
	})
}

func TestUserProfileDeletionProtection(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("prevent user_profile deletion when role_assignment exists", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Create user
		user := CreateTestUser(t, "test-profile-user", "test-profile@example.com", "password123")

		// Create role
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.roles (api_version, kind, metadata, spec)
			VALUES (
				'v1',
				'Role',
				ROW('test-role-3', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW(NULL, ARRAY['endpoint:read']::api.permission_action[])::api.role_spec
			)
		`)
		if err != nil {
			t.Fatalf("failed to create role: %v", err)
		}

		// Create global role_assignment
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES (
				'v1',
				'RoleAssignment',
				ROW('test-assignment-3', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, NULL, TRUE, 'test-role-3')::api.role_assignment_spec
			)
		`, user.ID)
		if err != nil {
			t.Fatalf("failed to create role assignment: %v", err)
		}

		// Try to delete user_profile
		_, err = tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW((metadata).name, NULL, (metadata).workspace, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE id = $1
		`, user.ID)

		if err == nil {
			t.Fatal("expected error when deleting user_profile with role_assignment, but deletion succeeded")
		}

		if !strings.Contains(err.Error(), "cannot delete user_profile") {
			t.Fatalf("expected 'cannot delete user_profile' error, got: %v", err)
		}

		if !strings.Contains(err.Error(), "role assignment(s) still reference this user") {
			t.Fatalf("expected role assignment reference message, got: %v", err)
		}

		if !strings.Contains(err.Error(), "1 role assignment(s)") {
			t.Fatalf("expected '1 role assignment(s)' in error message, got: %v", err)
		}

		t.Logf("successfully prevented user_profile deletion: %v", err)
	})
}
