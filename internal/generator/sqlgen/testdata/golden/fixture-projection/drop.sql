-- Drop statements for schema: fixture-projection
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop projection views first (they read the tables below)
DROP VIEW IF EXISTS app.preferences;

-- Drop indexes
DROP INDEX IF EXISTS idx_preference_account_slot_key CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS preference CASCADE;
DROP TABLE IF EXISTS channel CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS citext CASCADE;
