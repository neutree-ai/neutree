-- Endpoint lists sort by status_sort_priority, then newest first. The priority
-- is a coarse tier so that the newest endpoint that still needs attention is on
-- the first page:
--   0  active: Running, Failed, Deploying, ModelDownloading, Pending, and an
--      endpoint the controller has not reported a phase for yet
--   1  Paused
--   2  Deleting, Deleted
ALTER TABLE api.endpoints DROP COLUMN IF EXISTS status_sort_priority;

ALTER TABLE api.endpoints ADD COLUMN status_sort_priority integer
  GENERATED ALWAYS AS (
    CASE
      WHEN (status).phase = 'Paused'                  THEN 1
      WHEN (status).phase IN ('Deleting', 'Deleted')  THEN 2
      ELSE 0
    END
  ) STORED;
