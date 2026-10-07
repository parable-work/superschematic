-- Drop statements for schema: fixture-user-model-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS uq_user_role_grant_user_role CASCADE;
DROP INDEX IF EXISTS idx_session_user CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS user_role_grant CASCADE;
DROP TABLE IF EXISTS user_credential CASCADE;
DROP TABLE IF EXISTS "user" CASCADE;
DROP TABLE IF EXISTS "session" CASCADE;
DROP TABLE IF EXISTS role CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS citext CASCADE;
