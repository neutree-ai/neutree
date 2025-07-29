-- Revoke the anonymous user's query permission on the oem_configs table.
REVOKE SELECT ON api.oem_configs FROM anonymous;
REVOKE USAGE ON SCHEMA api FROM anonymous;

-- Remove the anonymous role.
DROP ROLE IF EXISTS anonymous;