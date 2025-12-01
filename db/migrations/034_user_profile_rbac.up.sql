SELECT api.update_admin_permissions();

DROP POLICY IF EXISTS "Profiles are viewable by everyone" ON api.user_profiles;
DROP POLICY IF EXISTS "Users can update their own profile" ON api.user_profiles;

CREATE POLICY "user_profile read policy" ON api.user_profiles
    FOR SELECT
    USING (
        id = auth.uid()
        OR
        api.has_permission(auth.uid(), 'user_profile:read', NULL)
    );

CREATE POLICY "user_profile update policy" ON api.user_profiles
    FOR UPDATE
    USING (
        id = auth.uid()
        OR
        api.has_permission(auth.uid(), 'user_profile:update', NULL)
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND id != auth.uid()
        )
    )
    WITH CHECK (
        (
            id = auth.uid()
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND id != auth.uid()
        )
    );

CREATE POLICY "user_profile delete policy" ON api.user_profiles
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'user_profile:delete', NULL)
        AND id != auth.uid()
    );

CREATE OR REPLACE FUNCTION api.validate_user_profile_soft_delete()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
BEGIN
    IF (OLD.metadata).deletion_timestamp IS NULL
       AND (NEW.metadata).deletion_timestamp IS NOT NULL THEN

        IF NEW.id = auth.uid() THEN
            RAISE EXCEPTION 'Cannot delete your own user profile';
        END IF;

        IF to_jsonb(NEW.spec) IS DISTINCT FROM to_jsonb(OLD.spec) THEN
            RAISE EXCEPTION 'Cannot modify spec during soft delete operation';
        END IF;
        IF to_jsonb(NEW.status) IS DISTINCT FROM to_jsonb(OLD.status) THEN
            RAISE EXCEPTION 'Cannot modify status during soft delete operation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_profile_soft_delete_validation
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_user_profile_soft_delete();

CREATE OR REPLACE FUNCTION api.validate_user_profile_self_update()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
BEGIN
    IF NEW.id = auth.uid() AND OLD.id = auth.uid() THEN
        IF (NEW.metadata).name IS DISTINCT FROM (OLD.metadata).name THEN
            RAISE EXCEPTION 'Cannot modify username (metadata.name) yourself';
        END IF;

        IF (NEW.metadata).workspace IS DISTINCT FROM (OLD.metadata).workspace THEN
            RAISE EXCEPTION 'Cannot modify workspace yourself';
        END IF;

        IF to_jsonb(NEW.status) IS DISTINCT FROM to_jsonb(OLD.status) THEN
            RAISE EXCEPTION 'Cannot modify status yourself';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_profile_self_update_validation
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_user_profile_self_update();
