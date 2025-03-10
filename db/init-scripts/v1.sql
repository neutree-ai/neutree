CREATE SCHEMA api;

---endpoint--

CREATE TYPE model_spec AS (
    registry TEXT,
    name TEXT,
    file TEXT,
    version TEXT
);

CREATE TYPE container_spec AS (
    engine TEXT,
    version TEXT
);

CREATE TYPE resource_spec AS (
    cpu FLOAT,
    gpu FLOAT,
    accelerator json,
    memory FLOAT
);

CREATE TYPE endpoint_spec AS (
    cluster TEXT,
    model model_spec,
    container container_spec,
    resources resource_spec,
    variables json
);

CREATE TYPE endpoint_status AS (
    phase TEXT,
    service_url TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TYPE metadata AS (
    name TEXT,
    deletion_timestamp TIMESTAMP,
    creation_timestamp TIMESTAMP,
    update_timestamp TIMESTAMP,
    labels json
);

CREATE TABLE api.endpoints (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata metadata,
    spec endpoint_spec,
    status endpoint_status
);

---image_registry---

CREATE TYPE image_registry_spec AS (
    url TEXT,
    repository TEXT,
    authconfig json,
    ca TEXT
);

CREATE TYPE image_registry_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.image_registries (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata metadata,
    spec image_registry_spec,
    status image_registry_status
);

---model_registry---

CREATE TYPE model_registry_spec AS (
    type TEXT,
    url TEXT,
    credentials TEXT
);

CREATE TYPE model_registry_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.model_registries (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata metadata,
    spec model_registry_spec,
    status model_registry_status
);

---engine---

CREATE TYPE engine_version AS (
    version TEXT,
    values_schema json
);

CREATE TYPE engine_spec AS (
    versions engine_version[]
);

CREATE TYPE engine_status AS (
    phase TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.engines (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata metadata,
    spec engine_spec,
    status engine_status
);

---cluster---

CREATE TYPE cluster_spec AS (
    type TEXT,
    config json,
    image_registry TEXT
);

CREATE TYPE cluster_status AS (
    phase TEXT,
    image TEXT,
    dashboard_url TEXT,
    last_transition_time TIMESTAMP,
    error_message TEXT
);

CREATE TABLE api.clusters (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata metadata,
    spec cluster_spec,
    status cluster_status
);

---auth---

CREATE ROLE web_anon nologin;

GRANT USAGE ON SCHEMA api TO web_anon;
GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA api TO web_anon;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA api TO web_anon;

ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT SELECT ON TABLES TO web_anon;
ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT SELECT ON SEQUENCES TO web_anon;

---function--
CREATE OR REPLACE FUNCTION update_metadata_update_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW((NEW.metadata).name,(NEW.metadata).deletion_timestamp,(NEW.metadata).creation_timestamp,CURRENT_TIMESTAMP,(NEW.metadata).labels);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION set_default_metadata_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW((NEW.metadata).name,(NEW.metadata).deletion_timestamp,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,(NEW.metadata).labels);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

---trigger---
---image_registries---
CREATE TRIGGER update_image_registries_update_timestamp
    BEFORE UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
CREATE TRIGGER set_image_registries_default_timestamp
    BEFORE INSERT ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

---endpoints---
CREATE TRIGGER update_endpoints_update_timestamp
    BEFORE UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
CREATE TRIGGER set_endpoints_default_timestamp
    BEFORE INSERT ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

---model_registries---
CREATE TRIGGER update_model_registries_update_timestamp
    BEFORE UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
CREATE TRIGGER set_model_registries_default_timestamp
    BEFORE INSERT ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

---engines---
CREATE TRIGGER update_engines_update_timestamp
    BEFORE UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
CREATE TRIGGER set_engines_default_timestamp
    BEFORE INSERT ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

---clusters---
CREATE TRIGGER update_clusters_update_timestamp
    BEFORE UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
CREATE TRIGGER set_clusters_default_timestamp
    BEFORE INSERT ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();