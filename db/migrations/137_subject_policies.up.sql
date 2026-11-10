-- Group policies on one user or one ServiceAccount, written in a scope (a Team
-- or an OrgUnit) by whoever manages that scope. They only narrow: each one's
-- allow list intersects with the rest and each pool counts the subject's own
-- usage, so the strictest pool rejects.

ALTER TYPE api.group_policy_spec ADD ATTRIBUTE scope_kind TEXT;
ALTER TYPE api.group_policy_spec ADD ATTRIBUTE scope_name TEXT;

DROP INDEX IF EXISTS api.group_policies_subject_unique_idx;

CREATE UNIQUE INDEX group_policies_subject_unique_idx
    ON api.group_policies (((spec).subject_kind), ((spec).subject_name),
                           (COALESCE((spec).scope_kind, '')), (COALESCE((spec).scope_name, '')));

CREATE INDEX group_policies_scope_idx
    ON api.group_policies (((spec).scope_kind), ((spec).scope_name));

CREATE INDEX IF NOT EXISTS group_daily_usage_user_idx
    ON api.group_daily_usage (user_id, usage_date) WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS group_daily_usage_service_account_idx
    ON api.group_daily_usage (service_account_id, usage_date) WHERE service_account_id IS NOT NULL;

-- ----------------------
-- Subjects and scopes
-- ----------------------

CREATE OR REPLACE FUNCTION api.is_subject_policy_kind(p_kind TEXT)
RETURNS BOOLEAN
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT COALESCE(p_kind IN ('User', 'ServiceAccount'), FALSE);
$$;

