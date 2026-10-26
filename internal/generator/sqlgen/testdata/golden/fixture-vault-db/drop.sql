-- Drop statements for schema: fixture-vault-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS plaid_item CASCADE;
DROP TABLE IF EXISTS api_credential CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS pg_trgm CASCADE;
