ALTER TABLE api.endpoints DROP COLUMN IF EXISTS status_sort_priority;

ALTER TABLE api.endpoints ADD COLUMN status_sort_priority integer
  GENERATED ALWAYS AS (
    CASE (status).phase
      WHEN 'Running'          THEN 0
      WHEN 'ModelDownloading' THEN 1
      WHEN 'Deploying'        THEN 2
      WHEN 'Pending'          THEN 3
      WHEN 'Paused'           THEN 4
      WHEN 'Failed'           THEN 5
      WHEN 'Deleting'         THEN 6
      WHEN 'Deleted'          THEN 7
      ELSE 9
    END
  ) STORED;
