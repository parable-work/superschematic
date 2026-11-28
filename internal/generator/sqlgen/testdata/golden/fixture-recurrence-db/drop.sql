-- Drop statements for schema: fixture-recurrence-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that write a recurring table's change
-- queue on a write of its relative recurrence's date
DROP TRIGGER IF EXISTS trg_contract_filing_dates ON contract;
DROP FUNCTION IF EXISTS contract_filing_dates();

-- Drop the triggers and functions that write a table's change queue
DROP TRIGGER IF EXISTS trg_filing_changes ON filing;
DROP FUNCTION IF EXISTS filing_changes();

-- Drop indexes
DROP INDEX IF EXISTS idx_filing_event_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_filing_event_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_filing_event_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_filing_event_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_filing_event_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_filing_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_filing_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_filing_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_filing_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_filing_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_filing_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_filing_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_filing_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_filing_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_filing_change_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_filing_recurrence_occurrences_filing_key CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_filing_bump_version ON filing;
DROP FUNCTION IF EXISTS filing_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS filing_event_queue CASCADE;
DROP TABLE IF EXISTS filing_deadline_queue CASCADE;
DROP TABLE IF EXISTS filing_change_queue CASCADE;
DROP TABLE IF EXISTS holiday CASCADE;
DROP TABLE IF EXISTS filing_recurrence_occurrences CASCADE;
DROP TABLE IF EXISTS filing CASCADE;
DROP TABLE IF EXISTS contract CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
