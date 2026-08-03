-- NEU-619: display aliases for models held in a private model registry.
--
-- Uniqueness is arbitrated by the database rather than by the application:
-- registry connections are built per HTTP request against shared storage with
-- no lock, so two concurrent renames can both pass an application-side check
-- and both land. The unique indexes below are the only thing that cannot be raced.
--
-- The table is a projection, not the source of truth: the registry filesystem
-- decides which models exist. Rows whose (model_name, model_version) has
-- disappeared are not shown, and they must not block a new alias -- a write
-- that collides with such a row overwrites it.

-- The owning registry's workspace, read past the registry's own RLS so it is
-- available to the policies below. Lives in public rather than api because
-- PostgREST only exposes the api schema, and this must not become a callable
-- RPC. STABLE so the planner can cache it within a statement.
CREATE FUNCTION public.model_registry_workspace(p_id INT)
RETURNS TEXT AS $$
    SELECT (metadata).workspace FROM api.model_registries WHERE id = p_id;
$$ LANGUAGE sql STABLE SECURITY DEFINER;

CREATE TABLE api.model_aliases (
    id SERIAL PRIMARY KEY,
    model_registry_id INT NOT NULL REFERENCES api.model_registries(id) ON DELETE CASCADE,
    -- Denormalized copy of the owning registry's workspace, for reads and
    -- filtering. It is derived by the trigger below and is NOT the value the
    -- RLS policies authorize against -- see the comment on those policies.
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

-- An alias is unique within a registry...
CREATE UNIQUE INDEX model_aliases_registry_alias_normalized_unique_idx
    ON api.model_aliases (model_registry_id, alias_normalized);

-- ...and a model version carries at most one alias. Without this a model could
-- accumulate several display names and the read path -- which joins aliases
-- onto the live model list -- would have no defined winner.
CREATE UNIQUE INDEX model_aliases_registry_model_unique_idx
    ON api.model_aliases (model_registry_id, model_name, model_version);

-- workspace is derived from the owning registry on every write, so a client
-- cannot store a workspace the registry does not belong to. BEFORE triggers run
-- ahead of NOT NULL and of the RLS WITH CHECK, so callers may omit it entirely.
CREATE FUNCTION public.set_model_alias_workspace()
RETURNS TRIGGER AS $$
BEGIN
    NEW.workspace := public.model_registry_workspace(NEW.model_registry_id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER set_model_alias_workspace
    BEFORE INSERT OR UPDATE ON api.model_aliases
    FOR EACH ROW
    EXECUTE FUNCTION public.set_model_alias_workspace();

ALTER TABLE api.model_aliases ENABLE ROW LEVEL SECURITY;

-- The policies authorize against the workspace of the referenced registry, not
-- against the row's own workspace column. Two reasons the stored column must
-- not be the authority: a client supplies it on write, and nothing propagates a
-- later change of the registry's workspace to rows already stored.
--
-- api.has_permission is the single authorization primitive in this schema, and
-- its signature is a stable contract: a policy is responsible for handing it the
-- real workspace of the resource being guarded. This schema caps a deployment at
-- one workspace (021), so the argument is not consumed today -- passing the
-- correct value is the policy's job either way, not the primitive's.
--
-- Were these policies to authorize against the stored column instead, a caller
-- holding model:push in one workspace could insert a row naming that workspace
-- while pointing model_registry_id at a registry in another, taking a slot in
-- the second registry's unique index in a row that registry's own users could
-- neither see nor clear.
--
-- Aliases are part of a registry's model data, so they reuse the model actions
-- from 042 rather than introducing new ones.
CREATE POLICY "model alias read policy" ON api.model_aliases
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'model:read', public.model_registry_workspace(model_registry_id))
    );

CREATE POLICY "model alias create policy" ON api.model_aliases
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'model:push', public.model_registry_workspace(model_registry_id))
    );

CREATE POLICY "model alias update policy" ON api.model_aliases
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model:push', public.model_registry_workspace(model_registry_id))
    );

CREATE POLICY "model alias delete policy" ON api.model_aliases
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'model:push', public.model_registry_workspace(model_registry_id))
    );
