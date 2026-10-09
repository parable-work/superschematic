-- Drop statements for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_ping_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_ping_queue_claim_expires_at CASCADE;
DROP INDEX IF EXISTS idx_order_placed_queue_due_at CASCADE;
DROP INDEX IF EXISTS idx_order_placed_queue_claim_expires_at CASCADE;

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS ping_queue CASCADE;
DROP TABLE IF EXISTS order_placed_queue CASCADE;
DROP TABLE IF EXISTS "order" CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
