BEGIN;

DROP TRIGGER IF EXISTS user_profile_identity_source ON api.user_profiles;
DROP FUNCTION IF EXISTS api.apply_user_profile_identity_source();

-- Drop the label the trigger kept; nothing maintains it any more.
UPDATE api.user_profiles
SET metadata = ROW(
  (metadata).name,
  (metadata).display_name,
  (metadata).workspace,
  (metadata).deletion_timestamp,
  (metadata).creation_timestamp,
  (metadata).update_timestamp,
  ((metadata).labels::jsonb - 'neutree.ai/identity-source')::json,
  (metadata).annotations
)::api.metadata
WHERE (metadata).labels::jsonb ? 'neutree.ai/identity-source';

COMMIT;
