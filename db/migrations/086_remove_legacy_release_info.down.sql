-- Restore the schema shape expected by migrations 081--085. Recreate the
-- composite type rather than appending attributes: PostgreSQL preserves append
-- order, while the v085 positional contract is channel/build/versions/baselines.
-- Historic values discarded by the forward migration are intentionally not
-- reconstructed.
ALTER TABLE api.release_infos
    ALTER COLUMN spec TYPE JSONB
    USING jsonb_build_object(
        'compatible_cluster_baselines',
        (spec).compatible_cluster_baselines
    );

DROP TYPE api.release_info_spec;

CREATE TYPE api.release_info_spec AS (
    channel TEXT,
    build_identity TEXT,
    cluster_versions JSONB,
    compatible_cluster_baselines JSONB
);

ALTER TABLE api.release_infos
    ALTER COLUMN spec TYPE api.release_info_spec
    USING ROW(
        NULL::TEXT,
        NULL::TEXT,
        NULL::JSONB,
        spec -> 'compatible_cluster_baselines'
    )::api.release_info_spec;

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
