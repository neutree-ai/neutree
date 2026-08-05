-- NEU-579 follow-up: require a workspace when creating an API key.
--
-- p_workspace has always been a mandatory positional parameter, and the UI and
-- CLI always supply one, but nothing stopped a direct PostgREST RPC call from
-- passing null -- there was no non-null check. A workspace-less key is not a
-- product concept: the enterprise api.has_permission treats a key whose
-- workspace is null as invalid and fails closed, so such a key silently loses
-- access to everything, which is expensive to diagnose. Reject it up front.
--
-- Deliberately limited to a presence check. Also requiring the workspace to
-- exist in api.workspaces would be stricter than any current caller assumes --
-- every create_api_key call in db/dbtest passes a workspace that was never
-- created -- so referential validation is left to a separate change.
--
-- Rebased on the 071 body (which added the validate_api_key_limits gate), not
-- the 070 one; only the guard above IF p_display_name differs from 071.
BEGIN;

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

    -- An API key is always scoped to exactly one workspace.
    IF p_workspace IS NULL OR p_workspace = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10044","message": "workspace is required to create an API key","hint": "Pass p_workspace with the name of an existing workspace"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
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

COMMIT;
