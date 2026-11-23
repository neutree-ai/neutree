-- ----------------------
-- Teams created in Neutree
-- ----------------------
-- A Team now has two sources:
--   * synced: spec.identity_source and spec.external_id are set. Directory
--     sync (service_role) owns the Team and its members; users cannot change
--     or delete either.
--   * manual: both are NULL. Users with team:create / team:update /
--     team:delete manage the Team, and team:assign-member manages its
--     members. Sync lists Teams by identity_source, so it never sees these.
--     A manual Team is always Active (10291); removing it means deleting it.
--
-- Permissions are global only (like team:read). A manual Team takes
-- any user, local or external; the "no local accounts" rule is about
-- synced Teams, whose members come from the directory.
--
-- Names. metadata.name is unique over all Teams. Sync names its Teams
-- '<source>-<12 hex>' (identitysync.ObjectName), so a manual name with that
-- ending is refused: it could block a later sync from creating its Team.
--
-- Deletion is a plain DELETE (no controller cleans up a soft-deleted Team);
-- team_members goes with it through ON DELETE CASCADE.
--
-- Who is writing: auth.uid() is set for a user request and NULL for sync
-- (service_role) and for maintenance as the database owner.

CREATE OR REPLACE FUNCTION api.teams_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_insert BOOLEAN := TG_OP = 'INSERT';
    v_spec api.team_spec := NEW.spec;
    v_meta api.metadata := NEW.metadata;
    v_old_synced BOOLEAN := (OLD.spec).identity_source IS NOT NULL;
    v_manual BOOLEAN;
BEGIN
    v_spec.identity_source := NULLIF(v_spec.identity_source, '');
    v_spec.external_id := NULLIF(v_spec.external_id, '');

    IF auth.uid() IS NOT NULL
        AND (v_old_synced OR v_spec.identity_source IS NOT NULL OR v_spec.external_id IS NOT NULL) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10289","message": "A Team synced from an identity source is read-only","hint": "Change it in the directory; a Team created in Neutree has no spec.identity_source or spec.external_id"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_manual := v_spec.identity_source IS NULL AND v_spec.external_id IS NULL;

    IF v_manual THEN
        IF v_old_synced THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10273","message": "spec.identity_source and spec.external_id of a Team cannot be changed","hint": "A synced Team stays synced"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF COALESCE(v_meta.workspace, '') <> '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10270","message": "A Team is global and has no workspace","hint": "Remove metadata.workspace"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_meta.workspace := NULL;

        IF COALESCE(btrim(v_meta.display_name), '') = '' THEN
            v_meta.display_name := v_meta.name;
        END IF;

        IF v_insert THEN
            IF v_meta.name ~ '-[0-9a-f]{12}$' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10288","message": "A Team name ending in a dash and 12 hex digits is reserved for synced Teams","hint": "Choose another metadata.name; metadata.display_name is free"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;
        ELSIF v_meta.name IS DISTINCT FROM (OLD.metadata).name THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10273","message": "metadata.name of a Team cannot be changed","hint": "A rename changes metadata.display_name only"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        NEW.metadata := v_meta;
    ELSE
        NEW.metadata := api.org_object_check_identity(
            'Team', v_meta, v_spec.identity_source, v_spec.external_id,
            (OLD.metadata).name, (OLD.spec).identity_source, (OLD.spec).external_id);
    END IF;

    NEW.spec := v_spec;
    NEW.status := api.org_object_status(NEW.status, OLD.status);

    -- Inactive means "gone from the directory"; a manual Team is deleted
    -- instead.
    IF v_manual AND (NEW.status).phase <> 'Active' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10291","message": "A Team created in Neutree is always Active","hint": "Delete the Team instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION api.teams_before_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF auth.uid() IS NOT NULL AND (OLD.spec).identity_source IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10289","message": "A Team synced from an identity source is read-only","hint": "Sync sets it Inactive when the group is gone from the directory"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER teams_before_delete
    BEFORE DELETE ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION api.teams_before_delete();

-- Members of a synced Team come from the directory. Only direct writes are
-- checked: deleting a user cascades into team_members from a referential
-- trigger (depth > 1), and that must keep working.
CREATE OR REPLACE FUNCTION api.team_members_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_team_id INTEGER;
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_team_id := OLD.team_id;
    ELSE
        v_team_id := NEW.team_id;
    END IF;

    -- The permission check keeps a user without it from learning which Teams
    -- are synced; RLS refuses that user anyway.
    IF auth.uid() IS NOT NULL
        AND pg_trigger_depth() = 1
        AND EXISTS (
            SELECT 1 FROM api.teams t
            WHERE t.id = v_team_id AND (t.spec).identity_source IS NOT NULL
        )
        AND api.has_permission(auth.uid(), 'team:assign-member', NULL) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10290","message": "Members of a Team synced from an identity source are read-only","hint": "Change the group in the directory, or use a Team created in Neutree"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER team_members_before_write
    BEFORE INSERT OR DELETE ON api.team_members
    FOR EACH ROW
    EXECUTE FUNCTION api.team_members_before_write();

REVOKE ALL ON FUNCTION api.teams_before_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.team_members_before_write() FROM PUBLIC;

-- ----------------------
-- RLS
-- ----------------------
-- UPDATE and DELETE do not filter out synced rows in USING, so the triggers
-- above can answer with 10289/10290 instead of an empty result. Writing
-- through PostgREST also needs the read policy (team:read).

CREATE POLICY "team create policy" ON api.teams
    FOR INSERT
    WITH CHECK (
        (spec).identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'team:create', NULL))
    );

CREATE POLICY "team update policy" ON api.teams
    FOR UPDATE
    USING ((SELECT api.has_permission(auth.uid(), 'team:update', NULL)))
    WITH CHECK (
        (spec).identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'team:update', NULL))
    );

CREATE POLICY "team delete policy" ON api.teams
    FOR DELETE
    USING ((SELECT api.has_permission(auth.uid(), 'team:delete', NULL)));

-- No UPDATE policy: a membership is added or removed, never moved.
CREATE POLICY "team_member create policy" ON api.team_members
    FOR INSERT
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL)));

CREATE POLICY "team_member delete policy" ON api.team_members
    FOR DELETE
    USING ((SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL)));

-- Admin holds every permission in the enum, including the team write ones.
-- department-admin gets none of them: Teams cross departments.
SELECT api.update_admin_permissions();
