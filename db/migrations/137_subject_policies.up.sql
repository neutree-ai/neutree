-- Group policies on one user or one ServiceAccount, written by whoever manages
-- them in a scope.

-- ----------------------
-- Subjects and scopes
-- ----------------------
-- Besides a Team or an OrgUnit, spec.subject_kind may be:
--   * 'User': spec.subject_name is the user's id;
--   * 'ServiceAccount': spec.subject_name is the SA's id.
-- spec.scope_kind / spec.scope_name say who manages such a policy:
--   * none: global group_policy:* only;
--   * 'Team': group_policy:* on that Team; the user is a member, or the SA
--     belongs to it;
--   * 'OrgUnit' (users only): group_policy:* on that OrgUnit; the user's
--     primary OrgUnit is in its subtree.
-- A subject has at most one policy per scope. A Team or OrgUnit policy has no
-- scope. Subject and scope do not change after creation.
--
-- A subject policy applies while its subject is in its scope. Every one that
-- applies is one more constraint: its allow list intersects with the rest
-- (Team allow lists still unite among themselves, as before), its deny list
-- adds to the union, and its token pool is the subject's own usage, all keys,
-- every workspace. So a policy can only narrow what other policies allow, and
-- several scopes setting a pool for one subject means the strictest one wins.

ALTER TYPE api.group_policy_spec ADD ATTRIBUTE scope_kind TEXT;
ALTER TYPE api.group_policy_spec ADD ATTRIBUTE scope_name TEXT;

DROP INDEX IF EXISTS api.group_policies_subject_unique_idx;

CREATE UNIQUE INDEX group_policies_subject_unique_idx
    ON api.group_policies (((spec).subject_kind), ((spec).subject_name),
                           (COALESCE((spec).scope_kind, '')), (COALESCE((spec).scope_name, '')));

-- Pools of one user or SA read their rows by owner.
CREATE INDEX IF NOT EXISTS group_daily_usage_user_idx
    ON api.group_daily_usage (user_id, usage_date) WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS group_daily_usage_service_account_idx
    ON api.group_daily_usage (service_account_id, usage_date) WHERE service_account_id IS NOT NULL;

-- ----------------------
-- Scope
-- ----------------------

