-- Drop the endpoint workspace validation trigger
DROP TRIGGER IF EXISTS validate_workspace_on_endpoints ON api.endpoints;

-- Drop all the name validation triggers
DROP TRIGGER IF EXISTS validate_name_on_workspaces ON api.workspaces;
DROP TRIGGER IF EXISTS validate_name_on_roles ON api.roles;
DROP TRIGGER IF EXISTS validate_name_on_role_assignments ON api.role_assignments;
DROP TRIGGER IF EXISTS validate_name_on_api_keys ON api.api_keys;
DROP TRIGGER IF EXISTS validate_name_on_endpoints ON api.endpoints;
DROP TRIGGER IF EXISTS validate_name_on_image_registries ON api.image_registries;
DROP TRIGGER IF EXISTS validate_name_on_model_registries ON api.model_registries;
DROP TRIGGER IF EXISTS validate_name_on_engines ON api.engines;
DROP TRIGGER IF EXISTS validate_name_on_clusters ON api.clusters;
DROP TRIGGER IF EXISTS validate_name_on_user_profiles ON api.user_profiles;
DROP TRIGGER IF EXISTS validate_name_on_api_daily_usage ON api.api_daily_usage;

-- Drop the workspace validation trigger
DROP TRIGGER IF EXISTS validate_workspace_on_clusters ON api.clusters;
DROP TRIGGER IF EXISTS validate_workspace_on_model_registries ON api.model_registries;
DROP TRIGGER IF EXISTS validate_workspace_on_image_registries ON api.image_registries;
DROP TRIGGER IF EXISTS validate_workspace_on_engines ON api.engines;
DROP TRIGGER IF EXISTS validate_workspace_on_endpoints ON api.endpoints;
DROP TRIGGER IF EXISTS validate_workspace_on_api_keys ON api.api_keys;

-- Drop the validation functions
DROP FUNCTION IF EXISTS api.validate_metadata_workspace();
DROP FUNCTION IF EXISTS api.validate_metadata_name();