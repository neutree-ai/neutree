DROP TRIGGER IF EXISTS validate_cluster_cache ON api.clusters;
DROP FUNCTION IF EXISTS api.validate_cluster_cache();
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS cache;
ALTER TYPE api.cluster_spec DROP ATTRIBUTE IF EXISTS cache;
