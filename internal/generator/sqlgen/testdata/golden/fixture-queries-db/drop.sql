-- Drop statements for schema: fixture-queries-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop projection views first (they read the tables below)
DROP VIEW IF EXISTS line_list;

-- Drop indexes
DROP INDEX IF EXISTS idx_line_item_date CASCADE;
DROP INDEX IF EXISTS idx_line_item_category CASCADE;
DROP INDEX IF EXISTS idx_category_parent CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS transfer_group CASCADE;
DROP TABLE IF EXISTS line_item CASCADE;
DROP TABLE IF EXISTS category CASCADE;
DROP TABLE IF EXISTS account CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
