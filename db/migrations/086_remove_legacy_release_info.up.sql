-- NEU-605: ReleaseInfo is immutable compatibility metadata. Exact component
-- images live only in ClusterProfile; no controller snapshot is retained.
DROP TABLE IF EXISTS api.cluster_upgrade_snapshots;

ALTER TYPE api.static_node_cluster_spec DROP ATTRIBUTE IF EXISTS components;
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS release_compatibility;
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS release_info;

ALTER TABLE api.release_infos DROP COLUMN IF EXISTS status;
DROP TYPE IF EXISTS api.release_info_status;

ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS cluster_versions;
ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS build_identity;
ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS channel;
