ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE base json;
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE variants json;
ALTER TYPE api.model_catalog_spec ADD ATTRIBUTE features json;

ALTER TYPE api.endpoint_spec ADD ATTRIBUTE model_catalog text;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE variant text;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE enabled_features text[];
