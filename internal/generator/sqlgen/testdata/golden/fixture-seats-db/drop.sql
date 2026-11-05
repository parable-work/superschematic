-- Drop statements for schema: fixture-seats-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_sandbox_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_sandbox_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_sandbox_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_sandbox_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_sandbox_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_problem_seats_occupants_problem_number CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_sandbox_bump_version ON sandbox;
DROP FUNCTION IF EXISTS sandbox_bump_version();
DROP TRIGGER IF EXISTS trg_problem_bump_version ON problem;
DROP FUNCTION IF EXISTS problem_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS sandbox_deadline_queue CASCADE;
DROP TABLE IF EXISTS sandbox CASCADE;
DROP TABLE IF EXISTS problem_seats_occupants CASCADE;
DROP TABLE IF EXISTS problem CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
