-- Add expires_in field to api_key_spec and update API key generation to use compact binary encoding

-- Drop existing functions first to avoid signature conflicts
DROP FUNCTION IF EXISTS api.generate_api_key();
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT);

-- Add expires_in field to api_key_spec 
ALTER TYPE api.api_key_spec ADD ATTRIBUTE expires_in INTEGER;

-- Update generate_api_key function to create compact self-contained encrypted API keys
CREATE OR REPLACE FUNCTION api.generate_api_key(
    p_user_id UUID,
    p_key_id UUID,
    p_expires_in INTEGER DEFAULT NULL
)
RETURNS TEXT
AS $$
DECLARE
    key_value TEXT;
    payload_binary BYTEA;
    encrypted_payload BYTEA;
    signature BYTEA;
    jwt_secret TEXT;
    expires_at BIGINT;
BEGIN
    -- Get JWT secret from PostgREST settings
    jwt_secret := current_setting('app.settings.jwt_secret', true);
    
    -- Fallback if secret is not available
    IF jwt_secret IS NULL OR jwt_secret = '' THEN
        RAISE EXCEPTION 'JWT secret not available in app.settings.jwt_secret';
    END IF;
    
    -- Calculate expiration timestamp
    IF p_expires_in IS NULL THEN
        expires_at := 0;  -- Never expires
    ELSE
        expires_at := extract(epoch from now())::bigint + p_expires_in;
    END IF;
    
    -- Create compact binary payload: user_id (16 bytes) + key_id (16 bytes) + expires_at (8 bytes) = 40 bytes
    payload_binary := decode(replace(p_user_id::text, '-', ''), 'hex') || 
                      decode(replace(p_key_id::text, '-', ''), 'hex') ||
                      substring(int8send(expires_at), 1, 8);  -- 8-byte big-endian timestamp
    
    -- Encrypt using AES (more compact than PGP)
    encrypted_payload := public.encrypt(payload_binary, jwt_secret::bytea, 'aes');
    
    -- Create HMAC signature (truncated to 16 bytes for compactness)
    signature := substring(public.hmac(encrypted_payload, jwt_secret::bytea, 'sha256'), 1, 16);
    
    -- Format: sk_<base64url_encrypted_payload><base64url_signature> (no separator for compactness)
    -- Use base64url encoding (URL-safe, no padding, no newlines)
    key_value := 'sk_' || 
                rtrim(translate(replace(encode(encrypted_payload || signature, 'base64'), E'\n', ''), '+/', '-_'), '=');
    
    RETURN key_value;
END;
$$ LANGUAGE plpgsql;

-- Update create_api_key function to support expires_in parameter
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
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
        ROW(p_quota, p_expires_in)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;