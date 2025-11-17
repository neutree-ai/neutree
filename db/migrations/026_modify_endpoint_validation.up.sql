DROP TRIGGER IF EXISTS validate_endpoint_replica_count_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_replica_count();
CREATE OR REPLACE FUNCTION api.validate_endpoint_replica_count()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).replicas.num IS NOT NULL THEN
        IF (NEW.spec).replicas.num < 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10106","message": "spec.replicas.num must be at least 0","hint": "Provide a valid replica count"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_replica_count_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_replica_count();