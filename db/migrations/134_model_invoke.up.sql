-- model:invoke gates gateway access: an API key reaches the endpoints of its
-- workspace when its owner holds model:invoke there. Keys stop working when
-- their owner is deactivated.

-- Every role with endpoint:read or external_endpoint:read holds model:invoke,
-- so no one loses access; model:invoke covers both kinds of endpoint.
CREATE OR REPLACE FUNCTION api.update_workspace_user_permissions()
RETURNS VOID AS $$
DECLARE
    workspace_user_permissions api.permission_action[];
BEGIN
    workspace_user_permissions := ARRAY[
        'workspace:read',
        'endpoint:read',
        'endpoint:create',
        'endpoint:update',
        'endpoint:delete',
        'image_registry:read',
        'image_registry:create',
        'image_registry:update',
        'image_registry:delete',
        'model_registry:read',
        'model_registry:create',
        'model_registry:update',
        'model_registry:delete',
        'model:read',
        'model:push',
        'model:pull',
        'model:delete',
        'engine:read',
        'engine:create',
        'engine:update',
        'engine:delete',
        'cluster:read',
        'cluster:create',
        'cluster:update',
        'cluster:delete',
        'model_catalog:read',
        'model_catalog:create',
        'model_catalog:update',
        'model_catalog:delete',
        'external_endpoint:read',
        'external_endpoint:create',
        'external_endpoint:update',
        'external_endpoint:delete',
        'endpoint:trace-read',
        'external_endpoint:trace-read',
        'model:invoke'
    ]::api.permission_action[];

    UPDATE api.roles
    SET spec = ROW((spec).preset_key, workspace_user_permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_workspace_user_permissions();

-- Custom roles (and any other preset) that could call models.
UPDATE api.roles
SET spec = ROW((spec).preset_key, (spec).permissions || 'model:invoke'::api.permission_action)::api.role_spec
WHERE ('endpoint:read' = ANY ((spec).permissions) OR 'external_endpoint:read' = ANY ((spec).permissions))
  AND NOT ('model:invoke' = ANY ((spec).permissions));

-- The models of spec.model_routes are listed too. Routes supersede the
-- per-upstream model_mapping when present, as in the gateway.
CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT, source_label TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.status).served_models[1] AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name,
        'self-hosted'::text       AS source_label
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.status).served_models[1] IS NOT NULL
      AND trim((e.status).served_models[1]) <> ''
    UNION
    SELECT DISTINCT
        k::text                    AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name,
        COALESCE(
            (ee.spec).model_sources ->> k,
            CASE WHEN u.endpoint_ref IS NOT NULL AND trim(u.endpoint_ref) <> ''
                 THEN 'self-hosted' END
        )::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND COALESCE(array_length((ee.spec).model_routes, 1), 0) = 0
      AND u.model_mapping IS NOT NULL
    UNION
    SELECT DISTINCT
        r.model::text              AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name,
        -- Self-hosted when every target is an upstream fronting an endpoint.
        COALESCE(
            (ee.spec).model_sources ->> r.model,
            CASE WHEN (
                SELECT bool_and(u.endpoint_ref IS NOT NULL AND trim(u.endpoint_ref) <> '')
                FROM unnest(r.targets) AS t
                LEFT JOIN unnest((ee.spec).upstreams) AS u ON u.name = t.upstream
            ) THEN 'self-hosted' END
        )::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).model_routes) AS r
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND r.model IS NOT NULL
      AND trim(r.model) <> '';
$$;

-- The models a key may pick in p_workspace. Without p_service_account_id the
-- list is what the caller may call: empty without model:invoke, narrowed by
-- the caller's group policy with the rules the gateway applies to a personal
-- key (api.get_api_key_model_rules).
CREATE OR REPLACE FUNCTION api.list_invokable_models(p_workspace TEXT, p_service_account_id UUID DEFAULT NULL)
RETURNS TABLE (model TEXT, type TEXT, endpoint_name TEXT, source_label TEXT, phase TEXT)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_subject UUID;
    v_allowed TEXT[];
    v_denied TEXT[] := ARRAY[]::TEXT[];
BEGIN
    IF COALESCE(btrim(p_workspace), '') = '' THEN
        RAISE invalid_parameter_value
            USING message = 'p_workspace is required';
    END IF;

    IF p_service_account_id IS NULL THEN
        v_subject := auth.uid();
    ELSE
        v_subject := p_service_account_id;

        IF api.has_permission_on_service_account(auth.uid(), 'service_account:read', p_service_account_id) IS NOT TRUE THEN
            RAISE insufficient_privilege
                USING message = 'service_account:read on the Team of the ServiceAccount is required to list the models of its API keys';
        END IF;
    END IF;

    IF api.has_permission(v_subject, 'model:invoke', p_workspace) IS NOT TRUE THEN
        RETURN;
    END IF;

    SELECT p.allowed_models, COALESCE(p.denied_models, ARRAY[]::TEXT[]) INTO v_allowed, v_denied
    FROM api.user_group_model_policy(v_subject, p_workspace) p;

    RETURN QUERY
    SELECT DISTINCT m.model,
           CASE m.source WHEN 'endpoint' THEN 'internal' ELSE 'external' END,
           m.endpoint_name,
           m.source_label,
           CASE m.source
               WHEN 'endpoint' THEN (
                   SELECT (e.status).phase FROM api.endpoints e
                   WHERE (e.metadata).workspace = p_workspace
                     AND (e.metadata).name = m.endpoint_name
                     AND (e.metadata).deletion_timestamp IS NULL
                   LIMIT 1)
               ELSE (
                   SELECT (ee.status).phase FROM api.external_endpoints ee
                   WHERE (ee.metadata).workspace = p_workspace
                     AND (ee.metadata).name = m.endpoint_name
                     AND (ee.metadata).deletion_timestamp IS NULL
                   LIMIT 1)
           END
    FROM api.get_workspace_models(p_workspace) m
    WHERE (v_allowed IS NULL OR m.model = ANY (v_allowed))
      AND NOT (m.model = ANY (v_denied))
    ORDER BY 1, 2, 3;
END;
$$;

-- A user's key is live only while the user is: the user exists, is not being
-- deleted, is not banned or deleted in GoTrue, and the organization sync has
-- not deactivated them. An SA key is live while the SA is enabled and its
-- Team is Active. neutree-api asks this for every sk_ request to the control
-- plane; the API key controller removes the key's gateway credential while it
-- is FALSE. service_role only: it reads auth.users.
CREATE OR REPLACE FUNCTION api.api_key_owner_active(p_key_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM api.api_keys k
        JOIN api.user_profiles p ON p.id = k.user_id
        JOIN auth.users u ON u.id = k.user_id
        WHERE k.id = p_key_id
          AND (p.metadata).deletion_timestamp IS NULL
          AND (u.banned_until IS NULL OR u.banned_until <= now())
          AND u.deleted_at IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM api.external_identities e
              WHERE e.user_id = k.user_id
                AND e.sync_deactivated IS NOT NULL)
    ) OR EXISTS (
        SELECT 1
        FROM api.api_keys k
        JOIN api.service_accounts s ON s.id = k.service_account_id
        JOIN api.teams t ON t.id = s.team_id
        WHERE k.id = p_key_id
          AND (s.spec).disabled IS NOT TRUE
          AND (t.status).phase = 'Active'
    );
END;
$$;

REVOKE ALL ON FUNCTION api.api_key_owner_active(UUID) FROM PUBLIC, anonymous, api_user;

GRANT EXECUTE ON FUNCTION api.api_key_owner_active(UUID) TO service_role;
