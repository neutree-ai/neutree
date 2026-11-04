-- ----------------------
-- Organization model: OrgUnit, Team and their members
-- ----------------------
-- OrgUnit (a department) and Team (a flat group) are global resources
-- (metadata.workspace is always NULL) written only by directory sync as
-- service_role. service_role bypasses RLS, so the tables have read policies
-- and no write policies: no user can create, change or delete them. Only a
-- local account's OrgUnit membership can be assigned by hand.
--
-- (spec.identity_source, spec.external_id) and metadata.name never change; a
-- directory rename changes metadata.display_name only. An object gone from
-- the directory is not deleted: sync sets status.phase to Inactive.
--
-- PostgREST replaces spec and status whole on PATCH. Sync always writes all
-- of spec, so a spec without parent moves the node to the root. A status
-- without phase keeps the stored phase.
--
-- path is a materialized path of ids, '/<root id>/.../<id>/', owned by the
-- triggers below (a written value is replaced). It uses the "C" collation, in
-- which '/' and digits sort below ':', so a subtree is exactly the range
-- [root.path, root.path || ':') and the plain btree index serves it.

CREATE TYPE api.org_unit_spec AS (
    identity_source TEXT,
    external_id TEXT,
    parent TEXT
);

-- The status of an OrgUnit and of a Team.
CREATE TYPE api.org_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ
);

CREATE TABLE api.org_units (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.org_unit_spec,
    status api.org_status,
    path TEXT COLLATE "C" NOT NULL CHECK (path ~ '^(/[0-9]+)+/$')
);

CREATE UNIQUE INDEX org_units_name_unique_idx ON api.org_units (((metadata).name));
CREATE UNIQUE INDEX org_units_external_id_unique_idx ON api.org_units (((spec).identity_source), ((spec).external_id));
CREATE INDEX org_units_parent_idx ON api.org_units (((spec).parent));
CREATE INDEX org_units_path_idx ON api.org_units (path);

CREATE TYPE api.team_spec AS (
    identity_source TEXT,
    external_id TEXT
);

CREATE TABLE api.teams (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.team_spec,
    status api.org_status
);

CREATE UNIQUE INDEX teams_name_unique_idx ON api.teams (((metadata).name));
CREATE UNIQUE INDEX teams_external_id_unique_idx ON api.teams (((spec).identity_source), ((spec).external_id));

