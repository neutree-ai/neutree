-- Per-model token quota on an API key.
--
-- Until now a key had exactly one token pool (spec.limits.token_quota). Models
-- served through one key can have very different costs (a locally hosted model
-- is already paid for; a third-party hosted one bills per token), so a single
-- pool cannot express "this model is free, that one is capped".
--
-- The quota is carried on the allowed_models entries themselves rather than in a
-- parallel per-model map. allowed_models entries are (model, type?, endpoint_name?)
-- triples, not bare model names -- the same model name can be served by both an
-- internal endpoint and an external one -- and the usage ledger's detail key is
-- the same triple ("<type>|<endpoint>|<model>"). Keying quota by bare model name
-- could not express "this model is free internally but capped externally", which
-- is the whole point. Hanging it on the entry also keeps one list instead of two
-- that must be kept consistent.
--
-- Mutual exclusion needs no mode flag: "any entry has a token_limit -> the key's
-- overall token_quota is not enforced" is a rule, evaluated in
-- get_api_key_remaining. Clearing every entry limit falls back to the overall
-- quota by itself.

-- 1) api_key_period_start: the CASE mapping a quota period to the start of the
--    current window, previously copy-pasted into every usage helper.
CREATE OR REPLACE FUNCTION api.api_key_period_start(p_period TEXT)
RETURNS DATE
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE p_period
        WHEN 'daily'   THEN CURRENT_DATE
        WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
        WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
        WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
        ELSE date_trunc('month', CURRENT_DATE)::date
    END;
$$;

-- 1b) api_key_period_reset: when the current window ends and the counter starts
--     again. Derived from the period rather than stored, and computed HERE
--     rather than in the client: CURRENT_DATE is the database's date, and a
--     browser in another timezone would disagree by a day around a boundary --
--     exactly when someone is looking to see whether their quota has reset.
CREATE OR REPLACE FUNCTION api.api_key_period_reset(p_period TEXT)
RETURNS DATE
LANGUAGE sql STABLE
AS $$
    SELECT CASE p_period
        WHEN 'daily'   THEN CURRENT_DATE + 1
        WHEN 'weekly'  THEN (date_trunc('week',  CURRENT_DATE) + interval '1 week')::date
        WHEN 'monthly' THEN (date_trunc('month', CURRENT_DATE) + interval '1 month')::date
        WHEN 'yearly'  THEN (date_trunc('year',  CURRENT_DATE) + interval '1 year')::date
        ELSE (date_trunc('month', CURRENT_DATE) + interval '1 month')::date
    END;
$$;

-- 2) api_key_model_period_usage: current-period tokens for one allowed_models
--    entry. The ledger's detailed_dimensional_usage is keyed
--    "<endpoint_type>|<endpoint_name>|<model>" (054_usage_statistics_enhance.up.sql:131),
--    so an entry that pins neither type nor endpoint_name must sum every detail
--    key for that model rather than read one value.
--
--    The model is the LAST segment and may itself contain "|", so it is taken as
--    "everything after the second separator" instead of split_part(key,'|',3).
--    endpoint_type is always internal/external and endpoint names are k8s-shaped,
--    so the first two segments are separator-free.
--
--    Note this only sees usage recorded with a non-null endpoint_type: rows
--    written before that column existed carry no detail key at all, so per-model
--    accounting starts when the quota is configured and does not backfill.
--
--    p_type is in the GATEWAY vocabulary ("internal" / "external") because that is
--    what allowed_models entries are written in and what the quota plugin reads
--    from kong.ctx.shared. The ledger stores a THIRD spelling: vector derives
--    endpoint_type from the request path
--    (deploy/docker/neutree-core/vector/vector.yml:24, url_split[3]), so it holds
--    the URL segment -- "endpoint" or "external-endpoint". They are translated
--    here rather than at the call sites, so callers only ever deal in the
--    vocabulary their own layer uses.
--
--    (For completeness there is a fourth spelling: get_workspace_models returns
--    "endpoint" / "external_endpoint", underscored. The UI maps that to the
--    gateway vocabulary when it builds an allowlist entry, so it never reaches
--    here.)
CREATE OR REPLACE FUNCTION api.api_key_model_period_usage(
    p_id       UUID,
    p_period   TEXT,
    p_model    TEXT,
    p_type     TEXT DEFAULT NULL,
    p_endpoint TEXT DEFAULT NULL
)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_ledger_type TEXT;
BEGIN
    IF NOT api.can_read_api_key_usage(p_id) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    v_ledger_type := CASE p_type
        WHEN 'internal' THEN 'endpoint'
        WHEN 'external' THEN 'external-endpoint'
        ELSE p_type
    END;

    RETURN COALESCE((
        SELECT SUM(COALESCE((kv.value ->> 'total')::bigint, 0))
        FROM api.api_daily_usage d
        CROSS JOIN LATERAL jsonb_each(
            COALESCE((d.spec).detailed_dimensional_usage, '{}'::jsonb)
        ) AS kv
        WHERE (d.spec).api_key_id = p_id
          AND (d.spec).usage_date >= api.api_key_period_start(p_period)
          AND (d.spec).usage_date <= CURRENT_DATE
          AND substr(
                  kv.key,
                  length(split_part(kv.key, '|', 1)) + length(split_part(kv.key, '|', 2)) + 3
              ) = p_model
          AND (v_ledger_type IS NULL OR split_part(kv.key, '|', 1) = v_ledger_type)
          AND (p_endpoint IS NULL OR split_part(kv.key, '|', 2) = p_endpoint)
    ), 0)::bigint;
