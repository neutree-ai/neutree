-- What a registry turned out to be able to do, beyond being reachable.
--
-- Repositories cannot be listed portably: the OCI distribution spec has no
-- endpoint for it, and the /v2/_catalog inherited from Docker Registry v2 is
-- refused by both registries that matter (Docker Hub issues no catalog scope to
-- anyone; Harbor reserves it for system administrators). So each registry is
-- asked in its own dialect, and which dialect applies is established while
-- connecting rather than at the moment a user is waiting on a dropdown.
ALTER TYPE api.image_registry_status ADD ATTRIBUTE capabilities JSONB;
