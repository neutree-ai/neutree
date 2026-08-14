-- API key projects. The default project is deterministic per workspace so the
-- legacy-data backfill and a rolling upgrade can safely be repeated.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:migrate';

CREATE TYPE api.project_spec AS (
    description TEXT,
    disabled BOOLEAN,
    is_default BOOLEAN
);
CREATE TYPE api.project_status AS (
    phase TEXT,
    error_message TEXT
);
CREATE TABLE api.projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_version TEXT NOT NULL DEFAULT 'v1',
    kind TEXT NOT NULL DEFAULT 'Project',
    metadata api.metadata NOT NULL,
    spec api.project_spec NOT NULL DEFAULT ROW('', FALSE, FALSE)::api.project_spec,
    status api.project_status NOT NULL DEFAULT ROW('Created', NULL)::api.project_status
);

CREATE OR REPLACE FUNCTION api.default_project_id(p_workspace TEXT)
RETURNS UUID LANGUAGE sql IMMUTABLE AS $$
    SELECT (substr(md5('neutree-default-project:' || p_workspace), 1, 8) || '-' ||
            substr(md5('neutree-default-project:' || p_workspace), 9, 4) || '-' ||
            substr(md5('neutree-default-project:' || p_workspace), 13, 4) || '-' ||
            substr(md5('neutree-default-project:' || p_workspace), 17, 4) || '-' ||
            substr(md5('neutree-default-project:' || p_workspace), 21, 12))::uuid;
$$;

CREATE OR REPLACE FUNCTION api.ensure_default_project(p_workspace TEXT)
RETURNS UUID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_id UUID := api.default_project_id(p_workspace);
BEGIN
    INSERT INTO api.projects (id, metadata, spec)
    VALUES (v_id,
      ROW('default', 'Default', p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::jsonb, '{}'::jsonb)::api.metadata,
      ROW('Default project for migrated API keys', FALSE, TRUE)::api.project_spec)
    ON CONFLICT (id) DO NOTHING;
    RETURN v_id;
END;
$$;

ALTER TABLE api.api_keys ADD COLUMN project_id UUID;
ALTER TABLE api.api_keys ADD COLUMN description TEXT NOT NULL DEFAULT '';

INSERT INTO api.projects (id, metadata, spec)
SELECT api.ensure_default_project((k.metadata).workspace),
       ROW('default', 'Default', (k.metadata).workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::jsonb, '{}'::jsonb)::api.metadata,
       ROW('Default project for migrated API keys', FALSE, TRUE)::api.project_spec
FROM api.api_keys k
WHERE (k.metadata).workspace IS NOT NULL AND (k.metadata).workspace <> ''
ON CONFLICT (id) DO NOTHING;

UPDATE api.api_keys k
SET project_id = api.ensure_default_project((k.metadata).workspace)
WHERE k.project_id IS NULL;
ALTER TABLE api.api_keys ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE api.api_keys ADD CONSTRAINT api_keys_project_id_fkey FOREIGN KEY (project_id) REFERENCES api.projects(id) ON DELETE RESTRICT;

DROP INDEX IF EXISTS api_key_name_workspace_unique_idx;
CREATE UNIQUE INDEX projects_workspace_name_unique_idx ON api.projects (((metadata).workspace), ((metadata).name)) WHERE (metadata).deletion_timestamp IS NULL;
CREATE UNIQUE INDEX api_keys_project_name_unique_idx ON api.api_keys (project_id, ((metadata).name)) WHERE (metadata).deletion_timestamp IS NULL;
CREATE INDEX api_keys_project_id_idx ON api.api_keys (project_id);

