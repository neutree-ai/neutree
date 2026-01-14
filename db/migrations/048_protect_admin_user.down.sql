DROP POLICY IF EXISTS "user_profile update policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile delete policy" ON api.user_profiles;

CREATE POLICY "user_profile update policy" ON api.user_profiles
    FOR UPDATE
    USING (
        id = auth.uid()
        OR
        api.has_permission(auth.uid(), 'user_profile:update', NULL)
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND id != auth.uid()
        )
    )
    WITH CHECK (
        (
            id = auth.uid()
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND id != auth.uid()
        )
    );

CREATE POLICY "user_profile delete policy" ON api.user_profiles
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'user_profile:delete', NULL)
        AND id != auth.uid()
    );
