-- Restore the schema shape expected by migrations 081--085. Historic values
-- discarded by the forward migration are intentionally not reconstructed.
ALTER TYPE api.release_info_spec ADD ATTRIBUTE channel TEXT;
ALTER TYPE api.release_info_spec ADD ATTRIBUTE build_identity TEXT;
ALTER TYPE api.release_info_spec ADD ATTRIBUTE cluster_versions JSONB;

CREATE TYPE api.release_info_status AS (
    revision TEXT
);

ALTER TABLE api.release_infos
    ADD COLUMN status api.release_info_status NOT NULL DEFAULT ROW(NULL)::api.release_info_status;
ALTER TABLE api.release_infos ALTER COLUMN status DROP DEFAULT;

ALTER TYPE api.cluster_status ADD ATTRIBUTE release_info JSONB;
ALTER TYPE api.cluster_status ADD ATTRIBUTE release_compatibility JSONB;
ALTER TYPE api.static_node_cluster_spec ADD ATTRIBUTE components JSONB;

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
