-- Policies, indexes and the trigger belong to the table and go with it.
DROP TABLE IF EXISTS api.model_aliases;

DROP FUNCTION IF EXISTS public.set_model_alias_workspace();
DROP FUNCTION IF EXISTS public.model_registry_workspace(INT);
