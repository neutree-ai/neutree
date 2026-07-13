-- Endpoint-scope the API key model allowlist (NEU-540).
--
-- Previously spec.limits.allowed_models was a flat array of bare model-name
-- strings (["gpt-4", ...]), so a key could not distinguish the same model name
-- exposed by different internal/external endpoints (IE/EE). It becomes an array
-- of endpoint-scoped objects:
--   [{ "model": "gpt-4", "type": "internal"|"external", "endpoint_name": "ep-a" }, ...]
-- where `type` / `endpoint_name` are optional; when both are absent the entry
-- means "any endpoint serving this model" — exactly the old name-only semantics.
--
-- Migration is a pure, lossless format change: each string element `s` becomes
-- { "model": s } (an unpinned entry). We deliberately do NOT expand names into
-- the endpoints that currently serve them: bare-name semantics include future
-- endpoints of the same name, and expanding would silently pin existing keys to
-- today's endpoint set (and turn a name with no current endpoint into deny-all).
-- Existing keys therefore keep identical behavior. Idempotent (skips objects)
-- and order-preserving; an empty [] (deny-all) is left untouched.

UPDATE api.api_keys k
SET spec = ROW(
        (k.spec).quota,
        (k.spec).expires_in,
        jsonb_set(
            (k.spec).limits,
            '{allowed_models}',
            (
                SELECT COALESCE(
                    jsonb_agg(
                        CASE
                            WHEN jsonb_typeof(t.elem) = 'string'
                                THEN jsonb_build_object('model', t.elem #>> '{}')
                            ELSE t.elem  -- already an object: leave as-is (idempotent)
                        END
                        ORDER BY t.ord
                    ),
                    '[]'::jsonb
                )
                FROM jsonb_array_elements((k.spec).limits -> 'allowed_models')
                     WITH ORDINALITY AS t(elem, ord)
            )
        )
    )::api.api_key_spec
WHERE (k.spec).limits IS NOT NULL
  AND jsonb_typeof((k.spec).limits -> 'allowed_models') = 'array'
  AND EXISTS (
      SELECT 1
      FROM jsonb_array_elements((k.spec).limits -> 'allowed_models') AS elem
      WHERE jsonb_typeof(elem) = 'string'
  );

-- Extend the limits validator to cover the new allowed_models shape. The numeric
-- checks are unchanged from migration 071; the appended block validates each
-- allowed_models entry. Callers (create_api_key / set_api_key_limits) resolve
-- api.validate_api_key_limits by name, so replacing it here is enough.
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

    -- allowed_models: optional array of endpoint-scoped entries. Each element must
    -- be an object with a non-empty string `model`; `type` / `endpoint_name`, when
    -- present, must be strings. An empty array (deny-all) is valid.
    IF p_limits ? 'allowed_models' AND jsonb_typeof(p_limits -> 'allowed_models') <> 'null' THEN
        IF jsonb_typeof(p_limits -> 'allowed_models') <> 'array' THEN
            RAISE EXCEPTION 'Invalid allowed_models: must be an array'
                USING ERRCODE = '22023';
        END IF;
        FOR v_node IN SELECT elem FROM jsonb_array_elements(p_limits -> 'allowed_models') AS elem LOOP
            -- Explicit key-presence check: a missing `model` makes v_node -> 'model'
            -- SQL NULL, so jsonb_typeof(...) <> 'string' would be NULL (not TRUE) and
            -- silently pass. `NOT (v_node ? 'model')` catches that case.
            IF jsonb_typeof(v_node) <> 'object'
               OR NOT (v_node ? 'model')
               OR jsonb_typeof(v_node -> 'model') <> 'string'
               OR length(trim(v_node ->> 'model')) = 0 THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: each item needs a non-empty string model'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'type' AND jsonb_typeof(v_node -> 'type') NOT IN ('string', 'null') THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: type must be a string'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'endpoint_name' AND jsonb_typeof(v_node -> 'endpoint_name') NOT IN ('string', 'null') THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: endpoint_name must be a string'
                    USING ERRCODE = '22023';
            END IF;
        END LOOP;
    END IF;
END;
$$ LANGUAGE plpgsql;

NOTIFY pgrst, 'reload schema';
