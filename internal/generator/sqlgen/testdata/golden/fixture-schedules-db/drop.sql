-- Drop statements for schema: fixture-schedules-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that write a table's change queue
DROP TRIGGER IF EXISTS trg_problem_changes ON problem;
DROP FUNCTION IF EXISTS problem_changes();

-- Drop indexes
DROP INDEX IF EXISTS idx_problem_event_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_problem_event_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_problem_event_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_problem_event_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_problem_event_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_problem_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_problem_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_problem_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_problem_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_problem_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_problem_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_problem_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_problem_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_problem_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_problem_change_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_problem_schedules_schedules_problem_name CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_problem_bump_version ON problem;
DROP FUNCTION IF EXISTS problem_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS problem_event_queue CASCADE;
DROP TABLE IF EXISTS problem_deadline_queue CASCADE;
DROP TABLE IF EXISTS problem_change_queue CASCADE;
DROP TABLE IF EXISTS problem_schedules_schedules CASCADE;
DROP TABLE IF EXISTS problem CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
