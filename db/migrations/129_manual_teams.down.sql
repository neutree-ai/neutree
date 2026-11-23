-- Reverts manual Teams. Teams created in Neutree are deleted first: the
-- restored teams_before_write requires an identity source on every Team.

DELETE FROM api.teams WHERE (spec).identity_source IS NULL;

DROP POLICY IF EXISTS "team_member delete policy" ON api.team_members;
DROP POLICY IF EXISTS "team_member create policy" ON api.team_members;
DROP POLICY IF EXISTS "team delete policy" ON api.teams;
DROP POLICY IF EXISTS "team update policy" ON api.teams;
DROP POLICY IF EXISTS "team create policy" ON api.teams;

DROP TRIGGER IF EXISTS team_members_before_write ON api.team_members;
DROP TRIGGER IF EXISTS teams_before_delete ON api.teams;
DROP FUNCTION IF EXISTS api.team_members_before_write();
DROP FUNCTION IF EXISTS api.teams_before_delete();

-- Take the team write permissions out of every role; the enum values stay
-- (see the team_write_permissions down migration).
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    ARRAY(
        SELECT p
        FROM unnest((spec).permissions) AS p
        WHERE p::text NOT IN ('team:create', 'team:update', 'team:delete', 'team:assign-member')
    )::api.permission_action[]
)::api.role_spec
WHERE EXISTS (
    SELECT 1 FROM unnest((spec).permissions) AS p
    WHERE p::text IN ('team:create', 'team:update', 'team:delete', 'team:assign-member')
);

-- api.teams_before_write as in the org_units_and_teams migration.
CREATE OR REPLACE FUNCTION api.teams_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    NEW.metadata := api.org_object_check_identity(
        'Team', NEW.metadata, (NEW.spec).identity_source, (NEW.spec).external_id,
        (OLD.metadata).name, (OLD.spec).identity_source, (OLD.spec).external_id);

    NEW.status := api.org_object_status(NEW.status, OLD.status);

    RETURN NEW;
END;
$$;
