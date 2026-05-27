-- PD same-host API fields on api.endpoint_spec / api.endpoint_status.
-- These fields model strategy, placement summary, role definitions, and
-- flattened RoleGroup replica status for the Phase 1 same-host design.

ALTER TYPE api.endpoint_spec ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE placement json;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE roles     json;

ALTER TYPE api.endpoint_status ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE placement TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE replicas  json;
