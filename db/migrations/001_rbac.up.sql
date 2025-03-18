-- ----------------------
-- Shared types for resources
-- ----------------------
CREATE TYPE api.metadata AS (
    name TEXT,
    workspace TEXT, -- null for global resources
    deletion_timestamp TIMESTAMP,
    creation_timestamp TIMESTAMP,
    update_timestamp TIMESTAMP,
    labels json
);

-- ----------------------
-- Shared functions for resources
-- ----------------------
CREATE OR REPLACE FUNCTION update_metadata_update_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW((NEW.metadata).name,(NEW.metadata).workspace,(NEW.metadata).deletion_timestamp,(NEW.metadata).creation_timestamp,CURRENT_TIMESTAMP,(NEW.metadata).labels);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION set_default_metadata_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW((NEW.metadata).name,(NEW.metadata).workspace,(NEW.metadata).deletion_timestamp,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,(NEW.metadata).labels);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ----------------------
-- Shared types for RBAC
-- ----------------------
CREATE TYPE api.permission_action AS ENUM (
    'workspace:read',
    'workspace:create',
    'workspace:update',
    'workspace:delete',
    'role:read',
    'role:create',
    'role:update',
    'role:delete',
    'role_assignment:read',
    'role_assignment:create',
    'role_assignment:update',
    'role_assignment:delete',
    'endpoint:read',
    'endpoint:create',
    'endpoint:update',
    'endpoint:delete',
    'image_registry:read',
    'image_registry:create',
    'image_registry:update',
    'image_registry:delete',
    'model_registry:read',
    'model_registry:create',
    'model_registry:update',
    'model_registry:delete',
    'engine:read',
    'engine:create',
    'engine:update',
    'engine:delete',
    'cluster:read',
    'cluster:create',
    'cluster:update',
    'cluster:delete'
    -- Add other permissions as needed
);

-- ----------------------
-- Shared functions for RBAC
-- ----------------------
CREATE OR REPLACE FUNCTION api.has_permission(
    user_uuid UUID,
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN AS $$
DECLARE
    has_perm BOOLEAN;
BEGIN
    IF workspace IS NULL THEN
        -- check global assignment
        SELECT EXISTS (
            SELECT 1
            FROM api.role_assignments ra
            JOIN api.roles r ON (ra.spec).role = (r.metadata).name
            WHERE (ra.spec).user_id = user_uuid
            AND (ra.spec).global = TRUE
            AND required_permission = ANY((r.spec).permissions)
        ) INTO has_perm;
    ELSE
        -- check global and workspace assignment
        SELECT EXISTS (
            SELECT 1
            FROM api.role_assignments ra
            JOIN api.roles r ON (ra.spec).role = (r.metadata).name
            WHERE (ra.spec).user_id = user_uuid
            AND (
                (ra.spec).global = TRUE 
                OR (ra.spec).workspace = workspace
            )
            AND required_permission = ANY((r.spec).permissions)
        ) INTO has_perm;
    END IF;
    
    RETURN has_perm;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- ----------------------
-- Resource: Workspace
-- ----------------------
CREATE TABLE api.workspaces (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata
);

CREATE TRIGGER update_workspaces_update_timestamp
    BEFORE UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_workspaces_default_timestamp
    BEFORE INSERT ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX workspaces_name_unique_idx ON api.workspaces (((metadata).name));

ALTER TABLE api.workspaces ENABLE ROW LEVEL SECURITY;

CREATE POLICY "workspace read policy" ON api.workspaces
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'workspace:read', (metadata).name)
    );

CREATE POLICY "workspace create policy" ON api.workspaces
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'workspace:create', NULL)
    );

CREATE POLICY "workspace update policy" ON api.workspaces
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'workspace:update', (metadata).name)
    );

CREATE POLICY "workspace delete policy" ON api.workspaces
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'workspace:delete', (metadata).name)
    );

-- ----------------------
-- Resource: Role
-- ----------------------
CREATE TYPE api.role_preset AS ENUM (
    'admin',
    'workspace_user'
);

CREATE TYPE api.role_spec AS (
    preset_key api.role_preset,
    permissions api.permission_action[]
);

CREATE TABLE api.roles (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.role_spec
);

CREATE OR REPLACE FUNCTION api.update_admin_permissions()
RETURNS VOID AS $$
DECLARE
    all_permissions api.permission_action[];
BEGIN
    SELECT array_agg(e.enumlabel::api.permission_action)
    INTO all_permissions
    FROM pg_enum e
    JOIN pg_type t ON e.enumtypid = t.oid
    WHERE t.typname = 'permission_action';

    UPDATE api.roles 
    SET spec = ROW((spec).preset_key, all_permissions)::api.role_spec
    WHERE (metadata).name = 'admin';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_roles_update_timestamp
    BEFORE UPDATE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_roles_default_timestamp
    BEFORE INSERT ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX roles_name_unique_idx ON api.roles (((metadata).name));

ALTER TABLE api.roles ENABLE ROW LEVEL SECURITY;

CREATE POLICY "role read policy" ON api.roles
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'role:read', NULL)
    );

CREATE POLICY "role create policy" ON api.roles
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'role:create', NULL)
    );

CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role:update', NULL)
    );

CREATE POLICY "role delete policy" ON api.roles
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'role:delete', NULL)
    );

-- ----------------------
-- Resource: Role Assignment
-- ----------------------
CREATE TYPE api.role_assignment_spec AS (
    user_id UUID,       -- This remains UUID as it refers to auth.users
    workspace TEXT,
    global BOOLEAN,
    role TEXT
);

CREATE TABLE api.role_assignments (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.role_assignment_spec
);

CREATE TRIGGER update_role_assignments_update_timestamp
    BEFORE UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_role_assignments_default_timestamp
    BEFORE INSERT ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX role_assignment_unique_user_workspace_role 
ON api.role_assignments (((spec).user_id), ((spec).workspace), ((spec).role));

CREATE UNIQUE INDEX role_assignments_name_unique_idx ON api.role_assignments (((metadata).name));

ALTER TABLE api.role_assignments ENABLE ROW LEVEL SECURITY;

CREATE POLICY "role assignment read policy" ON api.role_assignments
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'role_assignment:read', NULL)
    );

CREATE POLICY "role assignment create policy" ON api.role_assignments
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'role_assignment:create', NULL)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:update', NULL)
    );

CREATE POLICY "role assignment delete policy" ON api.role_assignments
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:delete', NULL)
    );
