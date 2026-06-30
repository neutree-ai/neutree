-- Recipe Model Catalog: extend model_catalog_spec with env and the recipe
-- template attributes (base / variants / features), and add display-only model
-- metadata to the shared model_spec.
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE env json;
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE base json;
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE variants json;
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE features json;

-- Display-only model metadata (parameter count, quantization, context length,
-- architecture), carried as json. Shared by endpoint_spec.model and
-- model_catalog_spec.model via api.model_spec.
ALTER TYPE api.model_spec ADD ATTRIBUTE info json;
