-- Reject non-positive numeric limits in spec.limits.
--
-- Previously create_api_key / set_api_key_limits accepted any JSONB, and the UI
-- silently dropped <= 0 values. That let "set 0 to disable" silently turn into
-- "no limit". Since the RPCs are a direct entry point (not just the UI), enforce
-- positive integers here so an invalid limit fails loudly instead of becoming
-- unlimited. Each numeric limit (token_quota.limit, rps, rpm, concurrency) is
-- optional; when present it must be a positive integer. allowed_models (an array,
-- where [] means deny-all), disabled (boolean) and the period enum are unaffected.

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

    -- token_quota.limit (nested)
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

    -- top-level numeric limits: rps, rpm, concurrency
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
END;
$$ LANGUAGE plpgsql;

-- Re-create create_api_key with a validation gate (body otherwise unchanged).
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

    -- Reject non-positive numeric limits (after the legacy derive above, which is
    -- already guarded by p_quota > 0).
    PERFORM api.validate_api_key_limits(p_limits);

    -- Keep the legacy spec.quota field consistent with the enforced token quota
    -- (spec.limits.token_quota.limit) so clients reading either see the same value.
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);

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

-- Re-create set_api_key_limits with the same validation gate (body otherwise unchanged).
CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    v_result api.api_keys;
BEGIN
    -- Reject non-positive numeric limits before persisting.
    PERFORM api.validate_api_key_limits(p_limits);

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
