-- Reverts subtree-scoped writes of hand-written OrgUnit memberships.

-- Member trigger as in the org_units_and_teams migration.
CREATE OR REPLACE FUNCTION api.org_unit_members_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.org_unit_id IS DISTINCT FROM OLD.org_unit_id OR NEW.user_id IS DISTINCT FROM OLD.user_id THEN
        IF EXISTS (
            SELECT 1 FROM api.org_units u
            WHERE u.id = NEW.org_unit_id AND (u.status).phase = 'Inactive'
        ) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10277","message": "Users cannot be put into an inactive OrgUnit","hint": "The OrgUnit is gone from its directory"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        NEW.assigned_at := CURRENT_TIMESTAMP;
    END IF;

    IF NEW.identity_source IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM api.identity_sources i WHERE (i.metadata).name = NEW.identity_source) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10272","message": "identity_source does not name an identity source","hint": "Use the metadata.name of an IdentitySource, or leave it empty for a local assignment"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    ELSIF EXISTS (SELECT 1 FROM api.external_identities e WHERE e.user_id = NEW.user_id) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10278","message": "The OrgUnit of a user from an identity source comes from that source","hint": "Only local accounts can be assigned to an OrgUnit by hand"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

-- Policies as in the org_units_and_teams migration.
DROP POLICY IF EXISTS "org_unit_member update policy" ON api.org_unit_members;
DROP POLICY IF EXISTS "org_unit_member delete policy" ON api.org_unit_members;

CREATE POLICY "org_unit_member update policy" ON api.org_unit_members
    FOR UPDATE
    USING (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    )
    WITH CHECK (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    );

CREATE POLICY "org_unit_member delete policy" ON api.org_unit_members
    FOR DELETE
    USING (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    );
