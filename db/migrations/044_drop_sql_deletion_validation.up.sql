-- Drop SQL-level deletion validation (migrated to application layer)
-- The deletion validation logic has been moved to middleware in the application layer
-- for better control and consistency across all resources

DROP TRIGGER IF EXISTS prevent_user_profile_deletion ON api.user_profiles;
DROP FUNCTION IF EXISTS prevent_user_profile_deletion_with_assignments();

DROP TRIGGER IF EXISTS prevent_role_deletion ON api.roles;
DROP FUNCTION IF EXISTS prevent_role_deletion_with_assignments();

DROP TRIGGER IF EXISTS prevent_model_registry_deletion ON api.model_registries;
DROP FUNCTION IF EXISTS prevent_model_registry_deletion_with_endpoints();

DROP TRIGGER IF EXISTS prevent_image_registry_deletion ON api.image_registries;
DROP FUNCTION IF EXISTS prevent_image_registry_deletion_with_clusters();

DROP TRIGGER IF EXISTS prevent_cluster_deletion ON api.clusters;
DROP FUNCTION IF EXISTS prevent_cluster_deletion_with_endpoints();

DROP TRIGGER IF EXISTS prevent_workspace_deletion ON api.workspaces;
DROP FUNCTION IF EXISTS prevent_workspace_deletion_with_dependencies();
