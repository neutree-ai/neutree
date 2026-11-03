-- NEU-827: a user profile's name never changes, whoever writes it.
--
-- The name is what links a profile to its role assignments, to username login
-- and, for the built-in admin, to the delete protection, which matches the
-- literal name 'admin'. Until now only a user renaming themself was refused,
-- so a user admin could rename admin and then delete it.
--
-- The system never renames a profile either: the name is chosen when
-- api.handle_new_user inserts the profile, and every later system write sends
-- the stored name back.
BEGIN;

CREATE OR REPLACE FUNCTION api.validate_user_profile_name_immutable()
RETURNS TRIGGER
AS $$
BEGIN
    IF (NEW.metadata).name IS DISTINCT FROM (OLD.metadata).name THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10265","message": "metadata.name of a user cannot be changed","hint": "Create a new user with the new name instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_profile_name_immutable
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_user_profile_name_immutable();

COMMIT;
