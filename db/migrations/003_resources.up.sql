-- ----------------------
-- Resource: Endpoint
-- ----------------------
CREATE TYPE api.model_spec AS (
    registry TEXT,
    name TEXT,
    file TEXT,
    version TEXT
);

CREATE TYPE api.container_spec AS (
    engine TEXT,
    version TEXT
);

CREATE TYPE api.resource_spec AS (
    cpu FLOAT,
    gpu FLOAT,
    accelerator json,
    memory FLOAT
);

CREATE TYPE api.endpoint_spec AS (
    cluster TEXT,
    model api.model_spec,
    container api.container_spec,
    resources api.resource_spec,
    replicas INTEGER,
    variables json
);

CREATE TYPE api.endpoint_status AS (
    phase TEXT,
    service_url TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.endpoints (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.endpoint_spec,
    status api.endpoint_status
);

CREATE TRIGGER update_endpoints_update_timestamp
    BEFORE UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_endpoints_default_timestamp
    BEFORE INSERT ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX endpoints_name_workspace_unique_idx ON api.endpoints (((metadata).workspace), ((metadata).name));

ALTER TABLE api.endpoints ENABLE ROW LEVEL SECURITY;

CREATE POLICY "endpoint read policy" ON api.endpoints
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'endpoint:read', (metadata).workspace)
    );

CREATE POLICY "endpoint create policy" ON api.endpoints
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'endpoint:create', (metadata).workspace)
    );

CREATE POLICY "endpoint update policy" ON api.endpoints
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'endpoint:update', (metadata).workspace)
    );

CREATE POLICY "endpoint delete policy" ON api.endpoints
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'endpoint:delete', (metadata).workspace)
    );

-- ----------------------
-- Resource: Image Registry
-- ----------------------
CREATE TYPE api.image_registry_spec AS (
    url TEXT,
    repository TEXT,
    authconfig json,
    ca TEXT
);

CREATE TYPE api.image_registry_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.image_registries (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.image_registry_spec,
    status api.image_registry_status
);

CREATE TRIGGER update_image_registries_update_timestamp
    BEFORE UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_image_registries_default_timestamp
    BEFORE INSERT ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX image_registries_name_workspace_unique_idx ON api.image_registries (((metadata).workspace), ((metadata).name));

ALTER TABLE api.image_registries ENABLE ROW LEVEL SECURITY;

CREATE POLICY "image_registry read policy" ON api.image_registries
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'image_registry:read', (metadata).workspace)
    );

CREATE POLICY "image_registry create policy" ON api.image_registries
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'image_registry:create', (metadata).workspace)
    );

CREATE POLICY "image_registry update policy" ON api.image_registries
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'image_registry:update', (metadata).workspace)
    );

CREATE POLICY "image_registry delete policy" ON api.image_registries
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'image_registry:delete', (metadata).workspace)
    );

-- ----------------------
-- Resource: Model Registry
-- ----------------------
CREATE TYPE api.model_registry_spec AS (
    type TEXT,
    url TEXT,
    credentials TEXT
);

CREATE TYPE api.model_registry_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.model_registries (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.model_registry_spec,
    status api.model_registry_status
);

CREATE TRIGGER update_model_registries_update_timestamp
    BEFORE UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_model_registries_default_timestamp
    BEFORE INSERT ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX model_registries_name_workspace_unique_idx ON api.model_registries (((metadata).workspace), ((metadata).name));

ALTER TABLE api.model_registries ENABLE ROW LEVEL SECURITY;

CREATE POLICY "model_registry read policy" ON api.model_registries
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'model_registry:read', (metadata).workspace)
    );

CREATE POLICY "model_registry create policy" ON api.model_registries
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'model_registry:create', (metadata).workspace)
    );

CREATE POLICY "model_registry update policy" ON api.model_registries
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model_registry:update', (metadata).workspace)
    );

CREATE POLICY "model_registry delete policy" ON api.model_registries
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'model_registry:delete', (metadata).workspace)
    );

-- ----------------------
-- Resource: Engine
-- ----------------------
CREATE TYPE api.engine_version AS (
    version TEXT,
    values_schema json
);

CREATE TYPE api.engine_spec AS (
    versions api.engine_version[]
);

CREATE TYPE api.engine_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.engines (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.engine_spec,
    status api.engine_status
);

CREATE TRIGGER update_engines_update_timestamp
    BEFORE UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_engines_default_timestamp
    BEFORE INSERT ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX engines_name_workspace_unique_idx ON api.engines (((metadata).workspace), ((metadata).name));

ALTER TABLE api.engines ENABLE ROW LEVEL SECURITY;

CREATE POLICY "engine read policy" ON api.engines
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'engine:read', (metadata).workspace)
    );

CREATE POLICY "engine create policy" ON api.engines
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'engine:create', (metadata).workspace)
    );

CREATE POLICY "engine update policy" ON api.engines
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'engine:update', (metadata).workspace)
    );

CREATE POLICY "engine delete policy" ON api.engines
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'engine:delete', (metadata).workspace)
    );

-- ----------------------
-- Resource: Cluster
-- ----------------------
CREATE TYPE api.cluster_spec AS (
    type TEXT,
    config json,
    image_registry TEXT,
    version TEXT
);

CREATE TYPE api.cluster_status AS (
    phase TEXT,
    image TEXT,
    dashboard_url TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT,
    ready_nodes integer,
    desired_nodes integer,
    version TEXT,
    ray_version TEXT,
    initialized BOOLEAN,
    node_provision_status TEXT
);

CREATE TABLE api.clusters (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.cluster_spec,
    status api.cluster_status
);

CREATE TRIGGER update_clusters_update_timestamp
    BEFORE UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_clusters_default_timestamp
    BEFORE INSERT ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX clusters_name_workspace_unique_idx ON api.clusters (((metadata).workspace), ((metadata).name));

ALTER TABLE api.clusters ENABLE ROW LEVEL SECURITY;

CREATE POLICY "cluster read policy" ON api.clusters
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'cluster:read', (metadata).workspace)
    );

CREATE POLICY "cluster create policy" ON api.clusters
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'cluster:create', (metadata).workspace)
    );

CREATE POLICY "cluster update policy" ON api.clusters
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
    );

CREATE POLICY "cluster delete policy" ON api.clusters
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'cluster:delete', (metadata).workspace)
    );