-- Drop statements for schema: fixture-approvals-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_task_approvals_sign_offs_task_number CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_task_bump_version ON task;
DROP FUNCTION IF EXISTS task_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS task_approvals_sign_offs CASCADE;
DROP TABLE IF EXISTS task CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
