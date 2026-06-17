ALTER TABLE api.endpoints DROP COLUMN IF EXISTS status_sort_priority;

ALTER TABLE api.endpoints ADD COLUMN status_sort_priority integer
  GENERATED ALWAYS AS (
    CASE (status).phase
      WHEN 'Running'   THEN 0
      WHEN 'Deploying'  THEN 1
      WHEN 'Pending'    THEN 2
      WHEN 'Paused'     THEN 3
      WHEN 'Failed'     THEN 4
      WHEN 'Deleting'   THEN 5
      WHEN 'Deleted'    THEN 6
      ELSE 9
    END
  ) STORED;

ALTER TYPE api.endpoint_status DROP ATTRIBUTE model_download_completed_hash;