-- identity_source is the IdentitySource whose sync wrote the row, or NULL when
-- an admin assigned a local account by hand. Sync may overwrite any row; a
-- user can only write rows with a NULL identity_source.
CREATE TABLE api.org_unit_members (
    user_id UUID PRIMARY KEY REFERENCES api.user_profiles (id) ON DELETE CASCADE,
    org_unit_id INTEGER NOT NULL REFERENCES api.org_units (id) ON DELETE CASCADE,
    identity_source TEXT CHECK (identity_source <> ''),
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX org_unit_members_org_unit_id_idx ON api.org_unit_members (org_unit_id);

CREATE TABLE api.team_members (
    team_id INTEGER NOT NULL REFERENCES api.teams (id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES api.user_profiles (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX team_members_user_id_idx ON api.team_members (user_id);

-- ----------------------
-- Write checks shared by OrgUnit and Team
-- ----------------------

-- Checks metadata and the (identity_source, external_id) identity of an
-- OrgUnit or Team write and returns the metadata to store. p_old_* are NULL on
-- INSERT.
CREATE OR REPLACE FUNCTION api.org_object_check_identity(
    p_kind TEXT,
    p_meta api.metadata,
    p_identity_source TEXT,
    p_external_id TEXT,
    p_old_name TEXT,
    p_old_identity_source TEXT,
    p_old_external_id TEXT
)
RETURNS api.metadata
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_meta api.metadata := p_meta;
BEGIN
    IF COALESCE(v_meta.workspace, '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10270","message": "A %s is global and has no workspace","hint": "Remove metadata.workspace"}', p_kind),
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_meta.workspace := NULL;

    IF COALESCE(btrim(v_meta.display_name), '') = '' THEN
        v_meta.display_name := v_meta.name;
    END IF;

    IF COALESCE(p_identity_source, '') = '' OR COALESCE(p_external_id, '') = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10271","message": "spec.identity_source and spec.external_id of a %s are required","hint": "A %s is synced from an identity source; give the source name and the directory ID"}', p_kind, p_kind),
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF p_old_name IS NULL THEN
        IF NOT EXISTS (SELECT 1 FROM api.identity_sources i WHERE (i.metadata).name = p_identity_source) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10272","message": "spec.identity_source does not name an identity source","hint": "Use the metadata.name of an IdentitySource, without a type prefix"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    ELSIF v_meta.name IS DISTINCT FROM p_old_name
        OR p_identity_source IS DISTINCT FROM p_old_identity_source
        OR p_external_id IS DISTINCT FROM p_old_external_id THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10273","message": "metadata.name, spec.identity_source and spec.external_id of a %s cannot be changed","hint": "A rename changes metadata.display_name only"}', p_kind),
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN v_meta;
END;
$$;

-- Returns the status to store. The phase is the written one, else the stored
-- one, else Active; last_transition_time moves only when the phase changes.
-- p_old is NULL on INSERT.
CREATE OR REPLACE FUNCTION api.org_object_status(p_new api.org_status, p_old api.org_status)
RETURNS api.org_status
LANGUAGE plpgsql
STABLE
SET search_path = pg_catalog
AS $$
DECLARE
    v_status api.org_status := p_new;
BEGIN
    v_status.phase := COALESCE(NULLIF(p_new.phase, ''), NULLIF(p_old.phase, ''), 'Active');

    IF v_status.phase NOT IN ('Active', 'Inactive') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10274","message": "status.phase must be Active or Inactive","hint": "Set Inactive when the object is gone from the directory"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_status.phase IS DISTINCT FROM p_old.phase THEN
        v_status.last_transition_time := CURRENT_TIMESTAMP;
    ELSIF v_status.last_transition_time IS NULL THEN
        v_status.last_transition_time := p_old.last_transition_time;
    END IF;

    RETURN v_status;
END;
$$;

-- ----------------------
-- OrgUnit triggers
-- ----------------------

CREATE OR REPLACE FUNCTION api.org_units_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.org_unit_spec := NEW.spec;
    v_parent_path TEXT;
    v_parent_source TEXT;
BEGIN
    -- One structural change at a time, so two concurrent moves cannot build a
    -- cycle and a node is never pathed under a parent whose move is not yet
    -- committed. Writes come from a single sync loop; contention is low.
    PERFORM pg_advisory_xact_lock(hashtext('api.org_units.path'));

    NEW.metadata := api.org_object_check_identity(
        'OrgUnit', NEW.metadata, v_spec.identity_source, v_spec.external_id,
        (OLD.metadata).name, (OLD.spec).identity_source, (OLD.spec).external_id);

    IF NEW.id IS DISTINCT FROM OLD.id AND OLD.id IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10273","message": "id of an OrgUnit cannot be changed","hint": "The id is part of the materialized path"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.parent := NULLIF(btrim(v_spec.parent), '');
    NEW.spec := v_spec;

    IF v_spec.parent IS NULL THEN
        NEW.path := '/' || NEW.id || '/';
    ELSE
        SELECT u.path, (u.spec).identity_source
        INTO v_parent_path, v_parent_source
        FROM api.org_units u
        WHERE (u.metadata).name = v_spec.parent;

        IF v_parent_path IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10275","message": "spec.parent does not name an OrgUnit","hint": "Sync the parent first, or leave spec.parent empty for a root"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_parent_source IS DISTINCT FROM v_spec.identity_source THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10275","message": "spec.parent belongs to another identity source","hint": "A tree comes from one identity source"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- The parent is this node or one of its descendants.
        IF position('/' || NEW.id || '/' IN v_parent_path) > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10276","message": "spec.parent would make the OrgUnit tree a cycle","hint": "The parent cannot be the OrgUnit itself or one of its descendants"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        NEW.path := v_parent_path || NEW.id || '/';
    END IF;

    NEW.status := api.org_object_status(NEW.status, OLD.status);

    RETURN NEW;
END;
$$;

-- Re-paths the children of a node whose path changed. Each child's update
-- recomputes its path from this node (api.org_units_before_write) and, its
-- path having changed, fires this trigger for its own children.
CREATE OR REPLACE FUNCTION api.org_units_after_path_change()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    UPDATE api.org_units c
    SET path = c.path
    WHERE (c.spec).parent = (NEW.metadata).name;

    RETURN NULL;
END;
$$;

-- OrgUnits are not deleted (they become Inactive). A physical delete of a node
-- that still has children would leave them pointing at a missing parent.
CREATE OR REPLACE FUNCTION api.org_units_before_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM api.org_units c WHERE (c.spec).parent = (OLD.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10275","message": "An OrgUnit with children cannot be deleted","hint": "Set status.phase to Inactive instead"}',
            detail = '{"status": 409, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER update_org_units_update_timestamp
    BEFORE UPDATE ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_org_units_default_timestamp
    BEFORE INSERT ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_org_units
    BEFORE INSERT OR UPDATE ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER org_units_before_write
    BEFORE INSERT OR UPDATE ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION api.org_units_before_write();

CREATE TRIGGER org_units_after_path_change
    AFTER UPDATE ON api.org_units
    FOR EACH ROW
    WHEN (OLD.path IS DISTINCT FROM NEW.path)
    EXECUTE FUNCTION api.org_units_after_path_change();

CREATE TRIGGER org_units_before_delete
    BEFORE DELETE ON api.org_units
    FOR EACH ROW
    EXECUTE FUNCTION api.org_units_before_delete();

-- ----------------------
-- Team triggers
-- ----------------------

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

CREATE TRIGGER update_teams_update_timestamp
    BEFORE UPDATE ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_teams_default_timestamp
    BEFORE INSERT ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_teams
    BEFORE INSERT OR UPDATE ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER teams_before_write
    BEFORE INSERT OR UPDATE ON api.teams
    FOR EACH ROW
    EXECUTE FUNCTION api.teams_before_write();

-- ----------------------
-- OrgUnit member trigger
-- ----------------------

-- A user is put into an Active OrgUnit only. A row with an identity_source
-- names an existing IdentitySource; a row without one (assigned by hand) is
-- for a local account, i.e. one with no row in api.external_identities.
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

CREATE TRIGGER org_unit_members_before_write
    BEFORE INSERT OR UPDATE ON api.org_unit_members
    FOR EACH ROW
    EXECUTE FUNCTION api.org_unit_members_before_write();

-- ----------------------
-- RLS
-- ----------------------

ALTER TABLE api.org_units ENABLE ROW LEVEL SECURITY;
ALTER TABLE api.teams ENABLE ROW LEVEL SECURITY;
ALTER TABLE api.org_unit_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE api.team_members ENABLE ROW LEVEL SECURITY;

CREATE POLICY "org_unit read policy" ON api.org_units
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'org_unit:read', NULL)));

CREATE POLICY "team read policy" ON api.teams
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'team:read', NULL)));

