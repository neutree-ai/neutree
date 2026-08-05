-- NEU-605: retain the resolved ReleaseInfo component image snapshot with each
-- StaticNodeCluster so controller restarts do not reinterpret historic releases.
ALTER TYPE api.static_node_cluster_spec ADD ATTRIBUTE components JSONB;
