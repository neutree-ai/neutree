DROP TRIGGER IF EXISTS validate_cluster_config_on_clusters ON api.clusters;
DROP FUNCTION IF EXISTS api.validate_cluster_config();
DROP FUNCTION IF EXISTS api.validate_accelerator_resources(JSONB, TEXT, TEXT, TEXT);

CREATE OR REPLACE FUNCTION api.validate_cluster_config()
RETURNS TRIGGER AS $$
BEGIN
    -- Check if cluster type is valid
    IF (New.spec).type is NULL or trim((New.spec).type) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10014","message": "spec.type is required","hint": "Provide cluster type"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Check if image registry is provided
    IF (NEW.spec).image_registry IS NULL OR trim((NEW.spec).image_registry) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10015","message": "spec.image_registry is required","hint": "Provide image registry"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Check if config is provided
    IF (NEW.spec).config IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10016","message": "spec.config is required","hint": "Provide cluster configuration"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Validate SSH clusters
    IF (NEW.spec).type = 'ssh' THEN
        -- Check if ssh_config exists
        IF (NEW.spec).config->>'ssh_config' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10206","message": "ssh_config is required for SSH clusters","hint": "Provide ssh_config configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check if provider exists
        IF (NEW.spec).config->'ssh_config'->>'provider' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10017","message": "ssh_config.provider is required for SSH clusters","hint": "Provide provider configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check if head_ip exists in provider and is not empty
        IF (NEW.spec).config->'ssh_config'->'provider'->>'head_ip' IS NULL OR trim((NEW.spec).config->'ssh_config'->'provider'->>'head_ip') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10018","message": "ssh_config.provider.head_ip is required for SSH clusters","hint": "Provide head_ip in provider configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check if auth exists
        IF (NEW.spec).config->'ssh_config'->>'auth' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10019","message": "ssh_config.auth is required for SSH clusters","hint": "Provide auth configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check if ssh_user exists
        IF (NEW.spec).config->'ssh_config'->'auth'->>'ssh_user' IS NULL OR trim((NEW.spec).config->'ssh_config'->'auth'->>'ssh_user') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10020","message": "ssh_config.auth.ssh_user is required for SSH clusters","hint": "Provide ssh_user in auth configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    -- Validate Kubernetes clusters
    IF (NEW.spec).type = 'kubernetes' THEN
        -- Check if kubernetes_config exists
        IF (NEW.spec).config->>'kubernetes_config' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10030","message": "kubernetes_config is required for Kubernetes clusters","hint": "Provide kubernetes_config configuration"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required kubeconfig fields
        IF (NEW.spec).config->'kubernetes_config'->>'kubeconfig' IS NULL OR trim((NEW.spec).config->'kubernetes_config'->>'kubeconfig') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10021","message": "kubernetes_config.kubeconfig is required for Kubernetes clusters","hint": "Provide kubeconfig in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required router spec
        IF (NEW.spec).config->'kubernetes_config'->>'router' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10022","message": "kubernetes_config.router is required for Kubernetes clusters","hint": "Provide router in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required access_mode
        IF (NEW.spec).config->'kubernetes_config'->'router'->>'access_mode' IS NULL OR trim((NEW.spec).config->'kubernetes_config'->'router'->>'access_mode') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10023","message": "kubernetes_config.router.access_mode is required for Kubernetes clusters","hint": "Provide router.access_mode in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required replicas
        -- Ensure replicas is an integer and >= 1
        -- Read as text, validate format, then cast safely
        DECLARE
            replicas_text TEXT := (NEW.spec).config->'kubernetes_config'->'router'->>'replicas';
            replicas_int  INTEGER;
        BEGIN
            IF replicas_text IS NULL OR trim(replicas_text) = '' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10024","message": "kubernetes_config.router.replicas is required for Kubernetes clusters","hint": "Provide router.replicas in kubernetes_config"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            -- Ensure the value is integer formatted (allow surrounding spaces)
            IF NOT replicas_text ~ '^\s*[0-9]+\s*$' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10028","message": "kubernetes_config.router.replicas must be an integer","hint": "Provide integer value for router.replicas in kubernetes_config"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            replicas_int := replicas_text::INTEGER;

            IF replicas_int < 1 THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10027","message": "kubernetes_config.router.replicas must be at least 1","hint": "Provide router.replicas >= 1 in kubernetes_config"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;
        END;

        -- Check for required resources (router spec)
        IF (NEW.spec).config->'kubernetes_config'->'router'->>'resources' IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10029","message": "kubernetes_config.router.resources is required for Kubernetes clusters","hint": "Provide router.resources in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required cpu fields
        IF (NEW.spec).config->'kubernetes_config'->'router'->'resources'->>'cpu' IS NULL OR trim((NEW.spec).config->'kubernetes_config'->'router'->'resources'->>'cpu') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10025","message": "kubernetes_config.router.resources.cpu is required for Kubernetes clusters","hint": "Provide router.resources.cpu in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Check for required memory fields (router spec)
        IF (NEW.spec).config->'kubernetes_config'->'router'->'resources'->>'memory' IS NULL OR trim((NEW.spec).config->'kubernetes_config'->'router'->'resources'->>'memory') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10026","message": "kubernetes_config.router.resources.memory is required for Kubernetes clusters","hint": "Provide router.resources.memory in kubernetes_config"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Validate memory format follows Kubernetes convention (e.g., 4Gi, 512Mi)
        IF NOT (NEW.spec).config->'kubernetes_config'->'router'->'resources'->>'memory' ~ '^[0-9]+([.][0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|[kKMGTPE]i?)$' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10114","message": "kubernetes_config.router.resources.memory must follow Kubernetes format (e.g., 4Gi, 512Mi)","hint": "Provide memory in correct format like 4Gi, 512Mi, 2Ti"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_cluster_config_on_clusters
    BEFORE INSERT OR UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_cluster_config();

