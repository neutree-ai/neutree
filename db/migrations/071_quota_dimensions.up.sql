-- Quota & usage control (NEUTREE-GENERAL-9): per-dimension quota.
--
-- Extends api.quota_policies so a policy can additionally be scoped to a single
-- dimension: a specific endpoint, external_endpoint, or model. A dimension
-- policy is an INDEPENDENT OVERLAY constraint — it does NOT participate in the
-- workspace -> user -> api_key sum hierarchy; it simply adds another ceiling
-- that the per-key minimum-remaining computation must respect when a request
-- targets that dimension.
--
-- Dimension usage is sourced from api_daily_usage.detailed_dimensional_usage,
-- a JSONB keyed by "endpoint_type|endpoint_name|model_name" with values
-- {total,prompt,completion}; we window-sum the matching keys' "total".

-- ----------------------
-- Columns
-- ----------------------
ALTER TABLE api.quota_policies
    ADD COLUMN dimension_type  TEXT,
    ADD COLUMN dimension_value TEXT;

ALTER TABLE api.quota_policies
    ADD CONSTRAINT quota_policies_dimension_chk CHECK (
        (dimension_type IS NULL AND dimension_value IS NULL) OR
        (dimension_type IN ('endpoint', 'external_endpoint', 'model')
            AND dimension_value IS NOT NULL AND dimension_value <> '')
    );

-- One policy per (level, scope, period, dimension). The unique indexes from 068
-- did not include the dimension, so widen them. COALESCE keeps the
-- dimension-agnostic row (NULL,NULL) distinct from dimension rows.
DROP INDEX IF EXISTS api.quota_policies_workspace_uniq;
DROP INDEX IF EXISTS api.quota_policies_user_uniq;
DROP INDEX IF EXISTS api.quota_policies_apikey_uniq;

CREATE UNIQUE INDEX quota_policies_workspace_uniq
    ON api.quota_policies (workspace, period,
        COALESCE(dimension_type, ''), COALESCE(dimension_value, ''))
    WHERE level = 'workspace';
CREATE UNIQUE INDEX quota_policies_user_uniq
    ON api.quota_policies (workspace, user_id, period,
        COALESCE(dimension_type, ''), COALESCE(dimension_value, ''))
    WHERE level = 'user';
CREATE UNIQUE INDEX quota_policies_apikey_uniq
    ON api.quota_policies (api_key_id, period,
        COALESCE(dimension_type, ''), COALESCE(dimension_value, ''))
    WHERE level = 'api_key';

-- ----------------------
-- Hierarchy invariant: unchanged for dimension-agnostic policies, but those
-- sums now exclude dimension rows, and dimension rows skip the hierarchy
-- entirely (independent overlays).
-- ----------------------
CREATE OR REPLACE FUNCTION api.validate_quota_hierarchy()
RETURNS TRIGGER AS $$
DECLARE
    v_parent_limit BIGINT;
    v_children_sum BIGINT;
    v_ws   TEXT;
    v_user UUID;
BEGIN
    -- Dimension overlays do not participate in the sum hierarchy.
    IF NEW.dimension_type IS NOT NULL THEN
        IF NEW.level = 'api_key' THEN
            SELECT (metadata).workspace INTO v_ws FROM api.api_keys WHERE id = NEW.api_key_id;
            NEW.workspace := v_ws;
        END IF;
        NEW.updated_at := now();
        RETURN NEW;
    END IF;

    IF NEW.level = 'user' THEN
        SELECT limit_tokens INTO v_parent_limit FROM api.quota_policies
            WHERE level = 'workspace' AND workspace = NEW.workspace AND period = NEW.period
              AND dimension_type IS NULL;
        IF FOUND THEN
            SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies
                WHERE level = 'user' AND workspace = NEW.workspace AND period = NEW.period
                  AND dimension_type IS NULL
                  AND id <> COALESCE(NEW.id, -1);
            IF v_children_sum + NEW.limit_tokens > v_parent_limit THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code":"10050","message":"User quota total exceeds workspace quota","hint":"Lower this quota or raise the workspace quota for this period"}',
                          detail  = '{"status":400,"headers":{"X-Powered-By":"Neutree"}}';
            END IF;
        END IF;
        SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies p
            WHERE p.level = 'api_key' AND p.period = NEW.period AND p.dimension_type IS NULL
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
            WHERE level = 'user' AND workspace = v_ws AND user_id = v_user AND period = NEW.period
              AND dimension_type IS NULL;
        IF FOUND THEN
            SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies p
                WHERE p.level = 'api_key' AND p.period = NEW.period AND p.dimension_type IS NULL
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
        NEW.workspace := v_ws;

    ELSIF NEW.level = 'workspace' THEN
        SELECT COALESCE(SUM(limit_tokens), 0) INTO v_children_sum FROM api.quota_policies
            WHERE level = 'user' AND workspace = NEW.workspace AND period = NEW.period
              AND dimension_type IS NULL;
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

