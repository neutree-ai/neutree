BEGIN;
DROP POLICY IF EXISTS projects_read_policy ON api.projects;
DROP POLICY IF EXISTS projects_insert_policy ON api.projects;
DROP POLICY IF EXISTS projects_update_policy ON api.projects;
DROP POLICY IF EXISTS projects_delete_policy ON api.projects;
CREATE POLICY projects_read_policy ON api.projects FOR SELECT USING (
    api.has_permission(auth.uid(), 'workspace:read', workspace)
);
CREATE POLICY projects_insert_policy ON api.projects FOR INSERT WITH CHECK (
    api.has_permission(auth.uid(), 'workspace:create', workspace)
);
CREATE POLICY projects_update_policy ON api.projects FOR UPDATE USING (
    api.has_permission(auth.uid(), 'workspace:update', workspace)
);
CREATE POLICY projects_delete_policy ON api.projects FOR DELETE USING (
    NOT is_default AND api.has_permission(auth.uid(), 'workspace:delete', workspace)
);
COMMIT;