-- Through api.user_groups, so "in a scope" means what group membership means
-- everywhere else.
CREATE OR REPLACE FUNCTION api.group_policy_in_scope(p_spec api.group_policy_spec, p_principal UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT p_spec.scope_kind IS NULL
        OR EXISTS (SELECT 1 FROM api.user_groups(p_principal) g
                   WHERE g.kind = p_spec.scope_kind AND g.name = p_spec.scope_name);
$$;

REVOKE ALL ON FUNCTION api.group_policy_in_scope(api.group_policy_spec, UUID) FROM PUBLIC;

-- The users and SAs a scope manages: a Team's members and SAs, an OrgUnit's
-- subtree users.
CREATE OR REPLACE FUNCTION api.scope_principals(p_kind TEXT, p_name TEXT)
RETURNS TABLE (kind TEXT, id UUID)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT 'User'::TEXT, tm.user_id
    FROM api.teams t JOIN api.team_members tm ON tm.team_id = t.id
    WHERE p_kind = 'Team' AND (t.metadata).name = p_name
    UNION
    SELECT 'ServiceAccount'::TEXT, sa.id
    FROM api.teams t JOIN api.service_accounts sa ON sa.team_id = t.id
    WHERE p_kind = 'Team' AND (t.metadata).name = p_name
    UNION
    SELECT 'User'::TEXT, m.user_id
    FROM api.org_units o CROSS JOIN LATERAL api.org_unit_subtree_members(o.id) m
    WHERE p_kind = 'OrgUnit' AND (o.metadata).name = p_name;
$$;

REVOKE ALL ON FUNCTION api.scope_principals(TEXT, TEXT) FROM PUBLIC;

-- p_permission on a Team or an OrgUnit, by name: globally or through an
-- assignment scoped to it. FALSE for an unknown name.
CREATE OR REPLACE FUNCTION api.has_permission_on_scope(
    p_user UUID,
    p_permission api.permission_action,
    p_kind TEXT,
    p_name TEXT
)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_id INTEGER;
BEGIN
    IF p_kind = 'Team' THEN
        SELECT t.id INTO v_id FROM api.teams t WHERE (t.metadata).name = p_name;
        RETURN v_id IS NOT NULL AND api.has_permission_on_team(p_user, p_permission, v_id) IS TRUE;
    ELSIF p_kind = 'OrgUnit' THEN
        SELECT o.id INTO v_id FROM api.org_units o WHERE (o.metadata).name = p_name;
        RETURN v_id IS NOT NULL AND api.has_permission_on_org_unit(p_user, p_permission, v_id) IS TRUE;
    END IF;

    RETURN FALSE;
END;
$$;

-- The scopes ('Team/<name>', 'OrgUnit/<name>') where p_user holds
-- p_permission through a scoped assignment. The RLS policies evaluate it once
-- per statement instead of once per row.
CREATE OR REPLACE FUNCTION api.group_policy_permitted_scopes(p_user UUID, p_permission api.permission_action)
RETURNS TEXT[]
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT ARRAY(
        SELECT 'Team/' || (t.metadata).name FROM api.teams t
        WHERE api.has_team_scoped_permission(p_user, p_permission, t.id) IS TRUE
        UNION ALL
        SELECT 'OrgUnit/' || (o.metadata).name FROM api.org_units o
        WHERE api.has_org_scoped_permission(p_user, p_permission, o.id) IS TRUE);
$$;

-- ----------------------
-- Write checks
-- ----------------------

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
    v_id UUID;
BEGIN
    IF COALESCE(v_meta.workspace, '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10299","message": "A group policy is global and has no workspace","hint": "Remove metadata.workspace; pin a model to a workspace in its entry instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.subject_kind := NULLIF(btrim(v_spec.subject_kind), '');
    v_spec.subject_name := NULLIF(btrim(v_spec.subject_name), '');
    v_spec.scope_kind := NULLIF(btrim(v_spec.scope_kind), '');
    v_spec.scope_name := NULLIF(btrim(v_spec.scope_name), '');

    -- spec is replaced as a whole on PATCH: a PATCH without them keeps them.
    IF TG_OP = 'UPDATE' AND v_spec.subject_kind IS NULL AND v_spec.subject_name IS NULL THEN
        v_spec.subject_kind := (OLD.spec).subject_kind;
        v_spec.subject_name := (OLD.spec).subject_name;
    END IF;

    IF TG_OP = 'UPDATE' AND v_spec.scope_kind IS NULL AND v_spec.scope_name IS NULL THEN
        v_spec.scope_kind := (OLD.spec).scope_kind;
        v_spec.scope_name := (OLD.spec).scope_name;
    END IF;

    IF api.is_subject_policy_kind(v_spec.subject_kind) AND v_spec.subject_name IS NOT NULL THEN
        BEGIN
            v_id := v_spec.subject_name::uuid;
        EXCEPTION WHEN invalid_text_representation THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10294","message": "spec.subject_name does not name a subject of spec.subject_kind","hint": "Use the id of a user or a ServiceAccount"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END;

        v_spec.subject_name := v_id::text;
    END IF;

    IF TG_OP = 'UPDATE' AND (
        v_spec.subject_kind IS DISTINCT FROM (OLD.spec).subject_kind
        OR v_spec.subject_name IS DISTINCT FROM (OLD.spec).subject_name
        OR v_spec.scope_kind IS DISTINCT FROM (OLD.spec).scope_kind
        OR v_spec.scope_name IS DISTINCT FROM (OLD.spec).scope_name
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10301","message": "The subject or scope of a group policy cannot be changed","hint": "Delete the policy and create another one"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.subject_kind IS NULL OR v_spec.subject_kind NOT IN ('Team', 'OrgUnit', 'User', 'ServiceAccount') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10300","message": "spec.subject_kind of a group policy must be Team, OrgUnit, User or ServiceAccount","hint": "Set spec.subject_name to the metadata.name of the group, or the id of the user or ServiceAccount"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF (v_spec.scope_kind IS NULL) <> (v_spec.scope_name IS NULL)
        OR (v_spec.subject_kind IN ('Team', 'OrgUnit') AND v_spec.scope_kind IS NOT NULL)
        OR (v_spec.subject_kind = 'ServiceAccount' AND v_spec.scope_kind IS DISTINCT FROM 'Team' AND v_spec.scope_kind IS NOT NULL)
        OR (v_spec.subject_kind = 'User' AND v_spec.scope_kind NOT IN ('Team', 'OrgUnit')) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10328","message": "spec.scope_kind does not fit spec.subject_kind","hint": "A Team or OrgUnit policy has no scope; a user policy is scoped to a Team, an OrgUnit or nothing; a ServiceAccount policy to its Team or nothing"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'INSERT' AND (
        v_spec.subject_name IS NULL
        OR (v_spec.subject_kind = 'Team'
            AND NOT EXISTS (SELECT 1 FROM api.teams t WHERE (t.metadata).name = v_spec.subject_name))
        OR (v_spec.subject_kind = 'OrgUnit'
            AND NOT EXISTS (SELECT 1 FROM api.org_units u WHERE (u.metadata).name = v_spec.subject_name))
        OR (v_spec.subject_kind = 'User'
            AND NOT EXISTS (SELECT 1 FROM api.user_profiles p WHERE p.id = v_id))
        OR (v_spec.subject_kind = 'ServiceAccount'
            AND NOT EXISTS (SELECT 1 FROM api.service_accounts s WHERE s.id = v_id))
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10294","message": "spec.subject_name does not name a subject of spec.subject_kind","hint": "Use the metadata.name of a Team or an OrgUnit, or the id of a user or a ServiceAccount"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Only on create: a subject that leaves the scope later keeps the row,
    -- which stops applying, and may still be edited or deleted.
    IF TG_OP = 'INSERT' AND v_spec.scope_kind IS NOT NULL
        AND api.group_policy_in_scope(v_spec, v_id) IS NOT TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10329","message": "The subject is not in the scope of the policy","hint": "Scope a user policy to a Team the user is in or an OrgUnit above the user''s department, and a ServiceAccount policy to its Team"}',
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
            IF v_spec.scope_kind IS NOT NULL THEN
                v_name := v_name || '-' || lower(v_spec.scope_kind) || '-' || v_spec.scope_name;
            END IF;
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


-- A policy goes with its group, and with the scope it was written in.
CREATE OR REPLACE FUNCTION api.group_policies_delete_with_group()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    DELETE FROM api.group_policies p
    WHERE (p.spec).subject_kind = TG_ARGV[0] AND (p.spec).subject_name = (OLD.metadata).name;

    DELETE FROM api.group_policies p
    WHERE (p.spec).scope_kind = TG_ARGV[0] AND (p.spec).scope_name = (OLD.metadata).name;

    RETURN OLD;
END;
$$;

CREATE OR REPLACE FUNCTION api.group_policies_delete_with_subject()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    DELETE FROM api.group_policies p
    WHERE (p.spec).subject_kind = TG_ARGV[0]
      AND (p.spec).subject_name = OLD.id::text;

    RETURN OLD;
END;
$$;

REVOKE ALL ON FUNCTION api.group_policies_delete_with_subject() FROM PUBLIC;

CREATE TRIGGER user_profiles_delete_group_policy
    AFTER DELETE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.group_policies_delete_with_subject('User');

CREATE TRIGGER service_accounts_delete_group_policy
    AFTER DELETE ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION api.group_policies_delete_with_subject('ServiceAccount');


-- ----------------------
-- RLS
-- ----------------------
-- A subject policy is also read and written through group_policy:* on its
-- scope, and read by its subject.

DROP POLICY IF EXISTS "group_policy read policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy create policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy update policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy delete policy" ON api.group_policies;

CREATE POLICY "group_policy read policy" ON api.group_policies
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:read', NULL))
        OR (api.is_subject_policy_kind((spec).subject_kind) AND (
            (spec).subject_name = (SELECT auth.uid()::text)
            OR (spec).scope_kind || '/' || (spec).scope_name
                = ANY ((SELECT api.group_policy_permitted_scopes(auth.uid(), 'group_policy:read'))::TEXT[])))
    );

CREATE POLICY "group_policy create policy" ON api.group_policies
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'group_policy:create', NULL))
        OR (api.is_subject_policy_kind((spec).subject_kind)
            AND (spec).scope_kind || '/' || (spec).scope_name
                = ANY ((SELECT api.group_policy_permitted_scopes(auth.uid(), 'group_policy:create'))::TEXT[]))
    );

