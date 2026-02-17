CREATE OR REPLACE FUNCTION api.notify_role_reconcile_event()
RETURNS TRIGGER AS $$
DECLARE
    role_id TEXT;
BEGIN
    role_id := COALESCE(NEW.id, OLD.id)::TEXT;

    PERFORM pg_notify(
        'neutree_reconcile',
        json_build_object(
            'kind', 'role',
            'id', role_id
        )::TEXT
    );

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS notify_role_reconcile_event ON api.roles;

CREATE TRIGGER notify_role_reconcile_event
    AFTER INSERT OR UPDATE OR DELETE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION api.notify_role_reconcile_event();
