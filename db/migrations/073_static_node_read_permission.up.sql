-- Add read permissions for controller-owned static node resources.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'static_node_cluster:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'static_node:read';
