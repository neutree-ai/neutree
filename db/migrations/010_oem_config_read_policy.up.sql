-- Update OEM config read policy to allow access for login page
-- Drop the existing read policy
DROP POLICY "oem_config read policy" ON api.oem_configs;

-- Create new read policy that allows all users to read OEM configs
CREATE POLICY "oem_config read policy" ON api.oem_configs
    FOR SELECT
    USING (TRUE);
