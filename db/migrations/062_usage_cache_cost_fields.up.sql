-- Capture extended usage fields on the per-request ledger.
-- Upstream OpenAI-compatible responses already carry these (e.g. OpenRouter:
-- usage.prompt_tokens_details.cached_tokens / cache_write_tokens,
-- usage.completion_tokens_details.reasoning_tokens, usage.cost, response id),
-- but neutree previously only persisted prompt_tokens / completion_tokens.

-- 1a. Add breakdown columns to api_usage_records
ALTER TABLE api.api_usage_records ADD COLUMN cache_read_tokens INTEGER;
ALTER TABLE api.api_usage_records ADD COLUMN cache_creation_tokens INTEGER;
ALTER TABLE api.api_usage_records ADD COLUMN reasoning_tokens INTEGER;
ALTER TABLE api.api_usage_records ADD COLUMN cost_usd DOUBLE PRECISION;
ALTER TABLE api.api_usage_records ADD COLUMN message_id TEXT;

-- 1b. Extend record_api_usage with the new optional parameters (appended with
-- DEFAULT NULL so existing callers keep working). Body mirrors migration 054
-- plus the new columns.
DROP FUNCTION IF EXISTS api.record_api_usage;
CREATE FUNCTION api.record_api_usage(
    p_api_key_id UUID,
    p_request_id TEXT,
    p_usage_amount INTEGER,
    p_endpoint_name TEXT DEFAULT NULL,
    p_endpoint_type TEXT DEFAULT NULL,
    p_model_name TEXT DEFAULT NULL,
    p_prompt_tokens INTEGER DEFAULT NULL,
    p_completion_tokens INTEGER DEFAULT NULL,
    p_cache_read_tokens INTEGER DEFAULT NULL,
    p_cache_creation_tokens INTEGER DEFAULT NULL,
    p_reasoning_tokens INTEGER DEFAULT NULL,
    p_cost_usd DOUBLE PRECISION DEFAULT NULL,
    p_message_id TEXT DEFAULT NULL
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
        cache_read_tokens,
        cache_creation_tokens,
        reasoning_tokens,
        cost_usd,
        message_id,
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
        p_cache_read_tokens,
        p_cache_creation_tokens,
        p_reasoning_tokens,
        p_cost_usd,
        p_message_id,
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

-- Ask PostgREST to reload its schema cache so the new RPC signature is visible.
NOTIFY pgrst, 'reload schema';
