-- Body restored from 019, the definition this migration replaced.
CREATE OR REPLACE FUNCTION update_metadata_update_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW(
        (NEW.metadata).name,
        (NEW.metadata).display_name,
        (NEW.metadata).workspace,
        (NEW.metadata).deletion_timestamp,
        (NEW.metadata).creation_timestamp,
        CURRENT_TIMESTAMP,
        (NEW.metadata).labels,
        (NEW.metadata).annotations
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
