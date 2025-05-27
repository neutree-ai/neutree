-- ----------------------
-- Create an kong_admin user for the kong gateway
-- ----------------------

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_user WHERE usename = 'kong_admin')
  THEN  
    CREATE USER kong_admin NOINHERIT LOGIN NOREPLICATION PASSWORD 'kong_admin_password';
  END IF;
END $$;

CREATE SCHEMA IF NOT EXISTS kong AUTHORIZATION kong_admin;
ALTER USER kong_admin SET search_path = 'kong';