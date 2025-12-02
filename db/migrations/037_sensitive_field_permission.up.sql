-- Add read-credentials permission for all resources that contain sensitive credentials
-- These permissions allow reading sensitive fields like passwords, tokens, kubeconfig, etc.

-- Core resources with credentials
ALTER TYPE api.permission_action ADD VALUE 'image_registry:read-credentials';
ALTER TYPE api.permission_action ADD VALUE 'model_registry:read-credentials';
ALTER TYPE api.permission_action ADD VALUE 'cluster:read-credentials';