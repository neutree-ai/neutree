-- Revert the endpoint-scoped API key model allowlist back to bare model-name
-- strings. This is LOSSY: entries pinned to a specific IE/EE collapse to their
-- model name (the endpoint dimension is dropped), and multiple pins of the same
-- model dedupe to one string. An empty [] (deny-all) is left untouched.

UPDATE api.api_keys k
SET spec = ROW(
        (k.spec).quota,
        (k.spec).expires_in,
        jsonb_set(
            (k.spec).limits,
            '{allowed_models}',
            (
                SELECT COALESCE(jsonb_agg(DISTINCT elem ->> 'model'), '[]'::jsonb)
                FROM jsonb_array_elements((k.spec).limits -> 'allowed_models') AS elem
                WHERE jsonb_typeof(elem) = 'object'
                  AND jsonb_typeof(elem -> 'model') = 'string'
            )
        )
    )::api.api_key_spec
WHERE (k.spec).limits IS NOT NULL
  AND jsonb_typeof((k.spec).limits -> 'allowed_models') = 'array'
  AND EXISTS (
      SELECT 1
      FROM jsonb_array_elements((k.spec).limits -> 'allowed_models') AS elem
      WHERE jsonb_typeof(elem) = 'object'
  );

-- Restore the migration-071 validator (without allowed_models validation).
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

NOTIFY pgrst, 'reload schema';
