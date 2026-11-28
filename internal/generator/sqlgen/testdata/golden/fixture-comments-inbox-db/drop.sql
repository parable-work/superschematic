-- Drop statements for schema: fixture-comments-inbox-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that keep an @audience table's
-- effective audience
DROP TRIGGER IF EXISTS trg_ticket_audience ON ticket;
DROP FUNCTION IF EXISTS ticket_audience();

-- Drop indexes
DROP INDEX IF EXISTS idx_ticket_event_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_event_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_event_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_ticket_event_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_ticket_event_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_notice_work_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_notice_work_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_notice_work_queue_updated_at CASCADE;
DROP INDEX IF EXISTS uq_user_role_grant_user_role CASCADE;
DROP INDEX IF EXISTS idx_user_api_key_user CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_reads_ticket_subject CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_reactions_ticket_reaction CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_messages_ticket_message CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_messages_blocks CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_follows_ticket_follow CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_edits_ticket_edit CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_drafts_ticket_chunk CASCADE;
DROP INDEX IF EXISTS idx_ticket_comments_deliveries_ticket_delivery CASCADE;
DROP INDEX IF EXISTS idx_session_user CASCADE;
DROP INDEX IF EXISTS idx_notice_route_event_kind CASCADE;
DROP INDEX IF EXISTS uq_notice_reference CASCADE;
DROP INDEX IF EXISTS uq_notice_event CASCADE;
DROP INDEX IF EXISTS idx_notice_waiting CASCADE;
DROP INDEX IF EXISTS idx_delivery_notice CASCADE;
DROP INDEX IF EXISTS idx_delivery_provider_message CASCADE;
DROP INDEX IF EXISTS idx_delivery_ended CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_ticket_comments_conversations_bump_version ON ticket_comments_conversations;
DROP FUNCTION IF EXISTS ticket_comments_conversations_bump_version();
DROP TRIGGER IF EXISTS trg_ticket_bump_version ON ticket;
DROP FUNCTION IF EXISTS ticket_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS ticket_event_queue CASCADE;
DROP TABLE IF EXISTS notice_work_queue CASCADE;
DROP TABLE IF EXISTS user_role_grant CASCADE;
DROP TABLE IF EXISTS user_credential CASCADE;
DROP TABLE IF EXISTS user_api_key CASCADE;
DROP TABLE IF EXISTS ticket_comments_reads CASCADE;
DROP TABLE IF EXISTS ticket_comments_reactions CASCADE;
DROP TABLE IF EXISTS ticket_comments_messages CASCADE;
DROP TABLE IF EXISTS ticket_comments_follows CASCADE;
DROP TABLE IF EXISTS ticket_comments_edits CASCADE;
DROP TABLE IF EXISTS ticket_comments_drafts CASCADE;
DROP TABLE IF EXISTS ticket_comments_deliveries CASCADE;
DROP TABLE IF EXISTS ticket_comments_conversations CASCADE;
DROP TABLE IF EXISTS ticket CASCADE;
DROP TABLE IF EXISTS "session" CASCADE;
DROP TABLE IF EXISTS role CASCADE;
DROP TABLE IF EXISTS notice_route CASCADE;
DROP TABLE IF EXISTS notice CASCADE;
DROP TABLE IF EXISTS delivery CASCADE;
DROP TABLE IF EXISTS account CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
-- DROP EXTENSION IF EXISTS citext CASCADE;
