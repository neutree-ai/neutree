-- ----------------------
-- Resource: StaticNodeCluster / StaticNode (v1)
-- ----------------------

CREATE TYPE api.static_node_cluster_spec AS (
    version TEXT,
    image_registry TEXT,
    nodes JSONB,
    upgrade_strategy JSONB
);

CREATE TYPE api.static_node_cluster_status AS (
    phase TEXT,
    desired_nodes INTEGER,
    ready_nodes INTEGER,
    head_ready BOOLEAN,
    warm_ready BOOLEAN,
    version TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

CREATE TYPE api.static_node_spec AS (
    cluster TEXT,
    ip TEXT,
    role TEXT,
    ssh_auth JSONB,
    warm JSONB,
    components JSONB
);

CREATE TYPE api.static_node_status AS (
    phase TEXT,
    accelerator JSONB,
    warm JSONB,
    components JSONB,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

CREATE TABLE api.static_node_clusters (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.static_node_cluster_spec,
    status api.static_node_cluster_status
);

CREATE TRIGGER update_static_node_clusters_update_timestamp
    BEFORE UPDATE ON api.static_node_clusters
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_static_node_clusters_default_timestamp
    BEFORE INSERT ON api.static_node_clusters
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX static_node_clusters_name_workspace_unique_idx
    ON api.static_node_clusters (((metadata).workspace), ((metadata).name));

CREATE INDEX static_node_clusters_workspace_phase_idx
    ON api.static_node_clusters (((metadata).workspace), ((status).phase));

-- Static node resources are controller-owned internal tables. Do not create
-- user RLS policies here; ordinary API users cannot read or write them
-- directly. The control plane uses service_role, which is created with
-- BYPASSRLS in db/init-scripts/000_init.sql.
ALTER TABLE api.static_node_clusters ENABLE ROW LEVEL SECURITY;

CREATE TABLE api.static_nodes (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.static_node_spec,
    status api.static_node_status
);

CREATE TRIGGER update_static_nodes_update_timestamp
    BEFORE UPDATE ON api.static_nodes
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_static_nodes_default_timestamp
    BEFORE INSERT ON api.static_nodes
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX static_nodes_name_workspace_unique_idx
    ON api.static_nodes (((metadata).workspace), ((metadata).name));

CREATE INDEX static_nodes_cluster_workspace_idx
    ON api.static_nodes (((metadata).workspace), ((spec).cluster));

CREATE INDEX static_nodes_cluster_role_phase_idx
    ON api.static_nodes (((metadata).workspace), ((spec).cluster), ((spec).role), ((status).phase));

-- See static_node_clusters above for the access model.
ALTER TABLE api.static_nodes ENABLE ROW LEVEL SECURITY;
