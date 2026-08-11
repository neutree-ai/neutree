-- API keys are grouped by an explicitly owned Project within their Workspace.
-- The backfill runs before the NOT NULL constraint so existing keys keep their
-- identity and all status/spec/usage data unchanged.
CREATE TABLE api.projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT projects_name_workspace_unique UNIQUE (workspace, name),
    CONSTRAINT projects_default_name CHECK (NOT is_default OR name = 'Default')
);

CREATE INDEX projects_workspace_idx ON api.projects (workspace);

INSERT INTO api.projects (workspace, name, is_default)
SELECT DISTINCT workspace, 'Default', TRUE
FROM (
    SELECT (metadata).workspace AS workspace FROM api.workspaces
    UNION
    SELECT (metadata).workspace AS workspace FROM api.api_keys
) workspaces
WHERE workspace IS NOT NULL
ON CONFLICT (workspace, name) DO NOTHING;

ALTER TABLE api.api_keys ADD COLUMN project_id UUID;
ALTER TABLE api.api_keys ADD COLUMN description TEXT;

UPDATE api.api_keys k
SET project_id = p.id
FROM api.projects p
WHERE p.workspace = (k.metadata).workspace AND p.is_default;

ALTER TABLE api.api_keys ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE api.api_keys ADD CONSTRAINT api_keys_project_fk
    FOREIGN KEY (project_id) REFERENCES api.projects(id);

DROP INDEX IF EXISTS api.api_key_name_workspace_unique_idx;
CREATE UNIQUE INDEX api_key_name_project_unique_idx
    ON api.api_keys (project_id, ((metadata).name));
CREATE INDEX api_keys_project_idx ON api.api_keys (project_id);

CREATE TABLE api.api_key_project_history (
    id BIGSERIAL PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api.api_keys(id) ON DELETE CASCADE,
    from_project_id UUID REFERENCES api.projects(id),
    to_project_id UUID NOT NULL REFERENCES api.projects(id),
    moved_by UUID NOT NULL,
    moved_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX api_key_project_history_key_idx
    ON api.api_key_project_history (api_key_id, moved_at DESC);

ALTER TABLE api.projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY "Project read policy" ON api.projects FOR SELECT USING (
    api.has_permission(auth.uid(), 'workspace:read', workspace)
);
CREATE POLICY "Project create policy" ON api.projects FOR INSERT WITH CHECK (
    api.has_permission(auth.uid(), 'workspace:create', workspace)
);
CREATE POLICY "Project update policy" ON api.projects FOR UPDATE USING (
    api.has_permission(auth.uid(), 'workspace:update', workspace)
);
CREATE POLICY "Project delete policy" ON api.projects FOR DELETE USING (
    NOT is_default AND api.has_permission(auth.uid(), 'workspace:delete', workspace)
);

-- Project status is deliberately independent of the key status. Existing keys
-- remain valid for gateway calls when their project is disabled.
CREATE OR REPLACE FUNCTION api.validate_project_write()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.name IS NULL OR btrim(NEW.name) = '' THEN
        RAISE EXCEPTION 'Project name is required';
    END IF;
    IF NEW.is_default AND (NEW.name <> 'Default' OR NEW.status <> 'enabled') THEN
        RAISE EXCEPTION 'Default Project cannot be renamed or disabled';
    END IF;
    NEW.updated_at := CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER projects_updated_at
    BEFORE INSERT OR UPDATE ON api.projects
    FOR EACH ROW EXECUTE FUNCTION api.validate_project_write();

CREATE OR REPLACE FUNCTION api.validate_api_key_project()
RETURNS TRIGGER AS $$
DECLARE v_project api.projects;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = NEW.project_id;
    IF NOT FOUND OR v_project.workspace <> (NEW.metadata).workspace THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace';
    END IF;
    IF TG_OP = 'INSERT' AND v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project is disabled';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER api_keys_project_validation
    BEFORE INSERT OR UPDATE OF project_id, metadata ON api.api_keys
    FOR EACH ROW EXECUTE FUNCTION api.validate_api_key_project();

CREATE OR REPLACE FUNCTION api.migrate_api_keys(
    p_api_key_ids UUID[],
    p_project_id UUID
) RETURNS SETOF api.api_keys
SECURITY DEFINER AS $$
DECLARE
    v_target api.projects;
    v_key api.api_keys;
BEGIN
    SELECT * INTO v_target FROM api.projects WHERE id = p_project_id;
    IF NOT FOUND OR v_target.status <> 'enabled' THEN
        RAISE EXCEPTION 'Migration target Project is missing or disabled';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:update', v_target.workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM api.api_keys k
        JOIN api.api_keys existing ON existing.project_id = p_project_id
            AND (existing.metadata).name = (k.metadata).name
            AND existing.id <> k.id
        WHERE k.id = ANY(p_api_key_ids)
    ) THEN
        RAISE EXCEPTION 'API key name already exists in target Project';
    END IF;

    FOR v_key IN
        SELECT k.* FROM api.api_keys k
        WHERE k.id = ANY(p_api_key_ids)
          AND (k.metadata).workspace = v_target.workspace
          AND api.has_permission(auth.uid(), 'workspace:update', (k.metadata).workspace)
    LOOP
        IF v_key.project_id <> p_project_id THEN
            INSERT INTO api.api_key_project_history(api_key_id, from_project_id, to_project_id, moved_by)
            VALUES (v_key.id, v_key.project_id, p_project_id, auth.uid());
            UPDATE api.api_keys SET project_id = p_project_id WHERE id = v_key.id;
        END IF;
        RETURN NEXT (SELECT k FROM api.api_keys k WHERE k.id = v_key.id);
    END LOOP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.create_project(
    p_workspace TEXT,
    p_name TEXT,
    p_description TEXT DEFAULT NULL
) RETURNS api.projects
SECURITY DEFINER AS $$
DECLARE v_project api.projects;
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:create', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM api.workspaces w WHERE (w.metadata).name = p_workspace
                   AND (w.metadata).deletion_timestamp IS NULL) THEN
        RAISE EXCEPTION 'workspace not found';
    END IF;
    INSERT INTO api.projects (workspace, name, description)
    VALUES (p_workspace, btrim(p_name), p_description)
    RETURNING * INTO v_project;
    RETURN v_project;
