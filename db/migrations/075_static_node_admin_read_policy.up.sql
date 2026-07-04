-- Grant static node read permissions to the built-in admin role only.
-- Separate migration because newly added enum values cannot be referenced in
-- the same transaction that adds them.
SELECT api.update_admin_permissions();

CREATE POLICY "static node cluster read policy" ON api.static_node_clusters
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'static_node_cluster:read', (metadata).workspace)
    );

CREATE POLICY "static node read policy" ON api.static_nodes
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'static_node:read', (metadata).workspace)
    );
