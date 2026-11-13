-- ----------------------
-- Rate-limit counters shared by all Kong pods (neutree-ai-access).
-- ----------------------
-- Unlogged: a counter lives for one 1s/60s window, so it is not worth WAL or
-- replication; after a crash or failover the counters start from zero.

CREATE SCHEMA IF NOT EXISTS neutree_gateway;

CREATE UNLOGGED TABLE IF NOT EXISTS neutree_gateway.rate_limit_counters (
    counter_key TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    hits BIGINT NOT NULL,
    PRIMARY KEY (counter_key, window_start)
);

CREATE INDEX IF NOT EXISTS rate_limit_counters_window_start_idx
    ON neutree_gateway.rate_limit_counters (window_start);

GRANT USAGE ON SCHEMA neutree_gateway TO kong_admin;
GRANT SELECT, INSERT, UPDATE ON neutree_gateway.rate_limit_counters TO kong_admin;

CREATE OR REPLACE FUNCTION api.cleanup_gateway_rate_limit_counters(
    p_older_than INTERVAL DEFAULT '2 minutes'::INTERVAL,
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS INTEGER
SECURITY DEFINER
AS $$
DECLARE
    v_count INTEGER;
BEGIN
    WITH deleted AS (
        DELETE FROM neutree_gateway.rate_limit_counters
        WHERE ctid = ANY (ARRAY(
            SELECT ctid
            FROM neutree_gateway.rate_limit_counters
            WHERE window_start < now() - p_older_than
            LIMIT p_batch_size
        ))
        RETURNING 1
    )
    SELECT COUNT(*) INTO v_count FROM deleted;
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

REVOKE ALL ON FUNCTION api.cleanup_gateway_rate_limit_counters(INTERVAL, INTEGER) FROM PUBLIC, anonymous, api_user;
GRANT EXECUTE ON FUNCTION api.cleanup_gateway_rate_limit_counters(INTERVAL, INTEGER) TO service_role;
