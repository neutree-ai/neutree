-- A downgrade must not produce ReleaseInfo rows that the pre-086 Core cannot
-- read. Rows created after the forward migration have no lossless old-matrix
-- representation, so fail rather than restore invalid legacy values.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api.release_infos AS release_info
        LEFT JOIN api.release_info_086_legacy_backup AS backup
            ON backup.release_info_id = release_info.id
        WHERE backup.release_info_id IS NULL
    ) THEN
        RAISE EXCEPTION
            'cannot roll back release info migration: a release info row has no legacy backup';
    END IF;
END $$;

ALTER TABLE api.release_infos
    ADD COLUMN release_info_086_legacy_channel TEXT,
    ADD COLUMN release_info_086_legacy_build_identity TEXT,
    ADD COLUMN release_info_086_legacy_cluster_versions JSONB;

UPDATE api.release_infos AS release_info
SET
    release_info_086_legacy_channel = backup.channel,
    release_info_086_legacy_build_identity = backup.build_identity,
    release_info_086_legacy_cluster_versions = backup.cluster_versions
FROM api.release_info_086_legacy_backup AS backup
WHERE backup.release_info_id = release_info.id;

-- Restore the schema shape expected by migrations 081--085. Recreate the
-- composite type rather than appending attributes: PostgreSQL preserves append
-- order, while the v085 positional contract is channel/build/versions/baselines.
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
        release_info_086_legacy_channel,
        release_info_086_legacy_build_identity,
        release_info_086_legacy_cluster_versions,
        spec -> 'compatible_cluster_baselines'
    )::api.release_info_spec;

ALTER TABLE api.release_infos
    DROP COLUMN release_info_086_legacy_channel,
    DROP COLUMN release_info_086_legacy_build_identity,
    DROP COLUMN release_info_086_legacy_cluster_versions;

CREATE TYPE api.release_info_status AS (
    revision TEXT
);

ALTER TABLE api.release_infos
    ADD COLUMN status api.release_info_status;

UPDATE api.release_infos AS release_info
SET status = COALESCE(
    jsonb_populate_record(NULL::api.release_info_status, backup.status),
    ROW(NULL)::api.release_info_status
)
FROM api.release_info_086_legacy_backup AS backup
WHERE backup.release_info_id = release_info.id;

ALTER TABLE api.release_infos ALTER COLUMN status SET NOT NULL;
DROP TABLE api.release_info_086_legacy_backup;

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
