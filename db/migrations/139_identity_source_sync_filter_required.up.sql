-- With sync enabled, require org_unit_base_dn or group_base_dn: both empty syncs nothing.

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
    v_old_ldap_sync api.identity_source_ldap_sync_spec;
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
        -- 0 or NULL means the default.
        IF v_sync.interval <> 0 AND v_sync.interval < 60 THEN
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

        IF v_ldap_sync.page_size < 0 THEN
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

        -- Only a write that enables sync or changes its base DNs is checked, so
        -- rows saved before this rule, and status or requested_at writes to them, still work.
        IF COALESCE(v_sync.enabled, FALSE)
            AND v_ldap_sync.org_unit_base_dn IS NULL AND v_ldap_sync.group_base_dn IS NULL THEN
            IF TG_OP = 'UPDATE' AND (OLD.spec).type = 'ldap' AND NOT ((OLD.spec).ldap IS NULL) THEN
                v_old_ldap_sync := ((OLD.spec).ldap).sync;
            END IF;

            IF TG_OP = 'INSERT' OR NOT COALESCE(((OLD.spec).sync).enabled, FALSE)
                OR (NOT (v_old_ldap_sync IS NULL)
                AND (NULLIF(btrim(v_old_ldap_sync.org_unit_base_dn), '') IS NOT NULL
                     OR NULLIF(btrim(v_old_ldap_sync.group_base_dn), '') IS NOT NULL)) THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10330","message": "spec.ldap.sync needs org_unit_base_dn or group_base_dn","hint": "With sync enabled, set at least one of the department base DN and the team base DN; otherwise nothing is synced"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;
        END IF;

        v_ldap.sync := v_ldap_sync;
        v_spec.ldap := v_ldap;
    END IF;

    NEW.spec := v_spec;

    RETURN NEW;
END;
$$;
