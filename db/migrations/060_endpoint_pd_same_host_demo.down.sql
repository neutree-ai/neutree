ALTER TYPE api.endpoint_status DROP ATTRIBUTE replicas;
ALTER TYPE api.endpoint_status DROP ATTRIBUTE placement;
ALTER TYPE api.endpoint_status DROP ATTRIBUTE strategy;

ALTER TYPE api.endpoint_spec DROP ATTRIBUTE roles;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE placement;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE strategy;
