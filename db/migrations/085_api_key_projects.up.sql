-- Group API keys under workspace-scoped projects.
BEGIN;

CREATE TABLE api.api_key_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    workspace TEXT NOT NULL CHECK (length(trim(workspace)) > 0),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    user_id UUID NOT NULL REFERENCES api.user_profiles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, workspace, name)
);

ALTER TABLE api.api_keys
    ADD COLUMN project_id UUID REFERENCES api.api_key_projects(id) ON DELETE RESTRICT,
    ADD COLUMN description TEXT NOT NULL DEFAULT '';

INSERT INTO api.api_key_projects (name, description, workspace, user_id)
SELECT 'Default', 'Migrated API keys', (metadata).workspace, user_id
FROM api.api_keys
GROUP BY (metadata).workspace, user_id;

UPDATE api.api_keys k
SET project_id = p.id
FROM api.api_key_projects p
WHERE p.user_id = k.user_id
  AND p.workspace = (k.metadata).workspace
  AND p.name = 'Default';

ALTER TABLE api.api_keys ALTER COLUMN project_id SET NOT NULL;
DROP INDEX api.api_key_name_workspace_unique_idx;
CREATE UNIQUE INDEX api_key_name_project_unique_idx
    ON api.api_keys (project_id, ((metadata).name));
CREATE INDEX api_keys_project_id_idx ON api.api_keys (project_id);

