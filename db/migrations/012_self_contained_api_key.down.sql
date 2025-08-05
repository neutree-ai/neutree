-- Rollback to the original simple generate_api_key function and remove expires_in from api_key_spec

-- Drop the new functions first to avoid signature conflicts
DROP FUNCTION IF EXISTS api.generate_api_key(UUID, UUID, INTEGER);
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER);

-- Restore original create_api_key function signature
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
    
    -- Generate a new API key value using original simple method
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
        ROW(p_quota)::api.api_key_spec,  -- Only quota, no expires_in
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- Restore original simple generate_api_key function
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

-- Remove expires_in field from api_key_spec type
-- Note: This will reset existing API keys' spec column
ALTER TYPE api.api_key_spec DROP ATTRIBUTE IF EXISTS expires_in;

-- Update existing api_keys to use the simplified spec structure
UPDATE api.api_keys SET spec = ROW((spec).quota)::api.api_key_spec;