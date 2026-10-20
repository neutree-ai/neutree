-- ----------------------
-- Resource: LogSink (v1)
-- ----------------------
-- A global (workspace-less) external log store that AI traces are replicated
-- to. Type-specific settings live in a JSONB field named after the type, so a
-- new type adds a field instead of reshaping the composite.
--
-- Error codes: 10243-10249 (shared and victorialogs), 10305-10311 (s3).
-- 10250-10304 are taken by other resources.

CREATE TYPE api.log_sink_spec AS (
    type TEXT,
    enabled BOOLEAN,
    victorialogs JSONB,
    buffer JSONB,
    s3 JSONB
);

CREATE TYPE api.log_sink_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT,
    observed_hash TEXT,
    sent_events BIGINT,
    dropped_events BIGINT,
    last_sent_time TIMESTAMPTZ,
    last_connection_test JSONB,
    component_id TEXT
);

CREATE TABLE api.log_sinks (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.log_sink_spec,
    status api.log_sink_status
);

CREATE TRIGGER update_log_sinks_update_timestamp
    BEFORE UPDATE ON api.log_sinks
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_log_sinks_default_timestamp
    BEFORE INSERT ON api.log_sinks
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_log_sinks
    BEFORE INSERT OR UPDATE ON api.log_sinks
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE UNIQUE INDEX log_sinks_name_unique_idx ON api.log_sinks (((metadata).name));

-- ----------------------
-- Validation
-- ----------------------
-- Runs on UPDATE as well: the API backfills masked secrets before a PATCH
-- reaches the database, so a stored token, password or key is still present
-- here. Also normalizes the row: no workspace, enabled defaults to true, only
-- the settings of spec.type are kept, and only the secrets they use.
CREATE OR REPLACE FUNCTION api.validate_log_sink_spec()
RETURNS TRIGGER AS $$
DECLARE
    sink_type TEXT;
    vl JSONB;
    s3 JSONB;
    sink_auth JSONB;
    auth_type TEXT;
    max_size JSONB;
    field TEXT;
    meta api.metadata;
    spec api.log_sink_spec;
