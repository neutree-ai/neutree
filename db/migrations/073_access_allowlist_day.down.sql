-- Revert 073: restore the 072 spec-shape CHECK (no 'day', no allowlist shapes)
-- and the 072 resolver (no allowlists).
ALTER TABLE api.access_policies DROP CONSTRAINT IF EXISTS access_policies_spec_shape;
ALTER TABLE api.access_policies ADD CONSTRAINT access_policies_spec_shape CHECK (
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
);

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
        WHERE (level = 'api_key'   AND api_key_id = p_api_key_id)
           OR (level = 'user'      AND workspace = v_ws AND user_id = v_user)
           OR (level = 'workspace' AND workspace = v_ws)
    ),
    rl AS (
        SELECT rule_spec->>'window' AS win,
               MIN((rule_spec->>'limit')::BIGINT) AS lim
        FROM applicable WHERE rule_type = 'rate_limit'
        GROUP BY rule_spec->>'window'
    ),
    cc AS (
        SELECT MIN((rule_spec->>'max')::BIGINT) AS max_c
        FROM applicable WHERE rule_type = 'concurrency'
    )
    SELECT jsonb_build_object(
        'rate_limits',
            COALESCE((SELECT jsonb_agg(jsonb_build_object('limit', lim, 'window', win)
                              ORDER BY win) FROM rl), '[]'::jsonb),
        'concurrency', (SELECT max_c FROM cc)
    ) INTO v_out;

    RETURN v_out;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

NOTIFY pgrst, 'reload schema';
