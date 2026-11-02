-- Drop statements for schema: fixture-timers-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that write a table's change queue
DROP TRIGGER IF EXISTS trg_invoice_changes ON invoice;
DROP FUNCTION IF EXISTS invoice_changes();
DROP TRIGGER IF EXISTS trg_ticket_changes ON ticket;
DROP FUNCTION IF EXISTS ticket_changes();

-- Drop indexes
DROP INDEX IF EXISTS idx_ticket_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_ticket_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_ticket_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_ticket_change_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_invoice_deadline_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_deadline_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_deadline_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_deadline_queue_row_key_use_id_name CASCADE;
DROP INDEX IF EXISTS idx_invoice_deadline_queue_row_key CASCADE;
DROP INDEX IF EXISTS idx_invoice_change_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_change_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_change_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_invoice_change_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_invoice_change_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_ticket_timers_deadlines_ticket_name CASCADE;
DROP INDEX IF EXISTS idx_invoice_timers_deadlines_invoice_name CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_ticket_bump_version ON ticket;
DROP FUNCTION IF EXISTS ticket_bump_version();
DROP TRIGGER IF EXISTS trg_invoice_bump_version ON invoice;
DROP FUNCTION IF EXISTS invoice_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS ticket_deadline_queue CASCADE;
DROP TABLE IF EXISTS ticket_change_queue CASCADE;
DROP TABLE IF EXISTS invoice_deadline_queue CASCADE;
DROP TABLE IF EXISTS invoice_change_queue CASCADE;
DROP TABLE IF EXISTS ticket_timers_deadlines CASCADE;
DROP TABLE IF EXISTS ticket CASCADE;
DROP TABLE IF EXISTS invoice_timers_deadlines CASCADE;
DROP TABLE IF EXISTS invoice CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
