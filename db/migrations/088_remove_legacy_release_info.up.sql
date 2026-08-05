-- NEU-605: ReleaseInfo is immutable compatibility metadata. Exact component
-- images live only in ClusterProfile; no controller snapshot is retained.
DROP TABLE IF EXISTS api.cluster_upgrade_snapshots;

-- Keep rollback-only copies of fields that an 086-era schema no longer
-- exposes. The table is protected by RLS and is removed by the down migration.
CREATE TABLE api.release_info_086_legacy_backup (
    release_info_id INTEGER PRIMARY KEY REFERENCES api.release_infos(id) ON DELETE CASCADE,
    channel TEXT,
    build_identity TEXT,
    cluster_versions JSONB,
    status JSONB
);

INSERT INTO api.release_info_086_legacy_backup (
    release_info_id,
    channel,
    build_identity,
    cluster_versions,
    status
)
SELECT
    id,
    (spec).channel,
    (spec).build_identity,
    (spec).cluster_versions,
    to_jsonb(status)
FROM api.release_infos;

ALTER TABLE api.release_info_086_legacy_backup ENABLE ROW LEVEL SECURITY;

ALTER TYPE api.static_node_cluster_spec DROP ATTRIBUTE IF EXISTS components;
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS release_compatibility;
ALTER TYPE api.cluster_status DROP ATTRIBUTE IF EXISTS release_info;

ALTER TABLE api.release_infos DROP COLUMN IF EXISTS status;
DROP TYPE IF EXISTS api.release_info_status;

ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS cluster_versions;
ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS build_identity;
ALTER TYPE api.release_info_spec DROP ATTRIBUTE IF EXISTS channel;