CREATE POLICY "group_policy update policy" ON api.group_policies
    FOR UPDATE
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL))
        OR (api.is_subject_policy_kind((spec).subject_kind)
            AND (spec).scope_kind || '/' || (spec).scope_name
                = ANY ((SELECT api.group_policy_permitted_scopes(auth.uid(), 'group_policy:update'))::TEXT[]))
    )
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL))
        OR (api.is_subject_policy_kind((spec).subject_kind)
            AND (spec).scope_kind || '/' || (spec).scope_name
                = ANY ((SELECT api.group_policy_permitted_scopes(auth.uid(), 'group_policy:update'))::TEXT[]))
    );

CREATE POLICY "group_policy delete policy" ON api.group_policies
    FOR DELETE
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:delete', NULL))
        OR (api.is_subject_policy_kind((spec).subject_kind)
            AND (spec).scope_kind || '/' || (spec).scope_name
                = ANY ((SELECT api.group_policy_permitted_scopes(auth.uid(), 'group_policy:delete'))::TEXT[]))
    );

-- ----------------------
-- Merge
-- ----------------------

-- Every policy of a user or an SA: its groups' (Inactive ones included) and
-- its own. Only rows with in_scope apply; the rest are listed for readers.
CREATE OR REPLACE FUNCTION api.principal_group_policies(p_principal UUID)
RETURNS TABLE (kind TEXT, name TEXT, active BOOLEAN, in_scope BOOLEAN, policy_name TEXT, spec api.group_policy_spec)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT g.kind, g.name, g.active, TRUE, (p.metadata).name, p.spec
    FROM api.user_groups(p_principal) g
    JOIN api.group_policies p
      ON (p.spec).subject_kind = g.kind
     AND (p.spec).subject_name = g.name
    UNION ALL
    SELECT (p.spec).subject_kind, (p.spec).subject_name, TRUE,
           api.group_policy_in_scope(p.spec, p_principal), (p.metadata).name, p.spec
    FROM api.group_policies p
    WHERE api.is_subject_policy_kind((p.spec).subject_kind)
      AND (p.spec).subject_name = p_principal::text;
