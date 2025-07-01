-- ----------------------
-- Remove system permissions from permission_action enum
-- ----------------------

UPDATE api.roles 
SET spec = ROW(
    (spec).preset_key, 
    array_remove((spec).permissions, 'system:admin'::api.permission_action)
)::api.role_spec
WHERE (metadata).name = 'admin';
