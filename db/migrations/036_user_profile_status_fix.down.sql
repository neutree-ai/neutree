CREATE TYPE api.user_profile_status_old AS (
    phase TEXT,
    service_url TEXT,
    error_message TEXT
);

ALTER TABLE api.user_profiles
    ALTER COLUMN status TYPE api.user_profile_status_old
    USING ROW(
        (status).phase,
        NULL,
        (status).error_message
    )::api.user_profile_status_old;

DROP TYPE api.user_profile_status;

ALTER TYPE api.user_profile_status_old RENAME TO user_profile_status;
