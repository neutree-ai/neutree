-- Access control policies (NEUTREE-GENERAL-9, 1.1 parallel track).
--
-- A sibling to quota_policies, but a fundamentally different concept: quota is a
-- resettable cumulative budget (period windows, "remaining", 429 quota_exceeded);
-- access is per-request gating / short-window rate limiting (no period, no
-- cumulative state). The two compose differently and therefore live in separate
-- resources: quota sums down the hierarchy (Σchildren <= parent); access takes
-- the MOST RESTRICTIVE applicable rule across levels (rate/concurrency = min,
-- allowlists = intersection). See access-policy-design.md.
--
-- This migration ships the table + RLS + RPCs and resolves the SHORT-WINDOW
-- rules the gateway enforces now (rate_limit, concurrency). The rule_type CHECK
-- already admits allowlist rule types so they can be added later without a
-- schema migration (only resolver/gateway wiring).

-- ----------------------
-- Policy table. Mirrors quota_policies' three-tier scope shape, but keyed by
-- rule_type (not period) with a JSONB rule_spec (not a single scalar limit).
-- ----------------------
CREATE TABLE api.access_policies (
    id          BIGSERIAL PRIMARY KEY,
    level       TEXT NOT NULL CHECK (level IN ('workspace', 'user', 'api_key')),
    workspace   TEXT NOT NULL,
    user_id     UUID,
    api_key_id  UUID REFERENCES api.api_keys(id) ON DELETE CASCADE,
    rule_type   TEXT NOT NULL CHECK (rule_type IN (
                    'rate_limit', 'concurrency',
                    'model_allowlist', 'endpoint_allowlist',
                    'ip_allowlist', 'header_allowlist')),
    rule_spec   JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- scope shape per level (identical to quota_policies)
    CONSTRAINT access_policies_shape CHECK (
        (level = 'workspace' AND user_id IS NULL     AND api_key_id IS NULL) OR
        (level = 'user'      AND user_id IS NOT NULL AND api_key_id IS NULL) OR
        (level = 'api_key'   AND api_key_id IS NOT NULL)
    ),
    -- light shape validation for the rule types the gateway enforces today
    CONSTRAINT access_policies_spec_shape CHECK (
        CASE rule_type
            WHEN 'rate_limit' THEN
                (rule_spec ? 'limit') AND (rule_spec ? 'window')
                AND (rule_spec->>'window') IN ('second', 'minute', 'hour')
                AND (rule_spec->>'limit') ~ '^[0-9]+$'
                AND (rule_spec->>'limit')::BIGINT > 0
            WHEN 'concurrency' THEN
                (rule_spec ? 'max') AND (rule_spec->>'max') ~ '^[0-9]+$'
                AND (rule_spec->>'max')::BIGINT > 0
            ELSE true
        END
    )
);

-- At most one rule of a given type per (level, scope) -- EXCEPT rate_limit,
-- where a scope may hold one rule per window (e.g. RPS and RPM together), so
-- those are keyed by (scope, window) instead.
CREATE UNIQUE INDEX access_policies_ws_uniq
    ON api.access_policies (workspace, rule_type)
    WHERE level = 'workspace' AND rule_type <> 'rate_limit';
CREATE UNIQUE INDEX access_policies_user_uniq
    ON api.access_policies (workspace, user_id, rule_type)
    WHERE level = 'user' AND rule_type <> 'rate_limit';
CREATE UNIQUE INDEX access_policies_apikey_uniq
    ON api.access_policies (api_key_id, rule_type)
    WHERE level = 'api_key' AND rule_type <> 'rate_limit';

CREATE UNIQUE INDEX access_policies_ws_rl_uniq
    ON api.access_policies (workspace, (rule_spec->>'window'))
    WHERE level = 'workspace' AND rule_type = 'rate_limit';
