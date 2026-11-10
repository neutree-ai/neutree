-- Subject policies go; the functions return to their 133 / 131 bodies.

DELETE FROM api.group_policies
WHERE (spec).subject_kind IN ('User', 'ServiceAccount') OR (spec).scope_kind IS NOT NULL;

DROP TRIGGER IF EXISTS user_profiles_delete_group_policy ON api.user_profiles;
DROP TRIGGER IF EXISTS service_accounts_delete_group_policy ON api.service_accounts;
DROP FUNCTION IF EXISTS api.group_policies_delete_with_subject();

DROP FUNCTION IF EXISTS api.get_scope_subject_policies(TEXT, TEXT);

DROP POLICY IF EXISTS "group_policy read policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy create policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy update policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy delete policy" ON api.group_policies;

CREATE POLICY "group_policy read policy" ON api.group_policies
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'group_policy:read', NULL)));

CREATE POLICY "group_policy create policy" ON api.group_policies
    FOR INSERT
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'group_policy:create', NULL)));

CREATE POLICY "group_policy update policy" ON api.group_policies
    FOR UPDATE
    USING ((SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL)))
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL)));

CREATE POLICY "group_policy delete policy" ON api.group_policies
    FOR DELETE
    USING ((SELECT api.has_permission(auth.uid(), 'group_policy:delete', NULL)));

CREATE OR REPLACE FUNCTION api.group_policies_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.group_policy_spec := NEW.spec;
    v_meta api.metadata := NEW.metadata;
    v_name TEXT;
