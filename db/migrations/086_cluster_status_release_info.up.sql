-- NEU-605: expose the ReleaseInfo baseline/revision that last validated a
-- successfully reconciled Cluster without changing its desired workload spec.
ALTER TYPE api.cluster_status ADD ATTRIBUTE release_info JSONB;
ALTER TYPE api.cluster_status ADD ATTRIBUTE release_compatibility JSONB;
