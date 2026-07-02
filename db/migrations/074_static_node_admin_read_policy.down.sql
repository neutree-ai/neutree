DROP POLICY IF EXISTS "static node read policy" ON api.static_nodes;
DROP POLICY IF EXISTS "static node cluster read policy" ON api.static_node_clusters;

UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            (spec).permissions,
            'static_node:read'::api.permission_action
        ),
        'static_node_cluster:read'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'admin';
