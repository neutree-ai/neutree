-- API key limits: carry the quota + access limits on the API key itself via an
-- extra field spec.limits (a single object), keeping create/edit atomic.
--
-- limits JSONB shape (absent object / absent field = unlimited):
--   {
--     "token_quota": { "limit": <bigint>, "period": "daily|weekly|monthly|yearly" },
--     "rps": <int>, "rpm": <int>, "concurrency": <int>,
--     "allowed_models": ["model-a", ...],   -- empty array / absent = unlimited
--     "disabled": <bool>
--   }
--
-- Scope: only configuration lives here. Usage/remaining is still derived from
-- the immutable api_daily_usage ledger and never written into spec. API key only.

-- 1) Extend api_key_spec via ALTER TYPE ADD ATTRIBUTE.
ALTER TYPE api.api_key_spec ADD ATTRIBUTE limits JSONB;

-- 2) Backfill the legacy spec.quota -> limits.token_quota (default monthly), only when quota > 0.
UPDATE api.api_keys k
SET spec = ROW(
        (k.spec).quota,
        (k.spec).expires_in,
        jsonb_build_object(
            'token_quota',
            jsonb_build_object('limit', (k.spec).quota, 'period', 'monthly')
        )
    )::api.api_key_spec
WHERE (k.spec).quota IS NOT NULL
  AND (k.spec).quota > 0
  AND (k.spec).limits IS NULL;

-- 3) Add p_limits to create_api_key (a third attribute on the spec ROW).
-- Adding a parameter creates an overload rather than replacing the function,
-- which makes create_api_key(p_workspace, p_name, p_quota) calls ambiguous
-- (function is not unique). So drop the old 5-arg signature first, then create
-- the 6-arg version.
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER);
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_quota BIGINT;
    v_result api.api_keys;
BEGIN
    p_user_id = auth.uid();

    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;

    IF p_display_name IS NULL THEN
        p_display_name := p_name;
    END IF;

    -- Preserve legacy behavior: when only p_quota is given (no explicit limits),
    -- derive a monthly token quota so the gateway still enforces it (mirrors the
    -- spec.quota -> token_quota backfill above).
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota > 0 THEN
        p_limits := jsonb_build_object(
            'token_quota', jsonb_build_object('limit', p_quota, 'period', 'monthly')
        );
    END IF;

    -- Keep the legacy spec.quota field consistent with the enforced token quota
    -- (spec.limits.token_quota.limit) so clients reading either see the same value.
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, p_quota);

    v_key_id := gen_random_uuid();
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);

    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
        ROW(v_quota, p_expires_in, p_limits)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- 4) set_api_key_limits: edit limits (owner-authorized). SECURITY DEFINER with an explicit owner check.
CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    v_result api.api_keys;
BEGIN
    UPDATE api.api_keys k
    -- Mirror the enforced token quota into the legacy spec.quota so both stay
    -- consistent (0 when the new limits carry no token_quota).
    SET spec = ROW(
        COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0),
        (k.spec).expires_in,
        p_limits
    )::api.api_key_spec
    WHERE k.id = p_id AND k.user_id = auth.uid()
    RETURNING * INTO v_result;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;
    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- 5) Period-usage helper: sum the key's total_usage over the current period window (from the immutable ledger).
-- Authorization for per-key usage/quota reads: the gateway (service_role, for
-- enforcement), the key owner, or a holder of workspace:usage-read on the key's
-- workspace. Guards the SECURITY DEFINER usage/remaining helpers below, which are
-- reachable directly via PostgREST and would otherwise leak per-key usage.
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

CREATE OR REPLACE FUNCTION api.api_key_period_usage(p_id UUID, p_period TEXT)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.can_read_api_key_usage(p_id) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    RETURN COALESCE((
        SELECT SUM((d.spec).total_usage)
        FROM api.api_daily_usage d
        WHERE (d.spec).api_key_id = p_id
          AND (d.spec).usage_date >= CASE p_period
                WHEN 'daily'   THEN CURRENT_DATE
                WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
                WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
                ELSE date_trunc('month', CURRENT_DATE)::date
              END
          AND (d.spec).usage_date <= CURRENT_DATE
    ), 0)::bigint;
END;
$$;

-- 6) get_api_key_remaining: the "remaining token" scalar the gateway quota
--    plugin pulls per request. No token_quota -> NULL (unlimited); otherwise
--    limit - current-period usage (may be negative; the gateway blocks at <= 0).
CREATE OR REPLACE FUNCTION api.get_api_key_remaining(p_id UUID)
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

-- 7) get_api_key_limits: the single object the UI reads (config + current-period used/remaining). Owner-only.
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
    -- NULL-safe owner check: a NULL auth.uid() (anon) must not slip past `<>`.
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

-- 8) get_workspace_models: options for the allowed-models dropdown — the
-- client-facing model names visible in the workspace plus the name/type of the
-- endpoint serving them. SECURITY INVOKER (RLS decides visibility).
CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.spec).model.name::text AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.spec).model.name IS NOT NULL
      AND trim((e.spec).model.name) <> ''
    UNION
    SELECT DISTINCT
        k::text                    AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

-- 9) get_api_keys_usage_summary: batched list-page usage (each api_key's total
-- token quota + current-period used/remaining) returned in one call to avoid
-- N+1. Reads spec.limits.token_quota + the usage ledger. SECURITY DEFINER
-- (ledger spans keys), guarded by workspace:usage-read.
CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (api_key_id UUID, period TEXT, token_limit BIGINT, used BIGINT, remaining BIGINT)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:usage-read', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    RETURN QUERY
        -- Single pass: derive each key's period / limit / period-start once, then
        -- LEFT JOIN + GROUP BY aggregates api_daily_usage in one scan rather than
        -- calling the ledger-summing helper once per key.
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
                    WHEN 'daily'   THEN CURRENT_DATE
                    WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
                    WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                    WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
                    ELSE date_trunc('month', CURRENT_DATE)::date
                END AS period_start
        ) lim
        LEFT JOIN api.api_daily_usage d
            ON (d.spec).api_key_id = k.id
           AND (d.spec).usage_date >= lim.period_start
           AND (d.spec).usage_date <= CURRENT_DATE
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND ((k.spec).limits #>> '{token_quota,limit}') IS NOT NULL
          AND ((k.spec).limits #>> '{token_quota,limit}')::bigint > 0
        GROUP BY k.id, lim.period, lim.token_limit;
END;
$$;

NOTIFY pgrst, 'reload schema';