CREATE TABLE api.api_key_project_history (
    id BIGSERIAL PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api.api_keys(id) ON DELETE CASCADE,
    from_project_id UUID REFERENCES api.api_key_projects(id) ON DELETE SET NULL,
    to_project_id UUID NOT NULL REFERENCES api.api_key_projects(id) ON DELETE RESTRICT,
    moved_by UUID NOT NULL REFERENCES api.user_profiles(id) ON DELETE RESTRICT,
    moved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE api.api_key_projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY api_key_projects_select ON api.api_key_projects FOR SELECT
    USING (user_id = auth.uid());
-- Mutations go through SECURITY DEFINER RPCs below so lifecycle validation
-- cannot be bypassed with direct PostgREST writes.

ALTER TABLE api.api_key_project_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY api_key_project_history_select ON api.api_key_project_history FOR SELECT
    USING (moved_by = auth.uid());

CREATE OR REPLACE FUNCTION api.touch_api_key_project()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
CREATE TRIGGER touch_api_key_project
    BEFORE UPDATE ON api.api_key_projects
    FOR EACH ROW EXECUTE FUNCTION api.touch_api_key_project();

CREATE OR REPLACE FUNCTION api.create_api_key_project(
    p_workspace TEXT, p_name TEXT, p_description TEXT DEFAULT ''
) RETURNS api.api_key_projects
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.api_key_projects;
BEGIN
    IF p_workspace IS NULL OR length(trim(p_workspace)) = 0 THEN
        RAISE EXCEPTION 'workspace is required' USING ERRCODE = '22023';
    END IF;
    IF p_name IS NULL OR length(trim(p_name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;
    INSERT INTO api.api_key_projects (name, description, workspace, user_id)
    VALUES (trim(p_name), COALESCE(p_description, ''), p_workspace, auth.uid())
    RETURNING * INTO v_result;
    RETURN v_result;
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'Project name already exists' USING ERRCODE = '23505';
END;
$$;

CREATE OR REPLACE FUNCTION api.delete_api_key_project(p_project_id UUID)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_count BIGINT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.api_key_projects WHERE id = p_project_id AND user_id = auth.uid()) THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;
    SELECT count(*) INTO v_count FROM api.api_keys WHERE project_id = p_project_id;
    IF v_count > 0 THEN
        RAISE EXCEPTION 'Project has % API keys', v_count USING ERRCODE = '23503', DETAIL = v_count::text;
    END IF;
    DELETE FROM api.api_key_projects WHERE id = p_project_id;
END;
$$;

CREATE OR REPLACE FUNCTION api.update_api_key_project(
    p_project_id UUID, p_name TEXT DEFAULT NULL, p_description TEXT DEFAULT NULL,
    p_enabled BOOLEAN DEFAULT NULL
) RETURNS api.api_key_projects LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.api_key_projects;
BEGIN
    IF p_name IS NOT NULL AND length(trim(p_name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;
    UPDATE api.api_key_projects SET
        name = COALESCE(trim(p_name), name),
        description = COALESCE(p_description, description),
        enabled = COALESCE(p_enabled, enabled)
    WHERE id = p_project_id AND user_id = auth.uid()
    RETURNING * INTO v_result;
    IF NOT FOUND THEN RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501'; END IF;
    RETURN v_result;
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'Project name already exists' USING ERRCODE = '23505';
END;
$$;

CREATE OR REPLACE FUNCTION api.move_api_keys_to_project(p_api_key_ids UUID[], p_project_id UUID)
RETURNS INTEGER LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_target api.api_key_projects; v_conflicts TEXT; v_count INTEGER;
BEGIN
    IF COALESCE(array_length(p_api_key_ids, 1), 0) = 0 THEN
        RAISE EXCEPTION 'at least one API key is required' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO v_target FROM api.api_key_projects
      WHERE id = p_project_id AND user_id = auth.uid();
    IF NOT FOUND THEN RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501'; END IF;
    IF NOT v_target.enabled THEN RAISE EXCEPTION 'target project is disabled' USING ERRCODE = '22023'; END IF;
    IF EXISTS (SELECT 1 FROM unnest(p_api_key_ids) id LEFT JOIN api.api_keys k ON k.id=id
               WHERE k.id IS NULL OR k.user_id <> auth.uid() OR (k.metadata).workspace <> v_target.workspace) THEN
        RAISE EXCEPTION 'API key not found, permission denied, or workspace mismatch' USING ERRCODE = '42501';
    END IF;
    SELECT string_agg((moving.metadata).name, ', ' ORDER BY (moving.metadata).name) INTO v_conflicts
    FROM api.api_keys moving JOIN api.api_keys existing
      ON existing.project_id = p_project_id AND (existing.metadata).name = (moving.metadata).name
     AND existing.id <> moving.id
    WHERE moving.id = ANY(p_api_key_ids);
    IF v_conflicts IS NOT NULL THEN
        RAISE EXCEPTION 'API key name conflicts: %', v_conflicts USING ERRCODE = '23505', DETAIL = v_conflicts;
    END IF;
    INSERT INTO api.api_key_project_history (api_key_id, from_project_id, to_project_id, moved_by)
    SELECT id, project_id, p_project_id, auth.uid() FROM api.api_keys
    WHERE id = ANY(p_api_key_ids) AND project_id <> p_project_id;
    UPDATE api.api_keys SET project_id = p_project_id
    WHERE id = ANY(p_api_key_ids) AND project_id <> p_project_id;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;

CREATE OR REPLACE FUNCTION api.get_api_key_project_groups(
    p_workspace TEXT, p_search TEXT DEFAULT NULL, p_project_enabled BOOLEAN DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL, p_page INTEGER DEFAULT 1, p_page_size INTEGER DEFAULT 20
) RETURNS TABLE (
    project api.api_key_projects, api_keys JSONB, api_key_count BIGINT,
    current_usage BIGINT, total_projects BIGINT
) LANGUAGE sql STABLE SECURITY DEFINER AS $$
    WITH matching_projects AS (
        SELECT p.*,
               NULLIF(trim(p_search), '') IS NULL
                   OR p.name ILIKE '%' || trim(p_search) || '%' AS project_matches
        FROM api.api_key_projects p
        WHERE p.user_id = auth.uid()
          AND (p_workspace IS NULL OR p.workspace = p_workspace)
          AND (p_project_enabled IS NULL OR p.enabled = p_project_enabled)
          AND (
              NULLIF(trim(p_search), '') IS NULL
              OR p.name ILIKE '%' || trim(p_search) || '%'
              OR EXISTS (
                  SELECT 1 FROM api.api_keys k
                  WHERE k.project_id = p.id
                    AND ((k.metadata).name ILIKE '%' || trim(p_search) || '%'
                         OR k.description ILIKE '%' || trim(p_search) || '%'
                         OR (k.metadata).workspace ILIKE '%' || trim(p_search) || '%')
              )
          )
    ), visible_projects AS (
        SELECT p.*, count(*) OVER () AS total_projects
        FROM matching_projects p
        ORDER BY p.created_at, p.id
        OFFSET GREATEST(p_page - 1, 0) * LEAST(GREATEST(p_page_size, 1), 100)
        LIMIT LEAST(GREATEST(p_page_size, 1), 100)
    )
    SELECT ROW(p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
               p.created_at, p.updated_at)::api.api_key_projects,
           COALESCE(jsonb_agg(to_jsonb(k) ORDER BY (k.metadata).creation_timestamp)
                    FILTER (WHERE k.id IS NOT NULL), '[]'::jsonb),
           (SELECT count(*) FROM api.api_keys all_keys WHERE all_keys.project_id = p.id),
           COALESCE((SELECT sum((all_keys.status).usage)
                     FROM api.api_keys all_keys WHERE all_keys.project_id = p.id), 0)::bigint,
           p.total_projects
    FROM visible_projects p
    LEFT JOIN api.api_keys k ON k.project_id = p.id
      AND (p_api_key_disabled IS NULL
           OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false) = p_api_key_disabled)
      AND (p.project_matches
           OR (k.metadata).name ILIKE '%' || trim(p_search) || '%'
           OR k.description ILIKE '%' || trim(p_search) || '%'
           OR (k.metadata).workspace ILIKE '%' || trim(p_search) || '%')
    GROUP BY p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
             p.created_at, p.updated_at, p.project_matches, p.total_projects
    ORDER BY p.created_at, p.id;
$$;

-- Replace the latest create RPC, adding project and description while preserving limits.
DROP FUNCTION api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB);
CREATE FUNCTION api.create_api_key(
    p_workspace TEXT, p_name TEXT, p_quota INTEGER, p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL, p_limits JSONB DEFAULT NULL,
    p_project_id UUID DEFAULT NULL, p_description TEXT DEFAULT ''
) RETURNS api.api_keys SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE p_user_id UUID; v_key_id UUID; v_key_value TEXT; v_quota BIGINT; v_result api.api_keys; v_project api.api_key_projects;
BEGIN
    p_user_id := auth.uid();
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id=p_user_id) THEN RAISE EXCEPTION 'User profile not found'; END IF;
    IF p_workspace IS NULL OR p_workspace='' THEN RAISE EXCEPTION 'workspace is required to create an API key' USING ERRCODE='22023'; END IF;
    SELECT * INTO v_project FROM api.api_key_projects WHERE id=p_project_id AND user_id=p_user_id;
    IF NOT FOUND OR v_project.workspace <> p_workspace THEN RAISE EXCEPTION 'project is required and must belong to the selected workspace' USING ERRCODE='22023'; END IF;
    IF NOT v_project.enabled THEN RAISE EXCEPTION 'project is disabled' USING ERRCODE='22023'; END IF;
    IF p_display_name IS NULL THEN p_display_name := p_name; END IF;
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota>0 THEN p_limits:=jsonb_build_object('token_quota',jsonb_build_object('limit',p_quota,'period','monthly')); END IF;
    PERFORM api.validate_api_key_limits(p_limits);
    v_quota:=COALESCE((p_limits #>> '{token_quota,limit}')::bigint,0);
    v_key_id:=gen_random_uuid(); v_key_value:=api.generate_api_key(p_user_id,v_key_id,p_expires_in);
    INSERT INTO api.api_keys (id,api_version,kind,metadata,spec,status,user_id,project_id,description)
    VALUES (v_key_id,'v1','ApiKey',ROW(p_name,p_display_name,p_workspace,NULL,now(),now(),'{}'::json)::api.metadata,
      ROW(v_quota,p_expires_in,p_limits)::api.api_key_spec,
      ROW('Pending',now(),NULL,v_key_value,0,now(),NULL)::api.api_key_status,p_user_id,p_project_id,COALESCE(p_description,''))
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
