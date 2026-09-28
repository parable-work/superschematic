-- Drop statements for schema: fixture-optimistic-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_stock_bump_version ON stock;
DROP FUNCTION IF EXISTS stock_bump_version();
DROP TRIGGER IF EXISTS trg_shelf_bump_version ON shelf;
DROP FUNCTION IF EXISTS shelf_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS stock CASCADE;
DROP TABLE IF EXISTS shelf CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
