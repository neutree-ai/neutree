-- Optional, user-owned folders for organizing API keys within a workspace.
BEGIN;

CREATE TABLE api.api_key_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace TEXT NOT NULL CHECK (length(trim(workspace)) > 0),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    user_id UUID NOT NULL REFERENCES api.user_profiles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX api_key_projects_user_workspace_name_unique_idx
    ON api.api_key_projects (user_id, workspace, lower(name));

ALTER TABLE api.api_keys
    ADD COLUMN project_id UUID REFERENCES api.api_key_projects(id) ON DELETE RESTRICT,
    ADD COLUMN description TEXT NOT NULL DEFAULT '';

CREATE INDEX api_keys_project_id_idx ON api.api_keys (project_id);

ALTER TABLE api.api_key_projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY api_key_projects_select ON api.api_key_projects FOR SELECT
    USING (
        user_id = auth.uid()
        AND api.has_permission(auth.uid(), 'workspace:read', workspace)
    );

CREATE FUNCTION api.touch_api_key_project()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER touch_api_key_project
    BEFORE UPDATE ON api.api_key_projects
    FOR EACH ROW EXECUTE FUNCTION api.touch_api_key_project();

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
        workspace, name, description, user_id
    ) VALUES (
        p_workspace, trim(p_name), COALESCE(p_description, ''), auth.uid()
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
      AND p.workspace = p_workspace
      AND api.has_permission(auth.uid(), 'workspace:read', p.workspace)
    ORDER BY p.created_at, p.id;
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
    SET name = COALESCE(trim(p_name), p.name),
        description = COALESCE(p_description, p.description)
    WHERE p.id = p_project_id
      AND p.user_id = auth.uid()
      AND api.has_permission(auth.uid(), 'workspace:read', p.workspace)
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
    SELECT workspace INTO v_workspace
    FROM api.api_key_projects
    WHERE id = p_project_id
      AND user_id = auth.uid();

    IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;

    SELECT count(*) INTO v_count
    FROM api.api_keys
    WHERE project_id = p_project_id;

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
        SELECT workspace INTO v_workspace
        FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid();
        IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
            RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501';
        END IF;
    ELSE
        SELECT (metadata).workspace INTO v_workspace
        FROM api.api_keys
        WHERE id = p_api_key_ids[1] AND user_id = auth.uid();
    END IF;

    IF EXISTS (
        SELECT 1
        FROM unnest(p_api_key_ids) AS selected(key_id)
        LEFT JOIN api.api_keys k ON k.id = selected.key_id
        WHERE k.id IS NULL
           OR k.user_id <> auth.uid()
           OR (k.metadata).workspace <> v_workspace
    ) THEN
        RAISE EXCEPTION 'API key not found, permission denied, or workspace mismatch'
            USING ERRCODE = '42501';
    END IF;

    UPDATE api.api_keys
    SET project_id = p_project_id
    WHERE id = ANY(p_api_key_ids)
      AND project_id IS DISTINCT FROM p_project_id;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
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
        SELECT to_jsonb(p) AS project, p.id AS project_id, p.workspace, p.created_at
        FROM api.api_key_projects p
        WHERE p.user_id = auth.uid()
          AND (p_workspace IS NULL OR p.workspace = p_workspace)
          AND api.has_permission(auth.uid(), 'workspace:read', p.workspace)
        UNION ALL
        SELECT jsonb_build_object(
                   'id', '__ungrouped__:' || key_workspaces.workspace,
                   'workspace', key_workspaces.workspace,
                   'name', 'Ungrouped',
                   'description', '',
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
                   OR f.project->>'name' ILIKE '%' || trim(p_search) || '%' AS project_matches
        FROM folders f
        WHERE (
            p_api_key_disabled IS NULL
            OR EXISTS (
                SELECT 1 FROM api.api_keys status_key
                WHERE status_key.user_id = auth.uid()
                  AND (status_key.metadata).workspace = f.workspace
                  AND status_key.project_id IS NOT DISTINCT FROM f.project_id
                  AND (status_key.metadata).deletion_timestamp IS NULL
                  AND COALESCE(
                      ((status_key.spec).limits ->> 'disabled')::boolean,
                      false
                  ) = p_api_key_disabled
            )
        )
        AND (
           NULLIF(trim(p_search), '') IS NULL
           OR f.project->>'name' ILIKE '%' || trim(p_search) || '%'
           OR EXISTS (
               SELECT 1 FROM api.api_keys k
               WHERE k.user_id = auth.uid()
                 AND (k.metadata).workspace = f.workspace
                 AND k.project_id IS NOT DISTINCT FROM f.project_id
                 AND (k.metadata).deletion_timestamp IS NULL
                 AND (
                     COALESCE((k.metadata).display_name, (k.metadata).name)
                         ILIKE '%' || trim(p_search) || '%'
                     OR k.description ILIKE '%' || trim(p_search) || '%'
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
     AND k.project_id IS NOT DISTINCT FROM v.project_id
     AND (k.metadata).deletion_timestamp IS NULL
     AND (
         p_api_key_disabled IS NULL
         OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
             = p_api_key_disabled
     )
     AND (
         v.project_matches
         OR COALESCE((k.metadata).display_name, (k.metadata).name)
             ILIKE '%' || trim(p_search) || '%'
         OR k.description ILIKE '%' || trim(p_search) || '%'
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
    LEFT JOIN api.api_key_projects p ON p.id = k.project_id
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
          OR p.name ILIKE '%' || trim(p_search) || '%'
          OR COALESCE((k.metadata).display_name, (k.metadata).name)
              ILIKE '%' || trim(p_search) || '%'
          OR k.description ILIKE '%' || trim(p_search) || '%'
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
    IF p_workspace IS NULL OR p_workspace = '' THEN
        RAISE EXCEPTION 'workspace is required to create an API key' USING ERRCODE = '22023';
    END IF;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = p_user_id
          AND workspace = p_workspace
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
        id, api_version, kind, metadata, spec, status, user_id, project_id, description
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(
            p_name, p_display_name, p_workspace, NULL, now(), now(),
            '{}'::json, '{}'::json
        )::api.metadata,
        ROW(v_quota, p_expires_in, p_limits)::api.api_key_spec,
        ROW('Pending', now(), NULL, v_key_value, 0, now(), NULL)::api.api_key_status,
        p_user_id,
        p_project_id,
        COALESCE(p_description, '')
    )
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$;

CREATE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID, p_project_id UUID, p_limits JSONB
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_key api.api_keys; v_workspace TEXT;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    v_workspace := (v_key.metadata).workspace;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid()
          AND workspace = v_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace' USING ERRCODE = '22023';
    END IF;

    UPDATE api.api_keys SET project_id = p_project_id WHERE id = p_api_key_id;
    PERFORM api.set_api_key_limits(p_api_key_id, COALESCE(p_limits, '{}'::jsonb));
    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
