-- A resource's creation_timestamp was losable by accident.
--
-- Both timestamps are the database's to maintain, and the insert trigger
-- treats them that way: set_default_metadata_timestamp_column writes
-- CURRENT_TIMESTAMP into both positions and ignores whatever the payload said.
-- The update trigger held that line for update_timestamp but not for
-- creation_timestamp, which it read back out of NEW -- that is, out of the
-- caller's payload.
--
-- metadata is a composite column, so PostgREST rewrites the whole of it from
-- whatever JSON a PATCH carries: any subfield the payload leaves out comes back
-- NULL. A client that resends metadata to change one field -- a label, a
-- display name, a deletion timestamp -- and does not echo creation_timestamp
-- therefore erased it, and the trigger preserved that NULL faithfully. The UI
-- renders the result as "-" and the creation time is gone for good (NEU-717).
--
-- Take the value from OLD instead, unconditionally. Creation time is now the
-- database's alone on both paths: an insert stamps it, an update cannot touch
-- it, and neither one can be talked out of it by a payload.
CREATE OR REPLACE FUNCTION update_metadata_update_timestamp_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.metadata := ROW(
        (NEW.metadata).name,
        (NEW.metadata).display_name,
        (NEW.metadata).workspace,
        (NEW.metadata).deletion_timestamp,
        (OLD.metadata).creation_timestamp,
        CURRENT_TIMESTAMP,
        (NEW.metadata).labels,
        (NEW.metadata).annotations
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
