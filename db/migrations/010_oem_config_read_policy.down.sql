-- Rollback OEM config read policy to original state
-- Drop the current read policy
DROP POLICY "oem_config read policy" ON api.oem_configs;

-- Restore original read policy that requires api_user role
CREATE POLICY "oem_config read policy" ON api.oem_configs
    FOR SELECT
    USING (auth.role() = 'api_user');
