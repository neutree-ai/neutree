-- Drop function and trigger first --
DROP TRIGGER IF EXISTS update_workspaces_update_timestamp ON api.workspaces;
DROP TRIGGER IF EXISTS set_workspaces_default_timestamp ON api.workspaces;
DROP TRIGGER IF EXISTS update_roles_update_timestamp ON api.roles;
DROP TRIGGER IF EXISTS set_roles_default_timestamp ON api.roles;
DROP TRIGGER IF EXISTS update_role_assignments_update_timestamp ON api.role_assignments;
DROP TRIGGER IF EXISTS set_role_assignments_default_timestamp ON api.role_assignments;
DROP TRIGGER IF EXISTS update_user_profiles_update_timestamp ON api.user_profiles;
DROP TRIGGER IF EXISTS set_user_profiles_default_timestamp ON api.user_profiles;
DROP TRIGGER IF EXISTS update_api_daily_usage_update_timestamp ON api.api_daily_usage;
DROP TRIGGER IF EXISTS set_api_daily_usage_default_timestamp ON api.api_daily_usage;
DROP TRIGGER IF EXISTS update_endpoints_update_timestamp ON api.endpoints;
DROP TRIGGER IF EXISTS set_endpoints_default_timestamp ON api.endpoints;
DROP TRIGGER IF EXISTS update_image_registries_update_timestamp ON api.image_registries;
DROP TRIGGER IF EXISTS set_image_registries_default_timestamp ON api.image_registries;
DROP TRIGGER IF EXISTS update_model_registries_update_timestamp ON api.model_registries;
DROP TRIGGER IF EXISTS set_model_registries_default_timestamp ON api.model_registries;
DROP TRIGGER IF EXISTS update_engines_update_timestamp ON api.engines;
DROP TRIGGER IF EXISTS set_engines_default_timestamp ON api.engines;
DROP TRIGGER IF EXISTS update_clusters_update_timestamp ON api.clusters;
DROP TRIGGER IF EXISTS set_clusters_default_timestamp ON api.clusters;
DROP TRIGGER IF EXISTS update_model_catalogs_update_timestamp ON api.model_catalogs;
DROP TRIGGER IF EXISTS set_model_catalogs_default_timestamp ON api.model_catalogs;
DROP TRIGGER IF EXISTS update_oem_configs_update_timestamp ON api.oem_configs;
DROP TRIGGER IF EXISTS set_oem_configs_default_timestamp ON api.oem_configs;
DROP FUNCTION IF EXISTS update_metadata_update_timestamp_column();
DROP FUNCTION IF EXISTS set_default_metadata_timestamp_column();
DROP TRIGGER IF EXISTS on_auth_user_created ON auth.users;
DROP FUNCTION IF EXISTS api.handle_new_user();
DROP FUNCTION IF EXISTS api.aggregate_usage_records(TIMESTAMP WITH TIME ZONE);
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER);

-- Add metadata annotations --
ALTER TYPE api.metadata ADD ATTRIBUTE annotations json;