$$;

REVOKE ALL ON FUNCTION api.principal_group_policies(UUID) FROM PUBLIC;

-- has_pool: a token pool applies, so the controller attaches the quota plugin.
DROP FUNCTION IF EXISTS api.user_group_model_policy(UUID, TEXT);

CREATE FUNCTION api.user_group_model_policy(
    p_user_id UUID,
    p_workspace TEXT,
    OUT allowed_models TEXT[],
    OUT denied_models TEXT[],
    OUT has_pool BOOLEAN
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    -- OrgUnits and subject policies: each one narrows.
    v_narrow TEXT[];
    v_team TEXT[];
    v_allow TEXT[];
    r RECORD;
BEGIN
    denied_models := ARRAY[]::TEXT[];
    has_pool := FALSE;

    FOR r IN
        SELECT g.kind,
               (g.spec).token_quota IS NOT NULL AS pool,
               api.group_policy_model_names((g.spec).allowed_models, p_workspace) AS allow,
               api.group_policy_model_names((g.spec).denied_models, p_workspace) AS deny
        FROM api.principal_group_policies(p_user_id) g
        WHERE g.in_scope
    LOOP
        has_pool := has_pool OR r.pool;
        denied_models := denied_models || COALESCE(r.deny, ARRAY[]::TEXT[]);

        CONTINUE WHEN r.allow IS NULL;

        IF r.kind = 'Team' THEN
            v_team := COALESCE(v_team, ARRAY[]::TEXT[]) || r.allow;
        ELSE
            v_narrow := CASE WHEN v_narrow IS NULL THEN r.allow
                ELSE ARRAY(SELECT unnest(v_narrow) INTERSECT SELECT unnest(r.allow)) END;
        END IF;
    END LOOP;

    IF v_narrow IS NOT NULL AND v_team IS NOT NULL THEN
        v_allow := ARRAY(SELECT unnest(v_narrow) INTERSECT SELECT unnest(v_team));
    ELSE
        v_allow := COALESCE(v_narrow, v_team);
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

REVOKE ALL ON FUNCTION api.user_group_model_policy(UUID, TEXT) FROM PUBLIC;

-- ----------------------
-- Pools
-- ----------------------

-- A User or ServiceAccount pool counts the subject's own usage; p_name is its
-- id.
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
    v_principal UUID;
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
    ELSIF api.is_subject_policy_kind(p_kind) THEN
        -- Users and SAs share one id space, and a row has one of the two.
        v_principal := p_name::uuid;

        SELECT SUM(u.total_tokens) INTO v_used
        FROM api.group_daily_usage u
        WHERE (u.user_id = v_principal OR u.service_account_id = v_principal)
          AND u.usage_date >= v_start
          AND u.usage_date <= CURRENT_DATE;
    END IF;

    RETURN COALESCE(v_used, 0)::BIGINT;
END;
$$;

-- The return type gains the scope; plpgsql callers read columns by name.
DROP FUNCTION IF EXISTS api.user_group_token_pools(UUID);

CREATE FUNCTION api.user_group_token_pools(p_user_id UUID)
RETURNS TABLE (kind TEXT, name TEXT, token_limit BIGINT, period TEXT, used BIGINT,
               scope_kind TEXT, scope_name TEXT)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT g.kind, g.name,
           ((g.spec).token_quota ->> 'limit')::BIGINT,
           (g.spec).token_quota ->> 'period',
           api.group_token_pool_usage(g.kind, g.name, (g.spec).token_quota ->> 'period'),
           (g.spec).scope_kind,
           (g.spec).scope_name
    FROM api.principal_group_policies(p_user_id) g
    WHERE g.in_scope AND (g.spec).token_quota IS NOT NULL;
$$;

REVOKE ALL ON FUNCTION api.user_group_token_pools(UUID) FROM PUBLIC;

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
    v_pool BOOLEAN;
    v_list JSONB;
BEGIN
    SELECT p.allowed_models, p.denied_models, p.has_pool INTO v_allowed, v_denied, v_pool
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
        'group_token_pool', COALESCE(v_pool, FALSE));
