-- NEU-857: advance metadata.update_timestamp on every UPDATE of api.api_keys,
-- like the other resource tables.
CREATE TRIGGER update_api_keys_update_timestamp
    BEFORE UPDATE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();
