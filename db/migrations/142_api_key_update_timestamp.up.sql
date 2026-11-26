-- NEU-857: api_keys was missing the timestamp trigger pair that every other
-- resource table has (update_timestamp on UPDATE, default timestamps on INSERT).
CREATE TRIGGER update_api_keys_update_timestamp
    BEFORE UPDATE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_api_keys_default_timestamp
    BEFORE INSERT ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();