-- ----------------------
-- Drop old signatures before recreating with the dimension params (adding
-- defaulted params would otherwise create overloads). get_all_api_keys_remaining
-- depends on get_api_key_remaining, so drop it first.
-- ----------------------
DROP FUNCTION IF EXISTS api.get_all_api_keys_remaining();
DROP FUNCTION IF EXISTS api.get_api_key_remaining(uuid);
DROP FUNCTION IF EXISTS api.quota_period_usage(uuid[], text, date);
DROP FUNCTION IF EXISTS api.set_quota_policy(text, text, bigint, text, uuid, uuid);
DROP FUNCTION IF EXISTS api.get_quota_scope_usage(text, text, text, uuid, uuid);

-- ----------------------
-- Period usage for a set of keys, optionally narrowed to one dimension.
-- NULL dimension -> total_usage (unchanged). Dimension set -> sum of the
-- matching detailed_dimensional_usage entries' "total".
-- ----------------------
CREATE OR REPLACE FUNCTION api.quota_period_usage(
    p_key_ids         UUID[],
    p_period          TEXT,
    p_today           DATE,
    p_dimension_type  TEXT DEFAULT NULL,
    p_dimension_value TEXT DEFAULT NULL
)
RETURNS BIGINT AS $$
    SELECT CASE
        WHEN p_dimension_type IS NULL THEN
            COALESCE(SUM((d.spec).total_usage), 0)::bigint
        ELSE
            COALESCE(SUM((
                SELECT COALESCE(SUM((kv.value->>'total')::bigint), 0)
                FROM jsonb_each((d.spec).detailed_dimensional_usage) kv
                WHERE (p_dimension_type = 'model'
                          AND split_part(kv.key, '|', 3) = p_dimension_value)
                   OR (p_dimension_type IN ('endpoint', 'external_endpoint')
                          AND split_part(kv.key, '|', 1) = p_dimension_type
                          AND split_part(kv.key, '|', 2) = p_dimension_value)
            )), 0)::bigint
    END
    FROM api.api_daily_usage d
    WHERE (d.spec).api_key_id = ANY(p_key_ids)
      AND (d.spec).usage_date >= api.quota_period_start(p_period, p_today)
      AND (d.spec).usage_date <= p_today;
$$ LANGUAGE sql STABLE SECURITY DEFINER;

-- ----------------------
-- set_quota_policy: upsert one (level, scope, period, dimension) policy.
-- ----------------------
CREATE OR REPLACE FUNCTION api.set_quota_policy(
    p_level           TEXT,
    p_period          TEXT,
    p_limit_tokens    BIGINT,
    p_workspace       TEXT DEFAULT NULL,
    p_user_id         UUID DEFAULT NULL,
    p_api_key_id      UUID DEFAULT NULL,
    p_dimension_type  TEXT DEFAULT NULL,
    p_dimension_value TEXT DEFAULT NULL
) RETURNS api.quota_policies AS $$
DECLARE
    v_ws     TEXT := p_workspace;
    v_result api.quota_policies;
    v_id     BIGINT;
    v_dt     TEXT := NULLIF(p_dimension_type, '');
    v_dv     TEXT := NULLIF(p_dimension_value, '');
BEGIN
    IF p_level = 'api_key' THEN
        IF p_api_key_id IS NULL THEN
            RAISE EXCEPTION 'api_key_id is required for api_key level';
        END IF;
        SELECT (metadata).workspace INTO v_ws FROM api.api_keys WHERE id = p_api_key_id;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'api_key' AND api_key_id = p_api_key_id AND period = p_period
              AND COALESCE(dimension_type, '') = COALESCE(v_dt, '')
              AND COALESCE(dimension_value, '') = COALESCE(v_dv, '');
    ELSIF p_level = 'user' THEN
        IF p_user_id IS NULL OR v_ws IS NULL THEN
            RAISE EXCEPTION 'user_id and workspace are required for user level';
        END IF;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'user' AND workspace = v_ws AND user_id = p_user_id AND period = p_period
              AND COALESCE(dimension_type, '') = COALESCE(v_dt, '')
              AND COALESCE(dimension_value, '') = COALESCE(v_dv, '');
    ELSIF p_level = 'workspace' THEN
        IF v_ws IS NULL THEN
            RAISE EXCEPTION 'workspace is required for workspace level';
        END IF;
        SELECT id INTO v_id FROM api.quota_policies
            WHERE level = 'workspace' AND workspace = v_ws AND period = p_period
              AND COALESCE(dimension_type, '') = COALESCE(v_dt, '')
              AND COALESCE(dimension_value, '') = COALESCE(v_dv, '');
    ELSE
        RAISE EXCEPTION 'invalid level: %', p_level;
    END IF;

    IF v_id IS NULL THEN
        INSERT INTO api.quota_policies
            (level, workspace, user_id, api_key_id, period, limit_tokens, dimension_type, dimension_value)
        VALUES (p_level, v_ws, p_user_id, p_api_key_id, p_period, p_limit_tokens, v_dt, v_dv)
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
-- Minimum remaining tokens for one API key. Dimension-agnostic policies always
-- apply; dimension policies apply only when the request targets that dimension
-- (p_model / p_endpoint + p_endpoint_type passed by the gateway). With no
-- request dimension (the batch/sync path), only agnostic policies constrain.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_api_key_remaining(
    p_api_key_id   UUID,
    p_model        TEXT DEFAULT NULL,
    p_endpoint     TEXT DEFAULT NULL,
    p_endpoint_type TEXT DEFAULT NULL
)
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
        SELECT period, limit_tokens, dimension_type, dimension_value,
               ARRAY[p_api_key_id] AS key_ids
        FROM api.quota_policies
        WHERE level = 'api_key' AND api_key_id = p_api_key_id
        UNION ALL
        SELECT period, limit_tokens, dimension_type, dimension_value,
               ARRAY(SELECT k.id FROM api.api_keys k
                     WHERE k.user_id = v_user AND (k.metadata).workspace = v_ws)
        FROM api.quota_policies
        WHERE level = 'user' AND user_id = v_user AND workspace = v_ws
        UNION ALL
        SELECT period, limit_tokens, dimension_type, dimension_value,
               ARRAY(SELECT k.id FROM api.api_keys k
                     WHERE (k.metadata).workspace = v_ws)
        FROM api.quota_policies
        WHERE level = 'workspace' AND workspace = v_ws
    )
    SELECT MIN(p.limit_tokens
               - api.quota_period_usage(p.key_ids, p.period, v_today,
                                        p.dimension_type, p.dimension_value))
    INTO v_min
    FROM policies p
    WHERE p.dimension_type IS NULL
       OR (p.dimension_type = 'model'
              AND p_model IS NOT NULL AND p.dimension_value = p_model)
       OR (p.dimension_type IN ('endpoint', 'external_endpoint')
              AND p.dimension_type = p_endpoint_type
              AND p_endpoint IS NOT NULL AND p.dimension_value = p_endpoint);

    RETURN v_min;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

