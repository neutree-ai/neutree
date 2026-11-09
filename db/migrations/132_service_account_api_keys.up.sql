-- API keys owned by a ServiceAccount, created and governed by service_account:*
-- on its Team, and usage reads that show SA keys to readers of the SA or of
-- its Team's usage.

-- An API key belongs to a user (user_id) or to an SA (service_account_id),
-- never both; created_by is the user who created it. An SA key has no project
-- (10316) and its workspace and owner never change (10318). It is live while
-- its SA is enabled and its Team is Active, and keeps working when its
-- creator leaves. An SA with API keys cannot be deleted (10319): deleting the
-- rows would leave gateway credentials behind. Folder listings stay personal.

ALTER TABLE api.api_keys
    ALTER COLUMN user_id DROP NOT NULL,
    ADD COLUMN service_account_id UUID REFERENCES api.service_accounts (id),
    ADD COLUMN created_by UUID REFERENCES api.user_profiles (id) ON DELETE SET NULL;

ALTER TABLE api.api_keys DISABLE TRIGGER USER;

UPDATE api.api_keys SET created_by = user_id WHERE created_by IS NULL;

ALTER TABLE api.api_keys ENABLE TRIGGER USER;

ALTER TABLE api.api_keys
    ADD CONSTRAINT api_keys_owner_check CHECK ((user_id IS NULL) <> (service_account_id IS NULL));

CREATE INDEX api_keys_service_account_id_idx ON api.api_keys (service_account_id);

-- Named to fire before validate_api_key_project_reference, so an SA key with
-- a project gets 10316 rather than the personal-folder error.
CREATE OR REPLACE FUNCTION api.api_keys_check_owner()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_by := COALESCE(auth.uid(), NEW.created_by, NEW.user_id);
    ELSIF auth.uid() IS NOT NULL AND (
        NEW.user_id IS DISTINCT FROM OLD.user_id
        OR NEW.service_account_id IS DISTINCT FROM OLD.service_account_id
        OR (NEW.created_by IS NOT NULL AND NEW.created_by IS DISTINCT FROM OLD.created_by)
        OR (NEW.service_account_id IS NOT NULL
            AND (NEW.metadata).workspace IS DISTINCT FROM (OLD.metadata).workspace)
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10318","message": "The owner of an API key cannot be changed","hint": "Create a new API key for the other owner or workspace"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NEW.service_account_id IS NOT NULL AND (NEW.spec).project_id IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10316","message": "An API key of a ServiceAccount has no project","hint": "Projects are personal folders; leave spec.project_id empty"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.api_keys_check_owner() FROM PUBLIC;

CREATE TRIGGER api_keys_check_owner
    BEFORE INSERT OR UPDATE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION api.api_keys_check_owner();

-- Soft-deleted keys count too: the controller still has to revoke them at
-- the gateway. role_assignments has no foreign key to its subject.
CREATE OR REPLACE FUNCTION api.service_accounts_before_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_keys INTEGER;
BEGIN
    SELECT count(*) INTO v_keys FROM api.api_keys k WHERE k.service_account_id = OLD.id;

    IF v_keys > 0 THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10319","message": "cannot delete ServiceAccount ''%s''","hint": "%s API key(s) of this ServiceAccount still exist; delete them first"}',
                (OLD.metadata).name, v_keys),
            detail = '{"status": 409, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    DELETE FROM api.role_assignments ra
    WHERE (ra.spec).subject_kind = 'ServiceAccount'
      AND (ra.spec).user_id = OLD.id;

    RETURN OLD;
END;
$$;

CREATE TRIGGER service_accounts_before_delete
    BEFORE DELETE ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION api.service_accounts_before_delete();

REVOKE ALL ON FUNCTION api.service_accounts_before_delete() FROM PUBLIC;

