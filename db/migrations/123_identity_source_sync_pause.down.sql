-- Reverts the sync pause: the request goes back to the identity_source_sync body.

CREATE OR REPLACE FUNCTION api.request_identity_source_sync(p_name TEXT)
RETURNS TIMESTAMPTZ
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.identity_source_spec;
    v_sync api.identity_source_sync_spec;
    v_now TIMESTAMPTZ := clock_timestamp();
BEGIN
    SELECT (i.spec).* INTO v_spec
    FROM api.identity_sources i
    WHERE (i.metadata).name = p_name
      AND (i.metadata).deletion_timestamp IS NULL;

    IF NOT FOUND THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10263","message": "Identity source not found","hint": "Check the name"}',
            detail = '{"status": 404, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.type IS DISTINCT FROM 'ldap' OR (v_spec.sync).enabled IS NOT TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10264","message": "Sync is not enabled for this identity source","hint": "Only LDAP sources sync; set spec.sync.enabled first"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_sync := v_spec.sync;
    v_sync.requested_at := v_now;
    v_spec.sync := v_sync;

    UPDATE api.identity_sources i
    SET spec = v_spec
    WHERE (i.metadata).name = p_name
      AND (i.metadata).deletion_timestamp IS NULL;

    RETURN v_now;
END;
$$;

REVOKE ALL ON FUNCTION api.request_identity_source_sync(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api.request_identity_source_sync(TEXT) TO api_user, service_role;
