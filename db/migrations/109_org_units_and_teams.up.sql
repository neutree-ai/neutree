-- ----------------------
-- Organization model (NEU-836): OrgUnit, Team and their members
-- ----------------------
-- OrgUnit (a department) and Team (a flat group) are global resources:
-- metadata.workspace is always NULL, and reads need org_unit:read / team:read
-- (migration 108) with a NULL workspace.
--
-- Both come only from directory sync (neutree-core, NEU-837), which writes as
-- service_role. service_role has BYPASSRLS, so the tables have read policies
-- only: no user can create, change or delete an OrgUnit or a Team.
--
-- Identity. spec.identity_source is the metadata.name of the IdentitySource the
-- object was synced from (the name alone: an IdentitySource name is unique
-- across types, and other resources also refer to each other by name).
-- spec.external_id is the directory's stable ID of the department or group.
-- (identity_source, external_id) is unique and, like metadata.name, cannot
-- change, so a rename in the directory only changes metadata.display_name.
--
-- Removal. An object that is gone from the directory is not deleted: sync sets
-- status.phase to Inactive. Authorization built on it (NEU-839) must treat an
-- Inactive object as granting nothing. Sync can set it back to Active.
--
-- Tree. spec.parent is the metadata.name of the parent OrgUnit (empty for a
-- root). The path column is a materialized path of ids, '/<root id>/.../<id>/'.
-- It is maintained by the triggers below and nobody else: a written value is
-- always replaced. When a node's path changes (its parent changed, or an
-- ancestor's did), its children are re-pathed, which recurses down the
-- subtree. The column uses the "C" collation so the plain btree index serves
-- prefix (LIKE '/1/2/%') and range queries; see api.org_unit_subtree.
--
-- Spec and status are composites that PostgREST replaces as a whole on PATCH.
-- Sync owns both and always writes all of spec; a spec without parent moves the
-- node to the root. A status written without phase keeps the stored phase.
--
-- Members. api.org_unit_members gives each user at most one OrgUnit (the
-- primary department); api.team_members lets a user be in any number of Teams.
-- They are plain tables keyed by api.user_profiles(id).

CREATE TYPE api.org_unit_spec AS (
    identity_source TEXT,
    external_id TEXT,
    parent TEXT
);

CREATE TYPE api.org_unit_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