END;
$$;

-- 3) validate_api_key_limits: additionally validate per-entry token_limit and
--    reject overlapping entries for a model that carries one.
--
--    Body is otherwise unchanged from 078_apikey_allowed_models_endpoint_scope.up.sql.
CREATE OR REPLACE FUNCTION api.validate_api_key_limits(p_limits JSONB)
RETURNS VOID
AS $$
DECLARE
    v_field TEXT;
    v_node  JSONB;
    v_num   NUMERIC;
    v_dup   TEXT;
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
    -- present, must be strings (JSON null is rejected -- Go unmarshals it to "" and
    -- the Kong schema/enforcement treat "unset" as absent, not null, so a stored
    -- null would be an ambiguous shape rather than a meaningful value). An empty
    -- array (deny-all) is valid. `token_limit`, when present, must be a positive
    -- integer, same two-state rule as every other numeric limit.
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
            IF v_node ? 'type' AND jsonb_typeof(v_node -> 'type') <> 'string' THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: type must be a string'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'endpoint_name' AND jsonb_typeof(v_node -> 'endpoint_name') <> 'string' THEN
                RAISE EXCEPTION 'Invalid allowed_models entry: endpoint_name must be a string'
                    USING ERRCODE = '22023';
            END IF;
            IF v_node ? 'token_limit' AND jsonb_typeof(v_node -> 'token_limit') <> 'null' THEN
                IF jsonb_typeof(v_node -> 'token_limit') <> 'number' THEN
                    RAISE EXCEPTION 'Invalid allowed_models entry: token_limit must be a positive integer'
                        USING ERRCODE = '22023';
                END IF;
                v_num := (v_node ->> 'token_limit')::numeric;
                IF v_num <= 0 OR v_num <> trunc(v_num) THEN
                    RAISE EXCEPTION 'Invalid allowed_models entry: token_limit must be a positive integer'
                        USING ERRCODE = '22023';
                END IF;
            END IF;
        END LOOP;

        -- Overlap check. An entry leaving `type` / `endpoint_name` empty is a
        -- wildcard, so two entries for one model can both match a request; a quota
        -- is only meaningful over a partition, and overlapping entries would make
        -- "used / remaining" unattributable. Two entries of the same model overlap
        -- unless they disagree on a dimension BOTH of them pin.
        --
        -- Enforced only for models that actually carry a token_limit: 078 migrated
        -- legacy name-only entries into this shape, so rejecting every overlap
        -- unconditionally would break existing keys on their next edit. Keys
        -- without per-model quotas keep their current behaviour exactly.
        WITH e AS (
            SELECT ord,
                   elem ->> 'model'                     AS model,
                   NULLIF(elem ->> 'type', '')          AS ep_type,
                   NULLIF(elem ->> 'endpoint_name', '') AS ep_name,
                   (elem ->> 'token_limit')             AS token_limit
            FROM jsonb_array_elements(p_limits -> 'allowed_models') WITH ORDINALITY AS t(elem, ord)
        )
        SELECT a.model INTO v_dup
        FROM e a
        JOIN e b ON a.model = b.model AND a.ord < b.ord
        WHERE a.model IN (SELECT model FROM e WHERE token_limit IS NOT NULL)
          AND (a.ep_type IS NULL OR b.ep_type IS NULL OR a.ep_type = b.ep_type)
          AND (a.ep_name IS NULL OR b.ep_name IS NULL OR a.ep_name = b.ep_name)
        LIMIT 1;

        IF v_dup IS NOT NULL THEN
            RAISE EXCEPTION 'Invalid allowed_models: entries for model % overlap; entries of a model with a token_limit must not overlap', v_dup
                USING ERRCODE = '22023';
        END IF;
    END IF;
