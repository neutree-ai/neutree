-- NEU-605: control-plane-owned compatibility metadata and exact-version
-- component profiles. Both resources are global internal state.
CREATE TYPE api.release_info_spec AS (
    default_cluster_version TEXT,
    compatible_cluster_baselines JSONB
);

CREATE TABLE api.release_infos (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata NOT NULL,
    spec api.release_info_spec NOT NULL,
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

CREATE TABLE api.cluster_profiles (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata NOT NULL,
    spec JSONB NOT NULL,
    CHECK ((metadata).workspace IS NULL)
);

CREATE TRIGGER update_cluster_profiles_update_timestamp
    BEFORE UPDATE ON api.cluster_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_cluster_profiles_default_timestamp
    BEFORE INSERT ON api.cluster_profiles
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_cluster_profiles
    BEFORE INSERT OR UPDATE ON api.cluster_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE UNIQUE INDEX cluster_profiles_version_unique_idx
    ON api.cluster_profiles (((metadata).name));

-- ClusterProfile has no user-facing REST resource. The control plane uses the
-- service_role (BYPASSRLS); ordinary API users cannot read or write it.
ALTER TABLE api.cluster_profiles ENABLE ROW LEVEL SECURITY;
