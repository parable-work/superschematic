-- Drop statements for schema: fixture-agents-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_session_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_session_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_session_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_session_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_session_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_job_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_job_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_job_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_job_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_job_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_workspace_budget_meters_workspace_meter CASCADE;
DROP INDEX IF EXISTS idx_workspace_budget_holds_workspace_hold CASCADE;
DROP INDEX IF EXISTS idx_workspace_budget_buckets_workspace_bucket CASCADE;
DROP INDEX IF EXISTS idx_job_retries_attempts_job_number CASCADE;
DROP INDEX IF EXISTS idx_job_lease_directives_job_number CASCADE;
DROP INDEX IF EXISTS idx_account_budget_meters_account_meter CASCADE;
DROP INDEX IF EXISTS idx_account_budget_holds_account_hold CASCADE;
DROP INDEX IF EXISTS idx_account_budget_buckets_account_bucket CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_workspace_bump_version ON workspace;
DROP FUNCTION IF EXISTS workspace_bump_version();
DROP TRIGGER IF EXISTS trg_session_bump_version ON "session";
DROP FUNCTION IF EXISTS session_bump_version();
DROP TRIGGER IF EXISTS trg_job_bump_version ON job;
DROP FUNCTION IF EXISTS job_bump_version();
DROP TRIGGER IF EXISTS trg_account_bump_version ON account;
DROP FUNCTION IF EXISTS account_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS session_deadline_queue CASCADE;
DROP TABLE IF EXISTS job_deadline_queue CASCADE;
DROP TABLE IF EXISTS workspace_budget_meters CASCADE;
DROP TABLE IF EXISTS workspace_budget_holds CASCADE;
DROP TABLE IF EXISTS workspace_budget_buckets CASCADE;
DROP TABLE IF EXISTS workspace CASCADE;
DROP TABLE IF EXISTS "session" CASCADE;
DROP TABLE IF EXISTS job_retries_attempts CASCADE;
DROP TABLE IF EXISTS job_lease_directives CASCADE;
DROP TABLE IF EXISTS job CASCADE;
DROP TABLE IF EXISTS account_budget_meters CASCADE;
DROP TABLE IF EXISTS account_budget_holds CASCADE;
DROP TABLE IF EXISTS account_budget_buckets CASCADE;
DROP TABLE IF EXISTS account CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
