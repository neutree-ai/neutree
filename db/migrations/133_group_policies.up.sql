-- Group policies: model access and a shared token pool per OrgUnit or Team.
-- Usage by user, Team, OrgUnit and ServiceAccount, recorded with the owner's
-- groups when it is aggregated.

-- ----------------------
-- Group policies
-- ----------------------
-- A group policy belongs to one group (spec.subject_kind 'Team' or
-- 'OrgUnit', spec.subject_name its metadata.name); a group has at most one.
-- spec.allowed_models and spec.denied_models are lists of
-- {"model": <client-facing name>, "workspace": <name, optional>}; an entry
-- without a workspace applies in every workspace. An empty or missing list
-- sets no rule.
--
-- Merge for a user in a workspace (api.user_group_model_policy):
--   * the user's groups: every Team they are in, and every OrgUnit on the
--     path to their primary OrgUnit (a policy covers the whole subtree);
--   * allow: OrgUnits with an allow list intersect; Teams with an allow list
--     unite; the two results intersect. No allow list anywhere = no limit;
--   * deny: the union of every deny list, removed last (deny wins).
-- Inactive groups count. Inactive grants nothing (role assignments), but a
-- constraint stays until someone deletes the policy.
--
-- The gateway applies key allowlist ∩ group allow − group deny; the
-- controller pushes the group rules of a key (api.get_api_key_model_rules) to
-- the neutree-ai-access plugin.
--
-- spec.token_quota = {"limit": <tokens>, "period": daily|weekly|monthly|yearly}
-- is one pool shared by every member: the tokens of all members' keys in the
-- current period, in every workspace, count against it. The period windows
-- are the ones of a key's token_quota, on the database's date. A key's
-- remaining = min(the key's own remaining, the remaining of every pool of its
-- owner's groups), NULL for no limit. The pool is soft: the gateway sees usage
-- after aggregation (30s) plus its 5s cache, so a pool can overshoot by what
-- the members use in that window.
--
-- Writes need global group_policy:*. Plain DELETE: no controller cleans up. A
-- policy goes with its group.

CREATE TYPE api.group_policy_spec AS (
    subject_kind TEXT,
    subject_name TEXT,
    allowed_models JSONB,
    denied_models JSONB,
    token_quota JSONB
);

CREATE TABLE api.group_policies (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.group_policy_spec
);

CREATE UNIQUE INDEX group_policies_name_unique_idx ON api.group_policies (((metadata).name));

CREATE UNIQUE INDEX group_policies_subject_unique_idx
    ON api.group_policies (((spec).subject_kind), ((spec).subject_name));

-- ----------------------
-- Write checks
-- ----------------------

-- NULL for no rule; otherwise the entries rebuilt as {model, workspace?},
-- without duplicates.
CREATE OR REPLACE FUNCTION api.group_policy_normalize_models(p_models JSONB, p_field TEXT)
RETURNS JSONB
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
DECLARE
    v_entry JSONB;
    v_out JSONB := '[]'::jsonb;
    v_item JSONB;
BEGIN
    IF p_models IS NULL OR jsonb_typeof(p_models) = 'null' THEN
        RETURN NULL;
    END IF;

    IF jsonb_typeof(p_models) <> 'array' THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10302","message": "spec.%s must be a list of {model, workspace}","hint": "Leave it empty for no rule"}', p_field),
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    FOR v_entry IN SELECT e FROM jsonb_array_elements(p_models) AS e LOOP
        IF jsonb_typeof(v_entry) <> 'object'
            OR jsonb_typeof(v_entry -> 'model') IS DISTINCT FROM 'string'
            OR btrim(v_entry ->> 'model') = ''
            OR COALESCE(jsonb_typeof(v_entry -> 'workspace'), 'null') NOT IN ('null', 'string') THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10302","message": "spec.%s must be a list of {model, workspace}","hint": "model is a non-empty string; workspace is a string or absent"}', p_field),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_item := jsonb_build_object('model', v_entry ->> 'model');

        IF COALESCE(btrim(v_entry ->> 'workspace'), '') <> '' THEN
            v_item := v_item || jsonb_build_object('workspace', v_entry ->> 'workspace');
        END IF;

        -- Not @>: object containment is partial, so {model} would match
        -- {model, workspace}.
        IF NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_out) AS x WHERE x = v_item) THEN
            v_out := v_out || jsonb_build_array(v_item);
        END IF;
    END LOOP;

    IF jsonb_array_length(v_out) = 0 THEN
        RETURN NULL;
    END IF;

    RETURN v_out;
