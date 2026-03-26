-- Remove external_endpoint:read-credentials from admin role
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        (spec).permissions,
        'external_endpoint:read-credentials'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'admin';