-- Is p_principal (a user or an SA) in the scope of p_spec now? A policy
-- without a scope covers its subject everywhere.
CREATE OR REPLACE FUNCTION api.group_policy_in_scope(p_spec api.group_policy_spec, p_principal UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT CASE
        WHEN p_spec.scope_kind IS NULL THEN TRUE
        WHEN p_spec.scope_kind = 'Team' THEN EXISTS (
            SELECT 1
            FROM api.teams t
            WHERE (t.metadata).name = p_spec.scope_name
              AND (EXISTS (SELECT 1 FROM api.team_members tm
                           WHERE tm.team_id = t.id AND tm.user_id = p_principal)
                   OR EXISTS (SELECT 1 FROM api.service_accounts sa
                              WHERE sa.id = p_principal AND sa.team_id = t.id)))
        WHEN p_spec.scope_kind = 'OrgUnit' THEN EXISTS (
            SELECT 1
            FROM api.org_unit_members m
            JOIN api.org_units u ON u.id = m.org_unit_id
            JOIN api.org_units r ON (r.metadata).name = p_spec.scope_name
            WHERE m.user_id = p_principal
              AND r.id = ANY (api.org_unit_ancestor_ids(u.path)))
        ELSE FALSE
    END;
$$;

REVOKE ALL ON FUNCTION api.group_policy_in_scope(api.group_policy_spec, UUID) FROM PUBLIC;

-- May p_user do p_permission on a subject policy through its scope? Global
-- assignments are checked apart, once per statement. Team and OrgUnit
-- policies have no scope, so FALSE.
CREATE OR REPLACE FUNCTION api.has_group_policy_scoped_permission(
    p_user UUID,
    p_permission api.permission_action,
    p_spec api.group_policy_spec
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
    IF p_user IS NULL OR p_spec.subject_kind NOT IN ('User', 'ServiceAccount') THEN
        RETURN FALSE;
    END IF;

    IF p_spec.scope_kind = 'Team' THEN
        SELECT t.id INTO v_id FROM api.teams t WHERE (t.metadata).name = p_spec.scope_name;

        RETURN v_id IS NOT NULL
            AND api.has_team_scoped_permission(p_user, p_permission, v_id) IS TRUE;
    ELSIF p_spec.scope_kind = 'OrgUnit' THEN
        SELECT o.id INTO v_id FROM api.org_units o WHERE (o.metadata).name = p_spec.scope_name;

        RETURN v_id IS NOT NULL
            AND api.has_org_scoped_permission(p_user, p_permission, v_id) IS TRUE;
    END IF;

    RETURN FALSE;
END;
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

    IF v_spec.subject_kind IN ('User', 'ServiceAccount') AND v_spec.subject_name IS NOT NULL THEN
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
    WHERE ((p.spec).subject_kind = TG_ARGV[0] AND (p.spec).subject_name = (OLD.metadata).name)
       OR ((p.spec).scope_kind = TG_ARGV[0] AND (p.spec).scope_name = (OLD.metadata).name);

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
-- Global group_policy:* reads and writes every policy. A subject policy is
-- also readable and writable through group_policy:* on its scope, and
-- readable by its subject.

DROP POLICY IF EXISTS "group_policy read policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy create policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy update policy" ON api.group_policies;
DROP POLICY IF EXISTS "group_policy delete policy" ON api.group_policies;

CREATE POLICY "group_policy read policy" ON api.group_policies
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:read', NULL))
        OR ((spec).subject_kind IN ('User', 'ServiceAccount') AND (spec).subject_name = auth.uid()::text)
        OR api.has_group_policy_scoped_permission(auth.uid(), 'group_policy:read', spec)
    );

CREATE POLICY "group_policy create policy" ON api.group_policies
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'group_policy:create', NULL))
        OR api.has_group_policy_scoped_permission(auth.uid(), 'group_policy:create', spec)
    );

CREATE POLICY "group_policy update policy" ON api.group_policies
    FOR UPDATE
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL))
        OR api.has_group_policy_scoped_permission(auth.uid(), 'group_policy:update', spec)
    )
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'group_policy:update', NULL))
        OR api.has_group_policy_scoped_permission(auth.uid(), 'group_policy:update', spec)
    );

CREATE POLICY "group_policy delete policy" ON api.group_policies
    FOR DELETE
    USING (
        (SELECT api.has_permission(auth.uid(), 'group_policy:delete', NULL))
        OR api.has_group_policy_scoped_permission(auth.uid(), 'group_policy:delete', spec)
    );

-- ----------------------
-- Merge
-- ----------------------

-- Every policy that applies to a user or an SA: those of its groups (Inactive
-- ones included, as before) and its own ones that are in scope.
CREATE OR REPLACE FUNCTION api.principal_group_policies(p_principal UUID)
RETURNS TABLE (kind TEXT, name TEXT, active BOOLEAN, spec api.group_policy_spec)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT g.kind, g.name, g.active, p.spec
    FROM api.user_groups(p_principal) g
    JOIN api.group_policies p
      ON (p.spec).subject_kind = g.kind
     AND (p.spec).subject_name = g.name
    UNION ALL
    SELECT (p.spec).subject_kind, (p.spec).subject_name, TRUE, p.spec
    FROM api.group_policies p
    WHERE (p.spec).subject_kind IN ('User', 'ServiceAccount')
      AND (p.spec).subject_name = p_principal::text
      AND api.group_policy_in_scope(p.spec, p_principal);
$$;

REVOKE ALL ON FUNCTION api.principal_group_policies(UUID) FROM PUBLIC;

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
    -- OrgUnits and subject policies: each one narrows.
    v_narrow TEXT[];
    v_team TEXT[];
    v_allow TEXT[];
    r RECORD;
