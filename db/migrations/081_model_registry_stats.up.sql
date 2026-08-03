-- NEU-619: cached content statistics for a model registry (model count, storage
-- footprint, refresh time). One JSONB attribute rather than three scalars, so
-- later additions to the same status need no further migration.
ALTER TYPE api.model_registry_status ADD ATTRIBUTE stats JSONB;
