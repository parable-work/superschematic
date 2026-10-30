-- Drop statements for schema: fixture-workflow-db
-- This file is auto-generated. Do not edit manually.
-- WARNING: This will drop all tables and data!

-- Drop indexes
DROP INDEX IF EXISTS idx_order_status_placed_at CASCADE;

-- Drop version bump triggers and functions
DROP TRIGGER IF EXISTS trg_shipment_bump_version ON shipment;
DROP FUNCTION IF EXISTS shipment_bump_version();
DROP TRIGGER IF EXISTS trg_order_bump_version ON "order";
DROP FUNCTION IF EXISTS order_bump_version();

-- Drop join tables first

-- Drop tables in reverse order (to handle foreign key dependencies)
DROP TABLE IF EXISTS shipment CASCADE;
DROP TABLE IF EXISTS "order" CASCADE;

-- Note: Extensions are not dropped automatically
-- To drop extensions, run:
-- DROP EXTENSION IF EXISTS pgcrypto CASCADE;
