-- Restore the field definition only; values removed by 089 up cannot be recovered.
ALTER TYPE api.oem_config_spec
    ADD ATTRIBUTE custom_css TEXT;
