BEGIN;

DROP POLICY IF EXISTS projects_read_policy ON api.projects;
DROP POLICY IF EXISTS projects_insert_policy ON api.projects;
DROP POLICY IF EXISTS projects_update_policy ON api.projects;
DROP POLICY IF EXISTS projects_delete_policy ON api.projects;
CREATE POLICY projects_read_policy ON api.projects FOR SELECT USING (
    api.has_permission(auth.uid(), 'project:read', workspace)
);
CREATE POLICY projects_insert_policy ON api.projects FOR INSERT WITH CHECK (
    api.has_permission(auth.uid(), 'project:create', workspace)
);
CREATE POLICY projects_update_policy ON api.projects FOR UPDATE USING (
    api.has_permission(auth.uid(), 'project:update', workspace)
);
CREATE POLICY projects_delete_policy ON api.projects FOR DELETE USING (
    NOT is_default AND api.has_permission(auth.uid(), 'project:delete', workspace)
);

CREATE OR REPLACE FUNCTION api.validate_api_key_project()
RETURNS TRIGGER AS $$
DECLARE
    v_project api.projects;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = NEW.project_id;
    IF NOT FOUND OR v_project.workspace <> (NEW.metadata).workspace THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace';
    END IF;
    IF TG_OP = 'INSERT' AND v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project is disabled';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.project_id IS DISTINCT FROM OLD.project_id
       AND NOT api.has_permission(auth.uid(), 'project:migrate', v_project.workspace) THEN
        RAISE EXCEPTION 'project migration permission required';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.update_workspace_user_permissions()
RETURNS VOID AS $$
DECLARE
    permissions api.permission_action[];
BEGIN
    permissions := ARRAY[
        'workspace:read', 'endpoint:read', 'endpoint:create', 'endpoint:update', 'endpoint:delete',
        'image_registry:read', 'image_registry:create', 'image_registry:update', 'image_registry:delete',
        'model_registry:read', 'model_registry:create', 'model_registry:update', 'model_registry:delete',
        'model:read', 'model:push', 'model:pull', 'model:delete',
        'engine:read', 'engine:create', 'engine:update', 'engine:delete',
        'cluster:read', 'cluster:create', 'cluster:update', 'cluster:delete',
        'model_catalog:read', 'model_catalog:create', 'model_catalog:update', 'model_catalog:delete',
        'external_endpoint:read', 'external_endpoint:create', 'external_endpoint:update', 'external_endpoint:delete',
        'project:read', 'project:create', 'project:update', 'project:delete', 'project:migrate'
    ]::api.permission_action[];
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_admin_permissions();
SELECT api.update_workspace_user_permissions();
COMMIT;
