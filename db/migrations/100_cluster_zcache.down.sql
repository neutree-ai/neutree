DROP TRIGGER IF EXISTS validate_cluster_zcache ON api.clusters;
DROP FUNCTION IF EXISTS api.validate_cluster_zcache();
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS zcache;
ALTER TYPE api.cluster_spec DROP ATTRIBUTE IF EXISTS zcache;
