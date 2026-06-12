-- Quota & usage control (NEUTREE-GENERAL-9).
--
-- Three-tier token quota: workspace -> user -> api_key. Each (level, scope,
-- period) may have at most one policy. Periods are daily/weekly/monthly/yearly.
-- The hierarchy invariant (sum of children <= parent, per period) is enforced by
-- a trigger. Per-API-key "minimum remaining tokens" across all applicable
-- levels/periods is computed here for the AI gateway to enforce.
--
-- Quota usage is sourced from the existing daily aggregation (api.api_daily_usage,
-- keyed by api_key_id + usage_date); period usage is a window-sum over that table.

-- ----------------------
-- Policy table
-- ----------------------
CREATE TABLE api.quota_policies (
    id           BIGSERIAL PRIMARY KEY,
    level        TEXT   NOT NULL CHECK (level IN ('workspace', 'user', 'api_key')),
    workspace    TEXT   NOT NULL,
    user_id      UUID,
    api_key_id   UUID   REFERENCES api.api_keys(id) ON DELETE CASCADE,
    period       TEXT   NOT NULL CHECK (period IN ('daily', 'weekly', 'monthly', 'yearly')),
    limit_tokens BIGINT NOT NULL CHECK (limit_tokens >= 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- shape per level
    CONSTRAINT quota_policies_shape CHECK (
        (level = 'workspace' AND user_id IS NULL     AND api_key_id IS NULL) OR
        (level = 'user'      AND user_id IS NOT NULL AND api_key_id IS NULL) OR
        (level = 'api_key'   AND api_key_id IS NOT NULL)
    )
);

-- one policy per (level, scope, period)
CREATE UNIQUE INDEX quota_policies_workspace_uniq
    ON api.quota_policies (workspace, period) WHERE level = 'workspace';
CREATE UNIQUE INDEX quota_policies_user_uniq
    ON api.quota_policies (workspace, user_id, period) WHERE level = 'user';
CREATE UNIQUE INDEX quota_policies_apikey_uniq
    ON api.quota_policies (api_key_id, period) WHERE level = 'api_key';

CREATE INDEX quota_policies_workspace_idx ON api.quota_policies (workspace);
CREATE INDEX quota_policies_api_key_idx   ON api.quota_policies (api_key_id);

-- ----------------------
-- RLS
--   workspace/user policies: managed by holders of workspace:update on the
--     workspace (workspace quota = "create/edit workspace"; user quota =
--     "edit workspace"). Readable by workspace members (workspace:read).
--   api_key policies: managed and read by the owner of the API key (a user sets
--     quota for their own keys). api_key:* permissions are added in 064 for
--     explicit/forward-looking gating.
-- ----------------------
ALTER TABLE api.quota_policies ENABLE ROW LEVEL SECURITY;

CREATE POLICY quota_policies_select ON api.quota_policies
    FOR SELECT USING (
        api.has_permission(auth.uid(), 'workspace:read', workspace)
        OR (level = 'user' AND user_id = auth.uid())
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

CREATE POLICY quota_policies_insert ON api.quota_policies
    FOR INSERT WITH CHECK (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

CREATE POLICY quota_policies_update ON api.quota_policies
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

CREATE POLICY quota_policies_delete ON api.quota_policies
    FOR DELETE USING (
        (level IN ('workspace', 'user')
            AND api.has_permission(auth.uid(), 'workspace:update', workspace))
        OR (level = 'api_key' AND EXISTS (
                SELECT 1 FROM api.api_keys k
                WHERE k.id = api_key_id AND k.user_id = auth.uid()))
    );

-- ----------------------
-- Hierarchy invariant: sum(children) <= parent, evaluated per period.
-- SECURITY DEFINER so the cross-user/cross-key aggregation is not narrowed by
-- the caller's RLS (a workspace admin sets quota for keys they do not own).
-- ----------------------
CREATE OR REPLACE FUNCTION api.validate_quota_hierarchy()
RETURNS TRIGGER AS $$
DECLARE
    v_parent_limit BIGINT;
    v_children_sum BIGINT;
    v_ws   TEXT;
    v_user UUID;
BEGIN
    IF NEW.level = 'user' THEN
        -- child of workspace policy
        SELECT limit_tokens INTO v_parent_limit FROM api.quota_policies
            WHERE level = 'workspace' AND workspace = NEW.workspace AND period = NEW.period;
        IF FOUND THEN
            SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies
                WHERE level = 'user' AND workspace = NEW.workspace AND period = NEW.period
                  AND id <> COALESCE(NEW.id, -1);
            IF v_children_sum + NEW.limit_tokens > v_parent_limit THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code":"10050","message":"User quota total exceeds workspace quota","hint":"Lower this quota or raise the workspace quota for this period"}',
                          detail  = '{"status":400,"headers":{"X-Powered-By":"Neutree"}}';
            END IF;
        END IF;
        -- parent of api_key policies: must not drop below sum of its keys
        SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies p
            WHERE p.level = 'api_key' AND p.period = NEW.period
              AND p.api_key_id IN (
                  SELECT k.id FROM api.api_keys k
                  WHERE k.user_id = NEW.user_id AND (k.metadata).workspace = NEW.workspace);
        IF v_children_sum > NEW.limit_tokens THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code":"10051","message":"User quota is below the sum of its API key quotas","hint":"Raise this quota or lower the API key quotas for this period"}',
                      detail  = '{"status":400,"headers":{"X-Powered-By":"Neutree"}}';
        END IF;

    ELSIF NEW.level = 'api_key' THEN
        SELECT (metadata).workspace, user_id INTO v_ws, v_user
            FROM api.api_keys WHERE id = NEW.api_key_id;
        SELECT limit_tokens INTO v_parent_limit FROM api.quota_policies
            WHERE level = 'user' AND workspace = v_ws AND user_id = v_user AND period = NEW.period;
        IF FOUND THEN
            SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies p
                WHERE p.level = 'api_key' AND p.period = NEW.period
                  AND p.id <> COALESCE(NEW.id, -1)
                  AND p.api_key_id IN (
                      SELECT k.id FROM api.api_keys k
                      WHERE k.user_id = v_user AND (k.metadata).workspace = v_ws);
            IF v_children_sum + NEW.limit_tokens > v_parent_limit THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code":"10052","message":"API key quota total exceeds user quota","hint":"Lower this quota or raise the user quota for this period"}',
                          detail  = '{"status":400,"headers":{"X-Powered-By":"Neutree"}}';
            END IF;
        END IF;
        -- keep workspace stored on the row consistent with the key's workspace
        NEW.workspace := v_ws;

    ELSIF NEW.level = 'workspace' THEN
        -- parent of user policies: must not drop below sum of its users
        SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies
            WHERE level = 'user' AND workspace = NEW.workspace AND period = NEW.period;
        IF v_children_sum > NEW.limit_tokens THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code":"10053","message":"Workspace quota is below the sum of its user quotas","hint":"Raise this quota or lower the user quotas for this period"}',
                      detail  = '{"status":400,"headers":{"X-Powered-By":"Neutree"}}';
        END IF;
    END IF;

    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

CREATE TRIGGER validate_quota_hierarchy_trigger
    BEFORE INSERT OR UPDATE ON api.quota_policies
    FOR EACH ROW EXECUTE FUNCTION api.validate_quota_hierarchy();

-- ----------------------
-- Period window start for a given period and reference day.
-- weekly uses ISO week (Monday start), consistent with date_trunc('week').
-- ----------------------
CREATE OR REPLACE FUNCTION api.quota_period_start(p_period TEXT, p_today DATE)
RETURNS DATE AS $$
    SELECT CASE p_period
        WHEN 'daily'   THEN p_today
        WHEN 'weekly'  THEN date_trunc('week',  p_today)::date
        WHEN 'monthly' THEN date_trunc('month', p_today)::date
        WHEN 'yearly'  THEN date_trunc('year',  p_today)::date
    END;
$$ LANGUAGE sql IMMUTABLE;

-- ----------------------
-- set_quota_policy: upsert a single (level, scope, period) policy.
-- SECURITY INVOKER so RLS authorizes the write and the hierarchy trigger fires.
-- ----------------------
CREATE OR REPLACE FUNCTION api.set_quota_policy(
    p_level        TEXT,
    p_period       TEXT,
    p_limit_tokens BIGINT,
    p_workspace    TEXT DEFAULT NULL,
    p_user_id      UUID DEFAULT NULL,
    p_api_key_id   UUID DEFAULT NULL
) RETURNS api.quota_policies AS $$
DECLARE
    v_ws     TEXT := p_workspace;
    v_result api.quota_policies;
    v_id     BIGINT;
BEGIN
    IF p_level = 'api_key' THEN
        IF p_api_key_id IS NULL THEN
            RAISE EXCEPTION 'api_key_id is required for api_key level';
        END IF;
        SELECT (metadata).workspace INTO v_ws FROM api.api_keys WHERE id = p_api_key_id;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'api_key' AND api_key_id = p_api_key_id AND period = p_period;
    ELSIF p_level = 'user' THEN
        IF p_user_id IS NULL OR v_ws IS NULL THEN
            RAISE EXCEPTION 'user_id and workspace are required for user level';
        END IF;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'user' AND workspace = v_ws AND user_id = p_user_id AND period = p_period;
    ELSIF p_level = 'workspace' THEN
        IF v_ws IS NULL THEN
            RAISE EXCEPTION 'workspace is required for workspace level';
        END IF;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'workspace' AND workspace = v_ws AND period = p_period;
    ELSE
        RAISE EXCEPTION 'invalid level: %', p_level;
    END IF;

    IF v_id IS NULL THEN
        INSERT INTO api.quota_policies (level, workspace, user_id, api_key_id, period, limit_tokens)
        VALUES (p_level, v_ws, p_user_id, p_api_key_id, p_period, p_limit_tokens)
        RETURNING * INTO v_result;
    ELSE
        UPDATE api.quota_policies
           SET limit_tokens = p_limit_tokens
         WHERE id = v_id
        RETURNING * INTO v_result;
    END IF;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- ----------------------
-- Period usage for a set of API keys (window-sum over daily aggregates).
-- ----------------------
CREATE OR REPLACE FUNCTION api.quota_period_usage(p_key_ids UUID[], p_period TEXT, p_today DATE)
RETURNS BIGINT AS $$
    SELECT COALESCE(SUM((d.spec).total_usage), 0)::bigint
    FROM api.api_daily_usage d
    WHERE (d.spec).api_key_id = ANY(p_key_ids)
      AND (d.spec).usage_date >= api.quota_period_start(p_period, p_today)
      AND (d.spec).usage_date <= p_today;
$$ LANGUAGE sql STABLE SECURITY DEFINER;

-- ----------------------
-- Minimum remaining tokens for one API key across every applicable
-- (level, period) policy. NULL when the key is unconstrained (unlimited).
-- May be negative if already over a quota; the gateway treats <= 0 as "deny".
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_api_key_remaining(p_api_key_id UUID)
RETURNS BIGINT AS $$
DECLARE
    v_ws    TEXT;
    v_user  UUID;
    v_today DATE := CURRENT_DATE;
    v_min   BIGINT;
BEGIN
    SELECT (metadata).workspace, user_id INTO v_ws, v_user
        FROM api.api_keys WHERE id = p_api_key_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    WITH policies AS (
        SELECT period, limit_tokens, ARRAY[p_api_key_id] AS key_ids
        FROM api.quota_policies
        WHERE level = 'api_key' AND api_key_id = p_api_key_id
        UNION ALL
        SELECT period, limit_tokens,
               ARRAY(SELECT k.id FROM api.api_keys k
                     WHERE k.user_id = v_user AND (k.metadata).workspace = v_ws)
        FROM api.quota_policies
        WHERE level = 'user' AND user_id = v_user AND workspace = v_ws
        UNION ALL
        SELECT period, limit_tokens,
               ARRAY(SELECT k.id FROM api.api_keys k
                     WHERE (k.metadata).workspace = v_ws)
        FROM api.quota_policies
        WHERE level = 'workspace' AND workspace = v_ws
    )
    SELECT MIN(p.limit_tokens - api.quota_period_usage(p.key_ids, p.period, v_today))
    INTO v_min
    FROM policies p;

    RETURN v_min;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

-- ----------------------
-- Batch variant for the gateway sync: every constrained API key with its
-- current minimum remaining tokens. Unconstrained keys are omitted.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_all_api_keys_remaining()
RETURNS TABLE (api_key_id UUID, remaining BIGINT) AS $$
    SELECT k.id, api.get_api_key_remaining(k.id) AS remaining
    FROM api.api_keys k
    WHERE (k.metadata).deletion_timestamp IS NULL
      AND api.get_api_key_remaining(k.id) IS NOT NULL;
$$ LANGUAGE sql STABLE SECURITY DEFINER;

-- ----------------------
-- Grant api_key:* to admin (auto) and workspace-user; keep all prior perms.
-- ----------------------
SELECT api.update_admin_permissions();

CREATE OR REPLACE FUNCTION api.update_workspace_user_permissions()
RETURNS VOID AS $$
DECLARE
    workspace_user_permissions api.permission_action[];
BEGIN
    workspace_user_permissions := ARRAY[
        'workspace:read',
        'endpoint:read',
        'endpoint:create',
        'endpoint:update',
        'endpoint:delete',
        'image_registry:read',
        'image_registry:create',
        'image_registry:update',
        'image_registry:delete',
        'model_registry:read',
        'model_registry:create',
        'model_registry:update',
        'model_registry:delete',
        'model:read',
        'model:push',
        'model:pull',
        'model:delete',
        'engine:read',
        'engine:create',
        'engine:update',
        'engine:delete',
        'cluster:read',
        'cluster:create',
        'cluster:update',
        'cluster:delete',
        'model_catalog:read',
        'model_catalog:create',
        'model_catalog:update',
        'model_catalog:delete',
        'external_endpoint:read',
        'external_endpoint:create',
        'external_endpoint:update',
        'external_endpoint:delete',
        'endpoint:trace-read',
        'external_endpoint:trace-read',
        'api_key:create',
        'api_key:update',
        'api_key:delete'
    ]::api.permission_action[];

    UPDATE api.roles
    SET spec = ROW((spec).preset_key, workspace_user_permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_workspace_user_permissions();

-- Make the new table/functions visible to PostgREST immediately.
NOTIFY pgrst, 'reload schema';
