-- Reverts service_account_api_keys: API keys belong to users only. SA keys are
-- deleted; their gateway credentials are not revoked by this migration.

DELETE FROM api.api_keys WHERE service_account_id IS NOT NULL;

DROP POLICY IF EXISTS "api_key read policy" ON api.api_keys;
DROP POLICY IF EXISTS "api_key create policy" ON api.api_keys;
DROP POLICY IF EXISTS "api_key update policy" ON api.api_keys;
DROP POLICY IF EXISTS "api_key delete policy" ON api.api_keys;

CREATE POLICY "Users can see their own API keys" ON api.api_keys
    FOR SELECT
    USING (user_id = auth.uid());

CREATE POLICY "Users can create their own API keys" ON api.api_keys
    FOR INSERT
    WITH CHECK (user_id = auth.uid());

CREATE POLICY "Users can update their own API keys" ON api.api_keys
    FOR UPDATE
    USING (user_id = auth.uid());

CREATE POLICY "Users can delete their own API keys" ON api.api_keys
    FOR DELETE
    USING (user_id = auth.uid());

DROP POLICY IF EXISTS "API keys owners can see related daily usage" ON api.api_daily_usage;

CREATE POLICY "API keys owners can see related daily usage" ON api.api_daily_usage
    FOR SELECT
    USING (((spec).api_key_id) IN (
        SELECT id FROM api.api_keys WHERE user_id = auth.uid()
    ));

DROP FUNCTION IF EXISTS api.create_service_account_api_key(UUID, TEXT, TEXT, TEXT, INTEGER, JSONB, TEXT);

CREATE OR REPLACE FUNCTION api.create_api_key(
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

CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits        JSONB;
    v_uid           UUID;
    v_limit         BIGINT;
    v_period        TEXT;
    v_used          BIGINT;
    v_has_per_model BOOLEAN;
    v_models        JSONB;
BEGIN
    SELECT (spec).limits, user_id INTO v_limits, v_uid FROM api.api_keys WHERE id = p_id;
    -- NULL-safe owner check: a NULL auth.uid() (anon) must not slip past `<>`.
    IF NOT FOUND OR v_uid IS DISTINCT FROM auth.uid() THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
        WHERE e ->> 'token_limit' IS NOT NULL
    ) INTO v_has_per_model;

    IF v_has_per_model THEN
        SELECT jsonb_agg(
                   CASE
                       WHEN e ->> 'token_limit' IS NULL THEN e
                       ELSE e || jsonb_build_object(
                           'used', u.used,
                           'remaining', (e ->> 'token_limit')::bigint - u.used
                       )
                   END
                   ORDER BY ord
               )
          INTO v_models
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) WITH ORDINALITY AS t(e, ord)
        CROSS JOIN LATERAL (
            SELECT CASE
                WHEN e ->> 'token_limit' IS NULL THEN 0::bigint
                ELSE api.api_key_model_period_usage(
                    p_id, v_period, e ->> 'model',
                    NULLIF(e ->> 'type', ''), NULLIF(e ->> 'endpoint_name', ''))
            END AS used
        ) u;

        RETURN jsonb_set(v_limits, '{allowed_models}', COALESCE(v_models, '[]'::jsonb))
               || jsonb_build_object(
                    'quota_granularity', 'per_model',
                    'quota_period', v_period,
                    'quota_period_start', api.api_key_period_start(v_period),
                    'quota_resets_at', api.api_key_period_reset(v_period));
    END IF;

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NOT NULL AND v_limit > 0 THEN
        v_used := api.api_key_period_usage(p_id, v_period);
        v_limits := jsonb_set(
            v_limits, '{token_quota}',
            (v_limits->'token_quota')
                || jsonb_build_object('used', v_used, 'remaining', v_limit - v_used)
        );
    END IF;
    RETURN v_limits || jsonb_build_object(
        'quota_granularity', 'overall',
        'quota_period', v_period,
        'quota_period_start', api.api_key_period_start(v_period),
        'quota_resets_at', api.api_key_period_reset(v_period));
END;
$$;

CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(
    p_workspace   TEXT,
    p_api_key_ids UUID[] DEFAULT NULL
)
RETURNS TABLE (
    api_key_id       UUID,
    period           TEXT,
    granularity      TEXT,
    token_limit      BIGINT,
    used             BIGINT,
    remaining        BIGINT,
    top_model        TEXT,
    top_model_type   TEXT,
    top_model_used   BIGINT,
    top_model_limit  BIGINT,
    limited_models   INTEGER
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
    WITH visible AS (
        SELECT k.id,
               (k.spec).limits AS limits,
               COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly') AS period,
               EXISTS (
                   SELECT 1
                   FROM jsonb_array_elements(
                       api.api_key_allowed_models((k.spec).limits)
                   ) AS e
                   WHERE e ->> 'token_limit' IS NOT NULL
               ) AS has_per_model
        FROM api.api_keys k
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (k.user_id = auth.uid() OR v_can_read_workspace)
          AND (p_api_key_ids IS NULL OR k.id = ANY (p_api_key_ids))
    ),
    scoped AS (
        SELECT v.*, api.api_key_period_start(v.period) AS period_start
        FROM visible v
        WHERE v.has_per_model
           OR (
                (v.limits #>> '{token_quota,limit}') IS NOT NULL
                AND (v.limits #>> '{token_quota,limit}')::bigint > 0
              )
    ),
    -- Whole-key totals: one pass, as before.
    totals AS (
        SELECT s.id,
               COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used
        FROM scoped s
        LEFT JOIN api.api_daily_usage d
               ON (d.spec).api_key_id = s.id
              AND (d.spec).usage_date >= s.period_start
              AND (d.spec).usage_date <= CURRENT_DATE
        GROUP BY s.id
    ),
    -- Per-entry figures, only for keys actually on per-model quotas.
    entries AS (
        SELECT s.id,
               s.period_start,
               e ->> 'model'                     AS model,
               NULLIF(e ->> 'type', '')          AS ep_type,
               NULLIF(e ->> 'endpoint_name', '') AS ep_name,
               (e ->> 'token_limit')::bigint     AS token_limit
        FROM scoped s
        CROSS JOIN LATERAL jsonb_array_elements(
            api.api_key_allowed_models(s.limits)
        ) AS e
        WHERE s.has_per_model
          AND e ->> 'token_limit' IS NOT NULL
    ),
    -- The ledger's detail keys, expanded once for the keys in scope. The first
    -- segment is the ledger's own spelling of the endpoint type, which is not
    -- the gateway's -- see api_key_model_period_usage for why they differ.
    detail AS (
        SELECT (d.spec).api_key_id AS id,
               (d.spec).usage_date AS usage_date,
               split_part(kv.key, '|', 1) AS ledger_type,
               split_part(kv.key, '|', 2) AS ep_name,
               substr(kv.key,
                      length(split_part(kv.key, '|', 1))
                    + length(split_part(kv.key, '|', 2)) + 3) AS model,
               COALESCE((kv.value ->> 'total')::bigint, 0) AS used
        FROM api.api_daily_usage d
        CROSS JOIN LATERAL jsonb_each(
            COALESCE((d.spec).detailed_dimensional_usage, '{}'::jsonb)
        ) AS kv
        WHERE (d.spec).api_key_id IN (SELECT id FROM entries)
          AND (d.spec).usage_date <= CURRENT_DATE
    ),
    per_entry AS (
        SELECT e.id, e.model, e.ep_type, e.ep_name, e.token_limit,
               COALESCE(SUM(x.used), 0)::bigint AS used
        FROM entries e
        LEFT JOIN detail x
               ON x.id = e.id
              AND x.usage_date >= e.period_start
              AND x.model = e.model
              AND (e.ep_type IS NULL OR x.ledger_type = CASE e.ep_type
                       WHEN 'internal' THEN 'endpoint'
                       WHEN 'external' THEN 'external-endpoint'
                       ELSE e.ep_type END)
              AND (e.ep_name IS NULL OR x.ep_name = e.ep_name)
        GROUP BY e.id, e.model, e.ep_type, e.ep_name, e.token_limit
    ),
    -- Most utilised entry per key. Over-limit entries sort first by
    -- construction: their ratio is above 1, which is exactly what a list should
    -- surface.
    top AS (
        SELECT DISTINCT ON (pe.id)
               pe.id, pe.model, pe.ep_type, pe.used, pe.token_limit
        FROM per_entry pe
        ORDER BY pe.id, (pe.used::numeric / NULLIF(pe.token_limit, 0)) DESC NULLS LAST,
                 pe.model, pe.ep_type NULLS FIRST, pe.ep_name NULLS FIRST
    ),
    counts AS (
        SELECT id, count(*)::int AS limited_models FROM per_entry GROUP BY id
    )
    SELECT s.id,
           s.period,
           CASE WHEN s.has_per_model THEN 'per_model' ELSE 'overall' END,
           CASE WHEN s.has_per_model THEN NULL::bigint
                ELSE (s.limits #>> '{token_quota,limit}')::bigint END,
           t.used,
           CASE WHEN s.has_per_model THEN NULL::bigint
                ELSE (s.limits #>> '{token_quota,limit}')::bigint - t.used END,
           top.model,
           top.ep_type,
           top.used,
           top.token_limit,
           COALESCE(c.limited_models, 0)
    FROM scoped s
    JOIN totals t ON t.id = s.id
    LEFT JOIN top ON top.id = s.id
    LEFT JOIN counts c ON c.id = s.id;
END;
$$;

CREATE OR REPLACE FUNCTION api.get_usage_by_dimension(
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
    completion_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_creation_tokens BIGINT,
    reasoning_tokens BIGINT,
    cost_usd NUMERIC
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
            NULL::bigint AS c_tokens,
            NULL::bigint AS cr_tokens,
            NULL::bigint AS cc_tokens,
            NULL::bigint AS r_tokens,
            NULL::numeric AS cost
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
            (kv.value->>'completion')::bigint AS c_tokens,
            (kv.value->>'cache_read')::bigint AS cr_tokens,
            (kv.value->>'cache_creation')::bigint AS cc_tokens,
            (kv.value->>'reasoning')::bigint AS r_tokens,
            (kv.value->>'cost_usd')::numeric AS cost
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
        d.c_tokens,
        d.cr_tokens,
        d.cc_tokens,
        d.r_tokens,
        d.cost
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;

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

CREATE OR REPLACE FUNCTION api.update_api_key_configuration(
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

CREATE OR REPLACE FUNCTION api.move_api_keys_to_project(
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

CREATE OR REPLACE FUNCTION api.can_read_api_key_usage(p_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_owner UUID;
    v_ws    TEXT;
BEGIN
    -- The gateway calls with a service_role token. PostgREST exposes claims via
    -- request.jwt.claims (JSON); some setups also set request.jwt.claim.role.
    IF (NULLIF(current_setting('request.jwt.claims', true), '')::jsonb ->> 'role') = 'service_role'
       OR current_setting('request.jwt.claim.role', true) = 'service_role' THEN
        RETURN TRUE;
    END IF;
    -- No authenticated user -> deny. A NULL result here would slip past callers'
    -- `IF NOT api.can_read_api_key_usage(...)` guards (NOT NULL is not TRUE).
    IF auth.uid() IS NULL THEN
        RETURN FALSE;
    END IF;
    SELECT user_id, (metadata).workspace INTO v_owner, v_ws FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    RETURN COALESCE(
        auth.uid() = v_owner
        OR api.has_permission(auth.uid(), 'workspace:usage-read', v_ws),
        FALSE
    );
END;
$$;

DROP FUNCTION IF EXISTS api.insert_api_key(UUID, UUID, TEXT, TEXT, TEXT, INTEGER, JSONB, UUID, TEXT);
DROP FUNCTION IF EXISTS api.can_read_api_key_owner_usage(UUID, UUID);
DROP FUNCTION IF EXISTS api.can_act_on_api_key(UUID, UUID, api.permission_action);
DROP FUNCTION IF EXISTS api.can_read_service_account_usage(UUID, UUID);

DROP TRIGGER IF EXISTS service_accounts_before_delete ON api.service_accounts;
DROP FUNCTION IF EXISTS api.service_accounts_before_delete();

DROP TRIGGER IF EXISTS api_keys_check_owner ON api.api_keys;
DROP FUNCTION IF EXISTS api.api_keys_check_owner();
DROP INDEX IF EXISTS api.api_keys_service_account_id_idx;

ALTER TABLE api.api_keys
    DROP CONSTRAINT IF EXISTS api_keys_owner_check,
    DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS service_account_id,
    ALTER COLUMN user_id SET NOT NULL;
