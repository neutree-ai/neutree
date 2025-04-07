-- ----------------------
-- Resource: User Profile
-- ----------------------

CREATE TYPE api.user_profile_spec AS (
    email TEXT
);

CREATE TYPE api.user_profile_status AS (
    phase TEXT,
    service_url TEXT,
    error_message TEXT
);

CREATE TABLE api.user_profiles (
    id UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.user_profile_spec,
    status api.user_profile_status
);

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

CREATE TRIGGER update_user_profiles_update_timestamp
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_user_profiles_default_timestamp
    BEFORE INSERT ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX user_profiles_name_unique_idx ON api.user_profiles (((metadata).name));

ALTER TABLE api.user_profiles ENABLE ROW LEVEL SECURITY;

CREATE POLICY "Profiles are viewable by everyone" ON api.user_profiles
  FOR SELECT USING (true);

CREATE POLICY "Users can update their own profile" ON api.user_profiles
  FOR UPDATE USING (id = (SELECT auth.uid()));

-- ----------------------
-- Resource: API key
-- ----------------------
CREATE TYPE api.api_key_spec AS (
    quota BIGINT
);

CREATE TYPE api.api_key_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT,
    sk_value TEXT,  -- The secret key value
    usage BIGINT,
    last_used_at TIMESTAMP,
    last_sync_at TIMESTAMP
);

CREATE TABLE api.api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.api_key_spec,
    status api.api_key_status,
    user_id UUID NOT NULL REFERENCES api.user_profiles(id) ON DELETE CASCADE
);

CREATE OR REPLACE FUNCTION api.generate_api_key()
RETURNS TEXT
AS $$
DECLARE
    key_value TEXT;
BEGIN
    key_value := 'sk_' || encode(public.gen_random_bytes(24), 'hex');
    RETURN key_value;
END;
$$ LANGUAGE plpgsql;

-- expose as RPC in postgrest, user should ONLY use this to create API key
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    p_user_id UUID;
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
    
    -- Generate a new API key value
    v_key_value := api.generate_api_key();
    
    -- Insert the new API key with the generated key value in status.sk_value
    INSERT INTO api.api_keys (
        api_version,
        kind,
        metadata,
        spec,
        status,
        user_id
    ) VALUES (
        'v1',
        'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
        ROW(p_quota)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.validate_api_key(
    p_sk_value TEXT
) RETURNS UUID
SECURITY DEFINER
AS $$
DECLARE
    v_api_key api.api_keys;
    v_user_id UUID;
BEGIN
    -- Get the API key where status.sk_value matches
    SELECT * INTO v_api_key 
    FROM api.api_keys 
    WHERE (status).sk_value = p_sk_value;
    
    -- Check if the API key exists
    IF v_api_key IS NULL THEN
        RETURN NULL;
    END IF;
    
    -- Get user_id
    v_user_id := v_api_key.user_id;
    
    -- Check if the user exists
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = v_user_id) THEN
        RETURN NULL;
    END IF;
    
    -- Update last_used_at timestamp
    UPDATE api.api_keys
    SET status = ROW(
        (v_api_key.status).phase,
        (v_api_key.status).last_transition_time,
        (v_api_key.status).error_message,
        (v_api_key.status).sk_value,
        (v_api_key.status).usage, 
        CURRENT_TIMESTAMP,
        (v_api_key.status).last_sync_at
    )::api.api_key_status
    WHERE id = v_api_key.id;

    RETURN v_user_id;
END;
$$ LANGUAGE plpgsql;

CREATE UNIQUE INDEX api_key_name_workspace_unique_idx ON api.api_keys (((metadata).workspace), ((metadata).name));
CREATE INDEX api_keys_user_id_idx ON api.api_keys (user_id);
CREATE INDEX api_keys_sk_value_idx ON api.api_keys (((status).sk_value));

ALTER TABLE api.api_keys ENABLE ROW LEVEL SECURITY;

CREATE POLICY "Users can see their own API keys" ON api.api_keys
    FOR SELECT
    USING (user_id = auth.uid());

CREATE POLICY "Users can create their own API keys" ON api.api_keys
    FOR INSERT
    WITH CHECK (user_id = auth.uid());

-- Use service_role to run usage update
CREATE POLICY "Users can update their own API keys" ON api.api_keys
    FOR UPDATE
    USING (user_id = auth.uid());

CREATE POLICY "Users can delete their own API keys" ON api.api_keys
    FOR DELETE
    USING (user_id = auth.uid());

-- ----------------------
-- Resource: API Usage Record (Individual usage records)
-- ----------------------
CREATE TABLE api.api_usage_records (
    id BIGSERIAL PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api.api_keys(id) ON DELETE CASCADE,
    request_id TEXT,
    usage_amount INTEGER NOT NULL,
    model TEXT,
    workspace TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    is_aggregated BOOLEAN DEFAULT false,
    metadata JSONB DEFAULT '{}'::jsonb  -- Reserved for future extensions, can store additional dimensions
);