BEGIN
    IF COALESCE(v_meta.workspace, '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10299","message": "A group policy is global and has no workspace","hint": "Remove metadata.workspace; pin a model to a workspace in its entry instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.subject_kind := NULLIF(btrim(v_spec.subject_kind), '');
    v_spec.subject_name := NULLIF(btrim(v_spec.subject_name), '');

    IF TG_OP = 'UPDATE' AND v_spec.subject_kind IS NULL AND v_spec.subject_name IS NULL THEN
        v_spec.subject_kind := (OLD.spec).subject_kind;
        v_spec.subject_name := (OLD.spec).subject_name;
    END IF;

    IF TG_OP = 'UPDATE' AND (
        v_spec.subject_kind IS DISTINCT FROM (OLD.spec).subject_kind
        OR v_spec.subject_name IS DISTINCT FROM (OLD.spec).subject_name
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10301","message": "The group of a group policy cannot be changed","hint": "Delete the policy and create one for the other group"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.subject_kind IS NULL OR v_spec.subject_kind NOT IN ('Team', 'OrgUnit') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10300","message": "spec.subject_kind of a group policy must be Team or OrgUnit","hint": "Set spec.subject_name to the metadata.name of the group"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'INSERT' AND (
        v_spec.subject_name IS NULL
        OR (v_spec.subject_kind = 'Team'
            AND NOT EXISTS (SELECT 1 FROM api.teams t WHERE (t.metadata).name = v_spec.subject_name))
        OR (v_spec.subject_kind = 'OrgUnit'
            AND NOT EXISTS (SELECT 1 FROM api.org_units u WHERE (u.metadata).name = v_spec.subject_name))
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10294","message": "spec.subject_name does not name a group of spec.subject_kind","hint": "Use the metadata.name of a Team or an OrgUnit"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.allowed_models := api.group_policy_normalize_models(v_spec.allowed_models, 'allowed_models');
    v_spec.denied_models := api.group_policy_normalize_models(v_spec.denied_models, 'denied_models');
    v_spec.token_quota := api.group_policy_normalize_token_quota(v_spec.token_quota);

    IF COALESCE(v_meta.name, '') = '' THEN
        IF TG_OP = 'UPDATE' THEN
            v_meta.name := (OLD.metadata).name;
        ELSE
            v_name := lower(v_spec.subject_kind) || '-' || v_spec.subject_name;
            IF length(v_name) > 63 THEN
                v_name := left(v_name, 50) || '-' || left(md5(v_name), 12);
            END IF;
            v_meta.name := v_name;
        END IF;
    END IF;

    v_meta.workspace := NULL;

    NEW.spec := v_spec;
    NEW.metadata := v_meta;

    RETURN NEW;
END;
$$;

-- A policy goes with its group, so a later group of the same name does not
-- inherit it.
CREATE OR REPLACE FUNCTION api.group_policies_delete_with_group()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    DELETE FROM api.group_policies p
    WHERE (p.spec).subject_kind = TG_ARGV[0]
      AND (p.spec).subject_name = (OLD.metadata).name;

    RETURN OLD;
END;
$$;

-- The group rules for a user in a workspace. allowed_models NULL = no allow
-- rule; otherwise the allowed names with denied ones already removed.
CREATE OR REPLACE FUNCTION api.user_group_model_policy(
    p_user_id UUID,
    p_workspace TEXT,
    OUT allowed_models TEXT[],
    OUT denied_models TEXT[]
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_org_unit TEXT[];
    v_team TEXT[];
    v_allow TEXT[];
    r RECORD;
BEGIN
    denied_models := ARRAY[]::TEXT[];

    FOR r IN
        SELECT g.kind,
               api.group_policy_model_names((p.spec).allowed_models, p_workspace) AS allow,
               api.group_policy_model_names((p.spec).denied_models, p_workspace) AS deny
        FROM api.user_groups(p_user_id) g
        JOIN api.group_policies p
          ON (p.spec).subject_kind = g.kind
         AND (p.spec).subject_name = g.name
    LOOP
        denied_models := denied_models || COALESCE(r.deny, ARRAY[]::TEXT[]);

        CONTINUE WHEN r.allow IS NULL;

        IF r.kind = 'OrgUnit' THEN
            v_org_unit := CASE WHEN v_org_unit IS NULL THEN r.allow
                ELSE ARRAY(SELECT unnest(v_org_unit) INTERSECT SELECT unnest(r.allow)) END;
        ELSE
            v_team := COALESCE(v_team, ARRAY[]::TEXT[]) || r.allow;
        END IF;
    END LOOP;

    IF v_org_unit IS NOT NULL AND v_team IS NOT NULL THEN
        v_allow := ARRAY(SELECT unnest(v_org_unit) INTERSECT SELECT unnest(v_team));
    ELSE
        v_allow := COALESCE(v_org_unit, v_team);
    END IF;

    denied_models := ARRAY(SELECT DISTINCT d FROM unnest(denied_models) AS d ORDER BY 1);

    IF v_allow IS NOT NULL THEN
        allowed_models := ARRAY(
            SELECT a FROM unnest(v_allow) AS a
            EXCEPT
            SELECT d FROM unnest(denied_models) AS d
            ORDER BY 1);
    END IF;
END;
$$;

-- "group_token_pool": TRUE when any of the owner's groups has a pool, so the
-- controller attaches the quota plugin to a key without a quota of its own.
CREATE OR REPLACE FUNCTION api.get_api_key_model_rules(
    p_owner_id UUID,
    p_workspace TEXT,
    p_allowed_models JSONB
)
RETURNS JSONB
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_allowed TEXT[];
    v_denied TEXT[];
    v_list JSONB;
BEGIN
    SELECT p.allowed_models, p.denied_models INTO v_allowed, v_denied
    FROM api.user_group_model_policy(p_owner_id, p_workspace) p;

    IF jsonb_typeof(p_allowed_models) = 'array' THEN
        v_list := COALESCE((
            SELECT jsonb_agg(t.e ORDER BY t.i)
            FROM jsonb_array_elements(p_allowed_models) WITH ORDINALITY AS t(e, i)
            WHERE (v_allowed IS NULL OR (t.e ->> 'model') = ANY (v_allowed))
              AND NOT ((t.e ->> 'model') = ANY (v_denied))
        ), '[]'::jsonb);
    ELSIF v_allowed IS NOT NULL THEN
        v_list := COALESCE((
            SELECT jsonb_agg(jsonb_build_object('model', n) ORDER BY n)
            FROM unnest(v_allowed) AS n
        ), '[]'::jsonb);
    END IF;

    RETURN jsonb_build_object(
        'allowed_models', COALESCE(v_list, 'null'::jsonb),
        'denied_models', to_jsonb(COALESCE(v_denied, ARRAY[]::TEXT[])),
        'group_token_pool', EXISTS (
            SELECT 1
            FROM api.user_groups(p_owner_id) g
            JOIN api.group_policies p
              ON (p.spec).subject_kind = g.kind
             AND (p.spec).subject_name = g.name
            WHERE (p.spec).token_quota IS NOT NULL));
END;
$$;

-- Tokens a group used in the current window of p_period; 0 for an unknown
-- group.
CREATE OR REPLACE FUNCTION api.group_token_pool_usage(p_kind TEXT, p_name TEXT, p_period TEXT)
RETURNS BIGINT
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_start DATE := api.api_key_period_start(p_period);
    v_id INTEGER;
    v_used NUMERIC;
BEGIN
    IF p_kind = 'Team' THEN
        SELECT t.id INTO v_id FROM api.teams t WHERE (t.metadata).name = p_name;

        SELECT SUM(u.total_tokens) INTO v_used
        FROM api.group_daily_usage u
        WHERE u.team_ids @> ARRAY[v_id]
          AND u.usage_date >= v_start
          AND u.usage_date <= CURRENT_DATE;
    ELSIF p_kind = 'OrgUnit' THEN
        SELECT o.id INTO v_id FROM api.org_units o WHERE (o.metadata).name = p_name;

        SELECT SUM(u.total_tokens) INTO v_used
        FROM api.org_unit_subtree(v_id) s
        JOIN api.group_daily_usage u ON u.org_unit_id = s.id
        WHERE u.usage_date >= v_start
          AND u.usage_date <= CURRENT_DATE;
    END IF;

    RETURN COALESCE(v_used, 0)::BIGINT;
END;
$$;

DROP FUNCTION IF EXISTS api.user_group_token_pools(UUID);

-- Every pool that applies to a user: the pools of all their groups, Inactive
-- ones included (a constraint stays until the policy is deleted).
CREATE FUNCTION api.user_group_token_pools(p_user_id UUID)
RETURNS TABLE (kind TEXT, name TEXT, token_limit BIGINT, period TEXT, used BIGINT)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT g.kind, g.name,
           ((p.spec).token_quota ->> 'limit')::BIGINT,
           (p.spec).token_quota ->> 'period',
           api.group_token_pool_usage(g.kind, g.name, (p.spec).token_quota ->> 'period')
    FROM api.user_groups(p_user_id) g
    JOIN api.group_policies p
      ON (p.spec).subject_kind = g.kind
     AND (p.spec).subject_name = g.name
    WHERE (p.spec).token_quota IS NOT NULL;
$$;

REVOKE ALL ON FUNCTION api.user_group_token_pools(UUID) FROM PUBLIC;

-- team_usage:read on a Team reads that Team's pool only (p_kind 'Team',
-- p_name the Team). Writing a pool needs global group_policy:update.
CREATE OR REPLACE FUNCTION api.get_group_token_pools(
    p_kind TEXT DEFAULT NULL,
    p_name TEXT DEFAULT NULL
)
RETURNS TABLE (
    kind TEXT,
    name TEXT,
    token_limit BIGINT,
    period TEXT,
    used BIGINT,
    remaining BIGINT,
    period_start DATE,
    resets_at DATE
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF api.has_permission(auth.uid(), 'group_policy:read', NULL) IS NOT TRUE
        AND NOT (p_kind IS NOT DISTINCT FROM 'Team' AND p_name IS NOT NULL
                 AND api.can_read_team_usage(auth.uid(), p_name) IS TRUE) THEN
        RAISE insufficient_privilege
            USING message = 'global group_policy:read is required to read group token pools, or team_usage:read to read that Team''s pool';
    END IF;

    RETURN QUERY
    SELECT x.kind, x.name, x.token_limit, x.period, x.used, x.token_limit - x.used,
           api.api_key_period_start(x.period), api.api_key_period_reset(x.period)
    FROM (
        SELECT (p.spec).subject_kind AS kind,
               (p.spec).subject_name AS name,
               ((p.spec).token_quota ->> 'limit')::BIGINT AS token_limit,
               (p.spec).token_quota ->> 'period' AS period,
               api.group_token_pool_usage((p.spec).subject_kind, (p.spec).subject_name,
                                          (p.spec).token_quota ->> 'period') AS used
        FROM api.group_policies p
        WHERE (p.spec).token_quota IS NOT NULL
          AND (p_kind IS NULL OR (p.spec).subject_kind = p_kind)
          AND (p_name IS NULL OR (p.spec).subject_name = p_name)
    ) x
    ORDER BY x.kind, x.name;
END;
$$;

CREATE OR REPLACE FUNCTION api.get_user_group_policy(p_user_id UUID, p_workspace TEXT)
RETURNS JSONB
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_allowed TEXT[];
    v_denied TEXT[];
BEGIN
    IF auth.uid() IS DISTINCT FROM p_user_id
        AND api.has_permission_on_subject(auth.uid(), 'group_policy:read', p_user_id) IS NOT TRUE THEN
        RAISE insufficient_privilege
            USING message = 'group_policy:read is required to read the group policy of another user';
    END IF;

    IF p_workspace IS NOT NULL AND btrim(p_workspace) = '' THEN
        RAISE invalid_parameter_value
            USING message = 'p_workspace must be a workspace name or null';
    END IF;

    IF p_workspace IS NOT NULL THEN
        SELECT p.allowed_models, p.denied_models INTO v_allowed, v_denied
        FROM api.user_group_model_policy(p_user_id, p_workspace) p;
    END IF;

    RETURN jsonb_build_object(
        'allowed_models', to_jsonb(v_allowed),
        'denied_models', to_jsonb(v_denied),
        'groups', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'kind', g.kind,
                    'name', g.name,
                    'display_name', api.group_display_name(g.kind, g.name),
                    'active', g.active,
                    'allowed_models', (p.spec).allowed_models,
                    'denied_models', (p.spec).denied_models,
                    'token_quota', (p.spec).token_quota)
                ORDER BY g.kind, g.name)
            FROM api.user_groups(p_user_id) g
            JOIN api.group_policies p
              ON (p.spec).subject_kind = g.kind
             AND (p.spec).subject_name = g.name
        ), '[]'::jsonb),
        'token_pools', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'kind', t.kind,
                    'name', t.name,
                    'display_name', api.group_display_name(t.kind, t.name),
                    'token_limit', t.token_limit,
                    'period', t.period,
                    'used', t.used,
                    'remaining', t.token_limit - t.used,
                    'resets_at', api.api_key_period_reset(t.period))
                ORDER BY t.kind, t.name)
            FROM api.user_group_token_pools(p_user_id) t
        ), '[]'::jsonb));