BEGIN
    denied_models := ARRAY[]::TEXT[];

    FOR r IN
        SELECT g.kind,
               api.group_policy_model_names((g.spec).allowed_models, p_workspace) AS allow,
               api.group_policy_model_names((g.spec).denied_models, p_workspace) AS deny
        FROM api.principal_group_policies(p_user_id) g
    LOOP
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
    ELSIF p_kind = 'User' THEN
        SELECT SUM(u.total_tokens) INTO v_used
        FROM api.group_daily_usage u
        WHERE u.user_id = p_name::uuid
          AND u.usage_date >= v_start
          AND u.usage_date <= CURRENT_DATE;
    ELSIF p_kind = 'ServiceAccount' THEN
        SELECT SUM(u.total_tokens) INTO v_used
        FROM api.group_daily_usage u
        WHERE u.service_account_id = p_name::uuid
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
    WHERE (g.spec).token_quota IS NOT NULL;
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
            FROM api.principal_group_policies(p_owner_id) g
            WHERE (g.spec).token_quota IS NOT NULL));
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

-- May p_reader read the effective policy of p_principal (a user or an SA)?
-- Itself; group_policy:read globally or on the user's subtree; or
-- group_policy:read on a Team the user is in or the SA belongs to.
CREATE OR REPLACE FUNCTION api.can_read_principal_group_policy(p_reader UUID, p_principal UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF p_reader IS NULL OR p_principal IS NULL THEN
        RETURN FALSE;
    END IF;

    IF p_reader = p_principal
        OR api.has_permission_on_subject(p_reader, 'group_policy:read', p_principal) IS TRUE THEN
        RETURN TRUE;
    END IF;

    RETURN EXISTS (
        SELECT 1
        FROM (
            SELECT tm.team_id FROM api.team_members tm WHERE tm.user_id = p_principal
            UNION
            SELECT sa.team_id FROM api.service_accounts sa WHERE sa.id = p_principal
        ) t
        WHERE api.has_team_scoped_permission(p_reader, 'group_policy:read', t.team_id) IS TRUE);
END;
$$;

REVOKE ALL ON FUNCTION api.can_read_principal_group_policy(UUID, UUID) FROM PUBLIC;

-- The display name of a policy's scope, for readers who cannot read groups.
CREATE OR REPLACE FUNCTION api.group_policy_scope_display_name(p_spec api.group_policy_spec)
RETURNS TEXT
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT CASE WHEN p_spec.scope_kind IS NULL THEN NULL
                ELSE api.group_display_name(p_spec.scope_kind, p_spec.scope_name) END;
$$;

REVOKE ALL ON FUNCTION api.group_policy_scope_display_name(api.group_policy_spec) FROM PUBLIC;

-- p_user_id may be a user or an SA. subject_policies lists its own policies,
-- out-of-scope ones included (in_scope FALSE); token_pools every pool that
-- applies, each with the scope it was set in.
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
        'subject_policies', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'name', (p.metadata).name,
                    'subject_kind', (p.spec).subject_kind,
                    'scope_kind', (p.spec).scope_kind,
                    'scope_name', (p.spec).scope_name,
                    'scope_display_name', api.group_policy_scope_display_name(p.spec),
                    'in_scope', api.group_policy_in_scope(p.spec, p_user_id),
                    'allowed_models', (p.spec).allowed_models,
                    'denied_models', (p.spec).denied_models,
                    'token_quota', (p.spec).token_quota)
                ORDER BY (p.spec).scope_kind NULLS FIRST, (p.spec).scope_name)
            FROM api.group_policies p
            WHERE (p.spec).subject_kind IN ('User', 'ServiceAccount')
              AND (p.spec).subject_name = p_user_id::text
        ), '[]'::jsonb),
        'token_pools', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                    'kind', t.kind,
                    'name', t.name,
                    'display_name', CASE WHEN t.kind IN ('Team', 'OrgUnit')
                                         THEN api.group_display_name(t.kind, t.name) END,
                    'scope_kind', t.scope_kind,
                    'scope_name', t.scope_name,
                    'scope_display_name', CASE WHEN t.scope_kind IS NOT NULL
                                               THEN api.group_display_name(t.scope_kind, t.scope_name) END,
                    'token_limit', t.token_limit,
                    'period', t.period,
                    'used', t.used,
                    'remaining', t.token_limit - t.used,
                    'resets_at', api.api_key_period_reset(t.period))
                ORDER BY t.kind, t.name, t.scope_kind NULLS FIRST, t.scope_name)
            FROM api.user_group_token_pools(p_user_id) t
        ), '[]'::jsonb));
