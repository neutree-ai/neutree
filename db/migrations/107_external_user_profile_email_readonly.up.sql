-- NEU-656: an external user's profile email belongs to the identity source.
--
-- A user from an identity source (auth.users.raw_app_meta_data has a
-- non-empty identity_source) gets spec.email from the directory (migration
-- 103). Changing it in neutree does not change the directory and is lost or
-- contradicted on the next sync, so it is refused here.
--
-- How a system write is told apart from a user's: the system only ever sets
-- spec.email to public.user_profile_email(...) of the user's auth.users row
-- (handle_new_user on insert, the 103 AFTER UPDATE trigger, the 103 backfill).
-- So a change of spec.email is accepted when the new value is that value and
-- refused otherwise. No session flag is needed, and nothing a caller can set
-- (a GUC, a label) can unlock the field; the most a caller can do is set the
-- value the system would set anyway. A PATCH that resends the current email,
-- or changes other fields only, is not a change and passes.
--
-- The same trigger keeps a read-only label on the profile,
-- neutree.ai/identity-source = <identity_source>, so clients (the UI user
-- form) can tell an external user from the profile alone: the profile is
-- readable to user admins, auth.users is not. The label is recomputed from
-- auth.users on every insert and update, so a write cannot add, change or
-- drop it; local users never have it.
--
-- A second read-only label, neutree.ai/identity-source-name = <name>, names
-- the identity source itself (app_metadata.identity_source_name), so a client
-- can show which source of a type the user comes from. It is kept the same
-- way, and only on users that also have the type label.
BEGIN;

CREATE OR REPLACE FUNCTION api.apply_user_profile_identity_source()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
DECLARE
  v_source      TEXT;
  v_source_name TEXT;
  v_email       TEXT;
  v_meta   api.metadata := NEW.metadata;
  v_labels JSONB;
BEGIN
  SELECT NULLIF(u.raw_app_meta_data->>'identity_source', ''),
         NULLIF(u.raw_app_meta_data->>'identity_source_name', ''),
         public.user_profile_email(u.raw_app_meta_data, u.raw_user_meta_data, u.email)
    INTO v_source, v_source_name, v_email
    FROM auth.users u
   WHERE u.id = NEW.id;

  IF TG_OP = 'UPDATE'
     AND v_source IS NOT NULL
     AND (NEW.spec).email IS DISTINCT FROM (OLD.spec).email
     AND (NEW.spec).email IS DISTINCT FROM v_email THEN
    RAISE sqlstate 'PGRST'
      USING message = '{"code": "10260","message": "spec.email of a user from an identity source cannot be changed","hint": "The email comes from the identity source; change it there"}',
      detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
  END IF;

  v_labels := COALESCE(v_meta.labels::jsonb, '{}'::jsonb)
              - 'neutree.ai/identity-source' - 'neutree.ai/identity-source-name';
  IF v_source IS NOT NULL THEN
    v_labels := v_labels || jsonb_build_object('neutree.ai/identity-source', v_source);
    IF v_source_name IS NOT NULL THEN
      v_labels := v_labels || jsonb_build_object('neutree.ai/identity-source-name', v_source_name);
    END IF;
  END IF;

  IF v_labels IS DISTINCT FROM COALESCE(v_meta.labels::jsonb, '{}'::jsonb) THEN
    v_meta.labels := v_labels::json;
    NEW.metadata := v_meta;
  END IF;

  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_profile_identity_source
  BEFORE INSERT OR UPDATE ON api.user_profiles
  FOR EACH ROW
  EXECUTE FUNCTION api.apply_user_profile_identity_source();

-- The 103 trigger touches the profile only when identity_source is first set;
-- a later change of the name must relabel it too.
CREATE OR REPLACE FUNCTION api.handle_user_identity_source_name_set()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
BEGIN
  UPDATE api.user_profiles SET metadata = metadata WHERE id = NEW.id;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_auth_user_identity_source_name_set
  AFTER UPDATE OF raw_app_meta_data ON auth.users
  FOR EACH ROW
  WHEN (OLD.raw_app_meta_data->>'identity_source_name' IS DISTINCT FROM NEW.raw_app_meta_data->>'identity_source_name')
  EXECUTE PROCEDURE api.handle_user_identity_source_name_set();

-- Users created before identity_source_name was recorded: take the name from
-- their link, external_identities.source = <type>:<name>.
UPDATE auth.users u
SET raw_app_meta_data = COALESCE(u.raw_app_meta_data, '{}'::jsonb)
                        || jsonb_build_object('identity_source_name', l.name)
FROM (
  SELECT DISTINCT ON (user_id) user_id, substr(source, strpos(source, ':') + 1) AS name
    FROM api.external_identities
   WHERE strpos(source, ':') > 0
   ORDER BY user_id, created_at
) l
WHERE u.id = l.user_id
  AND NULLIF(u.raw_app_meta_data->>'identity_source', '') IS NOT NULL
  AND NULLIF(u.raw_app_meta_data->>'identity_source_name', '') IS NULL;

-- Label the profiles of existing external users; the trigger computes it.
UPDATE api.user_profiles p
SET metadata = p.metadata
FROM auth.users u
WHERE p.id = u.id
  AND NULLIF(u.raw_app_meta_data->>'identity_source', '') IS NOT NULL;

COMMIT;
