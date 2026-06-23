-- ----------------------
-- Resource: StaticNodeCluster / StaticNode (v1)
-- ----------------------

CREATE TABLE api.static_node_clusters (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec JSONB,
    status JSONB
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

ALTER TABLE api.static_node_clusters ENABLE ROW LEVEL SECURITY;

CREATE POLICY "static_node_cluster read policy" ON api.static_node_clusters
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'cluster:read', (metadata).workspace)
    );

CREATE POLICY "static_node_cluster create policy" ON api.static_node_clusters
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'cluster:create', (metadata).workspace)
    );

CREATE POLICY "static_node_cluster update policy" ON api.static_node_clusters
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
    );

CREATE POLICY "static_node_cluster delete policy" ON api.static_node_clusters
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'cluster:delete', (metadata).workspace)
    );

CREATE TABLE api.static_nodes (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec JSONB,
    status JSONB
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
    ON api.static_nodes (((metadata).workspace), ((spec->>'cluster')));

ALTER TABLE api.static_nodes ENABLE ROW LEVEL SECURITY;

CREATE POLICY "static_node read policy" ON api.static_nodes
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'cluster:read', (metadata).workspace)
    );

CREATE POLICY "static_node create policy" ON api.static_nodes
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'cluster:create', (metadata).workspace)
    );

CREATE POLICY "static_node update policy" ON api.static_nodes
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'cluster:update', (metadata).workspace)
    );

CREATE POLICY "static_node delete policy" ON api.static_nodes
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'cluster:delete', (metadata).workspace)
    );
