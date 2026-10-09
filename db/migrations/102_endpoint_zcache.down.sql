DROP TRIGGER guard_cluster_zcache_dependencies ON api.clusters;
DROP FUNCTION api.guard_cluster_zcache_dependencies();
DROP TRIGGER guard_endpoint_zcache ON api.endpoints;
DROP FUNCTION api.guard_endpoint_zcache();
ALTER TYPE api.endpoint_status DROP ATTRIBUTE zcache;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE zcache;