END;
$$;

-- Every subject a scope manages, with its policy in that scope (NULL when
-- none) and the pool in that policy, plus effective_remaining: the least
-- remaining of every pool that applies to the subject, from any group or
-- scope (NULL for no limit). For a Team: its members and its SAs; for an
-- OrgUnit: the users of its subtree. Needs group_policy:read globally or on
-- the scope.
CREATE OR REPLACE FUNCTION api.get_scope_subject_policies(p_scope_kind TEXT, p_scope_name TEXT)
RETURNS TABLE (
    subject_kind TEXT,
    subject_name TEXT,
    policy_name TEXT,
    allowed_models JSONB,
    denied_models JSONB,
    token_quota JSONB,
    used BIGINT,
    remaining BIGINT,
    resets_at DATE,
    effective_remaining BIGINT
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_id INTEGER;
    v_allowed BOOLEAN;
BEGIN
    IF p_scope_kind = 'Team' THEN
        SELECT t.id INTO v_id FROM api.teams t WHERE (t.metadata).name = p_scope_name;
        v_allowed := v_id IS NOT NULL
            AND api.has_permission_on_team(auth.uid(), 'group_policy:read', v_id) IS TRUE;
    ELSIF p_scope_kind = 'OrgUnit' THEN
        SELECT o.id INTO v_id FROM api.org_units o WHERE (o.metadata).name = p_scope_name;
        v_allowed := v_id IS NOT NULL
            AND api.has_permission_on_org_unit(auth.uid(), 'group_policy:read', v_id) IS TRUE;
    ELSE
        RAISE invalid_parameter_value
            USING message = 'p_scope_kind must be Team or OrgUnit';
    END IF;

    IF v_allowed IS NOT TRUE THEN
        RAISE insufficient_privilege
            USING message = 'group_policy:read on the scope is required to read its subject policies';
    END IF;

    RETURN QUERY
    WITH s AS (
        SELECT 'User'::TEXT AS kind, tm.user_id AS id
        FROM api.team_members tm
        WHERE p_scope_kind = 'Team' AND tm.team_id = v_id
        UNION
        SELECT 'ServiceAccount'::TEXT, sa.id
        FROM api.service_accounts sa
        WHERE p_scope_kind = 'Team' AND sa.team_id = v_id
        UNION
        SELECT 'User'::TEXT, m.user_id
        FROM api.org_unit_subtree_members(v_id) m
        WHERE p_scope_kind = 'OrgUnit'
    )
    SELECT s.kind,
           s.id::TEXT,
           (p.metadata).name,
           (p.spec).allowed_models,
           (p.spec).denied_models,
           (p.spec).token_quota,
           u.used,
           ((p.spec).token_quota ->> 'limit')::BIGINT - u.used,
           CASE WHEN (p.spec).token_quota IS NOT NULL
                THEN api.api_key_period_reset((p.spec).token_quota ->> 'period') END,
           (SELECT MIN(x.token_limit - x.used) FROM api.user_group_token_pools(s.id) x)
    FROM s
    LEFT JOIN api.group_policies p
      ON (p.spec).subject_kind = s.kind
     AND (p.spec).subject_name = s.id::TEXT
     AND (p.spec).scope_kind = p_scope_kind
     AND (p.spec).scope_name = p_scope_name
    LEFT JOIN LATERAL (
        SELECT api.group_token_pool_usage(s.kind, s.id::TEXT, (p.spec).token_quota ->> 'period') AS used
        WHERE (p.spec).token_quota IS NOT NULL
    ) u ON TRUE
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
