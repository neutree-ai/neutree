package dbtest

import (
	"context"
	"strings"
	"testing"
)

func TestKubernetesClusterConfigValidation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("cluster kubeconfig is empty - error code 10021", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with empty kubeconfig
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: kubeconfig is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10021"`) {
			t.Fatalf("expected error code 10021, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10021: %v", err)
	})

	t.Run("cluster router.replicas less than 1 - error code 10027", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with router.replicas < 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes','{"kubeconfig":"xxxx", "router": {"replicas": 0, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.replicas must be at least 1")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10027"`) {
			t.Fatalf("expected error code 10027, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10027: %v", err)
	})

	t.Run("cluster router.replicas is not int - error code 10028", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with router.replicas as string
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": "two", "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.replicas must be an integer")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10028"`) {
			t.Fatalf("expected error code 10027, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10028: %v", err)
	})

	t.Run("cluster router.resources missing - error code 10029", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster without router.resources
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-resources', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.resources is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10029"`) {
			t.Fatalf("expected error code 10029, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10029: %v", err)
	})

	t.Run("cluster router.resources.cpu is missing - error code 10025", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster without router.resources.cpu
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"memory":"1Gi"}, "access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-resources', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.resources is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10025"`) {
			t.Fatalf("expected error code 10025, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10025: %v", err)
	})

	t.Run("cluster router.resources.memory is missing - error code 10026", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster without router.resources.memory
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1"}, "access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-resources', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.resources is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10026"`) {
			t.Fatalf("expected error code 10026, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10026: %v", err)
	})

	t.Run("cluster router.resources.memory invalid - error code 10114", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with router.resources.memory invalid
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1", "memory":"1XXXX"}, "access_mode":"LoadBalancer"}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-resources', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.resources is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10114"`) {
			t.Fatalf("expected error code 10114, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10114: %v", err)
	})

	t.Run("cluster router.access_mode is missing - error code 10023", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster without router.access_mode
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"}}}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-access-mode', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: router.access_mode is required for Kubernetes clusters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10023"`) {
			t.Fatalf("expected error code 10023, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10023: %v", err)
	})

	t.Run("cluster modelcaches is not json array - error code 10201", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with invalid modelcaches type
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}, "model_caches": "invalid_type"}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-modelcache', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: model_caches must be a JSON array")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10201"`) {
			t.Fatalf("expected error code 10201, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10201: %v", err)
	})

	t.Run("cluster modelcaches now only can config one - error code 10202", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with multiple modelcaches
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}, "model_caches": [{"name": "cache1"}, {"name": "cache2"}]}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-modelcache', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: only one model_caches configuration is allowed")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10202"`) {
			t.Fatalf("expected error code 10202, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10202: %v", err)
	})

	t.Run("cluster modelcaches.name is required - error code 10203", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with modelcaches.name missing
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}, "model_caches": [{}]}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-modelcache', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: model_caches.name is required")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10203"`) {
			t.Fatalf("expected error code 10203, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10203: %v", err)
	})

	t.Run("cluster modelcaches.name is 'default' - error code 10204", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with modelcaches.name as 'default'
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}, "model_caches": [{"name": "default"}]}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-modelcache', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: model_caches.name must not be 'default'")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10204"`) {
			t.Fatalf("expected error code 10204, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10204: %v", err)
	})

	t.Run("cluster modelcaches.name with invalid characters - error code 10205", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert cluster with modelcaches.name having invalid characters
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.clusters (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'Cluster',
				ROW('kubernetes', '{"kubeconfig":"xxxx", "router": {"replicas": 2, "resources": {"cpu":"1","memory":"1Gi"},"access_mode":"LoadBalancer"}, "model_caches": [{"name": "Invalid_Name!"}]}'::jsonb, 'test-imageregistry', '')::api.cluster_spec,
				ROW('test-cluster-modelcache', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: model_caches.name contains invalid characters")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10205"`) {
			t.Fatalf("expected error code 10205, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10205: %v", err)
	})

}

func TestModelRegistryValidation(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("model registry url required - error code 10035", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert model registry without URL
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', NULL, NULL)::api.model_registry_spec,
				ROW('test-registry', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: spec.url is required")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10035"`) {
			t.Fatalf("expected error code 10035, got: %v", err)
		}

		if !strings.Contains(err.Error(), "spec.url is required") {
			t.Fatalf("expected error message 'spec.url is required', got: %v", err)
		}

		t.Logf("validation correctly blocked insert with error code 10035: %v", err)
	})

	t.Run("model registry url empty string - error code 10035", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Try to insert model registry with empty URL
		_, err = tx.ExecContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', '', NULL)::api.model_registry_spec,
				ROW('test-registry-empty', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
		`)

		if err == nil {
			t.Fatal("expected validation error: spec.url is required")
		}

		// Check that the error message contains the correct error code
		if !strings.Contains(err.Error(), `"code": "10035"`) {
			t.Fatalf("expected error code 10035, got: %v", err)
		}

		t.Logf("validation correctly blocked insert with empty URL: %v", err)
	})

	t.Run("model registry url valid - success", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer func() {
			_ = tx.Rollback()
		}()

		// Insert model registry with valid URL
		var registryID int
		err = tx.QueryRowContext(ctx, `
			INSERT INTO api.model_registries (api_version, kind, spec, metadata)
			VALUES (
				'v1',
				'ModelRegistry',
				ROW('hugging-face', 'https://registry.example.com', NULL)::api.model_registry_spec,
				ROW('test-registry-valid', NULL, 'test-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			)
			RETURNING id
		`).Scan(&registryID)

		if err != nil {
			t.Fatalf("failed to insert valid model registry: %v", err)
		}

		t.Logf("successfully inserted model registry with ID: %d", registryID)
	})
}