CREATE OR REPLACE FUNCTION api.record_api_usage(
    p_api_key_id UUID,
    p_request_id TEXT,
    p_usage_amount INTEGER,
    p_model TEXT DEFAULT NULL
) RETURNS JSONB
SECURITY DEFINER
AS $$
DECLARE
    v_api_key RECORD;
    v_workspace TEXT;
BEGIN
    -- Check if the API key exists and is valid
    SELECT id, (metadata).workspace INTO v_api_key
    FROM api.api_keys
    WHERE id = p_api_key_id;
    
    -- If API key not found, return JSON with error instead of raising exception
    IF v_api_key.id IS NULL THEN
        RETURN jsonb_build_object(
            'success', false,
            'error', format('Invalid API key: %s', p_api_key_id),
            'request_id', p_request_id
        );
    END IF;
    
    -- Get workspace from API key metadata
    v_workspace := v_api_key.workspace;
    
    -- Insert usage record with dimensional data
    INSERT INTO api.api_usage_records (
        api_key_id,
        request_id,
        usage_amount,
        model,
        workspace,
        created_at
    ) VALUES (
        p_api_key_id,
        p_request_id,
        p_usage_amount,
        p_model,
        v_workspace,
        now()
    );

    -- Return success response
    RETURN jsonb_build_object(
        'success', true,
        'api_key_id', p_api_key_id,
        'request_id', p_request_id,
        'usage_recorded', p_usage_amount
    );

EXCEPTION WHEN OTHERS THEN
    -- Catch any other errors and return as JSON
    RETURN jsonb_build_object(
        'success', false,
        'error', SQLERRM,
        'api_key_id', p_api_key_id,
        'request_id', p_request_id
    );
END;
$$ LANGUAGE plpgsql;

CREATE UNIQUE INDEX api_usage_records_request_id_idx ON api.api_usage_records(request_id);
CREATE INDEX api_usage_records_api_key_id_idx ON api.api_usage_records(api_key_id);
CREATE INDEX api_usage_records_created_at_idx ON api.api_usage_records(created_at);
CREATE INDEX api_usage_records_is_aggregated_idx ON api.api_usage_records(is_aggregated);

ALTER TABLE api.api_usage_records ENABLE ROW LEVEL SECURITY;

-- We use service role to do update
CREATE POLICY "No direct access to usage records" ON api.api_usage_records
    USING (false);

-- ----------------------
-- Resource: API Daily Usage (Aggregated daily usage statistics)
-- ----------------------

CREATE TYPE api.api_daily_usage_spec AS (
    api_key_id UUID,
    usage_date DATE,
    total_usage BIGINT,
    dimensional_usage JSONB
);

CREATE TYPE api.api_daily_usage_status AS (
    last_sync_time TIMESTAMP WITH TIME ZONE
);

CREATE TABLE api.api_daily_usage (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.api_daily_usage_spec,
    status api.api_daily_usage_status
);

-- Use trigger to maintain referential integrity with api_keys
CREATE OR REPLACE FUNCTION api.check_api_daily_usage_api_key_exists()
RETURNS TRIGGER AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.api_keys WHERE id = ((NEW.spec).api_key_id)) THEN
        RAISE EXCEPTION 'Referenced API key does not exist';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.handle_api_key_delete()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM api.api_daily_usage 
    WHERE ((spec).api_key_id) = OLD.id;
    
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.aggregate_usage_records(
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
            COALESCE(model, 'unknown') AS model,
            COALESCE(workspace, 'default') AS workspace,
            usage_amount
        FROM api.api_usage_records
        WHERE 
            is_aggregated = false AND
            created_at < p_older_than
        ORDER BY created_at
    LOOP
        v_date := v_record.usage_date;
        v_dimension_key := v_record.model || ':' || v_record.workspace;
        
        -- Get workspace from API key
        SELECT (ak.metadata).workspace INTO v_workspace
        FROM api.api_keys ak
        WHERE ak.id = v_record.api_key_id;
        
        -- Get or create the daily usage record
        SELECT 
            id, 
            ((spec).dimensional_usage) AS dimensional_usage 
        INTO v_daily_record 
        FROM api.api_daily_usage
        WHERE 
            ((spec).api_key_id) = v_record.api_key_id AND 
            ((spec).usage_date) = v_date;
            
        IF NOT FOUND THEN
            -- Create metadata for new record
            v_metadata := ROW(
                'daily-usage-' || nextval('api.api_daily_usage_id_seq'::regclass),
                NULL,
                v_workspace,
                NULL,
                CURRENT_TIMESTAMP,
                CURRENT_TIMESTAMP,
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
                        true  -- Create the key if it doesn't exist
                    )
                )::api.api_daily_usage_spec,
                status = ROW(
                    CURRENT_TIMESTAMP
                )::api.api_daily_usage_status
            WHERE id = v_daily_record.id;
        END IF;
        
        -- Mark record as aggregated
        UPDATE api.api_usage_records
        SET is_aggregated = true
        WHERE id = v_record.id;
        
        v_count := v_count + 1;
    END LOOP;
    
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.sync_api_key_usage()
RETURNS INTEGER
SECURITY DEFINER
AS $$
DECLARE
    v_count INTEGER := 0;
    v_api_key RECORD;
    v_total_usage BIGINT;
