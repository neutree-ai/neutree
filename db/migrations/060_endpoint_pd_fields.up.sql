-- PD API fields on api.endpoint_spec / api.endpoint_status.
-- These fields model strategy, placement summary, role definitions, KV
-- transfer configuration, and flattened RoleGroup replica status for the
-- Phase 1 design where same-host behavior is expressed by placement.roles.

ALTER TYPE api.endpoint_spec ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE placement json;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE roles     json;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE kv        json;

ALTER TYPE api.endpoint_status ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE placement TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE replicas  json;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE total_replicas INTEGER;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE ready_replicas INTEGER;