CREATE POLICY "org_unit_member read policy" ON api.org_unit_members
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'org_unit:read', NULL)));

-- Hand assignment: rows without an identity_source only, so a user never
-- overrides what sync wrote. Writing through PostgREST also needs the read
-- policy (filters and upserts read the row).
CREATE POLICY "org_unit_member create policy" ON api.org_unit_members
    FOR INSERT
    WITH CHECK (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    );

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

CREATE POLICY "team_member read policy" ON api.team_members
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'team:read', NULL)));

-- ----------------------
-- Subtree queries
-- ----------------------
-- Both run as the caller (RLS applies) and are plain SQL so the planner can
-- inline them. The range bounds are scalar subqueries (run once, as
-- parameters) so the path index serves the range whatever the statistics say.

CREATE OR REPLACE FUNCTION api.org_unit_subtree(p_org_unit_id INTEGER)
RETURNS SETOF api.org_units
LANGUAGE sql
STABLE
AS $$
    SELECT u.*
    FROM api.org_units u
    WHERE u.path >= (SELECT r.path FROM api.org_units r WHERE r.id = p_org_unit_id)
      AND u.path < (SELECT r.path || ':' FROM api.org_units r WHERE r.id = p_org_unit_id);
$$;

CREATE OR REPLACE FUNCTION api.org_unit_subtree_members(p_org_unit_id INTEGER)
RETURNS SETOF api.org_unit_members
LANGUAGE sql
STABLE
AS $$
    SELECT m.*
    FROM api.org_unit_subtree(p_org_unit_id) u
    JOIN api.org_unit_members m ON m.org_unit_id = u.id;
$$;

-- Trigger functions and internal helpers are not meant to be called; keep them
-- off the RPC surface.
REVOKE ALL ON FUNCTION api.org_object_check_identity(TEXT, api.metadata, TEXT, TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_object_status(api.org_status, api.org_status) FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_before_write() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_after_path_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_before_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.teams_before_write() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_unit_members_before_write() FROM PUBLIC;

-- Admin holds every permission in the enum, including org_unit:* and team:*.
-- Workspace roles get nothing: the organization is platform data.
SELECT api.update_admin_permissions();