-- Usage of an SA: whoever reads the SA, or reads its Team's usage.
CREATE OR REPLACE FUNCTION api.can_read_service_account_usage(p_user UUID, p_service_account_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    RETURN api.has_permission_on_service_account(p_user, 'service_account:read', p_service_account_id) IS TRUE
        OR api.has_permission_on_service_account(p_user, 'team_usage:read', p_service_account_id) IS TRUE;
END;
$$;

-- May the caller do p_permission on an API key owned by the user p_user_id or
-- the SA p_service_account_id? A user acts on their own keys; an SA key needs
-- p_permission on the SA's Team.
CREATE OR REPLACE FUNCTION api.can_act_on_api_key(
    p_user_id UUID,
    p_service_account_id UUID,
    p_permission api.permission_action
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT COALESCE(p_user_id = auth.uid(), FALSE)
        OR (p_service_account_id IS NOT NULL
            AND api.has_permission_on_service_account(auth.uid(), p_permission, p_service_account_id));
$$;

-- May the caller read the usage of an API key owned by the user p_user_id or
-- the SA p_service_account_id? Its user, or a usage reader of the SA.
CREATE OR REPLACE FUNCTION api.can_read_api_key_owner_usage(
    p_user_id UUID,
    p_service_account_id UUID
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT COALESCE(p_user_id = auth.uid(), FALSE)
        OR (p_service_account_id IS NOT NULL
            AND api.can_read_service_account_usage(auth.uid(), p_service_account_id));
$$;

CREATE OR REPLACE FUNCTION api.can_read_api_key_usage(p_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_owner UUID;
    v_sa    UUID;
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
    SELECT user_id, service_account_id, (metadata).workspace INTO v_owner, v_sa, v_ws FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    RETURN api.can_read_api_key_owner_usage(v_owner, v_sa)
        OR api.has_permission(auth.uid(), 'workspace:usage-read', v_ws) IS TRUE;
END;
$$;

-- ----------------------
-- RLS of API keys
-- ----------------------

DROP POLICY IF EXISTS "Users can see their own API keys" ON api.api_keys;

DROP POLICY IF EXISTS "Users can create their own API keys" ON api.api_keys;

DROP POLICY IF EXISTS "Users can update their own API keys" ON api.api_keys;

DROP POLICY IF EXISTS "Users can delete their own API keys" ON api.api_keys;

CREATE POLICY "api_key read policy" ON api.api_keys
    FOR SELECT
    USING (
        user_id = auth.uid()
        OR (service_account_id IS NOT NULL AND (
            (SELECT api.has_permission(auth.uid(), 'service_account:read', NULL))
            OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:read', service_account_id)))
    );

CREATE POLICY "api_key create policy" ON api.api_keys
    FOR INSERT
    WITH CHECK (
        (service_account_id IS NULL AND user_id = auth.uid())
        OR (user_id IS NULL AND service_account_id IS NOT NULL
            AND api.has_permission_on_service_account(auth.uid(), 'service_account:create', service_account_id))
    );

-- A soft delete is an update that sets metadata.deletion_timestamp.
CREATE POLICY "api_key update policy" ON api.api_keys
    FOR UPDATE
    USING (
        user_id = auth.uid()
        OR (service_account_id IS NOT NULL AND (
            (SELECT api.has_permission(auth.uid(), 'service_account:update', NULL))
            OR (SELECT api.has_permission(auth.uid(), 'service_account:delete', NULL))
            OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:update', service_account_id)
            OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:delete', service_account_id)))
    )
    WITH CHECK (
        user_id = auth.uid()
        OR (service_account_id IS NOT NULL AND (
            ((metadata).deletion_timestamp IS NULL AND (
                (SELECT api.has_permission(auth.uid(), 'service_account:update', NULL))
                OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:update', service_account_id)))
            OR ((metadata).deletion_timestamp IS NOT NULL AND (
                (SELECT api.has_permission(auth.uid(), 'service_account:delete', NULL))
                OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:delete', service_account_id)))))
    );

CREATE POLICY "api_key delete policy" ON api.api_keys
    FOR DELETE
    USING (
        user_id = auth.uid()
        OR (service_account_id IS NOT NULL AND (
            (SELECT api.has_permission(auth.uid(), 'service_account:delete', NULL))
            OR api.has_service_account_scoped_permission(auth.uid(), 'service_account:delete', service_account_id)))
    );

-- The keys api_keys RLS shows, with SA keys only for usage readers.
DROP POLICY IF EXISTS "API keys owners can see related daily usage" ON api.api_daily_usage;

CREATE POLICY "API keys owners can see related daily usage" ON api.api_daily_usage
    FOR SELECT
    USING (((spec).api_key_id) IN (
        SELECT id FROM api.api_keys
        WHERE api.can_read_api_key_owner_usage(user_id, service_account_id)
    ));

-- ----------------------
-- Create
-- ----------------------

-- Inserts an API key owned by the user p_user_id or the SA
-- p_service_account_id, after the caller's own checks. Returns the row with its
-- key value.
CREATE OR REPLACE FUNCTION api.insert_api_key(
    p_user_id UUID,
    p_service_account_id UUID,
    p_workspace TEXT,
    p_name TEXT,
    p_display_name TEXT,
    p_expires_in INTEGER,
    p_limits JSONB,
    p_project_id UUID,
    p_description TEXT
) RETURNS api.api_keys
SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE
    v_key_id UUID;
    v_key_value TEXT;
    v_quota BIGINT;
    v_result api.api_keys;
BEGIN
    PERFORM api.validate_api_key_limits(p_limits);
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);
    v_key_id := gen_random_uuid();
    IF p_name IS NULL OR length(trim(p_name)) = 0 THEN
        p_name := 'apikey-' || v_key_id::text;
    END IF;
    IF p_display_name IS NULL OR length(trim(p_display_name)) = 0 THEN
        p_display_name := p_name;
    END IF;
    v_key_value := api.generate_api_key(COALESCE(p_user_id, p_service_account_id), v_key_id, p_expires_in);

    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id, service_account_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(
            p_name, p_display_name, p_workspace, NULL, now(), now(),
            '{}'::json, '{}'::json
        )::api.metadata,
        ROW(v_quota, p_expires_in, p_limits, p_project_id, COALESCE(p_description, ''))::api.api_key_spec,
        ROW('Pending', now(), NULL, v_key_value, 0, now(), NULL)::api.api_key_status,
        p_user_id,
        p_service_account_id
    )
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$;

REVOKE ALL ON FUNCTION api.insert_api_key(UUID, UUID, TEXT, TEXT, TEXT, INTEGER, JSONB, UUID, TEXT) FROM PUBLIC;

CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL,
    p_project_id UUID DEFAULT NULL,
    p_description TEXT DEFAULT ''
) RETURNS api.api_keys
SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE
    v_user_id UUID := auth.uid();
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = v_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;
    -- An API key is always scoped to exactly one workspace.
    IF p_workspace IS NULL OR p_workspace = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10044","message": "workspace is required to create an API key","hint": "Pass p_workspace with the name of an existing workspace"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = v_user_id
          AND (metadata).workspace = p_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the selected workspace' USING ERRCODE = '22023';
    END IF;
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota > 0 THEN
        p_limits := jsonb_build_object(
            'token_quota', jsonb_build_object('limit', p_quota, 'period', 'monthly')
        );
    END IF;

    RETURN api.insert_api_key(v_user_id, NULL, p_workspace, p_name, p_display_name,
        p_expires_in, p_limits, p_project_id, p_description);
END;
$$;

-- Creates an API key of the SA p_service_account_id in the workspace
-- p_workspace; the caller needs service_account:create on the SA's Team.
-- Parameters as in api.create_api_key, less the project. The key works where
-- the SA's roles grant model:invoke; it is created even where they do not yet.
CREATE OR REPLACE FUNCTION api.create_service_account_api_key(
    p_service_account_id UUID,
    p_workspace TEXT,
    p_name TEXT DEFAULT NULL,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL,
    p_description TEXT DEFAULT ''
) RETURNS api.api_keys
SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE
    v_user_id UUID := auth.uid();
BEGIN
    PERFORM api.refuse_service_account_actor();

    IF v_user_id IS NULL OR NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = v_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;

    IF p_service_account_id IS NULL THEN
        RAISE invalid_parameter_value USING message = 'p_service_account_id is required';
    END IF;

    IF api.has_permission_on_service_account(v_user_id, 'service_account:create', p_service_account_id) IS NOT TRUE THEN
        RAISE insufficient_privilege
            USING message = 'service_account:create on the Team of the ServiceAccount is required to create its API keys';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM api.service_accounts s
        JOIN api.teams t ON t.id = s.team_id
        WHERE s.id = p_service_account_id
          AND (t.status).phase = 'Active'
          AND (s.spec).disabled IS NOT TRUE
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10317","message": "API keys can only be created for an enabled ServiceAccount of an Active Team","hint": "Enable the ServiceAccount; an Inactive Team is gone from its directory"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF p_workspace IS NULL OR p_workspace = ''
        OR NOT EXISTS (
            SELECT 1 FROM api.workspaces w
            WHERE (w.metadata).name = p_workspace AND (w.metadata).deletion_timestamp IS NULL) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10044","message": "workspace is required to create an API key","hint": "Pass p_workspace with the name of an existing workspace"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN api.insert_api_key(NULL, p_service_account_id, p_workspace, p_name, p_display_name,
        p_expires_in, p_limits, NULL, p_description);
END;
$$;

CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits        JSONB;
    v_uid           UUID;
    v_sa            UUID;
    v_limit         BIGINT;
    v_period        TEXT;
    v_used          BIGINT;
    v_has_per_model BOOLEAN;
    v_models        JSONB;
BEGIN
    SELECT (spec).limits, user_id, service_account_id INTO v_limits, v_uid, v_sa FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND OR NOT api.can_read_api_key_owner_usage(v_uid, v_sa) THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');

    SELECT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
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
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) WITH ORDINALITY AS t(e, ord)
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

CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(
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
                       api.api_key_allowed_models((k.spec).limits)
                   ) AS e
                   WHERE e ->> 'token_limit' IS NOT NULL
               ) AS has_per_model
        FROM api.api_keys k
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (v_can_read_workspace OR api.can_read_api_key_owner_usage(k.user_id, k.service_account_id))
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
    -- Whole-key totals: one pass.
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
            api.api_key_allowed_models(s.limits)
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
        SELECT e.id, e.model, e.ep_type, e.ep_name, e.token_limit,
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
        GROUP BY e.id, e.model, e.ep_type, e.ep_name, e.token_limit
    ),
    -- Most utilised entry per key. Over-limit entries sort first by
    -- construction: their ratio is above 1, which is exactly what a list should
    -- surface.
    top AS (
        SELECT DISTINCT ON (pe.id)
               pe.id, pe.model, pe.ep_type, pe.used, pe.token_limit
        FROM per_entry pe
        ORDER BY pe.id, (pe.used::numeric / NULLIF(pe.token_limit, 0)) DESC NULLS LAST,
                 pe.model, pe.ep_type NULLS FIRST, pe.ep_name NULLS FIRST
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

CREATE OR REPLACE FUNCTION api.get_usage_by_dimension(
    p_start_date DATE,
    p_end_date DATE,
    p_api_key_id UUID DEFAULT NULL,
    p_endpoint_name TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL
)
RETURNS TABLE (
    date DATE,
    api_key_id UUID,
    api_key_name TEXT,
    api_key_display_name TEXT,
    api_key_description TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    workspace TEXT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_creation_tokens BIGINT,
    reasoning_tokens BIGINT,
    cost_usd NUMERIC
)
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).display_name AS key_display_name,
            (ak.spec).description AS key_description,
            (ak.metadata).workspace AS key_workspace
        FROM api.api_keys ak
        WHERE (
            api.can_read_api_key_owner_usage(ak.user_id, ak.service_account_id)
            OR api.has_permission(
                auth.uid(), 'workspace:usage-read', (ak.metadata).workspace
            )
        )
        AND (p_api_key_id IS NULL OR ak.id = p_api_key_id)
    ), old_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            NULL::text AS endpoint_type,
            kv.key AS endpoint_name,
            NULL::text AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value)::bigint AS dimension_usage,
            NULL::bigint AS p_tokens,
            NULL::bigint AS c_tokens,
            NULL::bigint AS cr_tokens,
            NULL::bigint AS cc_tokens,
            NULL::bigint AS r_tokens,
            NULL::numeric AS cost
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NULL
    ), new_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            split_part(kv.key, '|', 1) AS endpoint_type,
            split_part(kv.key, '|', 2) AS endpoint_name,
            NULLIF(split_part(kv.key, '|', 3), '') AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value->>'total')::bigint AS dimension_usage,
            (kv.value->>'prompt')::bigint AS p_tokens,
            (kv.value->>'completion')::bigint AS c_tokens,
            (kv.value->>'cache_read')::bigint AS cr_tokens,
            (kv.value->>'cache_creation')::bigint AS cc_tokens,
            (kv.value->>'reasoning')::bigint AS r_tokens,
            (kv.value->>'cost_usd')::numeric AS cost
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ), dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
    )
    SELECT
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.key_display_name,
        d.key_description,
        d.endpoint_type,
        d.endpoint_name,
        d.model_name,
        d.workspace,
        d.dimension_usage,
        d.p_tokens,
        d.c_tokens,
        d.cr_tokens,
        d.cc_tokens,
        d.r_tokens,
        d.cost
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;

CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE v_result api.api_keys;
BEGIN
    PERFORM api.validate_api_key_limits(p_limits);
    UPDATE api.api_keys k
    SET spec = ROW(
        COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0),
        (k.spec).expires_in,
        p_limits,
        (k.spec).project_id,
        (k.spec).description
    )::api.api_key_spec
    WHERE k.id = p_id
      AND api.can_act_on_api_key(k.user_id, k.service_account_id, 'service_account:update')
    RETURNING * INTO v_result;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;
    RETURN v_result;
