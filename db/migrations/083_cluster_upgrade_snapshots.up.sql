-- NEU-605: freeze the ReleaseInfo inputs of an in-flight Cluster upgrade.
-- This is controller-only state; user roles have no RLS policy on this table.
CREATE TABLE api.cluster_upgrade_snapshots (
    cluster_id INTEGER PRIMARY KEY REFERENCES api.clusters(id) ON DELETE CASCADE,
    source_cluster_version TEXT NOT NULL,
    target_cluster_version TEXT NOT NULL,
    source_release_info JSONB,
    target_release_info JSONB NOT NULL,
    allowed_edge JSONB NOT NULL,
    components JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE api.cluster_upgrade_snapshots ENABLE ROW LEVEL SECURITY;