CREATE UNIQUE INDEX access_policies_user_rl_uniq
    ON api.access_policies (workspace, user_id, (rule_spec->>'window'))
    WHERE level = 'user' AND rule_type = 'rate_limit';
CREATE UNIQUE INDEX access_policies_apikey_rl_uniq
    ON api.access_policies (api_key_id, (rule_spec->>'window'))
    WHERE level = 'api_key' AND rule_type = 'rate_limit';

CREATE INDEX access_policies_workspace_idx ON api.access_policies (workspace);
CREATE INDEX access_policies_api_key_idx   ON api.access_policies (api_key_id);

-- ----------------------
-- RLS: identical authorization model to quota_policies (068).
--   workspace/user policies: managed by workspace:update, read by workspace:read.
--   api_key policies: managed and read by the owner of the API key.
-- ----------------------
ALTER TABLE api.access_policies ENABLE ROW LEVEL SECURITY;

CREATE POLICY access_policies_select ON api.access_policies
    FOR SELECT USING (
        api.has_permission(auth.uid(), 'workspace:read', workspace)
        OR (level = 'user' AND user_id = auth.uid())
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

CREATE POLICY access_policies_insert ON api.access_policies
    FOR INSERT WITH CHECK (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

CREATE POLICY access_policies_update ON api.access_policies
    FOR UPDATE USING (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    ) WITH CHECK (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

CREATE POLICY access_policies_delete ON api.access_policies
    FOR DELETE USING (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

-- ----------------------
-- Upsert one rule. SECURITY INVOKER so access_policies RLS authorizes the
-- caller. Unlike quota there is no hierarchy trigger: access rules never sum, so
-- a child rule cannot violate a parent (the gateway just takes the most
-- restrictive at request time).
-- ----------------------
CREATE OR REPLACE FUNCTION api.set_access_policy(
    p_level      TEXT,
    p_rule_type  TEXT,
    p_rule_spec  JSONB,
    p_workspace  TEXT DEFAULT NULL,
    p_user_id    UUID DEFAULT NULL,
    p_api_key_id UUID DEFAULT NULL
) RETURNS api.access_policies AS $$
DECLARE
    v_ws     TEXT := p_workspace;
    v_id     BIGINT;
    v_result api.access_policies;
    -- rate_limit is keyed per window within a scope; other rule types are unique
    -- per scope. v_win narrows the upsert target for rate_limit only.
    v_win    TEXT := CASE WHEN p_rule_type = 'rate_limit'
                          THEN p_rule_spec->>'window' END;
BEGIN
    IF p_level = 'api_key' THEN
        IF p_api_key_id IS NULL THEN
            RAISE EXCEPTION 'api_key_id is required for api_key level';
        END IF;
        SELECT (metadata).workspace INTO v_ws FROM api.api_keys WHERE id = p_api_key_id;
        SELECT id INTO v_id FROM api.access_policies
            WHERE level = 'api_key' AND api_key_id = p_api_key_id AND rule_type = p_rule_type
              AND (p_rule_type <> 'rate_limit' OR rule_spec->>'window' = v_win);
    ELSIF p_level = 'user' THEN
        IF p_user_id IS NULL OR v_ws IS NULL THEN
            RAISE EXCEPTION 'user_id and workspace are required for user level';
        END IF;
        SELECT id INTO v_id FROM api.access_policies
            WHERE level = 'user' AND workspace = v_ws AND user_id = p_user_id AND rule_type = p_rule_type
              AND (p_rule_type <> 'rate_limit' OR rule_spec->>'window' = v_win);
    ELSIF p_level = 'workspace' THEN
        IF v_ws IS NULL THEN
            RAISE EXCEPTION 'workspace is required for workspace level';
        END IF;
        SELECT id INTO v_id FROM api.access_policies
            WHERE level = 'workspace' AND workspace = v_ws AND rule_type = p_rule_type
              AND (p_rule_type <> 'rate_limit' OR rule_spec->>'window' = v_win);
    ELSE
        RAISE EXCEPTION 'invalid level: %', p_level;
    END IF;

    IF v_id IS NULL THEN
        INSERT INTO api.access_policies (level, workspace, user_id, api_key_id, rule_type, rule_spec)
        VALUES (p_level, v_ws,
                CASE WHEN p_level = 'user' THEN p_user_id END,
                CASE WHEN p_level = 'api_key' THEN p_api_key_id END,
                p_rule_type, p_rule_spec)
        RETURNING * INTO v_result;
    ELSE
        UPDATE api.access_policies
            SET rule_spec = p_rule_spec, updated_at = now()
            WHERE id = v_id
        RETURNING * INTO v_result;
    END IF;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql SECURITY INVOKER;

-- List policies the caller may see (RLS-scoped), with optional filters.
CREATE OR REPLACE FUNCTION api.get_access_policies(
    p_workspace  TEXT DEFAULT NULL,
    p_level      TEXT DEFAULT NULL,
    p_user_id    UUID DEFAULT NULL,
    p_api_key_id UUID DEFAULT NULL
)
RETURNS SETOF api.access_policies
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT *
    FROM api.access_policies a
    WHERE (p_workspace  IS NULL OR a.workspace  = p_workspace)
      AND (p_level      IS NULL OR a.level      = p_level)
      AND (p_user_id    IS NULL OR a.user_id    = p_user_id)
      AND (p_api_key_id IS NULL OR a.api_key_id = p_api_key_id)
    ORDER BY a.level, a.rule_type;
$$;

-- Delete one policy. SECURITY INVOKER so the DELETE RLS authorizes the caller.
CREATE OR REPLACE FUNCTION api.delete_access_policy(p_id BIGINT)
RETURNS BIGINT
LANGUAGE sql SECURITY INVOKER
AS $$
    DELETE FROM api.access_policies WHERE id = p_id RETURNING id;
$$;

-- ----------------------
-- Resolve the effective access gate for one API key: the MOST RESTRICTIVE rule
-- across its api_key / user / workspace scopes. The gateway consumes only this
-- resolved object (it never sees the three levels), mirroring how
-- get_api_key_remaining hands the gateway a single scalar.
--   rate_limits: per window, the minimum limit set at any level.
--   concurrency: the minimum max set at any level (null = unconstrained).
-- SECURITY DEFINER: resolution spans levels/keys the caller may not own.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_api_key_access(p_api_key_id UUID)
RETURNS JSONB AS $$
DECLARE
    v_ws   TEXT;
    v_user UUID;
    v_out  JSONB;
BEGIN
    SELECT (metadata).workspace, user_id INTO v_ws, v_user
        FROM api.api_keys WHERE id = p_api_key_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    WITH applicable AS (
        SELECT rule_type, rule_spec
        FROM api.access_policies
        WHERE (level = 'api_key'  AND api_key_id = p_api_key_id)
           OR (level = 'user'      AND workspace = v_ws AND user_id = v_user)
           OR (level = 'workspace' AND workspace = v_ws)
    ),
    rl AS (
        SELECT rule_spec->>'window' AS window,
               MIN((rule_spec->>'limit')::BIGINT) AS lim
        FROM applicable
        WHERE rule_type = 'rate_limit'
        GROUP BY rule_spec->>'window'
    ),
    cc AS (
        SELECT MIN((rule_spec->>'max')::BIGINT) AS max_c
        FROM applicable
        WHERE rule_type = 'concurrency'
    )
    SELECT jsonb_build_object(
        'rate_limits',
            COALESCE((SELECT jsonb_agg(jsonb_build_object('limit', lim, 'window', window)
                              ORDER BY window) FROM rl), '[]'::jsonb),
        'concurrency', (SELECT max_c FROM cc)
    ) INTO v_out;

    RETURN v_out;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

-- Make the new table/functions visible to PostgREST immediately.
NOTIFY pgrst, 'reload schema';
