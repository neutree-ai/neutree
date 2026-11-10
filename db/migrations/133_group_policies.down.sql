-- Reverts group_policies. The group policy and OrgUnit usage permissions go
-- out of every role but admin; department-admin as before.

DROP FUNCTION IF EXISTS api.get_usage_by_group(DATE, DATE, TEXT, TEXT, TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS api.get_user_group_policy(UUID, TEXT);
DROP FUNCTION IF EXISTS api.group_display_name(TEXT, TEXT);
DROP FUNCTION IF EXISTS api.get_group_token_pools(TEXT, TEXT);
DROP FUNCTION IF EXISTS api.can_read_team_usage(UUID, TEXT);

CREATE OR REPLACE FUNCTION api.get_api_key_remaining(
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
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
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
        FROM jsonb_array_elements(api.api_key_allowed_models(v_limits)) AS e
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

DROP FUNCTION IF EXISTS api.api_key_own_remaining(UUID, TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS api.user_group_token_pools(UUID);
DROP FUNCTION IF EXISTS api.group_token_pool_usage(TEXT, TEXT, TEXT);

DROP TRIGGER IF EXISTS group_daily_usage_on_aggregated ON api.api_usage_records;
DROP FUNCTION IF EXISTS api.group_daily_usage_on_aggregated();
DROP TABLE IF EXISTS api.group_daily_usage;

DROP FUNCTION IF EXISTS api.get_api_key_model_rules(UUID, TEXT, JSONB);
DROP FUNCTION IF EXISTS api.user_group_model_policy(UUID, TEXT);
DROP FUNCTION IF EXISTS api.group_policy_model_names(JSONB, TEXT);

DROP TRIGGER IF EXISTS teams_delete_group_policy ON api.teams;
DROP TRIGGER IF EXISTS org_units_delete_group_policy ON api.org_units;
DROP TABLE IF EXISTS api.group_policies;
DROP TYPE IF EXISTS api.group_policy_spec;
DROP FUNCTION IF EXISTS api.group_policies_delete_with_group();
DROP FUNCTION IF EXISTS api.group_policies_before_write();
DROP FUNCTION IF EXISTS api.group_policy_normalize_token_quota(JSONB);
DROP FUNCTION IF EXISTS api.group_policy_normalize_models(JSONB, TEXT);

CREATE OR REPLACE FUNCTION api.update_department_admin_permissions()
RETURNS VOID AS $$
BEGIN
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, ARRAY[
        'user_profile:read',
        'user_profile:update',
        'role_assignment:read',
        'role_assignment:create',
        'role_assignment:update',
        'role_assignment:delete',
        'role:read',
        'org_unit:read',
        'org_unit:assign-member'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'department-admin';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_department_admin_permissions();

UPDATE api.roles
SET spec = ROW((spec).preset_key, ARRAY(
        SELECT p FROM unnest((spec).permissions) AS p
        WHERE p NOT IN ('group_policy:read', 'group_policy:create', 'group_policy:update',
                        'group_policy:delete', 'org_unit:usage-read')))::api.role_spec
WHERE (spec).permissions && ARRAY['group_policy:read', 'group_policy:create', 'group_policy:update',
                                  'group_policy:delete', 'org_unit:usage-read']::api.permission_action[]
  AND (metadata).name <> 'admin';
