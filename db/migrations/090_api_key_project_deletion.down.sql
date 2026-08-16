BEGIN;

DELETE FROM api.api_key_project_history WHERE to_project_id IS NULL;

ALTER TABLE api.api_key_project_history
    DROP CONSTRAINT api_key_project_history_to_project_id_fkey,
    ALTER COLUMN to_project_id SET NOT NULL,
    ADD CONSTRAINT api_key_project_history_to_project_id_fkey
        FOREIGN KEY (to_project_id)
        REFERENCES api.api_key_projects(id)
        ON DELETE RESTRICT;

COMMIT;