END;
$$;

-- The pools of groups only; a subject's pools come with its policy
-- (get_user_group_policy, get_scope_subject_policies).
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
          AND (p.spec).subject_kind IN ('Team', 'OrgUnit')
          AND (p_kind IS NULL OR (p.spec).subject_kind = p_kind)
          AND (p_name IS NULL OR (p.spec).subject_name = p_name)
    ) x
    ORDER BY x.kind, x.name;
END;
$$;


-- ----------------------
-- Reading a subject's policy
-- ----------------------

-- Itself; group_policy:read on the user (globally or on its subtree), on the
-- SA's Team, or on a Team the user is in.
CREATE OR REPLACE FUNCTION api.can_read_principal_group_policy(p_reader UUID, p_principal UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT p_reader IS NOT NULL AND p_principal IS NOT NULL AND (
        p_reader = p_principal
        OR api.has_permission_on_subject(p_reader, 'group_policy:read', p_principal) IS TRUE
        OR api.has_permission_on_service_account(p_reader, 'group_policy:read', p_principal) IS TRUE
        OR EXISTS (
            SELECT 1 FROM api.user_groups(p_principal) g
            WHERE g.kind = 'Team'
              AND api.has_permission_on_scope(p_reader, 'group_policy:read', 'Team', g.name)));
$$;

REVOKE ALL ON FUNCTION api.can_read_principal_group_policy(UUID, UUID) FROM PUBLIC;

-- p_user_id may be a user or an SA. subject_policies lists its own policies,
-- those out of scope too (in_scope FALSE).
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
    v_out JSONB;
BEGIN
    IF api.can_read_principal_group_policy(auth.uid(), p_user_id) IS NOT TRUE THEN
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

    WITH g AS (
        SELECT * FROM api.principal_group_policies(p_user_id)
    ), pools AS (
        SELECT g.kind, g.name, (g.spec).scope_kind AS scope_kind, (g.spec).scope_name AS scope_name,
               ((g.spec).token_quota ->> 'limit')::BIGINT AS token_limit,
               (g.spec).token_quota ->> 'period' AS period,
               api.group_token_pool_usage(g.kind, g.name, (g.spec).token_quota ->> 'period') AS used
        FROM g
        WHERE g.in_scope AND (g.spec).token_quota IS NOT NULL
    )
    SELECT jsonb_build_object(
        'groups', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'kind', g.kind,
                    'name', g.name,
                    'display_name', api.group_display_name(g.kind, g.name),
                    'active', g.active,
                    'allowed_models', (g.spec).allowed_models,
                    'denied_models', (g.spec).denied_models,
                    'token_quota', (g.spec).token_quota)
                ORDER BY g.kind, g.name)
            FROM g WHERE NOT api.is_subject_policy_kind(g.kind)), '[]'::jsonb),
        'subject_policies', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'name', g.policy_name,
                    'subject_kind', g.kind,
                    'scope_kind', (g.spec).scope_kind,
                    'scope_name', (g.spec).scope_name,
                    'scope_display_name', api.group_display_name((g.spec).scope_kind, (g.spec).scope_name),
                    'in_scope', g.in_scope,
                    'allowed_models', (g.spec).allowed_models,
                    'denied_models', (g.spec).denied_models,
                    'token_quota', (g.spec).token_quota)
                ORDER BY (g.spec).scope_kind NULLS FIRST, (g.spec).scope_name)
            FROM g WHERE api.is_subject_policy_kind(g.kind)), '[]'::jsonb),
        'token_pools', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'kind', t.kind,
                    'name', t.name,
                    'display_name', api.group_display_name(t.kind, t.name),
                    'scope_kind', t.scope_kind,
                    'scope_name', t.scope_name,
                    'scope_display_name', api.group_display_name(t.scope_kind, t.scope_name),
                    'token_limit', t.token_limit,
                    'period', t.period,
                    'used', t.used,
                    'remaining', t.token_limit - t.used,
                    'resets_at', api.api_key_period_reset(t.period))
                ORDER BY t.kind, t.name, t.scope_kind NULLS FIRST, t.scope_name)
            FROM pools t), '[]'::jsonb))
    INTO v_out;

    RETURN jsonb_build_object('allowed_models', to_jsonb(v_allowed), 'denied_models', to_jsonb(v_denied)) || v_out;