END;
$$;

DROP FUNCTION IF EXISTS api.group_policy_scope_display_name(api.group_policy_spec);
DROP FUNCTION IF EXISTS api.can_read_principal_group_policy(UUID, UUID);
DROP FUNCTION IF EXISTS api.principal_group_policies(UUID);
DROP FUNCTION IF EXISTS api.has_group_policy_scoped_permission(UUID, api.permission_action, api.group_policy_spec);
DROP FUNCTION IF EXISTS api.group_policy_in_scope(api.group_policy_spec, UUID);

DROP INDEX IF EXISTS api.group_daily_usage_user_idx;
DROP INDEX IF EXISTS api.group_daily_usage_service_account_idx;

DROP INDEX IF EXISTS api.group_policies_subject_unique_idx;

ALTER TYPE api.group_policy_spec DROP ATTRIBUTE IF EXISTS scope_name;
ALTER TYPE api.group_policy_spec DROP ATTRIBUTE IF EXISTS scope_kind;

CREATE UNIQUE INDEX group_policies_subject_unique_idx
    ON api.group_policies (((spec).subject_kind), ((spec).subject_name));

CREATE OR REPLACE FUNCTION api.update_team_admin_permissions()
RETURNS VOID AS $$
BEGIN
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, ARRAY[
        'service_account:read',
        'service_account:create',
        'service_account:update',
        'service_account:delete',
        'team_usage:read',
        'team:read',
        'team:assign-member'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'team-admin';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.update_department_admin_permissions()
RETURNS VOID AS $$
BEGIN
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, ARRAY[
        'user_profile:read',
        'user_profile:update',
        'role_assignment:read',
        'role_assignment:create',
        'role_assignment:update',
        'role_assignment:delete',
        'role:read',
        'org_unit:read',
        'org_unit:assign-member',
        'org_unit:usage-read',
        'group_policy:read'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'department-admin';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_team_admin_permissions();

SELECT api.update_department_admin_permissions();
