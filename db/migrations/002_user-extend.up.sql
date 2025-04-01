-- ----------------------
-- Resource: User Profile
-- ----------------------

CREATE TYPE api.user_profile_spec AS (
    email TEXT
);

CREATE TABLE api.user_profiles (
    id UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.user_profile_spec
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
    ROW(NEW.raw_user_meta_data->>'username', null, null, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
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
    p_quota INTEGER
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
        ROW(p_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
        ROW(p_quota)::api.api_key_spec,
        ROW('Active', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
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
    
    -- Check if the API key has reached quota
    IF (v_api_key.status).usage >= (v_api_key.spec).quota THEN
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

CREATE UNIQUE INDEX api_key_name_unique_idx ON api.api_keys (((metadata).name));
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
