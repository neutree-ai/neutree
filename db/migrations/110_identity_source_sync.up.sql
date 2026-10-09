-- ----------------------
-- Organization sync of identity sources (NEU-837)
-- ----------------------
-- neutree-core reads an LDAP directory in full on a schedule and brings the
-- OrgUnits, Teams, users and memberships of the source in line with it
-- (pkg/identity/orgsync). This migration adds what it needs:
--
--   * spec.sync (enabled, interval, requested_at) and spec.ldap.sync (where to
--     find departments, groups and users), plus status.last_sync.
--   * The users' last synced username and email, and the sync deactivation
--     marker, on api.external_identities.
--   * api.list_identity_source_sync_users, the stored users of a source as the
--     sync compares them with the directory (service_role only).
--   * api.request_identity_source_sync, which asks for a sync now.
--
-- spec and status are composites that PostgREST replaces as a whole on PATCH.
-- A client written before this migration (or one that just leaves them out)
-- sends spec without sync, so the trigger below keeps the stored spec.sync and
-- spec.ldap.sync when a write has none (NEU-717). To turn sync off, write
-- spec.sync.enabled = false. status is written by neutree-core only, always
-- whole.

CREATE TYPE api.identity_source_sync_spec AS (
    enabled BOOLEAN,
    interval INTEGER, -- seconds
    requested_at TIMESTAMPTZ
);

CREATE TYPE api.identity_source_ldap_sync_spec AS (
    org_unit_base_dn TEXT,
    org_unit_filter TEXT,
    org_unit_name_attribute TEXT,
    group_base_dn TEXT,
    group_filter TEXT,
    group_name_attribute TEXT,
    group_member_attribute TEXT,
    user_list_filter TEXT,
    disabled_user_filter TEXT,
    page_size INTEGER
);

CREATE TYPE api.identity_source_sync_status AS (
    requested_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    ok BOOLEAN,
    message TEXT,
    last_success_time TIMESTAMPTZ,
    created INTEGER,
    updated INTEGER,
    reactivated INTEGER,
    deactivated INTEGER,
    memberships INTEGER,
    pending INTEGER
);

ALTER TYPE api.identity_source_ldap_spec ADD ATTRIBUTE sync api.identity_source_ldap_sync_spec;
ALTER TYPE api.identity_source_spec ADD ATTRIBUTE sync api.identity_source_sync_spec;
ALTER TYPE api.identity_source_status ADD ATTRIBUTE last_sync api.identity_source_sync_status;

-- Runs after api.identity_sources_before_write (triggers fire in name order),
-- which has already validated and normalized the rest of spec.
CREATE OR REPLACE FUNCTION api.identity_sources_sync_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.identity_source_spec := NEW.spec;
    v_sync api.identity_source_sync_spec;
    v_ldap api.identity_source_ldap_spec;
    v_ldap_sync api.identity_source_ldap_sync_spec;
BEGIN
    IF v_spec IS NULL OR v_spec.type IS NULL THEN
        RETURN NEW;
    END IF;

    -- A write without sync keeps the stored one. "IS NULL" is also true for a
    -- composite whose attributes are all NULL, i.e. an empty object.
    IF TG_OP = 'UPDATE' AND v_spec.sync IS NULL THEN
        v_spec.sync := (OLD.spec).sync;
    END IF;

    IF TG_OP = 'UPDATE' AND v_spec.type = 'ldap' AND (OLD.spec).type = 'ldap'
        AND NOT (v_spec.ldap IS NULL) AND (v_spec.ldap).sync IS NULL THEN
        v_ldap := v_spec.ldap;
        v_ldap.sync := ((OLD.spec).ldap).sync;
        v_spec.ldap := v_ldap;
    END IF;

    v_sync := v_spec.sync;

    -- NOT (x IS NULL), not x IS NOT NULL: the latter is false for a composite
    -- with any NULL attribute.
    IF NOT (v_sync IS NULL) THEN
        IF COALESCE(v_sync.interval, 0) < 0
            OR (COALESCE(v_sync.interval, 0) > 0 AND v_sync.interval < 60) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10262","message": "spec.sync.interval must be at least 60 seconds","hint": "Every sync reads the whole directory; leave it empty for one hour"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_sync.enabled := COALESCE(v_sync.enabled, FALSE);
        v_sync.interval := COALESCE(NULLIF(v_sync.interval, 0), 3600);
        v_spec.sync := v_sync;
    END IF;

    IF v_spec.type = 'ldap' AND NOT (v_spec.ldap IS NULL) AND NOT ((v_spec.ldap).sync IS NULL) THEN
        v_ldap := v_spec.ldap;
        v_ldap_sync := v_ldap.sync;

        IF COALESCE(v_ldap_sync.page_size, 0) < 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10262","message": "spec.ldap.sync.page_size must not be negative","hint": "Leave it empty for the default"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF position('{username}' IN COALESCE(v_ldap_sync.user_list_filter, '')) > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10262","message": "spec.ldap.sync.user_list_filter must not contain {username}","hint": "It selects every user to sync; leave it empty to use user_filter with {username} replaced by *"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_ldap_sync.org_unit_base_dn := NULLIF(btrim(v_ldap_sync.org_unit_base_dn), '');
        v_ldap_sync.org_unit_filter := NULLIF(btrim(v_ldap_sync.org_unit_filter), '');
        v_ldap_sync.org_unit_name_attribute := NULLIF(btrim(v_ldap_sync.org_unit_name_attribute), '');
        v_ldap_sync.group_base_dn := NULLIF(btrim(v_ldap_sync.group_base_dn), '');
        v_ldap_sync.group_filter := NULLIF(btrim(v_ldap_sync.group_filter), '');
        v_ldap_sync.group_name_attribute := NULLIF(btrim(v_ldap_sync.group_name_attribute), '');
        v_ldap_sync.group_member_attribute := NULLIF(btrim(v_ldap_sync.group_member_attribute), '');
        v_ldap_sync.user_list_filter := NULLIF(btrim(v_ldap_sync.user_list_filter), '');
        v_ldap_sync.disabled_user_filter := NULLIF(btrim(v_ldap_sync.disabled_user_filter), '');
        v_ldap_sync.page_size := NULLIF(v_ldap_sync.page_size, 0);

        v_ldap.sync := v_ldap_sync;
        v_spec.ldap := v_ldap;
    END IF;

    NEW.spec := v_spec;

    RETURN NEW;