CREATE OR REPLACE FUNCTION api.validate_api_key_project(p_project_id UUID, p_workspace TEXT, p_require_enabled BOOLEAN DEFAULT TRUE)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_workspace TEXT; v_disabled BOOLEAN;
BEGIN
  SELECT (metadata).workspace, COALESCE((spec).disabled, FALSE) INTO v_workspace, v_disabled FROM api.projects WHERE id = p_project_id AND (metadata).deletion_timestamp IS NULL;
  IF NOT FOUND THEN RAISE EXCEPTION 'project not found'; END IF;
  IF v_workspace IS DISTINCT FROM p_workspace THEN RAISE EXCEPTION 'project belongs to a different workspace'; END IF;
  IF p_require_enabled AND v_disabled THEN RAISE EXCEPTION 'project is disabled'; END IF;
END;
$$;

ALTER TABLE api.projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY "project read policy" ON api.projects FOR SELECT USING (api.has_permission(auth.uid(), 'project:read', (metadata).workspace));
CREATE POLICY "project create policy" ON api.projects FOR INSERT WITH CHECK (api.has_permission(auth.uid(), 'project:create', (metadata).workspace));
CREATE POLICY "project update policy" ON api.projects FOR UPDATE USING (api.has_permission(auth.uid(), 'project:update', (metadata).workspace)) WITH CHECK (api.has_permission(auth.uid(), 'project:update', (metadata).workspace));
CREATE POLICY "project delete policy" ON api.projects FOR DELETE USING (api.has_permission(auth.uid(), 'project:delete', (metadata).workspace));

CREATE OR REPLACE FUNCTION api.prevent_project_delete()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE v_count BIGINT;
BEGIN
  IF COALESCE((OLD.spec).is_default, FALSE) THEN RAISE EXCEPTION 'default project cannot be deleted'; END IF;
  SELECT count(*) INTO v_count FROM api.api_keys WHERE project_id = OLD.id AND (metadata).deletion_timestamp IS NULL;
  IF v_count > 0 THEN RAISE EXCEPTION 'project has % API keys', v_count; END IF;
  RETURN OLD;
END;
$$;
CREATE TRIGGER prevent_project_delete BEFORE DELETE ON api.projects FOR EACH ROW EXECUTE FUNCTION api.prevent_project_delete();

CREATE OR REPLACE FUNCTION api.create_project(p_workspace TEXT, p_name TEXT, p_description TEXT DEFAULT '')
RETURNS api.projects LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.projects;
BEGIN
  IF NOT api.has_permission(auth.uid(), 'project:create', p_workspace) THEN RAISE EXCEPTION 'permission denied'; END IF;
  INSERT INTO api.projects (metadata, spec)
  VALUES (ROW(p_name, p_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::jsonb, '{}'::jsonb)::api.metadata,
          ROW(COALESCE(p_description, ''), FALSE, FALSE)::api.project_spec)
  RETURNING * INTO v_result;
  RETURN v_result;
END;
$$;

CREATE OR REPLACE FUNCTION api.update_project(p_id UUID, p_name TEXT DEFAULT NULL, p_description TEXT DEFAULT NULL, p_disabled BOOLEAN DEFAULT NULL)
RETURNS api.projects LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.projects; v_workspace TEXT;
BEGIN
  SELECT (metadata).workspace INTO v_workspace FROM api.projects WHERE id = p_id;
  IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'project:update', v_workspace) THEN RAISE EXCEPTION 'permission denied'; END IF;
  UPDATE api.projects SET
    metadata = ROW(COALESCE(p_name, (metadata).name), COALESCE(p_name, (metadata).display_name), (metadata).workspace, (metadata).deletion_timestamp, (metadata).creation_timestamp, CURRENT_TIMESTAMP, (metadata).labels, (metadata).annotations)::api.metadata,
    spec = ROW(COALESCE(p_description, (spec).description), COALESCE(p_disabled, (spec).disabled), (spec).is_default)::api.project_spec
  WHERE id = p_id RETURNING * INTO v_result;
  RETURN v_result;
END;
$$;