END;
$$;

REVOKE ALL ON FUNCTION api.group_policy_normalize_models(JSONB, TEXT) FROM PUBLIC;

-- NULL for no pool; otherwise {limit, period} with the period defaulted.
CREATE OR REPLACE FUNCTION api.group_policy_normalize_token_quota(p_quota JSONB)
RETURNS JSONB
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
DECLARE
    v_limit JSONB;
    v_num NUMERIC;
    v_period TEXT;
BEGIN
    IF p_quota IS NULL OR jsonb_typeof(p_quota) = 'null' THEN
        RETURN NULL;
    END IF;

    IF jsonb_typeof(p_quota) <> 'object' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10303","message": "spec.token_quota must be {limit, period}","hint": "limit is a positive integer; period is daily, weekly, monthly or yearly"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_limit := p_quota -> 'limit';

    IF v_limit IS NULL OR jsonb_typeof(v_limit) = 'null' THEN
        RETURN NULL;
    END IF;

    IF jsonb_typeof(v_limit) = 'number' THEN
        v_num := v_limit::text::numeric;
    END IF;

    v_period := COALESCE(NULLIF(btrim(p_quota ->> 'period'), ''), 'monthly');

    IF v_num IS NULL OR v_num <= 0 OR v_num <> trunc(v_num) OR v_num > 9223372036854775807
        OR v_period NOT IN ('daily', 'weekly', 'monthly', 'yearly') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10303","message": "spec.token_quota must be {limit, period}","hint": "limit is a positive integer; period is daily, weekly, monthly or yearly"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN jsonb_build_object('limit', v_num::bigint, 'period', v_period);
END;
$$;

REVOKE ALL ON FUNCTION api.group_policy_normalize_token_quota(JSONB) FROM PUBLIC;

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

REVOKE ALL ON FUNCTION api.group_policies_before_write() FROM PUBLIC;

CREATE TRIGGER update_group_policies_update_timestamp
    BEFORE UPDATE ON api.group_policies
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_group_policies_default_timestamp
    BEFORE INSERT ON api.group_policies
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

-- Named to fire before validate_name_on_group_policies (triggers fire in name
-- order), so a derived name is validated too.
CREATE TRIGGER group_policies_before_write
    BEFORE INSERT OR UPDATE ON api.group_policies
    FOR EACH ROW
    EXECUTE FUNCTION api.group_policies_before_write();

CREATE TRIGGER validate_name_on_group_policies
    BEFORE INSERT OR UPDATE ON api.group_policies
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

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

REVOKE ALL ON FUNCTION api.group_policies_delete_with_group() FROM PUBLIC;

CREATE TRIGGER teams_delete_group_policy
    AFTER DELETE ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION api.group_policies_delete_with_group('Team');

CREATE TRIGGER org_units_delete_group_policy
    AFTER DELETE ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION api.group_policies_delete_with_group('OrgUnit');

-- ----------------------
-- RLS: global group_policy:* only
-- ----------------------

ALTER TABLE api.group_policies ENABLE ROW LEVEL SECURITY;

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

-- ----------------------
-- Merge
-- ----------------------

-- The model names of a list that apply in p_workspace; NULL when none does,
-- so a list pinned only to other workspaces sets no rule here (workspace
-- access is up to role assignments).
CREATE OR REPLACE FUNCTION api.group_policy_model_names(p_models JSONB, p_workspace TEXT)
RETURNS TEXT[]
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT NULLIF(ARRAY(
        SELECT DISTINCT e ->> 'model'
        FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p_models) = 'array' THEN p_models ELSE '[]'::jsonb END) AS e
        WHERE COALESCE(e ->> 'workspace', '') IN ('', COALESCE(p_workspace, ''))
        ORDER BY 1), ARRAY[]::TEXT[]);
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

REVOKE ALL ON FUNCTION api.user_group_model_policy(UUID, TEXT) FROM PUBLIC;

-- ----------------------
-- Controller: attach the quota plugin when a pool applies
-- ----------------------

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

REVOKE ALL ON FUNCTION api.get_api_key_model_rules(UUID, TEXT, JSONB) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION api.get_api_key_model_rules(UUID, TEXT, JSONB) TO service_role;

