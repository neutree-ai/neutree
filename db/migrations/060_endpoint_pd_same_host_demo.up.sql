-- Demo (Phase 0) — PD same-host minimum API fields.
-- MVP PR-01 will extend api.endpoint_spec with Sidecars[], full kv
-- schema, and ReplicaStatus.Roles map; this migration only adds the Demo-minimum
-- attributes to api.endpoint_spec / api.endpoint_status composite types.

ALTER TYPE api.endpoint_spec ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE placement json;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE roles     json;

ALTER TYPE api.endpoint_status ADD ATTRIBUTE strategy  TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE placement TEXT;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE replicas  json;
