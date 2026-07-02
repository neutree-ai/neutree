DROP POLICY IF EXISTS "static node read policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static node cluster read policy" ON api.static_node_clusters;

-- Keep admin role permissions consistent with recent enum-backed permission
-- migrations such as 061/065: enum values cannot be removed, and
-- api.update_admin_permissions() always re-aggregates all enum values.
