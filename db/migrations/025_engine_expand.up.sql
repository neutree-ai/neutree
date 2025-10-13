ALTER TYPE api.engine_version ADD ATTRIBUTE deploy_template json;
ALTER TYPE api.engine_version ADD ATTRIBUTE images json;
ALTER TYPE api.engine_version ADD ATTRIBUTE supported_tasks TEXT[];