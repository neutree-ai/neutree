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
