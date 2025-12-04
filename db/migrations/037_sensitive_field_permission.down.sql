UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            (spec).permissions,
            'image_registry:read-credentials'::api.permission_action
        ),
        'model_registry:read-credentials'::api.permission_action
    ),
    'cluster:read-credentials'::api.permission_action
)::api.role_spec
WHERE (metadata).name = 'admin';