-- NEU-619: display aliases for models held in a private model registry.
--
-- Uniqueness is arbitrated by the database rather than by the application:
-- registry connections are built per HTTP request against shared storage with
-- no lock, so two concurrent renames can both pass an application-side check
-- and both land. The unique index below is the only thing that cannot be raced.
--
-- The table is a projection, not the source of truth: the registry filesystem
-- decides which models exist. Rows whose (model_name, model_version) has
-- disappeared are not shown, and they must not block a new alias -- a write
-- that collides with such a row overwrites it.
CREATE TABLE api.model_aliases (
    id SERIAL PRIMARY KEY,
    model_registry_id INT NOT NULL REFERENCES api.model_registries(id) ON DELETE CASCADE,
    -- Redundant copy of the registry's workspace so the RLS policies below can
    -- call api.has_permission without a join, as on the other workspaced tables.
    workspace TEXT NOT NULL,
    -- Physical coordinates of the aliased model inside the registry.
    model_name TEXT NOT NULL,
    model_version TEXT NOT NULL,
    -- alias keeps the user input verbatim: it is a display name, so case and
    -- inner spacing are part of it. alias_normalized (NFKC -> trim -> lowercase)
    -- is what uniqueness compares, and it is computed in Go: Postgres' lower()
    -- is not a full Unicode casefold and NFKC would need an extra extension.
    alias TEXT NOT NULL,
    alias_normalized TEXT NOT NULL
);

CREATE UNIQUE INDEX model_aliases_registry_alias_normalized_unique_idx
    ON api.model_aliases (model_registry_id, alias_normalized);

-- The read path joins aliases onto the live model list and the model-delete
-- path removes aliases by physical coordinates; both key off this index.
CREATE INDEX model_aliases_registry_model_idx
    ON api.model_aliases (model_registry_id, model_name, model_version);

ALTER TABLE api.model_aliases ENABLE ROW LEVEL SECURITY;

-- Aliases are part of a registry's model data, so they reuse the model
-- permission actions defined in 042 rather than introducing new ones.
CREATE POLICY "model alias read policy" ON api.model_aliases
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'model:read', workspace)
    );

CREATE POLICY "model alias create policy" ON api.model_aliases
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'model:push', workspace)
    );

CREATE POLICY "model alias update policy" ON api.model_aliases
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model:push', workspace)
    );

CREATE POLICY "model alias delete policy" ON api.model_aliases
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'model:push', workspace)
    );
