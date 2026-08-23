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
    CHECK ((metadata).workspace IS NULL),
    CHECK (
        NULLIF(BTRIM((spec).default_cluster_version), '') IS NOT NULL
    ),
    CHECK (
        COALESCE(jsonb_typeof((spec).compatible_cluster_baselines) = 'array', FALSE)
    )
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

ALTER TABLE api.cluster_profiles
    ADD CONSTRAINT cluster_profiles_components_check
    CHECK (
        jsonb_typeof(spec) = 'object'
        AND spec ? 'components'
        AND jsonb_typeof(spec->'components') = 'object'
        AND jsonb_object_length(spec->'components') = 2
        AND spec->'components' ? 'ssh'
        AND spec->'components' ? 'kubernetes'
        AND jsonb_typeof(spec->'components'->'ssh') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes') = 'object'
        AND (spec->'components'->'ssh') ?& ARRAY['ray_runtime', 'node_agent', 'node_exporter', 'vmagent']
        AND jsonb_typeof(spec->'components'->'ssh'->'ray_runtime') = 'object'
        AND jsonb_typeof(spec->'components'->'ssh'->'ray_runtime'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'ssh'->'ray_runtime'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'ray_runtime'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'ray_runtime'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'ssh'->'node_agent') = 'object'
        AND jsonb_typeof(spec->'components'->'ssh'->'node_agent'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'ssh'->'node_agent'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'node_agent'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'node_agent'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'ssh'->'node_exporter') = 'object'
        AND jsonb_typeof(spec->'components'->'ssh'->'node_exporter'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'ssh'->'node_exporter'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'node_exporter'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'node_exporter'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'ssh'->'vmagent') = 'object'
        AND jsonb_typeof(spec->'components'->'ssh'->'vmagent'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'ssh'->'vmagent'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'vmagent'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'ssh'->'vmagent'->>'tag'), '') IS NOT NULL
        AND (spec->'components'->'kubernetes') ?& ARRAY['kubernetes_runtime', 'router', 'node_agent', 'node_exporter', 'vmagent', 'kube_state_metrics']
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kubernetes_runtime') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kubernetes_runtime'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kubernetes_runtime'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'kubernetes_runtime'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'kubernetes_runtime'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'kubernetes'->'router') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'router'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'router'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'router'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'router'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_agent') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_agent'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_agent'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'node_agent'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'node_agent'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_exporter') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_exporter'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'node_exporter'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'node_exporter'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'node_exporter'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'kubernetes'->'vmagent') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'vmagent'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'vmagent'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'vmagent'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'vmagent'->>'tag'), '') IS NOT NULL
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kube_state_metrics') = 'object'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kube_state_metrics'->'image') = 'string'
        AND jsonb_typeof(spec->'components'->'kubernetes'->'kube_state_metrics'->'tag') = 'string'
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'kube_state_metrics'->>'image'), '') IS NOT NULL
        AND NULLIF(BTRIM(spec->'components'->'kubernetes'->'kube_state_metrics'->>'tag'), '') IS NOT NULL
    );

CREATE UNIQUE INDEX cluster_profiles_version_unique_idx
    ON api.cluster_profiles (((metadata).name));

-- ClusterProfile has no user-facing REST resource. The control plane uses the
-- service_role (BYPASSRLS); ordinary API users cannot read or write it.
ALTER TABLE api.cluster_profiles ENABLE ROW LEVEL SECURITY;
