-- NEU-624: when a model registry was last checked, and whether it is public.

-- last_transition_time answers "when did the phase last change", which cannot
-- serve as a check time: a registry reachable for days still reports the moment
-- it first connected. Every writer of this composite must carry this attribute
-- forward, or a partial PATCH nulls it (see model_registry.NextStatus).
ALTER TYPE api.model_registry_status ADD ATTRIBUTE last_checked_at TIMESTAMPTZ;

-- Public vs private is derived from the registry kind, and clients need it to
-- filter the list and to know that storage figures and write operations are
-- absent. A computed field rather than a stored column: nothing has to keep it
-- true, it can be selected ("?select=*,visibility") and filtered
-- ("?visibility=eq.public"), and it is never accepted on a write.
--
-- Must be kept in step with v1.VisibilityForModelRegistryType, which is the same
-- rule for callers inside the control plane.
CREATE FUNCTION api.visibility(registry api.model_registries)
RETURNS TEXT AS $$
    SELECT CASE
        WHEN (registry.spec).type = 'hugging-face' THEN 'public'
        ELSE 'private'
    END;
$$ LANGUAGE sql STABLE;

NOTIFY pgrst, 'reload schema';