END;
$$;

CREATE OR REPLACE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID,
    p_project_id UUID,
    p_limits JSONB,
    p_display_name TEXT,
    p_description TEXT
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_key api.api_keys; v_workspace TEXT;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id
      AND api.can_act_on_api_key(user_id, service_account_id, 'service_account:update');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    IF p_display_name IS NULL OR length(trim(p_display_name)) = 0 THEN
        RAISE EXCEPTION 'API key display name is required' USING ERRCODE = '22023';
    END IF;

    v_workspace := (v_key.metadata).workspace;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid()
          AND (metadata).workspace = v_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace' USING ERRCODE = '22023';
    END IF;

    UPDATE api.api_keys
    SET metadata = ROW(
            (metadata).name,
            trim(p_display_name),
            (metadata).workspace,
            (metadata).deletion_timestamp,
            (metadata).creation_timestamp,
            now(),
            (metadata).labels,
            (metadata).annotations
        )::api.metadata,
        spec = ROW(
            (spec).quota, (spec).expires_in, (spec).limits,
            p_project_id, COALESCE(p_description, '')
        )::api.api_key_spec
    WHERE id = p_api_key_id;
    -- NULL means "leave the limits alone". Coalescing to '{}' instead made an
    -- omitted argument wipe every limit, reset the quota to 0 and drop the
    -- disabled flag.
    IF p_limits IS NOT NULL THEN
        PERFORM api.set_api_key_limits(p_api_key_id, p_limits);
    END IF;
    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

