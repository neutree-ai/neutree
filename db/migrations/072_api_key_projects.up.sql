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
    SELECT (metadata).name AS workspace FROM api.workspaces
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

-- Every workspace gets a Default Project so existing clients that omit
-- project_id keep working; the workspace seed (and any later workspace
-- creation) is covered because the trigger fires on workspace INSERT.
CREATE OR REPLACE FUNCTION api.ensure_default_project()
RETURNS TRIGGER
SECURITY DEFINER AS $$
BEGIN
    INSERT INTO api.projects (workspace, name, is_default)
    VALUES ((NEW.metadata).name, 'Default', TRUE)
    ON CONFLICT (workspace, name) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workspaces_ensure_default_project
    AFTER INSERT ON api.workspaces
    FOR EACH ROW EXECUTE FUNCTION api.ensure_default_project();

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

    -- Reject the whole migration when any selected key collides with an
    -- existing key in the target Project, and name every conflicting key so
    -- the UI can show exactly what must be renamed first.
    DECLARE
        v_conflicts TEXT;
    BEGIN
        SELECT string_agg(DISTINCT (existing.metadata).name, ', '
                          ORDER BY (existing.metadata).name)
        INTO v_conflicts
        FROM api.api_keys k
        JOIN api.api_keys existing ON existing.project_id = p_project_id
            AND (existing.metadata).name = (k.metadata).name
            AND existing.id <> k.id
        WHERE k.id = ANY(p_api_key_ids);

        IF v_conflicts IS NOT NULL THEN
            RAISE EXCEPTION 'API key name conflict in target Project: %', v_conflicts;
        END IF;
    END;

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
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
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

CREATE OR REPLACE FUNCTION api.update_project(
    p_project_id UUID,
    p_name TEXT DEFAULT NULL,
    p_description TEXT DEFAULT NULL,
    p_status TEXT DEFAULT NULL
) RETURNS api.projects
SECURITY DEFINER AS $$
DECLARE
    v_project api.projects;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = p_project_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Project not found';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:update', v_project.workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    IF v_project.is_default AND (p_name IS NOT NULL OR p_status IS NOT NULL) THEN
        RAISE EXCEPTION 'Default Project cannot be renamed or disabled';
    END IF;
    IF p_name IS NOT NULL THEN
        IF btrim(p_name) = '' THEN
            RAISE EXCEPTION 'Project name is required';
        END IF;
        IF EXISTS (
            SELECT 1 FROM api.projects
            WHERE workspace = v_project.workspace AND name = btrim(p_name) AND id <> p_project_id
        ) THEN
            RAISE EXCEPTION 'Project name already exists';
        END IF;
    END IF;
    IF p_status IS NOT NULL AND p_status NOT IN ('enabled', 'disabled') THEN
        RAISE EXCEPTION 'Invalid project status';
    END IF;
    UPDATE api.projects
    SET name = COALESCE(btrim(p_name), name),
        description = CASE WHEN p_description IS NOT NULL THEN p_description ELSE description END,
        status = COALESCE(p_status, status)
    WHERE id = p_project_id
    RETURNING * INTO v_project;
    RETURN v_project;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.delete_project(
    p_project_id UUID
) RETURNS VOID
SECURITY DEFINER AS $$
DECLARE
    v_project api.projects;
    v_key_count BIGINT;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = p_project_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Project not found';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:delete', v_project.workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    IF v_project.is_default THEN
        RAISE EXCEPTION 'Default Project cannot be deleted';
    END IF;
    SELECT count(*) INTO v_key_count FROM api.api_keys WHERE project_id = p_project_id;
    IF v_key_count > 0 THEN
        RAISE EXCEPTION 'Project has % API key(s); migrate or delete them first', v_key_count;
    END IF;
    DELETE FROM api.projects WHERE id = p_project_id;
END;
$$ LANGUAGE plpgsql;


-- Project rows with their API key count and current-cycle usage, returned in
-- one batched call (no per-project queries). p_search / p_status keep the
-- payload small when the UI already narrowed the scope; usage follows the same
-- ledger/period rules as get_api_keys_usage_summary.
CREATE OR REPLACE FUNCTION api.group_projects(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_status TEXT DEFAULT NULL
) RETURNS TABLE (
    id UUID,
    name TEXT,
    description TEXT,
    status TEXT,
    is_default BOOLEAN,
    api_key_count BIGINT,
    usage_used BIGINT,
    usage_limit BIGINT,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:read', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    RETURN QUERY
    SELECT
        p.id,
        p.name,
        p.description,
        p.status,
        p.is_default,
        count(k.id)::bigint AS api_key_count,
        COALESCE(sum(u.used), 0)::bigint AS usage_used,
        COALESCE(sum(u.token_limit), 0)::bigint AS usage_limit,
        p.created_at,
        p.updated_at
    FROM api.projects p
    LEFT JOIN api.api_keys k
        ON k.project_id = p.id
       AND (k.metadata).deletion_timestamp IS NULL
    LEFT JOIN LATERAL (
        SELECT
            ((k.spec).limits #>> '{token_quota,limit}')::bigint AS token_limit,
            CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                WHEN 'daily'   THEN CURRENT_DATE
                WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
                WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
                ELSE date_trunc('month', CURRENT_DATE)::date
            END AS period_start
    ) lim ON TRUE
    LEFT JOIN LATERAL (
        SELECT COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used
        FROM api.api_daily_usage d
        WHERE (d.spec).api_key_id = k.id
          AND (d.spec).usage_date >= lim.period_start
          AND (d.spec).usage_date <= CURRENT_DATE
    ) u ON TRUE
    WHERE p.workspace = p_workspace
      AND (p_status IS NULL OR p.status = p_status)
      AND (
          p_search IS NULL
          OR btrim(p_search) = ''
          OR p.name ILIKE '%' || p_search || '%'
          OR COALESCE(p.description, '') ILIKE '%' || p_search || '%'
          OR EXISTS (
              SELECT 1 FROM api.api_keys k2
              WHERE k2.project_id = p.id
                AND (k2.metadata).deletion_timestamp IS NULL
                AND (
                    (k2.metadata).name ILIKE '%' || p_search || '%'
                    OR COALESCE(k2.description, '') ILIKE '%' || p_search || '%'
                    OR (k2.metadata).workspace ILIKE '%' || p_search || '%'
                )
          )
      )
    GROUP BY p.id
    ORDER BY p.is_default DESC, p.created_at ASC, p.name ASC;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE api.projects FORCE ROW LEVEL SECURITY;
