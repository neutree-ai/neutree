-- Quota & usage control (NEUTREE-GENERAL-9): read-side RPCs for the UI.
--
-- neutree-api only proxies a whitelist of PostgREST tables plus the generic
-- /rpc/* endpoint, so the quota UI reads through these functions rather than
-- the quota_policies table directly.
--
--  * get_quota_policies  - list the policies the caller may see (RLS-scoped).
--  * get_quota_scope_usage - current-period token usage for a single scope,
--    so the UI can render "used / limit / remaining" next to each policy.

-- ----------------------
-- List policies. SECURITY INVOKER so the quota_policies RLS in 065 decides
-- visibility (workspace members see workspace/user policies; key owners see
-- their own api_key policies). Optional filters narrow the result.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_quota_policies(
    p_workspace  TEXT DEFAULT NULL,
    p_level      TEXT DEFAULT NULL,
    p_user_id    UUID DEFAULT NULL,
    p_api_key_id UUID DEFAULT NULL
)
RETURNS SETOF api.quota_policies
LANGUAGE sql
STABLE
SECURITY INVOKER
AS $$
    SELECT *
    FROM api.quota_policies q
    WHERE (p_workspace  IS NULL OR q.workspace  = p_workspace)
      AND (p_level      IS NULL OR q.level      = p_level)
      AND (p_user_id    IS NULL OR q.user_id    = p_user_id)
      AND (p_api_key_id IS NULL OR q.api_key_id = p_api_key_id)
    ORDER BY q.level, q.period;
$$;

-- ----------------------
-- Current-period usage for one scope (workspace / user / api_key). The token
-- ledger spans keys the caller may not own, so this is SECURITY DEFINER and
-- guards access explicitly: workspace/user scopes require workspace:read,
-- api_key scope requires ownership of the key.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_quota_scope_usage(
    p_level      TEXT,
    p_period     TEXT,
    p_workspace  TEXT DEFAULT NULL,
    p_user_id    UUID DEFAULT NULL,
    p_api_key_id UUID DEFAULT NULL
)
RETURNS BIGINT
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $$
DECLARE
    v_ws      TEXT := p_workspace;
    v_key_ids UUID[];
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

    RETURN api.quota_period_usage(v_key_ids, p_period, CURRENT_DATE);
END;
$$;

-- Make the new functions visible to PostgREST immediately.
NOTIFY pgrst, 'reload schema';
