-- Reintroduce CA field for rollback compatibility
ALTER TYPE api.image_registry_spec
    ADD ATTRIBUTE ca TEXT;
