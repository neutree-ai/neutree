-- ----------------------
-- Create an anonymous user for anonymous access
-- ----------------------
CREATE ROLE anonymous NOLOGIN;

-- Configure query permissions for anonymous users in the oem_configs table to ensure that the login page is displayed normally.
GRANT USAGE ON SCHEMA api TO anonymous;
GRANT SELECT ON api.oem_configs TO anonymous;