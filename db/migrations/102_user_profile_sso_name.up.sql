-- NEU-656: give users created by an SSO login a valid profile name.
--
-- api.handle_new_user() named the profile after user_metadata.username, which
-- only neutree's own admin-create path sets. GoTrue custom OIDC/OAuth providers
-- store the IdP claims (preferred_username, user_name, name, email, sub, ...)
-- and never a username, so the name was NULL, api.validate_metadata_name()
-- raised, and the auth.users insert -- the login -- rolled back.
--
-- metadata.name is neutree's own identifier: Kubernetes-style, unique across
-- user_profiles, chosen once when the user is created and never touched by a
-- later login (GoTrue only UPDATEs auth.users then, and the trigger is AFTER
-- INSERT only). metadata.display_name carries the human-readable IdP name.
--
-- An explicit username keeps the old behaviour verbatim, including rejection
-- by the name check when it is invalid.

-- Derives a profile name for a user that has no explicit username. Lives in
-- public rather than api because PostgREST only exposes the api schema, and
-- this must not become a callable RPC. Not SECURITY DEFINER: its only caller,
-- api.handle_new_user(), already runs as the definer.
--
--   1. Take the first non-empty of preferred_username, user_name and the local
--      part of the email; lowercase it, turn every character outside
--      [a-z0-9.-] into '-', collapse runs of '-', and trim '-' and '.' from both
--      ends. It is cut to 56 characters so the 7-character suffix of step 2
--      still fits in 63.
--   2. If it is taken (by any profile, soft-deleted ones included, since the
--      unique index covers them) or is 'admin', append '-' and 6 hex digits of
--      md5(user id).
--   3. If nothing usable is left, or the suffixed name is still taken, fall
--      back to 'u-' and the first 8 hex digits of the user id.
CREATE OR REPLACE FUNCTION public.derive_user_profile_name(p_user_id UUID, p_meta JSONB, p_email TEXT)
RETURNS TEXT
AS $$
DECLARE
    v_fallback TEXT := 'u-' || left(replace(p_user_id::text, '-', ''), 8);
    v_email    TEXT := COALESCE(NULLIF(p_meta->>'email', ''), p_email);
    v_base     TEXT;
    v_name     TEXT;
BEGIN
    v_base := COALESCE(
        NULLIF(btrim(p_meta->>'preferred_username'), ''),
        NULLIF(btrim(p_meta->>'user_name'), ''),
        NULLIF(split_part(v_email, '@', 1), '')
    );

    IF v_base IS NOT NULL THEN
        v_base := regexp_replace(lower(v_base), '[^a-z0-9.-]', '-', 'g');
        v_base := regexp_replace(v_base, '-{2,}', '-', 'g');
        v_base := btrim(v_base, '-.');
        -- Cutting can expose a '-' or '.' at the end again.
        v_base := btrim(left(v_base, 56), '-.');
    END IF;

    IF v_base IS NULL OR v_base !~ '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$' THEN
        RETURN v_fallback;
    END IF;

    v_name := v_base;

    IF v_name = 'admin'
       OR EXISTS (SELECT 1 FROM api.user_profiles WHERE (metadata).name = v_name) THEN
        v_name := v_base || '-' || left(md5(p_user_id::text), 6);

        IF EXISTS (SELECT 1 FROM api.user_profiles WHERE (metadata).name = v_name) THEN
            v_name := v_fallback;
        END IF;
    END IF;

    RETURN v_name;
END;
$$ LANGUAGE plpgsql;

-- Same as 019 except for how metadata.name and metadata.display_name are chosen.
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
