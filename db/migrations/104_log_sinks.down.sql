DROP TABLE IF EXISTS api.log_sinks;

DROP FUNCTION IF EXISTS api.validate_log_sink_spec();

DROP TYPE IF EXISTS api.log_sink_status;
DROP TYPE IF EXISTS api.log_sink_spec;

-- admin keeps the log_sink permissions: enum values cannot be removed, and
-- update_admin_permissions() always re-aggregates the full enum.
