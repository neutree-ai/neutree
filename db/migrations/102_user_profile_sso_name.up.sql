-- SSO users have no username in user_metadata, only IdP claims, so derive
-- metadata.name from those. The name is set once at insert and never changes.

-- In public, not api, so PostgREST does not expose it as an RPC. The base is
-- cut to 56 characters so the 7-character suffix still fits in 63.
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
    -- The caller chose the name; the name check still rejects an invalid one.
    v_name := v_meta->>'username';
    v_display_name := v_name;
  ELSE
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
