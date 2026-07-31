-- NEU-580: per-upstream status detail on external endpoints.
-- A single unresolvable upstream no longer fails the whole endpoint, so the
-- status needs room to report which upstreams are unavailable and why.
ALTER TYPE api.external_endpoint_status ADD ATTRIBUTE upstream_status JSONB;
