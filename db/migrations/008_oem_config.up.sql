-- ----------------------
-- Add system permissions to permission_action enum
-- ----------------------

ALTER TYPE api.permission_action
  ADD VALUE IF NOT EXISTS 'system:admin';