BEGIN
    meta := NEW.metadata;
    meta.workspace := NULL;
    NEW.metadata := meta;

    sink_type := (NEW.spec).type;

    IF sink_type IS NULL OR sink_type NOT IN ('victorialogs', 's3') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10243","message": "spec.type is invalid","hint": "Supported log sink types: victorialogs, s3"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF sink_type = 'victorialogs' THEN
        vl := (NEW.spec).victorialogs;

        IF vl IS NULL OR jsonb_typeof(vl) <> 'object'
           OR vl->>'url' IS NULL OR trim(vl->>'url') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10244","message": "spec.victorialogs.url is required","hint": "Provide the VictoriaLogs base URL, e.g. https://vl.example.com:9428"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF NOT (vl->>'url' ~* '^https?://') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10245","message": "spec.victorialogs.url must start with http:// or https://"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        sink_auth := vl->'auth';

        -- A PATCH that omits auth comes back from the backfill as just the stored
        -- secrets with no type. Treat it as auth removed.
        IF TG_OP = 'UPDATE' AND jsonb_typeof(sink_auth) = 'object'
           AND sink_auth->>'type' IS NULL
           AND (sink_auth - 'password' - 'token') = '{}'::jsonb
           AND sink_auth <@ coalesce((OLD.spec).victorialogs->'auth', '{}'::jsonb) THEN
            vl := vl - 'auth';
            sink_auth := NULL;
        END IF;

        IF sink_auth IS NOT NULL AND jsonb_typeof(sink_auth) <> 'null' THEN
            auth_type := CASE WHEN jsonb_typeof(sink_auth) = 'object' THEN sink_auth->>'type' END;

            IF auth_type IS NULL OR auth_type NOT IN ('none', 'basic', 'bearer') THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10246","message": "spec.victorialogs.auth.type is invalid","hint": "Supported auth types: none, basic, bearer"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            IF auth_type = 'basic' AND coalesce(trim(sink_auth->>'username'), '') = '' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10247","message": "spec.victorialogs.auth.username is required for basic auth"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            IF auth_type = 'bearer' AND coalesce(trim(sink_auth->>'token'), '') = '' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10248","message": "spec.victorialogs.auth.token is required for bearer auth"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;

            -- The backfill restores every stored secret, so drop the ones the
            -- current type does not use (e.g. the password after basic -> bearer).
            sink_auth := CASE auth_type
                WHEN 'basic' THEN sink_auth - 'token'
                WHEN 'bearer' THEN sink_auth - 'username' - 'password'
                ELSE sink_auth - 'username' - 'password' - 'token'
            END;
            vl := jsonb_set(vl, '{auth}', sink_auth);
        END IF;
    ELSE
        s3 := (NEW.spec).s3;

        IF s3 IS NULL OR jsonb_typeof(s3) <> 'object' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10305","message": "spec.s3 is required when spec.type is s3"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Wrong JSON types would make the whole list unreadable for the API and
        -- the controller, not just this sink.
        FOREACH field IN ARRAY ARRAY['endpoint', 'region', 'bucket', 'key_prefix', 'compression'] LOOP
            IF jsonb_typeof(s3->field) NOT IN ('string', 'null') THEN
                RAISE sqlstate 'PGRST'
                    USING message = format('{"code": "10305","message": "spec.s3.%s must be a string"}', field),
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;
        END LOOP;

        IF jsonb_typeof(s3->'force_path_style') NOT IN ('boolean', 'null') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10305","message": "spec.s3.force_path_style must be a boolean"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF jsonb_typeof(s3->'auth') NOT IN ('object', 'null') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10305","message": "spec.s3.auth must be an object"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF coalesce(trim(s3->>'bucket'), '') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10306","message": "spec.s3.bucket is required","hint": "Provide the name of an existing bucket"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF coalesce(trim(s3->>'region'), '') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10307","message": "spec.s3.region is required","hint": "S3-compatible stores usually accept us-east-1"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF coalesce(s3->>'endpoint', '') <> '' AND NOT (s3->>'endpoint' ~* '^https?://') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10308","message": "spec.s3.endpoint must start with http:// or https://","hint": "Leave it empty to use the AWS endpoint of the region"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF coalesce(s3->>'compression', '') NOT IN ('', 'gzip', 'zstd', 'none') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10309","message": "spec.s3.compression is invalid","hint": "Supported compression: gzip, zstd, none"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- CASE, because the cast must only see a number; parenthesized so the
        -- IF does not end at its first THEN.
        IF (CASE jsonb_typeof(s3->'flush_interval_seconds')
               WHEN 'number' THEN (s3->>'flush_interval_seconds')::numeric NOT BETWEEN 1 AND 86400
                   OR (s3->>'flush_interval_seconds')::numeric <> trunc((s3->>'flush_interval_seconds')::numeric)
               WHEN 'null' THEN false
               ELSE s3 ? 'flush_interval_seconds'
           END) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10310","message": "spec.s3.flush_interval_seconds must be an integer from 1 to 86400"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        -- Both keys are masked on read and backfilled on PATCH, so a PATCH that
        -- omits them keeps the stored ones; they cannot be removed, only replaced.
        IF coalesce(trim(s3->'auth'->>'access_key_id'), '') = ''
           OR coalesce(s3->'auth'->>'secret_access_key', '') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10311","message": "spec.s3.auth.access_key_id and spec.s3.auth.secret_access_key are required"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        s3 := jsonb_set(s3, '{auth}', jsonb_build_object(
            'access_key_id', s3->'auth'->'access_key_id',
            'secret_access_key', s3->'auth'->'secret_access_key'));
    END IF;

    max_size := (NEW.spec).buffer->'max_size';

    -- 268435488 bytes is the smallest disk buffer Vector accepts.
    IF max_size IS NOT NULL AND jsonb_typeof(max_size) <> 'null'
       AND (jsonb_typeof(max_size) <> 'number' OR (max_size #>> '{}')::numeric < 268435488) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10249","message": "spec.buffer.max_size must be at least 268435488 bytes","hint": "268435488 bytes (256MiB) is the smallest disk buffer the log shipper accepts"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Only the settings of spec.type are kept: the backfill re-creates the
    -- secrets of the other type's field when a PATCH changes spec.type.
    spec := NEW.spec;
    spec.enabled := coalesce(spec.enabled, true);
    spec.victorialogs := vl;
    spec.s3 := s3;
    NEW.spec := spec;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_log_sink_spec_on_log_sinks
    BEFORE INSERT OR UPDATE ON api.log_sinks
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_log_sink_spec();

-- ----------------------
-- RLS: global resource, so the workspace argument is NULL.
-- ----------------------
ALTER TABLE api.log_sinks ENABLE ROW LEVEL SECURITY;

CREATE POLICY "log_sink read policy" ON api.log_sinks
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'log_sink:read', NULL)
    );

CREATE POLICY "log_sink create policy" ON api.log_sinks
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'log_sink:create', NULL)
    );

CREATE POLICY "log_sink update policy" ON api.log_sinks
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'log_sink:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'log_sink:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE POLICY "log_sink delete policy" ON api.log_sinks
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'log_sink:delete', NULL)
    );

CREATE TRIGGER enforce_soft_delete_integrity_log_sinks
    BEFORE UPDATE ON api.log_sinks
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

-- Separate migration from the enum values: they cannot be referenced in the
-- transaction that adds them.
SELECT api.update_admin_permissions();
