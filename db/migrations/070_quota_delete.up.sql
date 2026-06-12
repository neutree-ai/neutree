-- Quota & usage control (NEUTREE-GENERAL-9): delete-side RPC for the UI.
--
-- set_quota_policy only upserts, so a policy can be changed but never removed
-- (removing it means "unlimited" at that level/period). neutree-api proxies the
-- generic /rpc/* endpoint but not the quota_policies table directly, so the UI
-- deletes through this function. SECURITY INVOKER so the quota_policies DELETE
-- RLS in 067 authorizes the caller (workspace admins for workspace/user policies,
-- key owners for their own api_key policies); the hierarchy trigger does not run
-- on DELETE, and dropping a parent only relaxes the child invariant.
CREATE OR REPLACE FUNCTION api.delete_quota_policy(p_id BIGINT)
RETURNS BIGINT
LANGUAGE sql
SECURITY INVOKER
AS $$
    DELETE FROM api.quota_policies WHERE id = p_id RETURNING id;
$$;

NOTIFY pgrst, 'reload schema';
