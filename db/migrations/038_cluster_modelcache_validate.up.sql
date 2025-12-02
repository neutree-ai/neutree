CREATE OR REPLACE FUNCTION api.validate_cluster_modelcache_config()
RETURNS TRIGGER AS $$
BEGIN
    -- Validate model_cache configuration
    IF (NEW.spec).config->>'model_caches' IS NOT NULL THEN
        DECLARE
            model_cache_array JSONB;
            cache_count INTEGER;
            cache_item JSONB;
        BEGIN
            model_cache_array := (NEW.spec).config->'model_caches';

            -- Check if model_cache is an array
            IF jsonb_typeof(model_cache_array) != 'array' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10201","message": "model_caches must be an array","hint": "Provide model_caches as an array"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            cache_count := jsonb_array_length(model_cache_array);

            -- Only allow one model_cache configuration
            IF cache_count > 1 THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10202","message": "Only one model_caches configuration is allowed","hint": "Provide only one model_caches item"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            -- Validate the single cache item if exists
            IF cache_count = 1 THEN
                cache_item := model_cache_array->0;

                -- Check if name exists and is not empty
                IF cache_item->>'name' IS NULL OR trim(cache_item->>'name') = '' THEN
                    RAISE sqlstate 'PGRST'
                        USING message = '{"code": "10203","message": "model_cache.name is required","hint": "Provide name for model_cache"}',
                        detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
                END IF;

                -- Check that name is not 'default'
                IF cache_item->>'name' = 'default' THEN
                    RAISE sqlstate 'PGRST'
                        USING message = '{"code": "10204","message": "model_caches.name must not be ''default''","hint": "Set model_caches.name to a value other than ''default''"}',
                        detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
                END IF;

                IF NOT cache_item->>'name' ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' THEN
                    RAISE sqlstate 'PGRST'
                        USING message = '{"code": "10205","message": "Invalid model_caches.name format","hint": "Use lowercase alphanumeric and hyphens"}',
                        detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
                END IF;
            END IF;
        END;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_cluster_modelcache_config_on_clusters
    BEFORE INSERT OR UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_cluster_modelcache_config();