-- Create function and trigger --
CREATE OR REPLACE FUNCTION set_default_metadata_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW(
        (NEW.metadata).name,
        (NEW.metadata).display_name,
        (NEW.metadata).workspace,
        (NEW.metadata).deletion_timestamp,
        CURRENT_TIMESTAMP,
        CURRENT_TIMESTAMP,
        (NEW.metadata).labels,
        (NEW.metadata).annotations
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION update_metadata_update_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW(
        (NEW.metadata).name,
        (NEW.metadata).display_name,
        (NEW.metadata).workspace,
        (NEW.metadata).deletion_timestamp,
        (NEW.metadata).creation_timestamp,
        CURRENT_TIMESTAMP,
        (NEW.metadata).labels,
        (NEW.metadata).annotations
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_workspaces_update_timestamp
    BEFORE UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_workspaces_default_timestamp
    BEFORE INSERT ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_roles_update_timestamp
    BEFORE UPDATE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_roles_default_timestamp
    BEFORE INSERT ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_role_assignments_update_timestamp
    BEFORE UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_role_assignments_default_timestamp
    BEFORE INSERT ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_user_profiles_update_timestamp
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_user_profiles_default_timestamp
    BEFORE INSERT ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_api_daily_usage_update_timestamp
    BEFORE UPDATE ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_api_daily_usage_default_timestamp
    BEFORE INSERT ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_endpoints_update_timestamp
    BEFORE UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_endpoints_default_timestamp
    BEFORE INSERT ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_image_registries_update_timestamp
    BEFORE UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_image_registries_default_timestamp
    BEFORE INSERT ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_model_registries_update_timestamp
    BEFORE UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_model_registries_default_timestamp
    BEFORE INSERT ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_engines_update_timestamp
    BEFORE UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_engines_default_timestamp
    BEFORE INSERT ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_clusters_update_timestamp
    BEFORE UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_clusters_default_timestamp
    BEFORE INSERT ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_model_catalogs_update_timestamp
    BEFORE UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_model_catalogs_default_timestamp
    BEFORE INSERT ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER update_oem_configs_update_timestamp
    BEFORE UPDATE ON api.oem_configs
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_oem_configs_default_timestamp
    BEFORE INSERT ON api.oem_configs
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE OR REPLACE FUNCTION api.handle_new_user()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
BEGIN
  INSERT INTO api.user_profiles (
    id,
    api_version,
    kind,
    metadata,
    spec
  )
  VALUES (
    NEW.id,
    'v1',
    'UserProfile',
    ROW(
      NEW.raw_user_meta_data->>'username',
      NEW.raw_user_meta_data->>'username',
      null,
      null,
      CURRENT_TIMESTAMP,
      CURRENT_TIMESTAMP,
      '{}'::json,
      '{}'::json
    )::api.metadata,
    ROW(NEW.email)::api.user_profile_spec
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_auth_user_created
  AFTER INSERT ON auth.users
  FOR EACH ROW EXECUTE PROCEDURE api.handle_new_user();

CREATE FUNCTION api.aggregate_usage_records(
    p_older_than TIMESTAMP WITH TIME ZONE DEFAULT NULL
)
RETURNS INTEGER
SECURITY DEFINER
AS $$
DECLARE
    v_count INTEGER := 0;
    v_record RECORD;
    v_daily_record RECORD;
    v_date DATE;
    v_dimension_key TEXT;
    v_metadata api.metadata;
    v_workspace TEXT;
BEGIN
    IF p_older_than IS NULL THEN
        p_older_than := now();
    END IF;

    FOR v_record IN
        SELECT
            id,
            api_key_id,
            date_trunc('day', created_at)::date AS usage_date,
            COALESCE(endpoint_name, 'unknown') AS endpoint_name,
            usage_amount
        FROM api.api_usage_records
        WHERE
            is_aggregated = false AND
            created_at < p_older_than
        ORDER BY created_at
    LOOP
        v_date := v_record.usage_date;
        v_dimension_key := v_record.endpoint_name;

        SELECT (ak.metadata).workspace INTO v_workspace
        FROM api.api_keys ak
        WHERE ak.id = v_record.api_key_id;

        SELECT
            id,
            ((spec).dimensional_usage) AS dimensional_usage
        INTO v_daily_record
        FROM api.api_daily_usage
        WHERE
            ((spec).api_key_id) = v_record.api_key_id AND
            ((spec).usage_date) = v_date;

        IF NOT FOUND THEN
            v_metadata := ROW(
                'daily-usage-' || nextval('api.api_daily_usage_id_seq'::regclass),
                NULL,
                v_workspace,
                NULL,
                CURRENT_TIMESTAMP,
                CURRENT_TIMESTAMP,
                '{}'::json,
                '{}'::json
            )::api.metadata;

            INSERT INTO api.api_daily_usage (
                api_version,
                kind,
                metadata,
                spec,
                status
            ) VALUES (
                'v1',
                'ApiDailyUsage',
                v_metadata,
                ROW(
                    v_record.api_key_id,
                    v_date,
                    v_record.usage_amount,
                    jsonb_build_object(v_dimension_key, v_record.usage_amount)
                )::api.api_daily_usage_spec,
                ROW(
                    CURRENT_TIMESTAMP
                )::api.api_daily_usage_status
            )
            RETURNING id, ((spec).dimensional_usage) INTO v_daily_record;
        ELSE
            UPDATE api.api_daily_usage
            SET
                spec = ROW(
                    (spec).api_key_id,
                    (spec).usage_date,
                    ((spec).total_usage) + v_record.usage_amount,
                    jsonb_set(
                        (spec).dimensional_usage,
                        ARRAY[v_dimension_key],
                        to_jsonb(
                            COALESCE(
                                ((spec).dimensional_usage->>v_dimension_key)::int, 0
                            ) + v_record.usage_amount
                        ),
                        true
                    )
                )::api.api_daily_usage_spec,
                status = ROW(
                    CURRENT_TIMESTAMP
                )::api.api_daily_usage_status
            WHERE id = v_daily_record.id;
        END IF;

        UPDATE api.api_usage_records
        SET is_aggregated = true
        WHERE id = v_record.id;

        v_count := v_count + 1;
    END LOOP;

    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL  -- Expiration time in seconds, NULL = never expires
) RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_result api.api_keys;
BEGIN
    p_user_id = auth.uid();

    -- Check if the user exists
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;

    -- Use name as display_name if not provided
    IF p_display_name IS NULL THEN
        p_display_name := p_name;
    END IF;

    -- Generate UUID for this API key
    v_key_id := gen_random_uuid();

    -- Generate a new self-contained API key value
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);

    -- Insert the new API key with the generated key value in status.sk_value
    INSERT INTO api.api_keys (
        id,
        api_version,
        kind,
        metadata,
        spec,
        status,
        user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
        ROW(p_quota, p_expires_in)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;