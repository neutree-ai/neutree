-- Optional, user-owned folders for organizing API keys within a workspace.
BEGIN;

CREATE TYPE api.api_key_project_spec AS (
    description TEXT
);

CREATE TABLE api.api_key_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_version TEXT NOT NULL DEFAULT 'v1',
    kind TEXT NOT NULL DEFAULT 'ApiKeyProject',
    metadata api.metadata NOT NULL,
    spec api.api_key_project_spec NOT NULL DEFAULT ROW('')::api.api_key_project_spec,
    user_id UUID NOT NULL REFERENCES api.user_profiles(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX api_key_projects_user_workspace_name_unique_idx
    ON api.api_key_projects (
        user_id, ((metadata).workspace), lower((metadata).name)
    );

ALTER TYPE api.api_key_spec
    ADD ATTRIBUTE project_id UUID,
    ADD ATTRIBUTE description TEXT;

-- Keep historical keys ungrouped and give their newly introduced description
-- the same empty-value semantics as newly created keys.
UPDATE api.api_keys k
SET spec = ROW(
    (k.spec).quota, (k.spec).expires_in, (k.spec).limits, NULL, ''
)::api.api_key_spec;

CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE v_result api.api_keys;
BEGIN
    PERFORM api.validate_api_key_limits(p_limits);
    UPDATE api.api_keys k
    SET spec = ROW(
        COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0),
        (k.spec).expires_in,
        p_limits,
        (k.spec).project_id,
        (k.spec).description
    )::api.api_key_spec
    WHERE k.id = p_id AND k.user_id = auth.uid()
    RETURNING * INTO v_result;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;
    RETURN v_result;
END;
$$;

CREATE INDEX api_keys_project_id_idx ON api.api_keys (((spec).project_id));

ALTER TABLE api.api_key_projects ENABLE ROW LEVEL SECURITY;
-- SELECT is the only policy on purpose. Every write goes through the
-- SECURITY DEFINER RPCs below, which enforce ownership, workspace:read and
-- name uniqueness together; leaving INSERT/UPDATE/DELETE unpolicied means a
-- direct PostgREST write is denied rather than bypassing those invariants.
CREATE POLICY api_key_projects_select ON api.api_key_projects FOR SELECT
    USING (
        user_id = auth.uid()
        AND api.has_permission(auth.uid(), 'workspace:read', (metadata).workspace)
    );

