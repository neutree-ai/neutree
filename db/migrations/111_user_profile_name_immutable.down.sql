BEGIN;

DROP TRIGGER IF EXISTS user_profile_name_immutable ON api.user_profiles;
DROP FUNCTION IF EXISTS api.validate_user_profile_name_immutable();

COMMIT;
