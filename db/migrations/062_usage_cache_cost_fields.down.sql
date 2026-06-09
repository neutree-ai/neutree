-- Revert extended usage fields.

-- Restore the migration 054 version of record_api_usage (without the new params).
DROP FUNCTION IF EXISTS api.record_api_usage(
    UUID, TEXT, INTEGER, TEXT, TEXT, TEXT, INTEGER, INTEGER,
    INTEGER, INTEGER, INTEGER, DOUBLE PRECISION, TEXT
);
CREATE FUNCTION api.record_api_usage(
    p_api_key_id UUID,
    p_request_id TEXT,
    p_usage_amount INTEGER,
    p_endpoint_name TEXT DEFAULT NULL,
    p_endpoint_type TEXT DEFAULT NULL,
    p_model_name TEXT DEFAULT NULL,
    p_prompt_tokens INTEGER DEFAULT NULL,
    p_completion_tokens INTEGER DEFAULT NULL
) RETURNS JSONB
SECURITY DEFINER
AS $$
DECLARE
    v_api_key RECORD;
    v_workspace TEXT;
BEGIN
    SELECT id, (metadata).workspace INTO v_api_key
    FROM api.api_keys
    WHERE id = p_api_key_id;

    IF v_api_key.id IS NULL THEN
        RETURN jsonb_build_object(
            'success', false,
            'error', format('Invalid API key: %s', p_api_key_id),
            'request_id', p_request_id
        );
    END IF;
    v_workspace := v_api_key.workspace;

    INSERT INTO api.api_usage_records (
        api_key_id,
        request_id,
        usage_amount,
        endpoint_name,
        endpoint_type,
        model_name,
        prompt_tokens,
        completion_tokens,
        created_at
    ) VALUES (
        p_api_key_id,
        p_request_id,
        p_usage_amount,
        p_endpoint_name,
        p_endpoint_type,
        p_model_name,
        p_prompt_tokens,
        p_completion_tokens,
        now()
    );

    RETURN jsonb_build_object(
        'success', true,
        'api_key_id', p_api_key_id,
        'request_id', p_request_id,
        'usage_recorded', p_usage_amount
    );

EXCEPTION WHEN OTHERS THEN
    RETURN jsonb_build_object(
        'success', false,
        'error', SQLERRM,
        'api_key_id', p_api_key_id,
        'request_id', p_request_id
    );
END;
$$ LANGUAGE plpgsql;

ALTER TABLE api.api_usage_records DROP COLUMN IF EXISTS cache_read_tokens;
ALTER TABLE api.api_usage_records DROP COLUMN IF EXISTS cache_creation_tokens;
ALTER TABLE api.api_usage_records DROP COLUMN IF EXISTS reasoning_tokens;
ALTER TABLE api.api_usage_records DROP COLUMN IF EXISTS cost_usd;
ALTER TABLE api.api_usage_records DROP COLUMN IF EXISTS message_id;

NOTIFY pgrst, 'reload schema';
