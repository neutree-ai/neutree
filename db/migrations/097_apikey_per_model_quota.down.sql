-- Revert per-model token quota: restore the pre-097 function bodies.
--
-- Any token_limit already stored on allowed_models entries is left in the JSONB
-- (harmless: nothing below reads it, and the restored validator ignores unknown
-- keys), so re-applying 097 does not lose configuration.

DROP FUNCTION IF EXISTS api.get_api_key_remaining(UUID, TEXT, TEXT, TEXT);

-- 070_apikey_limits.up.sql
CREATE FUNCTION api.get_api_key_remaining(p_id UUID)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits  JSONB;
    v_limit   BIGINT;
    v_period  TEXT;
BEGIN
    IF NOT api.can_read_api_key_usage(p_id) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    SELECT (spec).limits INTO v_limits FROM api.api_keys WHERE id = p_id;
    IF v_limits IS NULL THEN
        RETURN NULL;
    END IF;
    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NULL OR v_limit <= 0 THEN
        RETURN NULL;
    END IF;
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');
    RETURN v_limit - api.api_key_period_usage(p_id, v_period);
END;
$$;

-- 070_apikey_limits.up.sql
CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits JSONB;
    v_uid    UUID;
    v_limit  BIGINT;
    v_period TEXT;
    v_used   BIGINT;
BEGIN
    SELECT (spec).limits, user_id INTO v_limits, v_uid FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND OR v_uid IS DISTINCT FROM auth.uid() THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NOT NULL AND v_limit > 0 THEN
        v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');
        v_used := api.api_key_period_usage(p_id, v_period);
        v_limits := jsonb_set(
            v_limits, '{token_quota}',
            (v_limits->'token_quota')
                || jsonb_build_object('used', v_used, 'remaining', v_limit - v_used)
        );
    END IF;
    RETURN v_limits;
END;
$$;

DROP FUNCTION IF EXISTS api.get_api_keys_usage_summary(TEXT, UUID[]);
DROP FUNCTION IF EXISTS api.get_api_keys_usage_summary(TEXT);

-- 092_api_key_project_folders.up.sql (the last definition before 097)
CREATE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (
    api_key_id UUID,
    period TEXT,
    token_limit BIGINT,
    used BIGINT,
    remaining BIGINT
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
        SELECT
            k.id,
            lim.period,
            lim.token_limit,
            COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used,
            lim.token_limit - COALESCE(SUM((d.spec).total_usage), 0)::bigint AS remaining
        FROM api.api_keys k
        CROSS JOIN LATERAL (
            SELECT
                COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly') AS period,
                ((k.spec).limits #>> '{token_quota,limit}')::bigint AS token_limit,
                CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                    WHEN 'daily' THEN CURRENT_DATE
                    WHEN 'weekly' THEN date_trunc('week', CURRENT_DATE)::date
                    WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                    WHEN 'yearly' THEN date_trunc('year', CURRENT_DATE)::date
                    ELSE date_trunc('month', CURRENT_DATE)::date
                END AS period_start
        ) lim
        LEFT JOIN api.api_daily_usage d
            ON (d.spec).api_key_id = k.id
           AND (d.spec).usage_date >= lim.period_start
           AND (d.spec).usage_date <= CURRENT_DATE
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (k.user_id = auth.uid() OR v_can_read_workspace)
          AND ((k.spec).limits #>> '{token_quota,limit}') IS NOT NULL
          AND ((k.spec).limits #>> '{token_quota,limit}')::bigint > 0
        GROUP BY k.id, lim.period, lim.token_limit;
END;
$$;

-- 078_apikey_allowed_models_endpoint_scope.up.sql
CREATE OR REPLACE FUNCTION api.validate_api_key_limits(p_limits JSONB)
RETURNS VOID
AS $$
DECLARE
    v_field TEXT;
    v_node  JSONB;
    v_num   NUMERIC;
BEGIN
    IF p_limits IS NULL THEN
        RETURN;
    END IF;

    v_node := p_limits #> '{token_quota,limit}';
    IF v_node IS NOT NULL AND jsonb_typeof(v_node) <> 'null' THEN
        IF jsonb_typeof(v_node) <> 'number' THEN
            RAISE EXCEPTION 'Invalid token quota limit: must be a positive integer'
                USING ERRCODE = '22023';
        END IF;
        v_num := v_node::text::numeric;
        IF v_num <= 0 OR v_num <> trunc(v_num) THEN
            RAISE EXCEPTION 'Invalid token quota limit: must be a positive integer'
                USING ERRCODE = '22023';
        END IF;
    END IF;

    FOREACH v_field IN ARRAY ARRAY['rps', 'rpm', 'concurrency'] LOOP
        IF p_limits ? v_field AND jsonb_typeof(p_limits -> v_field) <> 'null' THEN
            v_node := p_limits -> v_field;
            IF jsonb_typeof(v_node) <> 'number' THEN
                RAISE EXCEPTION 'Invalid % limit: must be a positive integer', v_field
                    USING ERRCODE = '22023';
            END IF;
            v_num := v_node::text::numeric;
            IF v_num <= 0 OR v_num <> trunc(v_num) THEN
                RAISE EXCEPTION 'Invalid % limit: must be a positive integer', v_field
                    USING ERRCODE = '22023';
            END IF;
        END IF;
    END LOOP;

    IF p_limits ? 'allowed_models' AND jsonb_typeof(p_limits -> 'allowed_models') <> 'null' THEN
        IF jsonb_typeof(p_limits -> 'allowed_models') <> 'array' THEN
            RAISE EXCEPTION 'Invalid allowed_models: must be an array'
                USING ERRCODE = '22023';
        END IF;
        FOR v_node IN SELECT elem FROM jsonb_array_elements(p_limits -> 'allowed_models') AS elem LOOP
            IF jsonb_typeof(v_node) <> 'object'
               OR NOT (v_node ? 'model')
               OR jsonb_typeof(v_node -> 'model') <> 'string'
               OR length(trim(v_node ->> 'model')) = 0 THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: each item needs a non-empty string model'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'type' AND jsonb_typeof(v_node -> 'type') <> 'string' THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: type must be a string'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'endpoint_name' AND jsonb_typeof(v_node -> 'endpoint_name') <> 'string' THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: endpoint_name must be a string'
                    USING ERRCODE = '22023';
            END IF;
        END LOOP;
    END IF;
END;
$$ LANGUAGE plpgsql;

DROP FUNCTION IF EXISTS api.api_key_model_period_usage(UUID, TEXT, TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS api.api_key_period_start(TEXT);
