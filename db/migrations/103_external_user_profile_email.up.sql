-- NEU-656: keep the directory email on the profiles of externally managed users.
--
-- A user created by an external identity source (LDAP today) is marked with
-- app_metadata.identity_source. GoTrue needs a unique email for every user, so
-- neutree-api gives such a user a placeholder address in auth.users.email
-- (<external id>@ldap.neutree.local) and puts the directory mail, when the
-- directory has one, in user_metadata.email. The profile copied
-- auth.users.email, so it showed the placeholder.
--
-- spec.email now takes user_metadata.email when the user has a non-empty
-- identity_source and a non-empty user_metadata.email, and auth.users.email
-- otherwise. Any identity source counts, not 'ldap' only, so a later source
-- behaves the same. Local and SSO users are unchanged.
--
-- The GoTrue admin API inserts the auth.users row first and sets the requested
-- app_metadata with a later UPDATE in the same transaction, so the AFTER INSERT
-- trigger (api.handle_new_user) does not see identity_source yet. The email is
-- therefore also applied by an AFTER UPDATE trigger that fires when a user
-- becomes externally managed. handle_new_user uses the same rule, so a row
-- inserted with app_metadata already set is covered too.
--
-- Side effect: username login (/auth/token with a profile name) resolves a name
-- to spec.email, so an external user's name now resolves to the directory mail
-- instead of the placeholder. Such users have no GoTrue password, so that
-- password grant fails either way.
--
-- Existing profiles of such users are backfilled at the end.
BEGIN;

-- The profile email for a user. In public rather than api so that PostgREST
-- does not expose it as an RPC.
CREATE OR REPLACE FUNCTION public.user_profile_email(p_app_meta JSONB, p_user_meta JSONB, p_email TEXT)
RETURNS TEXT
AS $$
  SELECT CASE
    WHEN NULLIF(p_app_meta->>'identity_source', '') IS NOT NULL
         AND NULLIF(p_user_meta->>'email', '') IS NOT NULL
      THEN p_user_meta->>'email'
    ELSE p_email
  END;
$$ LANGUAGE sql IMMUTABLE;

-- Same as 101 except for how spec.email is chosen.
CREATE OR REPLACE FUNCTION api.handle_new_user()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
DECLARE
  v_meta         JSONB := COALESCE(NEW.raw_user_meta_data, '{}'::jsonb);
  v_name         TEXT;
  v_display_name TEXT;
BEGIN
  IF NULLIF(v_meta->>'username', '') IS NOT NULL THEN
    -- Admin create (and any future directory sync): the caller chose the name,
    -- so use it as is and let the name check reject it if it is invalid.
    v_name := v_meta->>'username';
    v_display_name := v_name;
  ELSE
    -- SSO login: derive a name from the IdP claims.
    v_name := public.derive_user_profile_name(NEW.id, v_meta, NEW.email);
    v_display_name := COALESCE(
      NULLIF(btrim(v_meta->>'name'), ''),
      NULLIF(btrim(v_meta->>'full_name'), ''),
      NULLIF(btrim(v_meta->>'preferred_username'), ''),
      NULLIF(btrim(v_meta->>'user_name'), ''),
      NULLIF(v_meta->>'email', ''),
      NEW.email
    );
  END IF;

  INSERT INTO api.user_profiles (
    id,
    api_version,
    kind,
    metadata,
    spec
  )
  VALUES (
    NEW.id,
    'v1',
    'UserProfile',
    ROW(
      v_name,
      v_display_name,
      null,
      null,
      CURRENT_TIMESTAMP,
      CURRENT_TIMESTAMP,
      '{}'::json,
      '{}'::json
    )::api.metadata,
    ROW(public.user_profile_email(NEW.raw_app_meta_data, v_meta, NEW.email))::api.user_profile_spec
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Applies the email rule when a user becomes externally managed, which for
-- the GoTrue admin API happens in the UPDATE that follows the INSERT.
CREATE OR REPLACE FUNCTION api.handle_user_identity_source_set()
RETURNS TRIGGER
SECURITY DEFINER
AS $$
BEGIN
  UPDATE api.user_profiles
  SET spec = ROW(public.user_profile_email(NEW.raw_app_meta_data, NEW.raw_user_meta_data, NEW.email))::api.user_profile_spec
  WHERE id = NEW.id;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_auth_user_identity_source_set
  AFTER UPDATE OF raw_app_meta_data ON auth.users
  FOR EACH ROW
  WHEN (NULLIF(OLD.raw_app_meta_data->>'identity_source', '') IS NULL
        AND NULLIF(NEW.raw_app_meta_data->>'identity_source', '') IS NOT NULL)
  EXECUTE PROCEDURE api.handle_user_identity_source_set();

-- Backfill the users created before this migration.
UPDATE api.user_profiles p
SET spec = ROW(u.raw_user_meta_data->>'email')::api.user_profile_spec
FROM auth.users u
WHERE p.id = u.id
  AND NULLIF(u.raw_app_meta_data->>'identity_source', '') IS NOT NULL
  AND NULLIF(u.raw_user_meta_data->>'email', '') IS NOT NULL
  AND (p.spec).email IS DISTINCT FROM u.raw_user_meta_data->>'email';

COMMIT;
