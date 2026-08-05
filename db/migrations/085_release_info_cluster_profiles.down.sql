DROP TRIGGER IF EXISTS update_cluster_profiles_update_timestamp ON api.cluster_profiles;
DROP TRIGGER IF EXISTS set_cluster_profiles_default_timestamp ON api.cluster_profiles;
DROP TRIGGER IF EXISTS validate_name_on_cluster_profiles ON api.cluster_profiles;

DROP INDEX IF EXISTS api.cluster_profiles_name_unique_idx;
DROP TABLE IF EXISTS api.cluster_profiles;

ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS compatible_cluster_baselines;
