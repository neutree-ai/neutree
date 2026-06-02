DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        JOIN pg_class c ON c.oid = t.typrelid
        JOIN pg_attribute a ON a.attrelid = c.oid
        WHERE n.nspname = 'api'
          AND t.typname = 'engine_version'
          AND a.attname = 'capabilities'
          AND NOT a.attisdropped
    ) THEN
        ALTER TYPE api.engine_version DROP ATTRIBUTE capabilities;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        JOIN pg_class c ON c.oid = t.typrelid
        JOIN pg_attribute a ON a.attrelid = c.oid
        WHERE n.nspname = 'api'
          AND t.typname = 'engine_version'
          AND a.attname = 'sidecar'
          AND NOT a.attisdropped
    ) THEN
        ALTER TYPE api.engine_version DROP ATTRIBUTE sidecar;
    END IF;
END $$;
