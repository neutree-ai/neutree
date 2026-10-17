BEGIN;

DROP TRIGGER IF EXISTS on_auth_user_identity_source_name_set ON auth.users;
DROP FUNCTION IF EXISTS api.handle_user_identity_source_name_set();

DROP TRIGGER IF EXISTS user_profile_identity_source ON api.user_profiles;
DROP FUNCTION IF EXISTS api.apply_user_profile_identity_source();

-- Drop the labels the trigger kept; nothing maintains them any more.
-- app_metadata.identity_source_name is left: GoTrue owns app_metadata.
UPDATE api.user_profiles
SET metadata = ROW(
  (metadata).name,
  (metadata).display_name,
  (metadata).workspace,
  (metadata).deletion_timestamp,
  (metadata).creation_timestamp,
  (metadata).update_timestamp,
  ((metadata).labels::jsonb - 'neutree.ai/identity-source' - 'neutree.ai/identity-source-name')::json,
  (metadata).annotations
)::api.metadata
WHERE (metadata).labels::jsonb ?| ARRAY['neutree.ai/identity-source', 'neutree.ai/identity-source-name'];

COMMIT;