CREATE TRIGGER set_api_key_projects_default_timestamp
    BEFORE INSERT ON api.api_key_projects
    FOR EACH ROW EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_api_key_projects_update_timestamp
    BEFORE UPDATE ON api.api_key_projects
    FOR EACH ROW EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE FUNCTION api.validate_api_key_project()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF length(trim((NEW.metadata).workspace)) = 0 THEN
        RAISE EXCEPTION 'workspace is required' USING ERRCODE = '22023';
    END IF;
    IF length(trim((NEW.metadata).name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.user_id IS DISTINCT FROM OLD.user_id THEN
        RAISE EXCEPTION 'project owner cannot be changed' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_api_key_project
    BEFORE INSERT OR UPDATE ON api.api_key_projects
    FOR EACH ROW EXECUTE FUNCTION api.validate_api_key_project();

CREATE FUNCTION api.validate_api_key_project_reference()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.spec).project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects p
        WHERE p.id = (NEW.spec).project_id
          AND p.user_id = NEW.user_id
          AND (p.metadata).workspace = (NEW.metadata).workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the API key owner and workspace'
            USING ERRCODE = '22023';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_api_key_project_reference
    BEFORE INSERT OR UPDATE OF spec, metadata, user_id ON api.api_keys
    FOR EACH ROW EXECUTE FUNCTION api.validate_api_key_project_reference();

CREATE FUNCTION api.create_api_key_project(
    p_workspace TEXT, p_name TEXT, p_description TEXT DEFAULT ''
) RETURNS api.api_key_projects
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.api_key_projects;
BEGIN
    IF p_workspace IS NULL OR length(trim(p_workspace)) = 0 THEN
        RAISE EXCEPTION 'workspace is required' USING ERRCODE = '22023';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:read', p_workspace) THEN
        RAISE EXCEPTION 'workspace not found or permission denied' USING ERRCODE = '42501';
    END IF;
    IF p_name IS NULL OR length(trim(p_name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;

    INSERT INTO api.api_key_projects (
        api_version, kind, metadata, spec, user_id
    ) VALUES (
        'v1', 'ApiKeyProject',
        ROW(trim(p_name), NULL, p_workspace, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
        ROW(COALESCE(p_description, ''))::api.api_key_project_spec,
        auth.uid()
    )
    RETURNING * INTO v_result;
    RETURN v_result;
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'Project name already exists' USING ERRCODE = '23505';
END;
$$;

CREATE FUNCTION api.list_api_key_projects(p_workspace TEXT)
RETURNS SETOF api.api_key_projects
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT p.*
    FROM api.api_key_projects p
    WHERE p.user_id = auth.uid()
      AND (p.metadata).workspace = p_workspace
      AND api.has_permission(auth.uid(), 'workspace:read', (p.metadata).workspace)
    ORDER BY (p.metadata).creation_timestamp, p.id;
$$;

CREATE FUNCTION api.update_api_key_project(
    p_project_id UUID,
    p_name TEXT DEFAULT NULL,
    p_description TEXT DEFAULT NULL
) RETURNS api.api_key_projects
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.api_key_projects;
BEGIN
    IF p_name IS NOT NULL AND length(trim(p_name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;

    UPDATE api.api_key_projects p
    SET metadata = ROW(
            COALESCE(trim(p_name), (p.metadata).name),
            (p.metadata).display_name,
            (p.metadata).workspace,
            (p.metadata).deletion_timestamp,
            (p.metadata).creation_timestamp,
            (p.metadata).update_timestamp,
            (p.metadata).labels,
            (p.metadata).annotations
        )::api.metadata,
        spec = ROW(COALESCE(p_description, (p.spec).description))::api.api_key_project_spec
    WHERE p.id = p_project_id
      AND p.user_id = auth.uid()
      AND api.has_permission(auth.uid(), 'workspace:read', (p.metadata).workspace)
    RETURNING * INTO v_result;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;
    RETURN v_result;
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'Project name already exists' USING ERRCODE = '23505';
END;
$$;

CREATE FUNCTION api.delete_api_key_project(p_project_id UUID)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_count BIGINT; v_workspace TEXT;
BEGIN
    SELECT (metadata).workspace INTO v_workspace
    FROM api.api_key_projects
    WHERE id = p_project_id
      AND user_id = auth.uid();

    IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;

    -- Only live keys block deletion. The grouped listing filters soft-deleted
    -- keys out, so counting them here left a Project the UI shows as empty
    -- permanently undeletable.
    SELECT count(*) INTO v_count
    FROM api.api_keys
    WHERE (spec).project_id = p_project_id
      AND (metadata).deletion_timestamp IS NULL;

    IF v_count > 0 THEN
        RAISE EXCEPTION 'Project has % API keys', v_count
            USING ERRCODE = '23503', DETAIL = v_count::text;
    END IF;

    DELETE FROM api.api_key_projects WHERE id = p_project_id;
END;
$$;

CREATE FUNCTION api.move_api_keys_to_project(
    p_api_key_ids UUID[], p_project_id UUID DEFAULT NULL
) RETURNS INTEGER LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_workspace TEXT; v_count INTEGER;
BEGIN
    IF COALESCE(array_length(p_api_key_ids, 1), 0) = 0 THEN
        RAISE EXCEPTION 'at least one API key is required' USING ERRCODE = '22023';
    END IF;

    IF p_project_id IS NOT NULL THEN
        SELECT (metadata).workspace INTO v_workspace
        FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid();
        IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
            RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501';
        END IF;
    ELSE
        -- Moving to ungrouped still requires read access to the workspace the
        -- keys live in, the same check the targeted-project branch performs.
        SELECT (metadata).workspace INTO v_workspace
        FROM api.api_keys
        WHERE id = p_api_key_ids[1]
          AND user_id = auth.uid()
          AND (metadata).deletion_timestamp IS NULL;
        IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
            RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM unnest(p_api_key_ids) AS selected(key_id)
        LEFT JOIN api.api_keys k ON k.id = selected.key_id
        WHERE k.id IS NULL
           OR k.user_id <> auth.uid()
           OR (k.metadata).workspace <> v_workspace
           OR (k.metadata).deletion_timestamp IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'API key not found, permission denied, or workspace mismatch'
            USING ERRCODE = '42501';
    END IF;

    UPDATE api.api_keys
    SET spec = ROW(
        (spec).quota, (spec).expires_in, (spec).limits,
        p_project_id, (spec).description
    )::api.api_key_spec
    WHERE id = ANY(p_api_key_ids)
      AND (spec).project_id IS DISTINCT FROM p_project_id;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;

CREATE FUNCTION api.api_key_search_pattern(p_search TEXT)
RETURNS TEXT LANGUAGE sql IMMUTABLE AS $$
    SELECT '%' || replace(replace(replace(
        trim(p_search), '\', '\\'), '%', '\%'), '_', '\_') || '%';
$$;

CREATE FUNCTION api.get_api_key_project_groups(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL,
    p_page INTEGER DEFAULT 1,
    p_page_size INTEGER DEFAULT 20
) RETURNS TABLE (
    project JSONB,
    api_keys JSONB,
    api_key_count BIGINT,
    current_usage BIGINT,
    total_projects BIGINT
) LANGUAGE sql STABLE SECURITY DEFINER AS $$
    WITH folders AS (
        SELECT to_jsonb(p) AS project, p.id AS project_id,
               (p.metadata).workspace AS workspace,
               (p.metadata).creation_timestamp AS created_at
        FROM api.api_key_projects p
        WHERE p.user_id = auth.uid()
          AND (p_workspace IS NULL OR (p.metadata).workspace = p_workspace)
          AND api.has_permission(auth.uid(), 'workspace:read', (p.metadata).workspace)
        UNION ALL
        SELECT jsonb_build_object(
                   'id', '__ungrouped__:' || key_workspaces.workspace,
                   'api_version', 'v1',
                   'kind', 'ApiKeyProject',
                   'metadata', jsonb_build_object(
                       'workspace', key_workspaces.workspace,
                       'name', 'Ungrouped',
                       'display_name', NULL,
                       'deletion_timestamp', NULL,
                       'creation_timestamp', NULL,
                       'update_timestamp', NULL,
                       'labels', jsonb_build_object(),
                       'annotations', jsonb_build_object()
                   ),
                   'spec', jsonb_build_object('description', ''),
                   'user_id', auth.uid(),
                   'is_ungrouped', true
               ),
               NULL::uuid,
               key_workspaces.workspace,
               '-infinity'::timestamptz
        FROM (
            SELECT p_workspace AS workspace
            WHERE p_workspace IS NOT NULL
            UNION
            SELECT (w.metadata).name AS workspace
            FROM api.workspaces w
            WHERE p_workspace IS NULL
              AND (w.metadata).deletion_timestamp IS NULL
              AND api.has_permission(auth.uid(), 'workspace:read', (w.metadata).name)
        ) key_workspaces
        WHERE api.has_permission(auth.uid(), 'workspace:read', key_workspaces.workspace)
    ), matching AS (
        SELECT f.*,
               NULLIF(trim(p_search), '') IS NULL
                   OR f.project #>> '{metadata,name}' ILIKE api.api_key_search_pattern(p_search) AS project_matches
        FROM folders f
        WHERE (
            p_api_key_disabled IS NULL
            OR EXISTS (
                SELECT 1 FROM api.api_keys status_key
                WHERE status_key.user_id = auth.uid()
                  AND (status_key.metadata).workspace = f.workspace
                  AND (status_key.spec).project_id IS NOT DISTINCT FROM f.project_id
                  AND (status_key.metadata).deletion_timestamp IS NULL
                  AND COALESCE(
                      ((status_key.spec).limits ->> 'disabled')::boolean,
                      false
                  ) = p_api_key_disabled
            )
        )
        AND (
           NULLIF(trim(p_search), '') IS NULL
           OR f.project #>> '{metadata,name}' ILIKE api.api_key_search_pattern(p_search)
           OR EXISTS (
               SELECT 1 FROM api.api_keys k
               WHERE k.user_id = auth.uid()
                 AND (k.metadata).workspace = f.workspace
                 AND (k.spec).project_id IS NOT DISTINCT FROM f.project_id
                 AND (k.metadata).deletion_timestamp IS NULL
                 AND (
                     COALESCE((k.metadata).display_name, (k.metadata).name)
                         ILIKE api.api_key_search_pattern(p_search)
                     OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
                 )
           ))
    ), visible AS (
        SELECT m.*, count(*) OVER () AS total_projects
        FROM matching m
        ORDER BY m.created_at, m.project_id NULLS FIRST
        OFFSET GREATEST(p_page - 1, 0) * LEAST(GREATEST(p_page_size, 1), 100)
        LIMIT LEAST(GREATEST(p_page_size, 1), 100)
    )
    SELECT v.project,
           COALESCE(
               jsonb_agg(
                   to_jsonb(k) #- '{status,sk_value}'
                   ORDER BY (k.metadata).creation_timestamp
               )
                   FILTER (WHERE k.id IS NOT NULL),
               '[]'::jsonb
           ),
           count(k.id),
           COALESCE(sum((k.status).usage), 0)::bigint,
           v.total_projects
    FROM visible v
    LEFT JOIN api.api_keys k
      ON k.user_id = auth.uid()
     AND (k.metadata).workspace = v.workspace
     AND (k.spec).project_id IS NOT DISTINCT FROM v.project_id
     AND (k.metadata).deletion_timestamp IS NULL
     AND (
         p_api_key_disabled IS NULL
         OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
             = p_api_key_disabled
     )
     AND (
         v.project_matches
         OR COALESCE((k.metadata).display_name, (k.metadata).name)
             ILIKE api.api_key_search_pattern(p_search)
         OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
     )
    GROUP BY v.project, v.workspace, v.created_at, v.project_id, v.total_projects
    ORDER BY v.workspace, v.created_at, v.project_id NULLS FIRST;
$$;

CREATE FUNCTION api.count_api_key_project_group_api_keys(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL
) RETURNS BIGINT LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT count(*)
    FROM api.api_keys k
    LEFT JOIN api.api_key_projects p ON p.id = (k.spec).project_id
    WHERE k.user_id = auth.uid()
      AND (k.metadata).deletion_timestamp IS NULL
      AND (p_workspace IS NULL OR (k.metadata).workspace = p_workspace)
      AND api.has_permission(
          auth.uid(), 'workspace:read', (k.metadata).workspace
      )
      AND (
          p_api_key_disabled IS NULL
          OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
              = p_api_key_disabled
      )
      AND (
          NULLIF(trim(p_search), '') IS NULL
          OR (p.metadata).name ILIKE api.api_key_search_pattern(p_search)
          OR COALESCE((k.metadata).display_name, (k.metadata).name)
              ILIKE api.api_key_search_pattern(p_search)
          OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
      );
$$;

DROP FUNCTION api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB);
CREATE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL,
    p_project_id UUID DEFAULT NULL,
    p_description TEXT DEFAULT ''
) RETURNS api.api_keys
SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_quota BIGINT;
    v_result api.api_keys;
BEGIN
    p_user_id := auth.uid();
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;
    -- An API key is always scoped to exactly one workspace.
    IF p_workspace IS NULL OR p_workspace = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10044","message": "workspace is required to create an API key","hint": "Pass p_workspace with the name of an existing workspace"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = p_user_id
          AND (metadata).workspace = p_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the selected workspace' USING ERRCODE = '22023';
    END IF;
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota > 0 THEN
        p_limits := jsonb_build_object(
            'token_quota', jsonb_build_object('limit', p_quota, 'period', 'monthly')
        );
    END IF;
    PERFORM api.validate_api_key_limits(p_limits);
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);
    v_key_id := gen_random_uuid();
    IF p_name IS NULL OR length(trim(p_name)) = 0 THEN
        p_name := 'apikey-' || v_key_id::text;
    END IF;
    IF p_display_name IS NULL OR length(trim(p_display_name)) = 0 THEN
        p_display_name := p_name;
    END IF;
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);

    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(
            p_name, p_display_name, p_workspace, NULL, now(), now(),
            '{}'::json, '{}'::json
        )::api.metadata,
        ROW(v_quota, p_expires_in, p_limits, p_project_id, COALESCE(p_description, ''))::api.api_key_spec,
        ROW('Pending', now(), NULL, v_key_value, 0, now(), NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$;

CREATE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID,
    p_project_id UUID,
    p_limits JSONB,
    p_display_name TEXT,
    p_description TEXT
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_key api.api_keys; v_workspace TEXT;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    IF p_display_name IS NULL OR length(trim(p_display_name)) = 0 THEN
        RAISE EXCEPTION 'API key display name is required' USING ERRCODE = '22023';
    END IF;

    v_workspace := (v_key.metadata).workspace;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid()
          AND (metadata).workspace = v_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace' USING ERRCODE = '22023';
    END IF;

    UPDATE api.api_keys
    SET metadata = ROW(
            (metadata).name,
            trim(p_display_name),
            (metadata).workspace,
            (metadata).deletion_timestamp,
            (metadata).creation_timestamp,
            now(),
            (metadata).labels,
            (metadata).annotations
        )::api.metadata,
        spec = ROW(
            (spec).quota, (spec).expires_in, (spec).limits,
            p_project_id, COALESCE(p_description, '')
        )::api.api_key_spec
    WHERE id = p_api_key_id;
    -- NULL means "leave the limits alone". Coalescing to '{}' instead made an
    -- omitted argument wipe every limit, reset the quota to 0 and drop the
    -- disabled flag.
    IF p_limits IS NOT NULL THEN
        PERFORM api.set_api_key_limits(p_api_key_id, p_limits);
    END IF;
    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

-- A workspace user may inspect quota usage for their own API keys. Users with
-- workspace:usage-read retain the workspace-wide view used by administrators.
CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (
    api_key_id UUID,
    period TEXT,
    token_limit BIGINT,
    used BIGINT,
    remaining BIGINT
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_can_read_workspace BOOLEAN;
BEGIN
    v_can_read_workspace := api.has_permission(
        auth.uid(), 'workspace:usage-read', p_workspace
    );

    RETURN QUERY
        SELECT
            k.id,
            lim.period,
            lim.token_limit,
            COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used,
            lim.token_limit - COALESCE(SUM((d.spec).total_usage), 0)::bigint AS remaining
        FROM api.api_keys k
        CROSS JOIN LATERAL (
            SELECT
                COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly') AS period,
                ((k.spec).limits #>> '{token_quota,limit}')::bigint AS token_limit,
                CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                    WHEN 'daily' THEN CURRENT_DATE
                    WHEN 'weekly' THEN date_trunc('week', CURRENT_DATE)::date
                    WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                    WHEN 'yearly' THEN date_trunc('year', CURRENT_DATE)::date
                    ELSE date_trunc('month', CURRENT_DATE)::date
                END AS period_start
        ) lim
        LEFT JOIN api.api_daily_usage d
            ON (d.spec).api_key_id = k.id
           AND (d.spec).usage_date >= lim.period_start
           AND (d.spec).usage_date <= CURRENT_DATE
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (k.user_id = auth.uid() OR v_can_read_workspace)
          AND ((k.spec).limits #>> '{token_quota,limit}') IS NOT NULL
          AND ((k.spec).limits #>> '{token_quota,limit}')::bigint > 0
        GROUP BY k.id, lim.period, lim.token_limit;
END;
$$;

-- The usage RPC is the authority for which keys a caller may aggregate. Return
-- user-facing identity alongside the immutable technical name so every usage
-- view can render the same label without widening api_keys RLS.
DROP FUNCTION IF EXISTS api.get_usage_by_dimension;
CREATE FUNCTION api.get_usage_by_dimension(
    p_start_date DATE,
    p_end_date DATE,
    p_api_key_id UUID DEFAULT NULL,
    p_endpoint_name TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL
)
RETURNS TABLE (
    date DATE,
    api_key_id UUID,
    api_key_name TEXT,
    api_key_display_name TEXT,
    api_key_description TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    workspace TEXT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT
)
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).display_name AS key_display_name,
            (ak.spec).description AS key_description,
            (ak.metadata).workspace AS key_workspace
        FROM api.api_keys ak
        WHERE (
            ak.user_id = auth.uid()
            OR api.has_permission(
                auth.uid(), 'workspace:usage-read', (ak.metadata).workspace
            )
        )
        AND (p_api_key_id IS NULL OR ak.id = p_api_key_id)
    ), old_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            NULL::text AS endpoint_type,
            kv.key AS endpoint_name,
            NULL::text AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value)::bigint AS dimension_usage,
            NULL::bigint AS p_tokens,
            NULL::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NULL
    ), new_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            split_part(kv.key, '|', 1) AS endpoint_type,
            split_part(kv.key, '|', 2) AS endpoint_name,
            NULLIF(split_part(kv.key, '|', 3), '') AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value->>'total')::bigint AS dimension_usage,
            (kv.value->>'prompt')::bigint AS p_tokens,
            (kv.value->>'completion')::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ), dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
    )
    SELECT
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.key_display_name,
        d.key_description,
        d.endpoint_type,
        d.endpoint_name,
        d.model_name,
        d.workspace,
        d.dimension_usage,
        d.p_tokens,
        d.c_tokens
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;

COMMIT;
