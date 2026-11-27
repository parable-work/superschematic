-- Drop statements for schema: fixture-blueprint-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_job_dependencies_blockers_job_number CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_job_bump_version ON job;
DROP FUNCTION IF EXISTS job_bump_version();
DROP TRIGGER IF EXISTS trg_batch_bump_version ON batch;
DROP FUNCTION IF EXISTS batch_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS job_dependencies_blockers CASCADE;
DROP TABLE IF EXISTS job CASCADE;
DROP TABLE IF EXISTS batch CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
