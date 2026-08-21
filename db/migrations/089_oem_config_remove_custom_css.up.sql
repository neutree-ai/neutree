-- A database initialized while the released 087 source pair was absent can be
-- at version 88 without this attribute. Removing it when present discards its
-- stored values; the down migration restores only the field definition.
ALTER TYPE api.oem_config_spec
    DROP ATTRIBUTE IF EXISTS custom_css;