END;
$$ LANGUAGE plpgsql;

-- Replace the latest create function while retaining defaults for existing
-- clients. A missing project uses the Workspace Default Project.
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB);
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL,
    p_project_id UUID DEFAULT NULL,
    p_description TEXT DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER AS $$
DECLARE
    p_user_id UUID := auth.uid();
    v_project api.projects;
    v_key_id UUID;
    v_key_value TEXT;
    v_quota BIGINT;
    v_result api.api_keys;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;
    IF NOT api.has_permission(p_user_id, 'workspace:create', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    SELECT * INTO v_project FROM api.projects
    WHERE id = COALESCE(p_project_id, (SELECT id FROM api.projects WHERE workspace = p_workspace AND is_default))
      AND workspace = p_workspace;
    IF NOT FOUND OR v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project not found or disabled';
    END IF;
    IF p_display_name IS NULL THEN p_display_name := p_name; END IF;
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota > 0 THEN
        p_limits := jsonb_build_object('token_quota', jsonb_build_object('limit', p_quota, 'period', 'monthly'));
    END IF;
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);
    v_key_id := gen_random_uuid();
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);
    INSERT INTO api.api_keys (id, api_version, kind, metadata, spec, status, user_id, project_id, description)
    VALUES (v_key_id, 'v1', 'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
        ROW(v_quota, p_expires_in, p_limits)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id, v_project.id, p_description)
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- Direct API-key table writes remain workspace-scoped and cannot move a key to
-- another workspace by changing its project_id.
DROP POLICY IF EXISTS "Users can create their own API keys" ON api.api_keys;
CREATE POLICY "Users can create their own API keys" ON api.api_keys FOR INSERT WITH CHECK (
    user_id = auth.uid() AND api.has_permission(auth.uid(), 'workspace:create', (metadata).workspace)
    AND EXISTS (SELECT 1 FROM api.projects p WHERE p.id = project_id
                AND p.workspace = (metadata).workspace AND p.status = 'enabled')
);
DROP POLICY IF EXISTS "Users can update their own API keys" ON api.api_keys;
CREATE POLICY "Users can update their own API keys" ON api.api_keys FOR UPDATE USING (
    api.has_permission(auth.uid(), 'workspace:update', (metadata).workspace)
);
DROP POLICY IF EXISTS "Users can delete their own API keys" ON api.api_keys;
CREATE POLICY "Users can delete their own API keys" ON api.api_keys FOR DELETE USING (
    api.has_permission(auth.uid(), 'workspace:delete', (metadata).workspace)
);

ALTER TABLE api.projects FORCE ROW LEVEL SECURITY;