CREATE OR REPLACE FUNCTION api.delete_project(p_id UUID)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_workspace TEXT;
BEGIN
  SELECT (metadata).workspace INTO v_workspace FROM api.projects WHERE id = p_id;
  IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'project:delete', v_workspace) THEN RAISE EXCEPTION 'permission denied'; END IF;
  DELETE FROM api.projects WHERE id = p_id;
END;
$$;

-- Project changes are accepted only through these RPCs so metadata and
-- lifecycle constraints cannot be bypassed with a raw PATCH.
CREATE OR REPLACE FUNCTION api.move_api_keys(p_api_key_ids UUID[], p_target_project_id UUID)
RETURNS SETOF api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_workspace TEXT; v_count INTEGER;
BEGIN
  IF p_api_key_ids IS NULL OR cardinality(p_api_key_ids) = 0 THEN RAISE EXCEPTION 'no API keys selected'; END IF;
  SELECT (metadata).workspace INTO v_workspace FROM api.api_keys WHERE id = p_api_key_ids[1];
  IF NOT FOUND OR NOT api.has_permission(auth.uid(), 'project:migrate', v_workspace) THEN RAISE EXCEPTION 'permission denied'; END IF;
  SELECT count(*) INTO v_count FROM api.api_keys WHERE id = ANY(p_api_key_ids) AND (metadata).workspace = v_workspace;
  IF v_count <> cardinality(p_api_key_ids) THEN RAISE EXCEPTION 'API keys must belong to one workspace'; END IF;
  PERFORM api.validate_api_key_project(p_target_project_id, v_workspace, TRUE);
  IF EXISTS (SELECT 1 FROM api.api_keys s JOIN api.api_keys d ON d.project_id = p_target_project_id AND (d.metadata).name = (s.metadata).name AND d.id <> s.id WHERE s.id = ANY(p_api_key_ids)) THEN RAISE EXCEPTION 'target project already has an API key with the same name'; END IF;
  RETURN QUERY UPDATE api.api_keys SET project_id = p_target_project_id WHERE id = ANY(p_api_key_ids) RETURNING *;
END;
$$;

DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB);
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT, p_name TEXT, p_quota INTEGER, p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL, p_limits JSONB DEFAULT NULL,
    p_project_id UUID DEFAULT NULL, p_description TEXT DEFAULT ''
) RETURNS api.api_keys SECURITY DEFINER AS $$
DECLARE p_user_id UUID := auth.uid(); v_key_id UUID; v_key_value TEXT; v_quota BIGINT; v_result api.api_keys;
BEGIN
  IF p_workspace IS NULL OR p_workspace = '' THEN RAISE EXCEPTION 'workspace is required to create an API key'; END IF;
  IF NOT api.has_permission(p_user_id, 'project:read', p_workspace) THEN RAISE EXCEPTION 'permission denied'; END IF;
  IF p_project_id IS NULL THEN p_project_id := api.ensure_default_project(p_workspace); END IF;
  PERFORM api.validate_api_key_project(p_project_id, p_workspace, TRUE);
  PERFORM api.validate_api_key_limits(p_limits);
  v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);
  v_key_id := gen_random_uuid(); v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);
  INSERT INTO api.api_keys (id, api_version, kind, metadata, spec, status, user_id, project_id, description)
  VALUES (v_key_id, 'v1', 'ApiKey', ROW(p_name, COALESCE(p_display_name,p_name), p_workspace, NULL, CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'{}'::jsonb,'{}'::jsonb)::api.metadata, ROW(v_quota,p_expires_in,p_limits)::api.api_key_spec, ROW('Pending',CURRENT_TIMESTAMP,NULL,v_key_value,0,CURRENT_TIMESTAMP,NULL)::api.api_key_status, p_user_id, p_project_id, COALESCE(p_description,'')) RETURNING * INTO v_result;
  RETURN v_result;
END;
$$ LANGUAGE plpgsql;

SELECT api.update_admin_permissions();
