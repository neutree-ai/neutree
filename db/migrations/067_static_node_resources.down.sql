DROP POLICY IF EXISTS "static_node delete policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static_node update policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static_node create policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static_node read policy" ON api.static_nodes;
DROP TABLE IF EXISTS api.static_nodes;

DROP POLICY IF EXISTS "static_node_cluster delete policy" ON api.static_node_clusters;
DROP POLICY IF EXISTS "static_node_cluster update policy" ON api.static_node_clusters;
DROP POLICY IF EXISTS "static_node_cluster create policy" ON api.static_node_clusters;
DROP POLICY IF EXISTS "static_node_cluster read policy" ON api.static_node_clusters;
DROP TABLE IF EXISTS api.static_node_clusters;