END;
$$ LANGUAGE plpgsql;

-- 4) get_api_key_remaining: now resolves against the request's model when the key
--    uses per-model quotas. The old single-argument signature is dropped rather
--    than overloaded, because adding defaulted parameters to a same-named function
--    would make the existing one-argument call ambiguous. Callers that still pass
--    only p_id (a gateway not yet upgraded) keep working via the defaults.
DROP FUNCTION IF EXISTS api.get_api_key_remaining(UUID);

CREATE FUNCTION api.get_api_key_remaining(
    p_id       UUID,
    p_model    TEXT DEFAULT NULL,
    p_type     TEXT DEFAULT NULL,
    p_endpoint TEXT DEFAULT NULL
)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits        JSONB;
    v_limit         BIGINT;
    v_period        TEXT;
    v_has_per_model BOOLEAN;
    v_entry_type    TEXT;
    v_entry_name    TEXT;
BEGIN
    IF NOT api.can_read_api_key_usage(p_id) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    SELECT (spec).limits INTO v_limits FROM api.api_keys WHERE id = p_id;
    IF v_limits IS NULL THEN
        RETURN NULL;
    END IF;
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(COALESCE(v_limits -> 'allowed_models', '[]'::jsonb)) AS e
        WHERE e ->> 'token_limit' IS NOT NULL
    ) INTO v_has_per_model;

    IF v_has_per_model THEN
        -- Per-model granularity: the key's overall token_quota is not enforced.
        -- Without a model on the call there is nothing to resolve, so return
        -- "unlimited" and let the gateway through -- the same fail-open stance the
        -- quota plugin already takes when it cannot determine a remaining count.
        -- This is what a not-yet-upgraded gateway (p_id only) sees during a rolling
        -- upgrade.
        IF p_model IS NULL THEN
            RETURN NULL;
        END IF;

        -- validate_api_key_limits guarantees the matching entries of a model with a
        -- token_limit do not overlap, so at most one row can match here.
        SELECT (e ->> 'token_limit')::bigint,
               NULLIF(e ->> 'type', ''),
               NULLIF(e ->> 'endpoint_name', '')
          INTO v_limit, v_entry_type, v_entry_name
        FROM jsonb_array_elements(COALESCE(v_limits -> 'allowed_models', '[]'::jsonb)) AS e
        WHERE e ->> 'model' = p_model
          AND e ->> 'token_limit' IS NOT NULL
          AND (NULLIF(e ->> 'type', '')          IS NULL OR NULLIF(e ->> 'type', '')          = p_type)
          AND (NULLIF(e ->> 'endpoint_name', '') IS NULL OR NULLIF(e ->> 'endpoint_name', '') = p_endpoint)
        LIMIT 1;

        -- No entry, or an entry without a limit: this model is unlimited. Access
        -- control is the access plugin's job, not the quota plugin's.
        IF v_limit IS NULL THEN
            RETURN NULL;
        END IF;

        RETURN v_limit - api.api_key_model_period_usage(
            p_id, v_period, p_model, v_entry_type, v_entry_name);
    END IF;

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NULL OR v_limit <= 0 THEN
        RETURN NULL;
    END IF;
    RETURN v_limit - api.api_key_period_usage(p_id, v_period);
END;
$$;

-- 5) get_api_key_limits: the UI's single read. Per-entry used/remaining is folded
--    into each allowed_models entry that has a token_limit; the overall
--    token_quota keeps reporting used/remaining only while it is the granularity
--    actually in force, so the UI never shows two competing quota readouts.
CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits        JSONB;
    v_uid           UUID;
    v_limit         BIGINT;
    v_period        TEXT;
    v_used          BIGINT;
    v_has_per_model BOOLEAN;
    v_models        JSONB;
