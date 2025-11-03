-- =====================================================
-- Soft Delete Permission Enhancement
-- =====================================================
-- Ensures that soft delete (setting deletion_timestamp) requires
-- delete permission, not just update permission.

-- =====================================================
-- Helper Functions
-- =====================================================

-- Check if only specified keys have changed between two JSONB objects
CREATE OR REPLACE FUNCTION api.jsonb_only_keys_changed(
    old_jsonb JSONB,
    new_jsonb JSONB,
    allowed_keys TEXT[]
) RETURNS BOOLEAN AS $$
BEGIN
    RETURN (
        old_jsonb - allowed_keys
        IS NOT DISTINCT FROM
        new_jsonb - allowed_keys
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Check if an UPDATE is a soft delete operation
CREATE OR REPLACE FUNCTION api.metadata_is_soft_delete(
    old_metadata api.metadata,
    new_metadata api.metadata
) RETURNS BOOLEAN AS $$
BEGIN
    RETURN (
        old_metadata.deletion_timestamp IS NULL
        AND new_metadata.deletion_timestamp IS NOT NULL
        AND api.jsonb_only_keys_changed(
            to_jsonb(old_metadata),
            to_jsonb(new_metadata),
            ARRAY['deletion_timestamp', 'update_timestamp']
        )
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- =====================================================
-- Update Policies
-- =====================================================

-- Drop existing policies
DROP POLICY IF EXISTS "endpoint update policy" ON api.endpoints;
DROP POLICY IF EXISTS "image_registry update policy" ON api.image_registries;
DROP POLICY IF EXISTS "model_registry update policy" ON api.model_registries;
DROP POLICY IF EXISTS "engine update policy" ON api.engines;
DROP POLICY IF EXISTS "cluster update policy" ON api.clusters;
DROP POLICY IF EXISTS "model_catalog update policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "oem_config update policy" ON api.oem_configs;
DROP POLICY IF EXISTS "workspace update policy" ON api.workspaces;
DROP POLICY IF EXISTS "role update policy" ON api.roles;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;

-- Endpoint
CREATE POLICY "endpoint update policy" ON api.endpoints
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'endpoint:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'endpoint:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Image Registry
CREATE POLICY "image_registry update policy" ON api.image_registries
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'image_registry:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'image_registry:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Model Registry
CREATE POLICY "model_registry update policy" ON api.model_registries
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'model_registry:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'model_registry:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Engine
CREATE POLICY "engine update policy" ON api.engines
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'engine:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'engine:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Cluster
CREATE POLICY "cluster update policy" ON api.clusters
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'cluster:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Model Catalog
CREATE POLICY "model_catalog update policy" ON api.model_catalogs
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'model_catalog:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'model_catalog:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- OEM Config
CREATE POLICY "oem_config update policy" ON api.oem_configs
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'oem_config:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'oem_config:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
        )
    );

-- Workspace
CREATE POLICY "workspace update policy" ON api.workspaces
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'workspace:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'workspace:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
            AND NEW.status IS NOT DISTINCT FROM OLD.status
        )
    );

-- Role
CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'role:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'role:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
        )
    );

-- Role Assignment
CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (
        (
            api.has_permission(auth.uid(), 'role_assignment:update', (metadata).workspace)
            AND NOT api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
        )
        OR
        (
            api.has_permission(auth.uid(), 'role_assignment:delete', (metadata).workspace)
            AND api.metadata_is_soft_delete(OLD.metadata, NEW.metadata)
            AND NEW.spec IS NOT DISTINCT FROM OLD.spec
        )
    );
