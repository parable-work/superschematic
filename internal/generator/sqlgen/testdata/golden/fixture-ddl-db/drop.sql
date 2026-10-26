-- Drop statements for schema: fixture-ddl-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Undo the raw SQL the schema declares (@rawSql), last piece first
DROP MATERIALIZED VIEW IF EXISTS category_totals;

-- Drop indexes
DROP INDEX IF EXISTS idx_line_item_posted_at CASCADE;
DROP INDEX IF EXISTS uq_line_item_external CASCADE;
DROP INDEX IF EXISTS uq_category_label_lower CASCADE;
DROP INDEX IF EXISTS idx_category_active_kind CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS line_item CASCADE;
DROP TABLE IF EXISTS category CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