-- Batch variant unchanged in shape: every constrained key with its current
-- minimum remaining across its dimension-agnostic policies.
CREATE OR REPLACE FUNCTION api.get_all_api_keys_remaining()
RETURNS TABLE (api_key_id UUID, remaining BIGINT) AS $$
    SELECT k.id, api.get_api_key_remaining(k.id) AS remaining
    FROM api.api_keys k
    WHERE (k.metadata).deletion_timestamp IS NULL
      AND api.get_api_key_remaining(k.id) IS NOT NULL;
$$ LANGUAGE sql STABLE SECURITY DEFINER;

-- ----------------------
-- get_quota_scope_usage: current-period usage for one scope, optionally for a
-- single dimension (so the UI can show used/limit per dimension policy).
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_quota_scope_usage(
    p_level           TEXT,
    p_period          TEXT,
    p_workspace       TEXT DEFAULT NULL,
    p_user_id         UUID DEFAULT NULL,
    p_api_key_id      UUID DEFAULT NULL,
    p_dimension_type  TEXT DEFAULT NULL,
    p_dimension_value TEXT DEFAULT NULL
)
RETURNS BIGINT
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $$
DECLARE
    v_ws      TEXT := p_workspace;
    v_key_ids UUID[];
    v_dt      TEXT := NULLIF(p_dimension_type, '');
    v_dv      TEXT := NULLIF(p_dimension_value, '');
BEGIN
    IF p_level = 'api_key' THEN
        IF p_api_key_id IS NULL THEN
            RAISE EXCEPTION 'api_key_id is required for api_key level';
        END IF;
        SELECT (metadata).workspace INTO v_ws FROM api.api_keys WHERE id = p_api_key_id;
        IF NOT EXISTS (
            SELECT 1 FROM api.api_keys k
            WHERE k.id = p_api_key_id AND k.user_id = auth.uid()
        ) AND NOT api.has_permission(auth.uid(), 'workspace:read', v_ws) THEN
            RAISE EXCEPTION 'permission denied';
        END IF;
        v_key_ids := ARRAY[p_api_key_id];

    ELSIF p_level = 'user' THEN
        IF v_ws IS NULL OR p_user_id IS NULL THEN
            RAISE EXCEPTION 'workspace and user_id are required for user level';
        END IF;
        IF NOT api.has_permission(auth.uid(), 'workspace:read', v_ws) THEN
            RAISE EXCEPTION 'permission denied';
        END IF;
        v_key_ids := ARRAY(
            SELECT k.id FROM api.api_keys k
            WHERE k.user_id = p_user_id AND (k.metadata).workspace = v_ws);

    ELSIF p_level = 'workspace' THEN
        IF v_ws IS NULL THEN
            RAISE EXCEPTION 'workspace is required for workspace level';
        END IF;
        IF NOT api.has_permission(auth.uid(), 'workspace:read', v_ws) THEN
            RAISE EXCEPTION 'permission denied';
        END IF;
        v_key_ids := ARRAY(
            SELECT k.id FROM api.api_keys k
            WHERE (k.metadata).workspace = v_ws);

    ELSE
        RAISE EXCEPTION 'invalid level: %', p_level;
    END IF;

    RETURN api.quota_period_usage(v_key_ids, p_period, CURRENT_DATE, v_dt, v_dv);
END;
$$;

NOTIFY pgrst, 'reload schema';
