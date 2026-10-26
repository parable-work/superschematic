-- Drop statements for schema: fixture-vectors-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_text_embedding_embedding CASCADE;
DROP INDEX IF EXISTS idx_passage_embedding CASCADE;
DROP INDEX IF EXISTS idx_passage_embedding_l2 CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS text_embedding CASCADE;
DROP TABLE IF EXISTS passage CASCADE;
DROP TABLE IF EXISTS ledger_line CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS vector CASCADE;
