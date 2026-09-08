-- Drop statements for schema: fixture-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_tenant_user_tenant_display_name CASCADE;
DROP INDEX IF EXISTS uq_tenant_slug CASCADE;

-- Drop history capture triggers and functions
DROP TRIGGER IF EXISTS trg_tenant_user_capture_history_write ON tenant_user;
DROP TRIGGER IF EXISTS trg_tenant_user_capture_history_delete ON tenant_user;
DROP FUNCTION IF EXISTS tenant_user_capture_history();
DROP TRIGGER IF EXISTS trg_tenant_capture_history_write ON tenant;
DROP TRIGGER IF EXISTS trg_tenant_capture_history_delete ON tenant;
DROP FUNCTION IF EXISTS tenant_capture_history();

-- Drop history indexes
DROP INDEX IF EXISTS idx_tenant_user_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_tenant_user_history_id_version CASCADE;
DROP INDEX IF EXISTS idx_tenant_history_id_recorded CASCADE;
DROP INDEX IF EXISTS uq_tenant_history_id_version CASCADE;

-- Drop history tables
DROP TABLE IF EXISTS tenant_user_history CASCADE;
DROP TABLE IF EXISTS tenant_history CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS tenant_user CASCADE;
DROP TABLE IF EXISTS tenant CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS citext CASCADE;
-- DROP EXTENSION IF EXISTS pg_trgm CASCADE;
