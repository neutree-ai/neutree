-- Restore aggregate_usage_records as defined in 054 and get_usage_by_dimension
-- as defined in 092. Breakdown keys already written into
-- detailed_dimensional_usage are left in place; the 092 query ignores them.

DROP FUNCTION IF EXISTS api.aggregate_usage_records;
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
    v_detail_key TEXT;
    v_metadata api.metadata;
    v_workspace TEXT;
    v_initial_detail JSONB;
    v_existing_detail JSONB;
    v_existing_entry JSONB;
    v_new_detail JSONB;
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
            endpoint_type,
            model_name,
            usage_amount,
            prompt_tokens,
            completion_tokens
        FROM api.api_usage_records
        WHERE
            is_aggregated = false AND
            created_at < p_older_than
        ORDER BY created_at
    LOOP
        v_date := v_record.usage_date;
        v_dimension_key := v_record.endpoint_name;

        -- Build detailed dimension key when endpoint_type is available
        v_detail_key := NULL;
        v_initial_detail := NULL;
        IF v_record.endpoint_type IS NOT NULL THEN
            v_detail_key := v_record.endpoint_type || '|' || v_record.endpoint_name || '|' || COALESCE(v_record.model_name, '');
            v_initial_detail := jsonb_build_object(
                v_detail_key, jsonb_build_object(
                    'total', v_record.usage_amount,
                    'prompt', COALESCE(v_record.prompt_tokens, 0),
                    'completion', COALESCE(v_record.completion_tokens, 0)
                )
            );
        END IF;

        SELECT (ak.metadata).workspace INTO v_workspace
        FROM api.api_keys ak
        WHERE ak.id = v_record.api_key_id;

        SELECT
            id,
            ((spec).dimensional_usage) AS dimensional_usage,
            ((spec).detailed_dimensional_usage) AS detailed_dimensional_usage
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
                    jsonb_build_object(v_dimension_key, v_record.usage_amount),
                    v_initial_detail
                )::api.api_daily_usage_spec,
                ROW(
                    CURRENT_TIMESTAMP
                )::api.api_daily_usage_status
            )
            RETURNING id, ((spec).dimensional_usage), ((spec).detailed_dimensional_usage) INTO v_daily_record;
        ELSE
            -- Build updated detailed_dimensional_usage
            v_new_detail := v_daily_record.detailed_dimensional_usage;
            IF v_detail_key IS NOT NULL THEN
                v_existing_detail := COALESCE(v_new_detail, '{}'::jsonb);
                v_existing_entry := COALESCE(v_existing_detail->v_detail_key, '{"total":0,"prompt":0,"completion":0}'::jsonb);
                v_new_detail := jsonb_set(
                    v_existing_detail,
                    ARRAY[v_detail_key],
                    jsonb_build_object(
                        'total', (v_existing_entry->>'total')::int + v_record.usage_amount,
                        'prompt', (v_existing_entry->>'prompt')::int + COALESCE(v_record.prompt_tokens, 0),
                        'completion', (v_existing_entry->>'completion')::int + COALESCE(v_record.completion_tokens, 0)
                    ),
                    true
                );
            END IF;

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
                    ),
                    v_new_detail
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

DROP FUNCTION IF EXISTS api.get_usage_by_dimension;
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
    api_key_display_name TEXT,
    api_key_description TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    workspace TEXT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT
)
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).display_name AS key_display_name,
            (ak.spec).description AS key_description,
            (ak.metadata).workspace AS key_workspace
        FROM api.api_keys ak
        WHERE (
            ak.user_id = auth.uid()
            OR api.has_permission(
                auth.uid(), 'workspace:usage-read', (ak.metadata).workspace
            )
        )
        AND (p_api_key_id IS NULL OR ak.id = p_api_key_id)
    ), old_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            NULL::text AS endpoint_type,
            kv.key AS endpoint_name,
            NULL::text AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value)::bigint AS dimension_usage,
            NULL::bigint AS p_tokens,
            NULL::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NULL
    ), new_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            split_part(kv.key, '|', 1) AS endpoint_type,
            split_part(kv.key, '|', 2) AS endpoint_name,
            NULLIF(split_part(kv.key, '|', 3), '') AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value->>'total')::bigint AS dimension_usage,
            (kv.value->>'prompt')::bigint AS p_tokens,
            (kv.value->>'completion')::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ), dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
    )
    SELECT
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.key_display_name,
        d.key_description,
        d.endpoint_type,
        d.endpoint_name,
        d.model_name,
        d.workspace,
        d.dimension_usage,
        d.p_tokens,
        d.c_tokens
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;
