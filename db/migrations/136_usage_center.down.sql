-- Restores get_usage_by_dimension and can_read_api_key_usage of migration
-- 132, and get_usage_by_group, group_daily_usage_on_aggregated and the bucket
-- index of migration 133.

DROP FUNCTION IF EXISTS api.get_readable_usage_teams();

CREATE OR REPLACE FUNCTION api.can_read_api_key_usage(p_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_owner UUID;
    v_sa    UUID;
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
    SELECT user_id, service_account_id, (metadata).workspace INTO v_owner, v_sa, v_ws FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    RETURN api.can_read_api_key_owner_usage(v_owner, v_sa)
        OR api.has_permission(auth.uid(), 'workspace:usage-read', v_ws) IS TRUE;
END;
$$;

DROP FUNCTION IF EXISTS api.can_read_key_usage(UUID, UUID, TEXT);
DROP FUNCTION IF EXISTS api.usage_filter_org_unit_ids(TEXT, BOOLEAN);
DROP FUNCTION IF EXISTS api.usage_filter_team_id(TEXT);
DROP FUNCTION IF EXISTS api.readable_usage_org_unit_ids(UUID);
DROP FUNCTION IF EXISTS api.readable_usage_team_ids(UUID);

DROP FUNCTION IF EXISTS api.get_usage_by_group(DATE, DATE, TEXT, TEXT, TEXT, TEXT, TEXT, BOOLEAN);

-- By ServiceAccount, name is the SA's id; usage by user leaves SA rows out.
CREATE OR REPLACE FUNCTION api.get_usage_by_group(
    p_start_date DATE,
    p_end_date DATE,
    p_kind TEXT,
    p_name TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL,
    p_org_unit TEXT DEFAULT NULL,
    p_team TEXT DEFAULT NULL
)
RETURNS TABLE (
    date DATE,
    kind TEXT,
    name TEXT,
    display_name TEXT,
    workspace TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    requests BIGINT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_creation_tokens BIGINT,
    reasoning_tokens BIGINT,
    cost_usd NUMERIC
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    -- NULL = every row; otherwise only rows recorded under these OrgUnits.
    v_units INTEGER[];
    -- NULL = no filter; an unknown name matches no row.
    v_filter_units INTEGER[];
    v_filter_team INTEGER;
BEGIN
    IF api.has_permission(auth.uid(), 'workspace:usage-read', NULL) IS NOT TRUE THEN
        IF p_kind IS NOT DISTINCT FROM 'Team' OR p_team IS NOT NULL THEN
            -- team_usage:read on a Team reads that Team: by Team (p_name or
            -- p_team), or its users or SAs (p_team).
            IF p_kind IS NULL OR p_kind NOT IN ('Team', 'User', 'ServiceAccount')
                OR (p_team IS NOT NULL AND api.can_read_team_usage(auth.uid(), p_team) IS NOT TRUE)
                OR (p_kind = 'Team' AND api.can_read_team_usage(auth.uid(), COALESCE(p_name, p_team)) IS NOT TRUE) THEN
                RAISE insufficient_privilege
                    USING message = 'global workspace:usage-read is required to read usage by Team, or team_usage:read on that Team';
            END IF;
        ELSIF api.has_permission(auth.uid(), 'org_unit:usage-read', NULL) IS TRUE THEN
            v_units := ARRAY(SELECT u.id FROM api.org_units u);
        ELSE
            -- Through api.check_org_role_binding: it is an extension point.
            v_units := ARRAY(
                SELECT u.id FROM api.org_units u
                WHERE api.has_org_scoped_permission(auth.uid(), 'org_unit:usage-read', u.id) IS TRUE);
        END IF;

        IF v_units IS NOT NULL AND cardinality(v_units) = 0 THEN
            RAISE insufficient_privilege
                USING message = 'global workspace:usage-read is required to read usage by group, or org_unit:usage-read to read the users and OrgUnits you manage';
        END IF;
    END IF;

    IF p_kind IS NULL OR p_kind NOT IN ('User', 'Team', 'OrgUnit', 'ServiceAccount') THEN
        RAISE invalid_parameter_value
            USING message = 'p_kind must be User, Team, OrgUnit or ServiceAccount';
    END IF;

    IF p_org_unit IS NOT NULL THEN
        v_filter_units := ARRAY(
            SELECT s.id
            FROM api.org_units r
            CROSS JOIN LATERAL api.org_unit_subtree(r.id) s
            WHERE (r.metadata).name = p_org_unit);
    END IF;

    IF p_team IS NOT NULL THEN
        SELECT t.id INTO v_filter_team FROM api.teams t WHERE (t.metadata).name = p_team;
        v_filter_team := COALESCE(v_filter_team, 0);
    END IF;

    RETURN QUERY
    WITH g AS (
        SELECT u.*
        FROM api.group_daily_usage u
        WHERE u.usage_date BETWEEN p_start_date AND p_end_date
          AND (p_workspace IS NULL OR u.workspace = p_workspace)
          AND (v_units IS NULL OR u.org_unit_id = ANY (v_units))
          AND (v_filter_units IS NULL OR u.org_unit_id = ANY (v_filter_units))
          AND (v_filter_team IS NULL OR v_filter_team = ANY (u.team_ids))
    ), expanded AS (
        SELECT g.*, g.user_id::TEXT AS group_name,
               COALESCE(NULLIF((p.metadata).display_name, ''), (p.metadata).name, (p.spec).email) AS group_display
        FROM g
        LEFT JOIN api.user_profiles p ON p.id = g.user_id
        WHERE p_kind = 'User'
          AND g.user_id IS NOT NULL
        UNION ALL
        SELECT g.*, g.service_account_id::TEXT,
               COALESCE(NULLIF((s.metadata).display_name, ''), (s.metadata).name)
        FROM g
        LEFT JOIN api.service_accounts s ON s.id = g.service_account_id
        WHERE p_kind = 'ServiceAccount'
          AND g.service_account_id IS NOT NULL
        UNION ALL
        SELECT g.*, (t.metadata).name, (t.metadata).display_name
        FROM g
        CROSS JOIN LATERAL unnest(g.team_ids) AS tid(id)
        JOIN api.teams t ON t.id = tid.id
        WHERE p_kind = 'Team'
          AND (v_filter_team IS NULL OR t.id = v_filter_team)
        UNION ALL
        -- Every current ancestor of the recorded OrgUnit, itself included;
        -- with v_units, only those the caller may read; with p_org_unit, only
        -- those in its subtree.
        SELECT g.*, (s.metadata).name, (s.metadata).display_name
        FROM g
        JOIN api.org_units ou ON ou.id = g.org_unit_id
        CROSS JOIN LATERAL unnest(api.org_unit_ancestor_ids(ou.path)) AS aid(id)
        JOIN api.org_units s ON s.id = aid.id
        WHERE p_kind = 'OrgUnit'
          AND (v_units IS NULL OR s.id = ANY (v_units))
          AND (v_filter_units IS NULL OR s.id = ANY (v_filter_units))
    )
    SELECT
        e.usage_date,
        p_kind,
        e.group_name,
        e.group_display,
        e.workspace,
        NULLIF(e.endpoint_type, ''),
        e.endpoint_name,
        NULLIF(e.model_name, ''),
        SUM(e.requests)::BIGINT,
        SUM(e.total_tokens)::BIGINT,
        SUM(e.prompt_tokens)::BIGINT,
        SUM(e.completion_tokens)::BIGINT,
        SUM(e.cache_read_tokens)::BIGINT,
        SUM(e.cache_creation_tokens)::BIGINT,
        SUM(e.reasoning_tokens)::BIGINT,
        SUM(e.cost_usd)
    FROM expanded e
    WHERE p_name IS NULL OR e.group_name = p_name
    GROUP BY e.usage_date, e.group_name, e.group_display, e.workspace,
             e.endpoint_type, e.endpoint_name, e.model_name
    ORDER BY e.usage_date DESC, e.group_name, e.endpoint_name, e.model_name;
END;
$$;

DROP FUNCTION IF EXISTS api.get_usage_by_dimension(DATE, DATE, UUID, TEXT, TEXT, TEXT, TEXT);

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
            api.can_read_api_key_owner_usage(ak.user_id, ak.service_account_id)
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

-- An SA key's usage is recorded for the SA and its Team.
CREATE OR REPLACE FUNCTION api.group_daily_usage_on_aggregated()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    v_user UUID;
    v_sa UUID;
    v_team INTEGER;
    v_workspace TEXT;
    v_org_unit INTEGER;
    v_teams INTEGER[];
BEGIN
    SELECT ak.user_id, ak.service_account_id, s.team_id, COALESCE((ak.metadata).workspace, 'unknown')
    INTO v_user, v_sa, v_team, v_workspace
    FROM api.api_keys ak
    LEFT JOIN api.service_accounts s ON s.id = ak.service_account_id
    WHERE ak.id = NEW.api_key_id;

    IF v_sa IS NOT NULL THEN
        -- An SA key: the SA and its Team, no user and no OrgUnit.
        v_teams := ARRAY[v_team];
    ELSIF v_user IS NULL THEN
        RETURN NULL;
    ELSE
        SELECT m.org_unit_id INTO v_org_unit
        FROM api.org_unit_members m
        WHERE m.user_id = v_user;

        v_teams := ARRAY(
            SELECT tm.team_id
            FROM api.team_members tm
            JOIN api.teams t ON t.id = tm.team_id
            WHERE tm.user_id = v_user
              AND (t.status).phase = 'Active'
            ORDER BY tm.team_id);
    END IF;

    INSERT INTO api.group_daily_usage AS g (
        usage_date, user_id, service_account_id, org_unit_id, team_ids,
        workspace, endpoint_type, endpoint_name, model_name,
        requests, total_tokens, prompt_tokens, completion_tokens,
        cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost_usd)
    VALUES (
        -- Same day boundary as aggregate_usage_records.
        date_trunc('day', NEW.created_at)::date, v_user, v_sa, v_org_unit, v_teams,
        v_workspace, COALESCE(NEW.endpoint_type, ''),
        COALESCE(NEW.endpoint_name, 'unknown'), COALESCE(NEW.model_name, ''),
        1, NEW.usage_amount, COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        NEW.cache_read_tokens, NEW.cache_creation_tokens, NEW.reasoning_tokens, NEW.cost_usd)
    ON CONFLICT (usage_date, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
                 COALESCE(service_account_id, '00000000-0000-0000-0000-000000000000'::uuid),
                 COALESCE(org_unit_id, 0), team_ids,
                 workspace, endpoint_type, endpoint_name, model_name)
    DO UPDATE SET
        requests = g.requests + 1,
        total_tokens = g.total_tokens + EXCLUDED.total_tokens,
        prompt_tokens = g.prompt_tokens + EXCLUDED.prompt_tokens,
        completion_tokens = g.completion_tokens + EXCLUDED.completion_tokens,
        cache_read_tokens = COALESCE(g.cache_read_tokens + EXCLUDED.cache_read_tokens,
                                     g.cache_read_tokens, EXCLUDED.cache_read_tokens),
        cache_creation_tokens = COALESCE(g.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
                                         g.cache_creation_tokens, EXCLUDED.cache_creation_tokens),
        reasoning_tokens = COALESCE(g.reasoning_tokens + EXCLUDED.reasoning_tokens,
                                    g.reasoning_tokens, EXCLUDED.reasoning_tokens),
        cost_usd = COALESCE(g.cost_usd + EXCLUDED.cost_usd, g.cost_usd, EXCLUDED.cost_usd),
        update_timestamp = now();

    RETURN NULL;
END;
$$;

DROP INDEX IF EXISTS api.group_daily_usage_api_key_idx;

DROP INDEX api.group_daily_usage_bucket_idx;

-- Buckets that differed only by key merge back into one.
WITH old AS (
    DELETE FROM api.group_daily_usage RETURNING *
)
INSERT INTO api.group_daily_usage (
    usage_date, user_id, service_account_id, org_unit_id, team_ids,
    workspace, endpoint_type, endpoint_name, model_name,
    requests, total_tokens, prompt_tokens, completion_tokens,
    cache_read_tokens, cache_creation_tokens, reasoning_tokens, cost_usd, update_timestamp)
SELECT usage_date, user_id, service_account_id, org_unit_id, team_ids,
       workspace, endpoint_type, endpoint_name, model_name,
       SUM(requests), SUM(total_tokens), SUM(prompt_tokens), SUM(completion_tokens),
       SUM(cache_read_tokens), SUM(cache_creation_tokens), SUM(reasoning_tokens), SUM(cost_usd),
       MAX(update_timestamp)
FROM old
GROUP BY usage_date, user_id, service_account_id, org_unit_id, team_ids,
         workspace, endpoint_type, endpoint_name, model_name;

ALTER TABLE api.group_daily_usage DROP COLUMN api_key_id;

CREATE UNIQUE INDEX group_daily_usage_bucket_idx ON api.group_daily_usage (
    usage_date, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
    COALESCE(service_account_id, '00000000-0000-0000-0000-000000000000'::uuid),
    COALESCE(org_unit_id, 0), team_ids,
    workspace, endpoint_type, endpoint_name, model_name);
