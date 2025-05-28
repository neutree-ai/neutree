-- Remove model_catalog permissions from admin role
UPDATE api.roles 
SET spec = ROW(
    (spec).preset_key, 
    array_remove(
        array_remove(
            array_remove(
                array_remove((spec).permissions, 'model_catalog:read'::api.permission_action),
                'model_catalog:create'::api.permission_action
            ),
            'model_catalog:update'::api.permission_action
        ),
        'model_catalog:delete'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'admin';