BEGIN
    SELECT (spec).limits, user_id INTO v_limits, v_uid FROM api.api_keys WHERE id = p_id;
    -- NULL-safe owner check: a NULL auth.uid() (anon) must not slip past `<>`.
    IF NOT FOUND OR v_uid IS DISTINCT FROM auth.uid() THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(COALESCE(v_limits -> 'allowed_models', '[]'::jsonb)) AS e
        WHERE e ->> 'token_limit' IS NOT NULL
    ) INTO v_has_per_model;

    IF v_has_per_model THEN
        SELECT jsonb_agg(
                   CASE
                       WHEN e ->> 'token_limit' IS NULL THEN e
                       ELSE e || jsonb_build_object(
                           'used', u.used,
                           'remaining', (e ->> 'token_limit')::bigint - u.used
                       )
                   END
                   ORDER BY ord
               )
          INTO v_models
        FROM jsonb_array_elements(v_limits -> 'allowed_models') WITH ORDINALITY AS t(e, ord)
        CROSS JOIN LATERAL (
            SELECT CASE
                WHEN e ->> 'token_limit' IS NULL THEN 0::bigint
                ELSE api.api_key_model_period_usage(
                    p_id, v_period, e ->> 'model',
                    NULLIF(e ->> 'type', ''), NULLIF(e ->> 'endpoint_name', ''))
            END AS used
        ) u;

        RETURN jsonb_set(v_limits, '{allowed_models}', COALESCE(v_models, '[]'::jsonb))
               || jsonb_build_object(
                    'quota_granularity', 'per_model',
                    'quota_period', v_period,
                    'quota_period_start', api.api_key_period_start(v_period),
                    'quota_resets_at', api.api_key_period_reset(v_period));
    END IF;

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NOT NULL AND v_limit > 0 THEN
        v_used := api.api_key_period_usage(p_id, v_period);
        v_limits := jsonb_set(
            v_limits, '{token_quota}',
            (v_limits->'token_quota')
                || jsonb_build_object('used', v_used, 'remaining', v_limit - v_used)
        );
    END IF;
    RETURN v_limits || jsonb_build_object(
        'quota_granularity', 'overall',
        'quota_period', v_period,
        'quota_period_start', api.api_key_period_start(v_period),
        'quota_resets_at', api.api_key_period_reset(v_period));
END;
$$;

-- 6) get_api_keys_usage_summary: the list page's batched read. It previously
--    skipped every key without an overall token_quota, which would have hidden
--    keys that only carry per-model limits. It now returns a row per key that has
--    a quota of either granularity: `granularity` says which, and for per-model
--    keys token_limit / remaining are NULL (there is no single pool) while `used`
--    still carries the key's total period usage.
--
--    For a per-model key it also names the MOST UTILISED limited model, because
--    that is the only single figure the list can honestly show: unlimited models
--    have no denominator to average in, and two models with different limits
--    cannot be pooled. A list exists to answer "which key needs attention", and
--    the answer is the model closest to (or past) its limit.
--
--    p_api_key_ids bounds the work. The per-model figures need
--    detailed_dimensional_usage expanded and joined per allowlist entry, which
--    costs keys x models x days -- measured at ~1.2s for 1000 keys with 20
--    limited models each, against ~8ms for the totals alone. Restricted to one
--    page of keys the same worst case is ~18ms. Callers should pass the page
--    they are rendering; NULL keeps the whole-workspace behaviour and its cost.
--
--    Visibility is unchanged from 092_api_key_project_folders.up.sql: no hard
--    permission raise, rows are filtered to the caller's own keys unless they hold
--    workspace:usage-read.
DROP FUNCTION IF EXISTS api.get_api_keys_usage_summary(TEXT);

