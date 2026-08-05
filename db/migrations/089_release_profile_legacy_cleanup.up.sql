-- NEU-605: remove a rollback-only table left by the pre-merge preview chain.
-- Fresh installations never create it; preview installations reach this step at 088.
DROP TABLE IF EXISTS api.release_info_086_legacy_backup;