CREATE TABLE api.org_units (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.org_unit_spec,
    status api.org_unit_status,
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

CREATE TYPE api.team_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

CREATE TABLE api.teams (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.team_spec,
    status api.team_status
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
    p_is_insert BOOLEAN,
    p_meta api.metadata,
    p_old_name TEXT,
    p_identity_source TEXT,
    p_external_id TEXT,
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

    IF p_is_insert THEN
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

-- Returns the phase to store: the written one, else the stored one, else
-- Active.
CREATE OR REPLACE FUNCTION api.org_object_phase(p_new TEXT, p_old TEXT)
RETURNS TEXT
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
DECLARE
    v_phase TEXT := COALESCE(NULLIF(p_new, ''), NULLIF(p_old, ''), 'Active');
BEGIN
    IF v_phase NOT IN ('Active', 'Inactive') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10274","message": "status.phase must be Active or Inactive","hint": "Set Inactive when the object is gone from the directory"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN v_phase;
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
    v_insert BOOLEAN := TG_OP = 'INSERT';
    v_spec api.org_unit_spec := NEW.spec;
    v_status api.org_unit_status := NEW.status;
    v_old_phase TEXT;
    v_parent_path TEXT;
    v_parent_source TEXT;
BEGIN
    -- One structural change at a time, so two concurrent moves cannot build a
    -- cycle and a node is never pathed under a parent whose move is not yet
    -- committed. Writes come from a single sync loop; contention is low.
    PERFORM pg_advisory_xact_lock(hashtext('api.org_units.path'));

    NEW.metadata := api.org_object_check_identity(
        'OrgUnit', v_insert, NEW.metadata,
        CASE WHEN v_insert THEN NULL ELSE (OLD.metadata).name END,
        v_spec.identity_source, v_spec.external_id,
        CASE WHEN v_insert THEN NULL ELSE (OLD.spec).identity_source END,
        CASE WHEN v_insert THEN NULL ELSE (OLD.spec).external_id END);

    IF NOT v_insert AND NEW.id IS DISTINCT FROM OLD.id THEN
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

    IF NOT v_insert THEN
        v_old_phase := (OLD.status).phase;
    END IF;

    v_status.phase := api.org_object_phase(v_status.phase, v_old_phase);

    IF v_status.phase IS DISTINCT FROM v_old_phase THEN
        v_status.last_transition_time := CURRENT_TIMESTAMP;
    ELSIF v_status.last_transition_time IS NULL AND NOT v_insert THEN
        v_status.last_transition_time := (OLD.status).last_transition_time;
    END IF;

    NEW.status := v_status;

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
DECLARE
    v_insert BOOLEAN := TG_OP = 'INSERT';
    v_status api.team_status := NEW.status;
    v_old_phase TEXT;
BEGIN
    NEW.metadata := api.org_object_check_identity(
        'Team', v_insert, NEW.metadata,
        CASE WHEN v_insert THEN NULL ELSE (OLD.metadata).name END,
        (NEW.spec).identity_source, (NEW.spec).external_id,
        CASE WHEN v_insert THEN NULL ELSE (OLD.spec).identity_source END,
        CASE WHEN v_insert THEN NULL ELSE (OLD.spec).external_id END);

    IF NOT v_insert THEN
        v_old_phase := (OLD.status).phase;
    END IF;

    v_status.phase := api.org_object_phase(v_status.phase, v_old_phase);

    IF v_status.phase IS DISTINCT FROM v_old_phase THEN
        v_status.last_transition_time := CURRENT_TIMESTAMP;
    ELSIF v_status.last_transition_time IS NULL AND NOT v_insert THEN
        v_status.last_transition_time := (OLD.status).last_transition_time;
    END IF;

    NEW.status := v_status;

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
-- for a local account, i.e. one with no external identity link (102).
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
    USING (api.has_permission(auth.uid(), 'org_unit:read', NULL));

CREATE POLICY "team read policy" ON api.teams
    FOR SELECT
    USING (api.has_permission(auth.uid(), 'team:read', NULL));

CREATE POLICY "org_unit_member read policy" ON api.org_unit_members
    FOR SELECT
    USING (api.has_permission(auth.uid(), 'org_unit:read', NULL));

-- Hand assignment: rows without an identity_source only, so a user never
-- overrides what sync wrote. Writing through PostgREST also needs the read
-- policy (filters and upserts read the row).
CREATE POLICY "org_unit_member create policy" ON api.org_unit_members
    FOR INSERT
    WITH CHECK (
        identity_source IS NULL
        AND api.has_permission(auth.uid(), 'org_unit:assign-member', NULL)
    );

CREATE POLICY "org_unit_member update policy" ON api.org_unit_members
    FOR UPDATE
    USING (
        identity_source IS NULL
        AND api.has_permission(auth.uid(), 'org_unit:assign-member', NULL)
    )
    WITH CHECK (
        identity_source IS NULL
        AND api.has_permission(auth.uid(), 'org_unit:assign-member', NULL)
    );

CREATE POLICY "org_unit_member delete policy" ON api.org_unit_members
    FOR DELETE
    USING (
        identity_source IS NULL
        AND api.has_permission(auth.uid(), 'org_unit:assign-member', NULL)
    );

CREATE POLICY "team_member read policy" ON api.team_members
    FOR SELECT
    USING (api.has_permission(auth.uid(), 'team:read', NULL));

-- ----------------------
-- Subtree queries
-- ----------------------
-- Both run as the caller (RLS applies) and are plain SQL so the planner can
-- inline them. A subtree is every path in [root.path, root.path || ':'): a
-- path holds only '/' and digits, which sort below ':' in the "C" collation,
-- so the range is exactly the paths that start with root.path. The bounds are
-- scalar subqueries (run once, as parameters) so the btree index on path
-- serves the range whatever the table statistics say.

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
    FROM api.org_units u
    JOIN api.org_unit_members m ON m.org_unit_id = u.id
    WHERE u.path >= (SELECT r.path FROM api.org_units r WHERE r.id = p_org_unit_id)
      AND u.path < (SELECT r.path || ':' FROM api.org_units r WHERE r.id = p_org_unit_id);
$$;

-- Trigger functions and internal helpers are not meant to be called; keep them
-- off the RPC surface.
REVOKE ALL ON FUNCTION api.org_object_check_identity(TEXT, BOOLEAN, api.metadata, TEXT, TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_object_phase(TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_before_write() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_after_path_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_units_before_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.teams_before_write() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.org_unit_members_before_write() FROM PUBLIC;

-- Admin holds every permission in the enum, including the ones from 108.
-- Workspace roles get nothing: the organization is platform data.
SELECT api.update_admin_permissions();