-- Folders are personal: SA keys never move. IS DISTINCT FROM, as an SA key's
-- NULL user_id would pass <>.
CREATE OR REPLACE FUNCTION api.move_api_keys_to_project(
    p_api_key_ids UUID[], p_project_id UUID DEFAULT NULL
) RETURNS INTEGER LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_workspace TEXT; v_count INTEGER;
BEGIN
    IF COALESCE(array_length(p_api_key_ids, 1), 0) = 0 THEN
        RAISE EXCEPTION 'at least one API key is required' USING ERRCODE = '22023';
    END IF;

    IF p_project_id IS NOT NULL THEN
        SELECT (metadata).workspace INTO v_workspace
        FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid();
        IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
            RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501';
        END IF;
    ELSE
        -- Moving to ungrouped still requires read access to the workspace the
        -- keys live in, the same check the targeted-project branch performs.
        SELECT (metadata).workspace INTO v_workspace
        FROM api.api_keys
        WHERE id = p_api_key_ids[1]
          AND user_id = auth.uid()
          AND (metadata).deletion_timestamp IS NULL;
        IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'workspace:read', v_workspace) THEN
            RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM unnest(p_api_key_ids) AS selected(key_id)
        LEFT JOIN api.api_keys k ON k.id = selected.key_id
        WHERE k.id IS NULL
           OR k.user_id IS DISTINCT FROM auth.uid()
           OR (k.metadata).workspace <> v_workspace
           OR (k.metadata).deletion_timestamp IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'API key not found, permission denied, or workspace mismatch'
            USING ERRCODE = '42501';
    END IF;

    UPDATE api.api_keys
    SET spec = ROW(
        (spec).quota, (spec).expires_in, (spec).limits,
        p_project_id, (spec).description
    )::api.api_key_spec
    WHERE id = ANY(p_api_key_ids)
      AND (spec).project_id IS DISTINCT FROM p_project_id;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;
