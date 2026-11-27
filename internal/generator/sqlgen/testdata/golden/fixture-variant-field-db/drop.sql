-- Drop statements for schema: fixture-variant-field-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS step CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
