-- ----------------------
-- Remove model permissions from admin role
-- ----------------------
-- Note: PostgreSQL does not support removing enum values from an existing enum type.
-- This migration only removes the permissions from the admin role.
-- The enum values will remain in the database.

UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            array_remove(
                array_remove((spec).permissions, 'model:read'::api.permission_action),
                'model:push'::api.permission_action
            ),
            'model:pull'::api.permission_action
        ),
        'model:delete'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'admin';
