-- Add display-only model metadata (parameter count, quantization, context
-- length, architecture) to the shared model_spec composite type. Carried as
-- json to mirror the other free-form recipe attributes (base/variants/features)
-- and stay forward-compatible. Applies to both endpoint_spec.model and
-- model_catalog_spec.model since they share api.model_spec.
ALTER TYPE api.model_spec ADD ATTRIBUTE info json;
