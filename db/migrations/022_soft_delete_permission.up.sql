-- =====================================================
-- Soft Delete Permission Enhancement
-- =====================================================
-- Ensures that soft delete (setting deletion_timestamp) requires
-- delete permission, not just update permission.
-- Also ensures soft delete operations cannot modify spec or status.

-- =====================================================
-- Helper Function for Soft Delete Validation
-- =====================================================

-- Validates that soft delete operations only set deletion_timestamp
CREATE OR REPLACE FUNCTION api.validate_soft_delete()
RETURNS TRIGGER AS $$
BEGIN
    -- Check if this is a soft delete operation:
    -- deletion_timestamp changes from NULL to NOT NULL
    IF (OLD.metadata).deletion_timestamp IS NULL
       AND (NEW.metadata).deletion_timestamp IS NOT NULL THEN

        -- During soft delete, spec and status must remain unchanged
        -- Convert to jsonb for comparison since spec/status may contain json fields
        IF to_jsonb(NEW.spec) IS DISTINCT FROM to_jsonb(OLD.spec) THEN
            RAISE EXCEPTION 'Cannot modify spec during soft delete operation';
        END IF;

        IF to_jsonb(NEW.status) IS DISTINCT FROM to_jsonb(OLD.status) THEN
            RAISE EXCEPTION 'Cannot modify status during soft delete operation';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- =====================================================
-- Drop existing policies
-- =====================================================

DROP POLICY IF EXISTS "endpoint update policy" ON api.endpoints;
DROP POLICY IF EXISTS "image_registry update policy" ON api.image_registries;
DROP POLICY IF EXISTS "model_registry update policy" ON api.model_registries;
DROP POLICY IF EXISTS "engine update policy" ON api.engines;
DROP POLICY IF EXISTS "cluster update policy" ON api.clusters;
DROP POLICY IF EXISTS "model_catalog update policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "workspace update policy" ON api.workspaces;
DROP POLICY IF EXISTS "role update policy" ON api.roles;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;

-- Endpoint
CREATE POLICY "endpoint update policy" ON api.endpoints
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'endpoint:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'endpoint:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_endpoints
    BEFORE UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Image Registry
CREATE POLICY "image_registry update policy" ON api.image_registries
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'image_registry:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'image_registry:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_image_registries
    BEFORE UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Model Registry
CREATE POLICY "model_registry update policy" ON api.model_registries
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'model_registry:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'model_registry:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_model_registries
    BEFORE UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Engine
CREATE POLICY "engine update policy" ON api.engines
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'engine:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'engine:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_engines
    BEFORE UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Cluster
CREATE POLICY "cluster update policy" ON api.clusters
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'cluster:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_clusters
    BEFORE UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Model Catalog
CREATE POLICY "model_catalog update policy" ON api.model_catalogs
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'model_catalog:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'model_catalog:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_model_catalogs
    BEFORE UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Workspace
CREATE POLICY "workspace update policy" ON api.workspaces
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'workspace:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'workspace:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_workspaces
    BEFORE UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Role
CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'role:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'role:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_roles
    BEFORE UPDATE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Role Assignment
CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'role_assignment:update', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'role_assignment:delete', (metadata).workspace)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE TRIGGER enforce_soft_delete_integrity_role_assignments
    BEFORE UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();
