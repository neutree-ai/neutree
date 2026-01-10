DROP FUNCTION IF EXISTS api.sync_api_key_usage();
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