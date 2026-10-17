-- Restore the 101 behaviour: spec.email is always auth.users.email.
--
-- The profiles backfilled or set by 103 are deliberately left alone: the
-- directory email is the right value, and reversing it would only restore
-- placeholders.
BEGIN;

DROP TRIGGER IF EXISTS on_auth_user_identity_source_set ON auth.users;
DROP FUNCTION IF EXISTS api.handle_user_identity_source_set();

-- Same as 101.
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
    ROW(NEW.email)::api.user_profile_spec
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP FUNCTION IF EXISTS public.user_profile_email(JSONB, JSONB, TEXT);

COMMIT;
