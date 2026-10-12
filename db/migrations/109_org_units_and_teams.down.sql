-- Take org_unit:* and team:* out of every role; the enum values themselves stay
-- (see 108's down migration).
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    ARRAY(
        SELECT p
        FROM unnest((spec).permissions) AS p
        WHERE p::text NOT LIKE 'org_unit:%' AND p::text NOT LIKE 'team:%'
    )::api.permission_action[]
)::api.role_spec
WHERE EXISTS (
    SELECT 1 FROM unnest((spec).permissions) AS p
    WHERE p::text LIKE 'org_unit:%' OR p::text LIKE 'team:%'
);

DROP FUNCTION IF EXISTS api.org_unit_subtree_members(INTEGER);
DROP FUNCTION IF EXISTS api.org_unit_subtree(INTEGER);

DROP TABLE IF EXISTS api.team_members;
DROP TABLE IF EXISTS api.org_unit_members;
DROP TABLE IF EXISTS api.teams;
DROP TABLE IF EXISTS api.org_units;

DROP FUNCTION IF EXISTS api.org_unit_members_before_write();
DROP FUNCTION IF EXISTS api.teams_before_write();
DROP FUNCTION IF EXISTS api.org_units_before_delete();
DROP FUNCTION IF EXISTS api.org_units_after_path_change();
DROP FUNCTION IF EXISTS api.org_units_before_write();
DROP FUNCTION IF EXISTS api.org_object_phase(TEXT, TEXT);
DROP FUNCTION IF EXISTS api.org_object_check_identity(TEXT, BOOLEAN, api.metadata, TEXT, TEXT, TEXT, TEXT, TEXT);

DROP TYPE IF EXISTS api.team_status;
DROP TYPE IF EXISTS api.team_spec;
DROP TYPE IF EXISTS api.org_unit_status;
DROP TYPE IF EXISTS api.org_unit_spec;
