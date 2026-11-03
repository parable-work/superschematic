-- Drop statements for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop the triggers and functions that enqueue on a write
DROP TRIGGER IF EXISTS trg_order_line_enqueue_order_changed ON order_line;
DROP FUNCTION IF EXISTS order_line_enqueue_order_changed();

-- Drop indexes
DROP INDEX IF EXISTS idx_sync_requested_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_sync_requested_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_sync_requested_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_sync_requested_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_sync_requested_queue_claimed_key CASCADE;
DROP INDEX IF EXISTS idx_stock_checked_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_stock_checked_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_stock_checked_queue_updated_at CASCADE;
DROP INDEX IF EXISTS uq_stock_checked_queue_ready_key CASCADE;
DROP INDEX IF EXISTS idx_ping_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_ping_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_ping_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_order_placed_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_order_placed_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_order_placed_queue_updated_at CASCADE;
DROP INDEX IF EXISTS idx_order_changed_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_order_changed_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_order_changed_queue_updated_at CASCADE;
DROP INDEX IF EXISTS uq_order_changed_queue_ready_key CASCADE;
DROP INDEX IF EXISTS uq_order_changed_queue_claimed_key CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS sync_requested_queue CASCADE;
DROP TABLE IF EXISTS stock_checked_queue CASCADE;
DROP TABLE IF EXISTS ping_queue CASCADE;
DROP TABLE IF EXISTS order_placed_queue CASCADE;
DROP TABLE IF EXISTS order_changed_queue CASCADE;
DROP TABLE IF EXISTS product CASCADE;
DROP TABLE IF EXISTS order_line CASCADE;
DROP TABLE IF EXISTS "order" CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
