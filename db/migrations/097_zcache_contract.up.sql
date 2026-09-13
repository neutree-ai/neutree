-- Persist the zcache PoC fields introduced in the API resource contracts.
ALTER TYPE api.cluster_spec ADD ATTRIBUTE zcache json;
ALTER TYPE api.cluster_status ADD ATTRIBUTE zcache json;
ALTER TYPE api.endpoint_spec ADD ATTRIBUTE zcache json;
