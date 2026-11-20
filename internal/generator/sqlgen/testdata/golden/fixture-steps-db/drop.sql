-- Drop statements for schema: fixture-steps-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_step_dependencies_blockers_step_number CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_step_bump_version ON step;
DROP FUNCTION IF EXISTS step_bump_version();
DROP TRIGGER IF EXISTS trg_batch_bump_version ON batch;
DROP FUNCTION IF EXISTS batch_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS step_dependencies_blockers CASCADE;
DROP TABLE IF EXISTS step CASCADE;
DROP TABLE IF EXISTS batch CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
