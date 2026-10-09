-- NEU-839: preset key of the department-admin role (migration 112).
-- A separate migration because an enum value added here cannot be used in the
-- transaction that adds it.
ALTER TYPE api.role_preset ADD VALUE IF NOT EXISTS 'department-admin';