-- ----------------------
-- Usage by group
-- ----------------------
-- Usage counts toward the groups the key owner belonged to when it was used.
-- Raw records are deleted soon after aggregation, so the owner's groups are
-- recorded when a record is aggregated and never recomputed:
--   * Team pool    = rows whose team_ids hold the Team;
--   * OrgUnit pool = rows whose OrgUnit is in the pool OrgUnit's current subtree.
-- A member who leaves takes nothing out of the old pool and brings nothing
-- into the new one. Usage aggregated before this table existed has no row.

CREATE TABLE api.group_daily_usage (
    id BIGSERIAL PRIMARY KEY,
    usage_date DATE NOT NULL,
    -- The key owner, a user or an SA. No foreign key: usage outlives it.
    user_id UUID,
    -- The owner's OrgUnit and Active Teams (ids, sorted) at aggregation time.
    org_unit_id INTEGER,
    team_ids INTEGER[] NOT NULL DEFAULT '{}',
    workspace TEXT NOT NULL,
    endpoint_type TEXT NOT NULL,
    endpoint_name TEXT NOT NULL,
    model_name TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    -- NULL until a record reports the field, as in api_daily_usage.
    cache_read_tokens BIGINT,
    cache_creation_tokens BIGINT,
    reasoning_tokens BIGINT,
    cost_usd NUMERIC,
    update_timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    service_account_id UUID
);

CREATE UNIQUE INDEX group_daily_usage_bucket_idx ON api.group_daily_usage (
    usage_date, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
    COALESCE(service_account_id, '00000000-0000-0000-0000-000000000000'::uuid),
    COALESCE(org_unit_id, 0), team_ids,
    workspace, endpoint_type, endpoint_name, model_name);

-- usage_date first serves the period range; the GIN index the Team lookup.
CREATE INDEX group_daily_usage_team_ids_idx ON api.group_daily_usage USING GIN (team_ids);

CREATE INDEX group_daily_usage_org_unit_idx ON api.group_daily_usage (org_unit_id, usage_date);

ALTER TABLE api.group_daily_usage ENABLE ROW LEVEL SECURITY;

-- Read through api.get_usage_by_group only.
CREATE POLICY "No direct access to group usage" ON api.group_daily_usage
    USING (false);

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

CREATE TRIGGER group_daily_usage_on_aggregated
    AFTER UPDATE OF is_aggregated ON api.api_usage_records
    FOR EACH ROW
    WHEN (NEW.is_aggregated AND NOT COALESCE(OLD.is_aggregated, FALSE))
    EXECUTE FUNCTION api.group_daily_usage_on_aggregated();

REVOKE ALL ON FUNCTION api.group_daily_usage_on_aggregated() FROM PUBLIC;

-- ----------------------
-- Pool usage
-- ----------------------

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

REVOKE ALL ON FUNCTION api.group_token_pool_usage(TEXT, TEXT, TEXT) FROM PUBLIC;

-- Every pool that applies to a user: the pools of all their groups, Inactive
-- ones included (a constraint stays until the policy is deleted).
CREATE OR REPLACE FUNCTION api.user_group_token_pools(p_user_id UUID)
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

-- ----------------------
-- Remaining for the gateway
-- ----------------------

-- The key's own remaining: its token_quota and per-model limits.
CREATE OR REPLACE FUNCTION api.api_key_own_remaining(
    p_id       UUID,
    p_model    TEXT,
    p_type     TEXT,
    p_endpoint TEXT
)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits        JSONB;
    v_limit         BIGINT;
    v_period        TEXT;
    v_has_per_model BOOLEAN;
    v_entry_type    TEXT;
    v_entry_name    TEXT;
BEGIN
    SELECT (spec).limits INTO v_limits FROM api.api_keys WHERE id = p_id;
    IF v_limits IS NULL THEN
        RETURN NULL;
    END IF;
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
        WHERE e ->> 'token_limit' IS NOT NULL
    ) INTO v_has_per_model;

    IF v_has_per_model THEN
        IF p_model IS NULL THEN
            RETURN NULL;
        END IF;

        SELECT (e ->> 'token_limit')::bigint,
               NULLIF(e ->> 'type', ''),
               NULLIF(e ->> 'endpoint_name', '')
          INTO v_limit, v_entry_type, v_entry_name
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
        WHERE e ->> 'model' = p_model
          AND e ->> 'token_limit' IS NOT NULL
          AND (NULLIF(e ->> 'type', '')          IS NULL OR NULLIF(e ->> 'type', '')          = p_type)
          AND (NULLIF(e ->> 'endpoint_name', '') IS NULL OR NULLIF(e ->> 'endpoint_name', '') = p_endpoint)
        LIMIT 1;

        IF v_limit IS NULL THEN
            RETURN NULL;
        END IF;

        RETURN v_limit - api.api_key_model_period_usage(
            p_id, v_period, p_model, v_entry_type, v_entry_name);
    END IF;

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NULL OR v_limit <= 0 THEN
        RETURN NULL;
    END IF;
    RETURN v_limit - api.api_key_period_usage(p_id, v_period);
