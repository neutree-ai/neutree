-- =====================================================
-- Rollback Soft Delete Permission Enhancement
-- =====================================================

-- Drop triggers
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_endpoints ON api.endpoints;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_image_registries ON api.image_registries;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_model_registries ON api.model_registries;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_engines ON api.engines;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_clusters ON api.clusters;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_model_catalogs ON api.model_catalogs;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_workspaces ON api.workspaces;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_roles ON api.roles;
DROP TRIGGER IF EXISTS enforce_soft_delete_integrity_role_assignments ON api.role_assignments;

-- Drop helper functions
DROP FUNCTION IF EXISTS api.validate_soft_delete();

-- Restore original policies
DROP POLICY IF EXISTS "endpoint update policy" ON api.endpoints;
DROP POLICY IF EXISTS "image_registry update policy" ON api.image_registries;
DROP POLICY IF EXISTS "model_registry update policy" ON api.model_registries;
DROP POLICY IF EXISTS "engine update policy" ON api.engines;
DROP POLICY IF EXISTS "cluster update policy" ON api.clusters;
DROP POLICY IF EXISTS "model_catalog update policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "workspace update policy" ON api.workspaces;
DROP POLICY IF EXISTS "role update policy" ON api.roles;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;

-- Recreate original simple policies (from 001_rbac.up.sql)
CREATE POLICY "endpoint update policy" ON api.endpoints
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'endpoint:update', (metadata).workspace)
    );

CREATE POLICY "image_registry update policy" ON api.image_registries
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'image_registry:update', (metadata).workspace)
    );

CREATE POLICY "model_registry update policy" ON api.model_registries
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model_registry:update', (metadata).workspace)
    );

CREATE POLICY "engine update policy" ON api.engines
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'engine:update', (metadata).workspace)
    );

CREATE POLICY "cluster update policy" ON api.clusters
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
    );

CREATE POLICY "model_catalog update policy" ON api.model_catalogs
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model_catalog:update', (metadata).workspace)
    );

CREATE POLICY "workspace update policy" ON api.workspaces
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'workspace:update', (metadata).workspace)
    );

CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role:update', (metadata).workspace)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:update', (metadata).workspace)
    );
