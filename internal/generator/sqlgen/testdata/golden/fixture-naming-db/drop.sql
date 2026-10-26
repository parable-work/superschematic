-- Drop statements for schema: fixture-naming-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS "idx_lineItem_postedOn" CASCADE;
DROP INDEX IF EXISTS "uq_lineItem_external" CASCADE;

-- Drop history capture triggers and functions
DROP TRIGGER IF EXISTS "trg_ledgerNote_bump_version" ON "ledgerNote";
DROP TRIGGER IF EXISTS "trg_ledgerNote_capture_history_write" ON "ledgerNote";
DROP TRIGGER IF EXISTS "trg_ledgerNote_capture_history_delete" ON "ledgerNote";
DROP FUNCTION IF EXISTS "ledgerNote_capture_history"();

-- Drop history indexes
DROP INDEX IF EXISTS "idx_ledgerNote_history_id_recorded" CASCADE;
DROP INDEX IF EXISTS "uq_ledgerNote_history_id_version" CASCADE;

-- Drop history tables
DROP TABLE IF EXISTS "ledgerNote_history" CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS "ledgerNote" CASCADE;
DROP TABLE IF EXISTS "lineItem" CASCADE;
DROP TABLE IF EXISTS "budgetCategory" CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS pg_trgm CASCADE;