BEGIN
    FOR v_api_key IN 
        SELECT id, (status).usage AS current_usage
        FROM api.api_keys
    LOOP
        SELECT COALESCE(SUM((spec).total_usage), 0) INTO v_total_usage
        FROM api.api_daily_usage
        WHERE ((spec).api_key_id) = v_api_key.id;

        IF v_total_usage != v_api_key.current_usage THEN
            UPDATE api.api_keys
            SET status = ROW(
                (status).phase,
                (status).last_transition_time,
                (status).error_message,
                (status).sk_value,
                v_total_usage,
                (status).last_used_at,
                now()  -- Update last_sync_at
            )::api.api_key_status
            WHERE id = v_api_key.id;
            
            v_count := v_count + 1;
        END IF;
    END LOOP;
    
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.cleanup_aggregated_records(
    p_older_than INTERVAL DEFAULT '30 minutes'::INTERVAL,
    p_batch_size INTEGER DEFAULT 1000
)
RETURNS INTEGER
SECURITY DEFINER
AS $$
DECLARE
    v_count INTEGER;
BEGIN
    WITH to_delete AS (
        SELECT id
        FROM api.api_usage_records
        WHERE is_aggregated = true
          AND created_at < (now() - p_older_than)
        LIMIT p_batch_size
    ),
    deleted AS (
        DELETE FROM api.api_usage_records
        WHERE id IN (SELECT id FROM to_delete)
        RETURNING 1
    )
    SELECT COUNT(*) INTO v_count FROM deleted;
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.get_usage_by_dimension(
    p_start_date DATE,
    p_end_date DATE,
    p_api_key_id UUID DEFAULT NULL,
    p_model TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL
)
RETURNS TABLE (
    date DATE,
    api_key_id UUID,
    api_key_name TEXT,
    model TEXT,
    workspace TEXT,
    usage BIGINT
)
SECURITY DEFINER
AS $$
BEGIN
    -- Return usage data with dimension filtering
    RETURN QUERY
    WITH user_api_keys AS (
        -- Get all API keys owned by the user
        SELECT id, (metadata).name AS key_name
        FROM api.api_keys
        WHERE user_id = auth.uid()
        AND (p_api_key_id IS NULL OR id = p_api_key_id)
    ),
    dimension_data AS (
        SELECT 
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            split_part(kv.key, ':', 1) AS model,
            split_part(kv.key, ':', 2) AS workspace,
            (kv.value)::bigint AS dimension_usage
        FROM 
            api.api_daily_usage u
            JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
            jsonb_each((u.spec).dimensional_usage) kv
        WHERE 
            (u.spec).usage_date BETWEEN p_start_date AND p_end_date
    )
    SELECT 
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.model,
        d.workspace,
        d.dimension_usage
    FROM 
        dimension_data d
    WHERE 
        (p_model IS NULL OR d.model = p_model) AND
        (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY
        d.usage_date DESC,
        d.api_key_id,
        d.model,
        d.workspace;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER check_api_daily_usage_api_key_exists_trigger
    BEFORE INSERT OR UPDATE ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION api.check_api_daily_usage_api_key_exists();

CREATE TRIGGER api_key_delete_cascade
    BEFORE DELETE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION api.handle_api_key_delete();

CREATE TRIGGER update_api_daily_usage_update_timestamp
    BEFORE UPDATE ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_api_daily_usage_default_timestamp
    BEFORE INSERT ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX api_daily_usage_name_unique_idx ON api.api_daily_usage (((metadata).name));
CREATE UNIQUE INDEX api_daily_usage_key_date_idx ON api.api_daily_usage(((spec).api_key_id), ((spec).usage_date));
CREATE INDEX api_daily_usage_date_idx ON api.api_daily_usage(((spec).usage_date));

ALTER TABLE api.api_daily_usage ENABLE ROW LEVEL SECURITY;

CREATE POLICY "API keys owners can see related daily usage" ON api.api_daily_usage
    FOR SELECT
    USING (((spec).api_key_id) IN (
        SELECT id FROM api.api_keys WHERE user_id = auth.uid()
    ));
