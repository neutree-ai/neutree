-- ----------------------
-- Seed default roles
-- ----------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'admin') THEN
        INSERT INTO api.roles (api_version, kind, metadata, spec)
        VALUES (
            'v1',
            'Role',
            ROW('admin', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
            ROW('admin'::api.role_preset, ARRAY[]::api.permission_action[])::api.role_spec
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'workspace_user') THEN
        INSERT INTO api.roles (api_version, kind, metadata, spec)
        VALUES (
            'v1',
            'Role',
            ROW('workspace_user', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json)::api.metadata,
            ROW('workspace_user'::api.role_preset, ARRAY[]::api.permission_action[])::api.role_spec
        );
    END IF;

    PERFORM api.update_admin_permissions();
END
$$;

-- ----------------------
-- Seed default users
-- ----------------------
DO $$
DECLARE
    admin_user_id UUID;
    uuid UUID;
    random_password TEXT;
BEGIN
    SELECT encode(gen_random_bytes(8), 'hex') INTO random_password;
    SELECT gen_random_uuid() INTO uuid;

    -- Create admin user if not exists
    IF NOT EXISTS (SELECT 1 FROM auth.users WHERE email = 'admin@neutree.local') THEN
        -- Insert into GoTrue users table
        -- Using pgcrypto extension functions for password hashing
        INSERT INTO auth.users (
            instance_id,
            id,
            aud,
            role,
            email,
            encrypted_password,
            email_confirmed_at,
            invited_at,
            confirmation_token,
            confirmation_sent_at,
            recovery_token,
            recovery_sent_at,
            email_change_token_new,
            email_change,
            email_change_sent_at,
            last_sign_in_at,
            raw_app_meta_data,
            raw_user_meta_data,
            is_super_admin,
            created_at,
            updated_at
        ) VALUES (
            '00000000-0000-0000-0000-000000000000',
            uuid,
            '',
            'api_user',
            'admin@neutree.local',
            crypt(random_password, gen_salt('bf', 10)),
            CURRENT_TIMESTAMP,
            NULL,
            '',
            NULL,
            '',
            NULL,
            '',
            '',
            NULL,
            NULL,
            jsonb_build_object(
                'provider', 'email',
                'providers', ARRAY['email']
            ),
            jsonb_build_object(
                'sub', uuid,
                'username', 'admin',
                'email', 'admin@neutree.local',
                'email_verified', TRUE,
                'phone_verified', FALSE
            ),
            NULL,
            CURRENT_TIMESTAMP,
            CURRENT_TIMESTAMP
        )
        RETURNING id INTO admin_user_id;
        
        -- Assign admin role globally
        INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
        VALUES (
            'v1',
            'RoleAssignment',
            ROW(
                'admin-global-role-assignment',
                NULL,
                NULL,
                NULL,
                CURRENT_TIMESTAMP,
                CURRENT_TIMESTAMP,
                '{}'::json
            )::api.metadata,
            ROW(
                admin_user_id,
                NULL,        -- No specific workspace (global assignment)
                TRUE,        -- Global flag set to true
                'admin'      -- Role name
            )::api.role_assignment_spec
        );
        
        -- Print the generated credentials
        RAISE NOTICE 'Created admin user: admin@neutree.local with password: %', random_password;
    ELSE
        RAISE NOTICE 'Admin user already exists';
    END IF;
END
$$;
