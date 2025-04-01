-- ----------------------
-- Create a user for the service
-- ----------------------
CREATE USER service_role LOGIN CREATEROLE CREATEDB REPLICATION BYPASSRLS;

-- ----------------------
-- Create an admin user for the auth service
-- ----------------------
CREATE USER auth_admin NOINHERIT CREATEROLE LOGIN NOREPLICATION PASSWORD 'auth_admin_password';
CREATE SCHEMA IF NOT EXISTS auth AUTHORIZATION auth_admin;
GRANT CREATE ON DATABASE postgres TO auth_admin;
ALTER USER auth_admin SET search_path = 'auth';

-- ----------------------
-- Create an admin user for the api service
-- ----------------------
CREATE SCHEMA IF NOT EXISTS api;
CREATE ROLE api_user nologin;

GRANT USAGE ON SCHEMA api TO api_user;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA api TO api_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA api TO api_user;

ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT ALL PRIVILEGES ON TABLES TO api_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT ALL PRIVILEGES ON SEQUENCES TO api_user;

GRANT USAGE ON SCHEMA api TO service_role;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA api TO service_role;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA api TO service_role;

ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT ALL PRIVILEGES ON TABLES TO service_role;
ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT ALL PRIVILEGES ON SEQUENCES TO service_role;

-- ----------------------
-- Create extensions
-- ----------------------
CREATE EXTENSION IF NOT EXISTS pgcrypto;
