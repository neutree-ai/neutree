-- Simplify usage statistics from model to endpoint dimension
ALTER TABLE api.api_usage_records DROP COLUMN workspace;
ALTER TABLE api.api_usage_records RENAME COLUMN model TO endpoint_name;

DROP FUNCTION IF EXISTS api.record_api_usage(UUID, TEXT, INTEGER, TEXT);
CREATE FUNCTION api.record_api_usage(
    p_api_key_id UUID,
    p_request_id TEXT,
    p_usage_amount INTEGER,
    p_endpoint_name TEXT DEFAULT NULL
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
        created_at
    ) VALUES (
        p_api_key_id,
        p_request_id,
        p_usage_amount,
        p_endpoint_name,
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

DROP FUNCTION IF EXISTS api.aggregate_usage_records(TIMESTAMP WITH TIME ZONE);
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

DROP FUNCTION IF EXISTS api.get_usage_by_dimension(DATE, DATE, UUID, TEXT, TEXT);
CREATE FUNCTION api.get_usage_by_dimension(
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
    endpoint_name TEXT,
    workspace TEXT,
    usage BIGINT
)
SECURITY DEFINER
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
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
            kv.key AS endpoint_name,
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
        d.endpoint_name,
        COALESCE(e.workspace, 'unknown') AS workspace,
        d.dimension_usage
    FROM 
        dimension_data d
        LEFT JOIN (
            SELECT (metadata).name AS endpoint_name, (metadata).workspace AS workspace
            FROM api.endpoints
        ) e ON d.endpoint_name = e.endpoint_name
    WHERE 
        (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name) AND
        (p_workspace IS NULL OR e.workspace = p_workspace)
    ORDER BY
        d.usage_date DESC,
        d.api_key_id,
        d.endpoint_name;
END;
$$ LANGUAGE plpgsql;

TRUNCATE api.api_usage_records;
TRUNCATE api.api_daily_usage;
UPDATE api.api_keys SET status = ROW(
    (status).phase,
    (status).last_transition_time,
    (status).error_message,
    (status).sk_value,
    0,
    (status).last_used_at,
    (status).last_sync_at
)::api.api_key_status;