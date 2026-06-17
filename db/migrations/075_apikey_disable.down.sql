-- Revert API key disable: drop any 'disabled' rules, restore the resolver (073)
-- and the rule_type CHECK without 'disabled'.

DELETE FROM api.access_policies WHERE rule_type = 'disabled';

ALTER TABLE api.access_policies DROP CONSTRAINT IF EXISTS access_policies_rule_type_check;
ALTER TABLE api.access_policies ADD CONSTRAINT access_policies_rule_type_check CHECK (
    rule_type IN (
        'rate_limit', 'concurrency',
        'model_allowlist', 'endpoint_allowlist',
        'ip_allowlist', 'header_allowlist')
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
    ),
    ma     AS (SELECT rule_spec->'models' AS arr FROM applicable WHERE rule_type = 'model_allowlist'),
    ma_n   AS (SELECT count(*) AS n FROM ma),
    ma_int AS (
        SELECT e FROM ma, jsonb_array_elements_text(ma.arr) AS e
        GROUP BY e HAVING count(*) = (SELECT n FROM ma_n)
    ),
    ea     AS (SELECT rule_spec->'endpoints' AS arr FROM applicable WHERE rule_type = 'endpoint_allowlist'),
    ea_n   AS (SELECT count(*) AS n FROM ea),
    ea_int AS (
        SELECT e FROM ea, jsonb_array_elements(ea.arr) AS e
        GROUP BY e HAVING count(*) = (SELECT n FROM ea_n)
    )
    SELECT jsonb_build_object(
        'rate_limits',
            COALESCE((SELECT jsonb_agg(jsonb_build_object('limit', lim, 'window', win)
                              ORDER BY win) FROM rl), '[]'::jsonb),
        'concurrency', (SELECT max_c FROM cc),
        'allowed_models',
            CASE WHEN (SELECT n FROM ma_n) = 0 THEN NULL
                 ELSE COALESCE((SELECT jsonb_agg(e ORDER BY e) FROM ma_int), '[]'::jsonb) END,
        'allowed_endpoints',
            CASE WHEN (SELECT n FROM ea_n) = 0 THEN NULL
                 ELSE COALESCE((SELECT jsonb_agg(e) FROM ea_int), '[]'::jsonb) END
    ) INTO v_out;

    RETURN v_out;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

NOTIFY pgrst, 'reload schema';
