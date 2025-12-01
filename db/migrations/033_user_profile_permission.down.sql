UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            array_remove(
                array_remove((spec).permissions, 'user_profile:read'::api.permission_action),
                'user_profile:create'::api.permission_action
            ),
            'user_profile:update'::api.permission_action
        ),
        'user_profile:delete'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'admin';
