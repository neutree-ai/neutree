ALTER TYPE api.endpoint_spec DROP ATTRIBUTE feature_selections;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE variant;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE model_catalog;

ALTER TYPE api.model_catalog_spec DROP ATTRIBUTE features;
ALTER TYPE api.model_catalog_spec DROP ATTRIBUTE variants;
ALTER TYPE api.model_catalog_spec DROP ATTRIBUTE base;