END;
$$;

CREATE TRIGGER identity_sources_sync_before_write
    BEFORE INSERT OR UPDATE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION api.identity_sources_sync_before_write();

REVOKE ALL ON FUNCTION api.identity_sources_sync_before_write() FROM PUBLIC;

-- Asks for a sync of the named source now, by setting spec.sync.requested_at.
-- It runs as the caller, so RLS applies: without identity_source:read the
-- source is not found, without identity_source:update the write is refused
-- (42501, HTTP 403). neutree-core runs the sync on its next pass (within seconds).
CREATE OR REPLACE FUNCTION api.request_identity_source_sync(p_name TEXT)
RETURNS TIMESTAMPTZ
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.identity_source_spec;
    v_sync api.identity_source_sync_spec;
    v_now TIMESTAMPTZ := clock_timestamp();
BEGIN
    SELECT (i.spec).* INTO v_spec
    FROM api.identity_sources i
    WHERE (i.metadata).name = p_name
      AND (i.metadata).deletion_timestamp IS NULL;

    IF NOT FOUND THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10263","message": "Identity source not found","hint": "Check the name"}',
            detail = '{"status": 404, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.type IS DISTINCT FROM 'ldap' OR (v_spec.sync).enabled IS NOT TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10264","message": "Sync is not enabled for this identity source","hint": "Only LDAP sources sync; set spec.sync.enabled first"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_sync := v_spec.sync;
    v_sync.requested_at := v_now;
    v_spec.sync := v_sync;

    UPDATE api.identity_sources i
    SET spec = v_spec
    WHERE (i.metadata).name = p_name
      AND (i.metadata).deletion_timestamp IS NULL;

    RETURN v_now;
END;
$$;

REVOKE ALL ON FUNCTION api.request_identity_source_sync(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api.request_identity_source_sync(TEXT) TO api_user, service_role;

-- What the sync last applied to a linked account, and whether the sync
-- deactivated it. username and email are the directory values last written
-- (NULL for an account linked by a login before this migration, which the
-- next sync then updates). sync_deactivated is NULL for an active account,
-- 'banned' when the sync banned the GoTrue user, and 'kept' when the user was
-- already banned for another reason: a reactivation lifts only a ban the sync
-- set itself.
ALTER TABLE api.external_identities
    ADD COLUMN username TEXT,
    ADD COLUMN email TEXT,
    ADD COLUMN sync_deactivated TEXT CHECK (sync_deactivated IN ('banned', 'kept'));

GRANT UPDATE (username, email, sync_deactivated) ON api.external_identities TO service_role;

-- The stored users of one identity source as the sync compares them with the
-- directory: every account linked under p_link_source (e.g. ldap:corp-ldap),
-- its profile display name, and the department and teams the sync of
-- p_identity_source put it in. Department rows assigned by hand (no
-- identity_source) or by another source are not the sync's and are left out.
-- service_role only; it runs as the caller, which bypasses RLS.
CREATE OR REPLACE FUNCTION api.list_identity_source_sync_users(p_identity_source TEXT, p_link_source TEXT)
RETURNS TABLE (
    external_id TEXT,
    user_id UUID,
    username TEXT,
    email TEXT,
    display_name TEXT,
    sync_deactivated TEXT,
    org_unit_external_id TEXT,
    team_external_ids TEXT[]
)
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
    SELECT e.external_id,
           e.user_id,
           e.username,
           e.email,
           (p.metadata).display_name,
           e.sync_deactivated,
           (ou.spec).external_id,
           COALESCE(
               (SELECT array_agg((t.spec).external_id ORDER BY (t.spec).external_id)
                FROM api.team_members tm
                JOIN api.teams t ON t.id = tm.team_id
                WHERE tm.user_id = e.user_id
                  AND (t.spec).identity_source = p_identity_source),
               ARRAY[]::TEXT[])
    FROM api.external_identities e
    LEFT JOIN api.user_profiles p ON p.id = e.user_id
    LEFT JOIN api.org_unit_members m ON m.user_id = e.user_id AND m.identity_source = p_identity_source
    LEFT JOIN api.org_units ou ON ou.id = m.org_unit_id
    WHERE e.source = p_link_source
    ORDER BY e.external_id;
$$;

REVOKE ALL ON FUNCTION api.list_identity_source_sync_users(TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api.list_identity_source_sync_users(TEXT, TEXT) TO service_role;