END;
$$;

-- Each subject of a scope with its policy there (id NULL when none) and that
-- policy's pool; effective_remaining is the least remaining of every pool
-- that applies to the subject, from any group or scope.
CREATE OR REPLACE FUNCTION api.get_scope_subject_policies(p_scope_kind TEXT, p_scope_name TEXT)
RETURNS TABLE (
    subject_kind TEXT,
    subject_name TEXT,
    id INTEGER,
    policy_name TEXT,
    allowed_models JSONB,
    denied_models JSONB,
    token_quota JSONB,
    pool JSONB,
    effective_remaining BIGINT
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF p_scope_kind IS NULL OR p_scope_kind NOT IN ('Team', 'OrgUnit') THEN
        RAISE invalid_parameter_value
            USING message = 'p_scope_kind must be Team or OrgUnit';
    END IF;

    IF api.has_permission_on_scope(auth.uid(), 'group_policy:read', p_scope_kind, p_scope_name) IS NOT TRUE THEN
        RAISE insufficient_privilege
            USING message = 'group_policy:read on the scope is required to read its subject policies';
    END IF;

    RETURN QUERY
    WITH s AS (
        SELECT * FROM api.scope_principals(p_scope_kind, p_scope_name)
    ), sp AS (
        -- Every pool that applies to each subject.
        SELECT s.id AS sid, g.kind, g.name,
               ((g.spec).token_quota ->> 'limit')::BIGINT AS token_limit,
               (g.spec).token_quota ->> 'period' AS period
        FROM s CROSS JOIN LATERAL api.principal_group_policies(s.id) g
        WHERE g.in_scope AND (g.spec).token_quota IS NOT NULL
    ), pu AS (
        -- Once per pool: a Team's or OrgUnit's is shared by its subjects.
        SELECT DISTINCT ON (k.kind, k.name, k.period) k.kind, k.name, k.period,
               api.group_token_pool_usage(k.kind, k.name, k.period) AS used
        FROM (SELECT DISTINCT sp.kind, sp.name, sp.period FROM sp) k
    ), eff AS (
        SELECT sp.sid, MIN(sp.token_limit - u.used) AS remaining
        FROM sp JOIN pu u USING (kind, name, period)
        GROUP BY sp.sid
    )
    SELECT s.kind,
           s.id::TEXT,
           p.id,
           (p.metadata).name,
           (p.spec).allowed_models,
           (p.spec).denied_models,
           (p.spec).token_quota,
           CASE WHEN (p.spec).token_quota IS NOT NULL THEN jsonb_build_object(
               'used', u.used,
               'remaining', ((p.spec).token_quota ->> 'limit')::BIGINT - u.used,
               'resets_at', api.api_key_period_reset((p.spec).token_quota ->> 'period')) END,
           e.remaining
    FROM s
    LEFT JOIN api.group_policies p
      ON (p.spec).subject_kind = s.kind
     AND (p.spec).subject_name = s.id::TEXT
     AND (p.spec).scope_kind = p_scope_kind
     AND (p.spec).scope_name = p_scope_name
    LEFT JOIN pu u
      ON u.kind = s.kind AND u.name = s.id::TEXT AND u.period = (p.spec).token_quota ->> 'period'
    LEFT JOIN eff e ON e.sid = s.id
    ORDER BY s.kind DESC, s.id;
END;
$$;

-- ----------------------
-- Preset roles
-- ----------------------
-- team-admin and department-admin write the subject policies of their scope.
-- The policies of Teams and OrgUnits still need a global assignment.

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
        'team:assign-member',
        'group_policy:read',
        'group_policy:create',
        'group_policy:update',
        'group_policy:delete'
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
        'group_policy:read',
        'group_policy:create',
        'group_policy:update',
        'group_policy:delete'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'department-admin';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_team_admin_permissions();

SELECT api.update_department_admin_permissions();
