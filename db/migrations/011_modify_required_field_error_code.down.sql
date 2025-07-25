-- Drop user_profiles validation triggers
DROP TRIGGER IF EXISTS validate_name_on_user_profiles ON api.user_profiles;

-- Drop user_profiles validation function
DROP FUNCTION IF EXISTS api.validate_user_profiles_metadata_name();

-- Add triggers user_profies tables to validate metadata.name
CREATE TRIGGER validate_name_on_user_profiles
    BEFORE INSERT OR UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();