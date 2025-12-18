-- Add model-level permissions for fine-grained access control
-- These permissions control operations on individual models within model registries
-- Naming follows container registry conventions (push/pull)

-- model:read - List and view model details
ALTER TYPE api.permission_action ADD VALUE 'model:read';

-- model:push - Push/upload new models or update existing model versions
ALTER TYPE api.permission_action ADD VALUE 'model:push';

-- model:pull - Pull/download model files
ALTER TYPE api.permission_action ADD VALUE 'model:pull';

-- model:delete - Physically delete models from registry
ALTER TYPE api.permission_action ADD VALUE 'model:delete';
