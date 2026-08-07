-- NEU-649: when a model registry was last checked, and whether it is public.

-- last_transition_time answers "when did the phase last change", which is not
-- the same question as "when was this last checked": a registry that has been
-- reachable for three days reports the moment it first connected. A reachability
-- state shown without a check time cannot be told apart from a stale one, so the
-- check gets a timestamp of its own.
ALTER TYPE api.model_registry_status ADD ATTRIBUTE last_checked_at TIMESTAMPTZ;

-- Public vs private is a property of the registry kind, and every client needs
-- it: to filter the list, to know that storage figures will be absent, and to
-- know that no write operation exists. Deriving it from spec.type in each client
-- would put the same rule in several places and let them drift, so the server
-- states it.
--
-- A PostgREST computed field rather than a stored column: it is derived, so
-- storing it would need a trigger to keep it true, and a stored column would
-- also appear in write payloads that clients round-trip back to us. A computed
-- field can be selected ("?select=*,visibility") and filtered
-- ("?visibility=eq.public") but never written.
--
-- Kept in step with v1.VisibilityForModelRegistryType, which is the same rule for
-- callers inside the control plane.
CREATE FUNCTION api.visibility(registry api.model_registries)
RETURNS TEXT AS $$
    SELECT CASE
        WHEN (registry.spec).type = 'hugging-face' THEN 'public'
        ELSE 'private'
    END;
$$ LANGUAGE sql STABLE;

NOTIFY pgrst, 'reload schema';
