-- Take log_sink:* out of every role; the enum values themselves stay
-- (see 114's down migration).
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    ARRAY(
        SELECT p
        FROM unnest((spec).permissions) AS p
        WHERE p::text NOT LIKE 'log_sink:%'
    )::api.permission_action[]
)::api.role_spec
WHERE EXISTS (
    SELECT 1 FROM unnest((spec).permissions) AS p WHERE p::text LIKE 'log_sink:%'
);

DROP TABLE IF EXISTS api.log_sinks;

DROP FUNCTION IF EXISTS api.validate_log_sink_spec();

DROP TYPE IF EXISTS api.log_sink_status;
DROP TYPE IF EXISTS api.log_sink_spec;