END;
$$;

REVOKE ALL ON FUNCTION api.api_key_own_remaining(UUID, TEXT, TEXT, TEXT) FROM PUBLIC;

-- ----------------------
-- ServiceAccount keys at the gateway
-- ----------------------
-- An SA key is decided as a personal key is, with the SA as its owner:
--   * access: the SA holds model:invoke in the key's workspace
--     (api.has_permission);
--   * models: the group policies of the SA's groups, that is its Team
--     (api.get_api_key_model_rules); no allow list means no limit;
--   * tokens: its Team's token pool, shared with the Team's members
--     (api.get_api_key_remaining);
--   * usage: recorded for the SA and its Team, with no user and no OrgUnit
--     (api.group_daily_usage). get_usage_by_group reads it by ServiceAccount
--     or by Team; usage by user leaves it out.

-- The pool of an SA key is its Team's; its usage readers see it too.
CREATE OR REPLACE FUNCTION api.get_api_key_remaining(
    p_id       UUID,
    p_model    TEXT DEFAULT NULL,
    p_type     TEXT DEFAULT NULL,
    p_endpoint TEXT DEFAULT NULL
)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_owner UUID;
    v_sa    UUID;
    v_own   BIGINT;
    v_pool  BIGINT;
BEGIN
    IF NOT api.can_read_api_key_usage(p_id) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    v_own := api.api_key_own_remaining(p_id, p_model, p_type, p_endpoint);

    SELECT COALESCE(user_id, service_account_id), service_account_id INTO v_owner, v_sa
    FROM api.api_keys WHERE id = p_id;

    IF v_owner IS NOT NULL AND (
        (NULLIF(current_setting('request.jwt.claims', true), '')::jsonb ->> 'role') = 'service_role'
        OR current_setting('request.jwt.claim.role', true) = 'service_role'
        OR auth.uid() = v_owner
        OR api.has_permission(auth.uid(), 'workspace:usage-read', NULL) IS TRUE
        OR (v_sa IS NOT NULL AND api.can_read_service_account_usage(auth.uid(), v_sa))
    ) THEN
        SELECT MIN(p.token_limit - p.used) INTO v_pool
        FROM api.user_group_token_pools(v_owner) p;
    END IF;

    -- LEAST skips NULLs: NULL only when neither limits.
    RETURN LEAST(v_own, v_pool);
END;
$$;

-- ----------------------
-- Usage
-- ----------------------

-- May p_user read the usage of the Team named p_team? FALSE for an unknown
-- name: a NULL id would mean "any Team" to the entry point.
CREATE OR REPLACE FUNCTION api.can_read_team_usage(p_user UUID, p_team TEXT)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_id INTEGER;
BEGIN
    SELECT t.id INTO v_id FROM api.teams t WHERE (t.metadata).name = p_team;

    IF v_id IS NULL THEN
        RETURN FALSE;
    END IF;

    RETURN api.has_permission_on_team(p_user, 'team_usage:read', v_id) IS TRUE;
END;
$$;

-- ----------------------
-- Token pools
-- ----------------------

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

-- A user's own policy, or another user's with group_policy:read on that user
-- (api.has_permission_on_subject). Each group and pool carries its
-- display_name: a member reads their own policy but not the teams table.

CREATE OR REPLACE FUNCTION api.group_display_name(p_kind TEXT, p_name TEXT)
RETURNS TEXT
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
    SELECT (t.metadata).display_name FROM api.teams t
    WHERE p_kind = 'Team' AND (t.metadata).name = p_name
    UNION ALL
    SELECT (o.metadata).display_name FROM api.org_units o
    WHERE p_kind = 'OrgUnit' AND (o.metadata).name = p_name
    LIMIT 1;
$$;

REVOKE ALL ON FUNCTION api.group_display_name(TEXT, TEXT) FROM PUBLIC;

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

-- department-admin reads the usage (org_unit:usage-read) and group policies
-- (group_policy:read) of its subtree.
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

SELECT api.update_department_admin_permissions();
