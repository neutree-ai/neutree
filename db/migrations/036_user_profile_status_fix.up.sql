CREATE TYPE api.user_profile_status_new AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT,
    synced_spec api.user_profile_spec
);

ALTER TABLE api.user_profiles
    ALTER COLUMN status TYPE api.user_profile_status_new
    USING ROW(
        (status).phase,
        CURRENT_TIMESTAMP,
        (status).error_message,
        NULL
    )::api.user_profile_status_new;

DROP TYPE api.user_profile_status;

ALTER TYPE api.user_profile_status_new RENAME TO user_profile_status;
