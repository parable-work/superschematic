-- Drop statements for schema: fixture-reactions-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that write a table's change queue
DROP TRIGGER IF EXISTS trg_project_changes ON project;
DROP FUNCTION IF EXISTS project_changes();
DROP TRIGGER IF EXISTS trg_task_changes ON task;
DROP FUNCTION IF EXISTS task_changes();

-- Drop indexes
DROP INDEX IF EXISTS idx_task_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_task_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_task_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_task_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_task_change_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_project_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_project_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_project_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_project_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_project_change_queue_claimed_key CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_task_bump_version ON task;
DROP FUNCTION IF EXISTS task_bump_version();
DROP TRIGGER IF EXISTS trg_project_bump_version ON project;
DROP FUNCTION IF EXISTS project_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS task_change_queue CASCADE;
DROP TABLE IF EXISTS project_change_queue CASCADE;
DROP TABLE IF EXISTS task CASCADE;
DROP TABLE IF EXISTS project CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
