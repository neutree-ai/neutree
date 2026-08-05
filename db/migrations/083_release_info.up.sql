-- NEU-605: control-plane-owned, global release compatibility matrix.
CREATE TYPE api.release_info_spec AS (
    channel TEXT,
    build_identity TEXT,
    cluster_versions JSONB
);

CREATE TYPE api.release_info_status AS (
    revision TEXT
);

CREATE TABLE api.release_infos (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata NOT NULL,
    spec api.release_info_spec NOT NULL,
    status api.release_info_status NOT NULL,
    CHECK ((metadata).workspace IS NULL)
);

CREATE TRIGGER update_release_infos_update_timestamp
    BEFORE UPDATE ON api.release_infos
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_release_infos_default_timestamp
    BEFORE INSERT ON api.release_infos
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_release_infos
    BEFORE INSERT OR UPDATE ON api.release_infos
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE UNIQUE INDEX release_infos_name_unique_idx
    ON api.release_infos (((metadata).name));

-- ReleaseInfo has no user-facing REST resource. The control plane uses the
-- service_role (BYPASSRLS); ordinary API users cannot read or write it.
ALTER TABLE api.release_infos ENABLE ROW LEVEL SECURITY;
