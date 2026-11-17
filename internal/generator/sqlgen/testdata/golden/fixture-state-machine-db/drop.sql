-- Drop statements for schema: fixture-state-machine-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that guard a @stateMachine's column
DROP TRIGGER IF EXISTS trg_ticket_account_status_transition_guard ON ticket;
DROP FUNCTION IF EXISTS ticket_account_status_transition_guard();
DROP TRIGGER IF EXISTS trg_ticket_escalation_transition_guard ON ticket;
DROP FUNCTION IF EXISTS ticket_escalation_transition_guard();
DROP TRIGGER IF EXISTS trg_ticket_review_transition_guard ON ticket;
DROP FUNCTION IF EXISTS ticket_review_transition_guard();
DROP TRIGGER IF EXISTS trg_ticket_state_transition_guard ON ticket;
DROP FUNCTION IF EXISTS ticket_state_transition_guard();

-- Drop indexes

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS ticket CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
