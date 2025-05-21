-- ----------------------
-- Create an kong database for the kong gateway
-- ----------------------

CREATE DATABASE kong;
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_user WHERE usename = 'kong_admin') THEN
    CREATE USER kong_admin 
      NOINHERIT
      LOGIN
      NOREPLICATION
      PASSWORD 'kong_admin_password';
  END IF;
  GRANT ALL PRIVILEGES ON DATABASE kong TO kong_admin;
END $$;