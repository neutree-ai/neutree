-- Remove obsolete CA field from image registry spec
ALTER TYPE api.image_registry_spec
    DROP ATTRIBUTE ca;
