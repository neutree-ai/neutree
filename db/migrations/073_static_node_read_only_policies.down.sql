-- StaticNodeCluster and StaticNode are derived controller-owned resources.
-- Migration 072 creates only read policies for them; this migration is a
-- defensive cleanup for write policies that may exist in partially upgraded
-- environments. The rollback intentionally does not recreate write policies,
-- otherwise rolling back 073 would make these derived resources user-writable.

DROP POLICY IF EXISTS "static_node_cluster create policy" ON api.static_node_clusters;
DROP POLICY IF EXISTS "static_node_cluster update policy" ON api.static_node_clusters;
DROP POLICY IF EXISTS "static_node_cluster delete policy" ON api.static_node_clusters;

DROP POLICY IF EXISTS "static_node create policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static_node update policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static_node delete policy" ON api.static_nodes;
