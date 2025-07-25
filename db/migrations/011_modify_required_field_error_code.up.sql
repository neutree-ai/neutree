-- Drop user_profiles name validation triggers
DROP TRIGGER IF EXISTS validate_name_on_user_profiles ON api.user_profiles;

-- ----------------------
-- Create validation function for required user_profiles metadata.name
-- ----------------------
CREATE OR REPLACE FUNCTION api.validate_user_profiles_metadata_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.metadata).name IS NULL OR (NEW.metadata).name = '' THEN
        RAISE sqlstate 'PT400' USING
            message = 'metadata.name is required for the resource',
            hint = 'Please provide a valid name in the metadata object';
    END IF;

    -- Validate name format (Kubernetes-style naming convention)
    IF NOT (NEW.metadata).name ~ '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PT400' USING
            message = 'Invalid metadata.name format',
            hint = 'Name must consist of lowercase alphanumeric characters, ''-'' or ''.'', must start and end with an alphanumeric character';
    END IF;

    -- Validate maximum length
    IF length((NEW.metadata).name) > 63 THEN
        RAISE sqlstate 'PT400' USING
            message = 'metadata.name is too long',
            hint = 'Name cannot exceed 63 characters';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Add triggers user_profies tables to validate metadata.name
CREATE TRIGGER validate_name_on_user_profiles
    BEFORE INSERT OR UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_user_profiles_metadata_name();