CREATE FUNCTION api.get_api_keys_usage_summary(
    p_workspace   TEXT,
    p_api_key_ids UUID[] DEFAULT NULL
)
RETURNS TABLE (
    api_key_id       UUID,
    period           TEXT,
    granularity      TEXT,
    token_limit      BIGINT,
    used             BIGINT,
    remaining        BIGINT,
    top_model        TEXT,
    top_model_type   TEXT,
    top_model_used   BIGINT,
    top_model_limit  BIGINT,
    limited_models   INTEGER
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
    WITH visible AS (
        SELECT k.id,
               (k.spec).limits AS limits,
               COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly') AS period,
               EXISTS (
                   SELECT 1
                   FROM jsonb_array_elements(
                       COALESCE((k.spec).limits -> 'allowed_models', '[]'::jsonb)
                   ) AS e
                   WHERE e ->> 'token_limit' IS NOT NULL
               ) AS has_per_model
        FROM api.api_keys k
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (k.user_id = auth.uid() OR v_can_read_workspace)
          AND (p_api_key_ids IS NULL OR k.id = ANY (p_api_key_ids))
    ),
    scoped AS (
        SELECT v.*, api.api_key_period_start(v.period) AS period_start
        FROM visible v
        WHERE v.has_per_model
           OR (
                (v.limits #>> '{token_quota,limit}') IS NOT NULL
                AND (v.limits #>> '{token_quota,limit}')::bigint > 0
              )
    ),
    -- Whole-key totals: one pass, as before.
    totals AS (
        SELECT s.id,
               COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used
        FROM scoped s
        LEFT JOIN api.api_daily_usage d
               ON (d.spec).api_key_id = s.id
              AND (d.spec).usage_date >= s.period_start
              AND (d.spec).usage_date <= CURRENT_DATE
        GROUP BY s.id
    ),
    -- Per-entry figures, only for keys actually on per-model quotas.
    entries AS (
        SELECT s.id,
               s.period_start,
               e ->> 'model'                     AS model,
               NULLIF(e ->> 'type', '')          AS ep_type,
               NULLIF(e ->> 'endpoint_name', '') AS ep_name,
               (e ->> 'token_limit')::bigint     AS token_limit
        FROM scoped s
        CROSS JOIN LATERAL jsonb_array_elements(
            COALESCE(s.limits -> 'allowed_models', '[]'::jsonb)
        ) AS e
        WHERE s.has_per_model
          AND e ->> 'token_limit' IS NOT NULL
    ),
    -- The ledger's detail keys, expanded once for the keys in scope. The first
    -- segment is the ledger's own spelling of the endpoint type, which is not
    -- the gateway's -- see api_key_model_period_usage for why they differ.
    detail AS (
        SELECT (d.spec).api_key_id AS id,
               (d.spec).usage_date AS usage_date,
               split_part(kv.key, '|', 1) AS ledger_type,
               split_part(kv.key, '|', 2) AS ep_name,
               substr(kv.key,
                      length(split_part(kv.key, '|', 1))
                    + length(split_part(kv.key, '|', 2)) + 3) AS model,
               COALESCE((kv.value ->> 'total')::bigint, 0) AS used
        FROM api.api_daily_usage d
        CROSS JOIN LATERAL jsonb_each(
            COALESCE((d.spec).detailed_dimensional_usage, '{}'::jsonb)
        ) AS kv
        WHERE (d.spec).api_key_id IN (SELECT id FROM entries)
          AND (d.spec).usage_date <= CURRENT_DATE
    ),
    per_entry AS (
        SELECT e.id, e.model, e.ep_type, e.token_limit,
               COALESCE(SUM(x.used), 0)::bigint AS used
        FROM entries e
        LEFT JOIN detail x
               ON x.id = e.id
              AND x.usage_date >= e.period_start
              AND x.model = e.model
              AND (e.ep_type IS NULL OR x.ledger_type = CASE e.ep_type
                       WHEN 'internal' THEN 'endpoint'
                       WHEN 'external' THEN 'external-endpoint'
                       ELSE e.ep_type END)
              AND (e.ep_name IS NULL OR x.ep_name = e.ep_name)
        GROUP BY e.id, e.model, e.ep_type, e.token_limit
    ),
    -- Most utilised entry per key. Over-limit entries sort first by
    -- construction: their ratio is above 1, which is exactly what a list should
    -- surface.
    top AS (
        SELECT DISTINCT ON (pe.id)
               pe.id, pe.model, pe.ep_type, pe.used, pe.token_limit
        FROM per_entry pe
        ORDER BY pe.id, (pe.used::numeric / NULLIF(pe.token_limit, 0)) DESC NULLS LAST
    ),
    counts AS (
        SELECT id, count(*)::int AS limited_models FROM per_entry GROUP BY id
    )
    SELECT s.id,
           s.period,
           CASE WHEN s.has_per_model THEN 'per_model' ELSE 'overall' END,
           CASE WHEN s.has_per_model THEN NULL::bigint
                ELSE (s.limits #>> '{token_quota,limit}')::bigint END,
           t.used,
           CASE WHEN s.has_per_model THEN NULL::bigint
                ELSE (s.limits #>> '{token_quota,limit}')::bigint - t.used END,
           top.model,
           top.ep_type,
           top.used,
           top.token_limit,
           COALESCE(c.limited_models, 0)
    FROM scoped s
    JOIN totals t ON t.id = s.id
    LEFT JOIN top ON top.id = s.id
    LEFT JOIN counts c ON c.id = s.id;
END;
$$;
