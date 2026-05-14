DROP INDEX IF EXISTS api.idx_port_alloc_cluster_role;
DROP INDEX IF EXISTS api.idx_port_alloc_endpoint;
DROP TABLE IF EXISTS api.cluster_port_allocations;
ALTER TYPE api.cluster_spec DROP ATTRIBUTE port_range;
