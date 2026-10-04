-- Migration plan for shop-db (postgres)
-- from:    a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1
-- to:      b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2
-- plan:    c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3
-- renames: purchase=order, order.amount=order.total_cents
-- 6 steps (4 expand, 2 contract), 4 hazards (1 destructive, 1 compat, 1 data-dependent, 1 api-breaking)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, renameTable table/order
-- hazard compat:table/order
ALTER TABLE "purchase" RENAME TO "order";

-- Step 2, expand, addColumn table/order/column/note
ALTER TABLE "order" ADD COLUMN "note" TEXT;

-- Step 3, expand, createIndex table/order/index/order_note_idx
-- not in a transaction: each statement runs on its own
-- recovery: DROP INDEX CONCURRENTLY IF EXISTS "order_note_idx";
CREATE INDEX CONCURRENTLY "order_note_idx" ON "order" ("note");

-- Step 4, expand, replaceFunction function/order_history_capture
CREATE OR REPLACE FUNCTION "order_history_capture"() RETURNS TRIGGER AS $$
BEGIN
  INSERT INTO "order_history" SELECT NEW.*;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Contract. Runs after the new servers roll out.

-- Step 5, contract, setNotNull table/order/column/note
-- hazard data-dependent:table/order/column/note
ALTER TABLE "order" VALIDATE CONSTRAINT "order_note_not_null";
ALTER TABLE "order" ALTER COLUMN "note" SET NOT NULL;
ALTER TABLE "order" DROP CONSTRAINT "order_note_not_null";

-- Step 6, contract, dropColumn table/order/column/total
-- hazard destructive:table/order/column/total
-- hazard api-breaking:table/order/column/total@shop-api/OrderView.total
ALTER TABLE "order" DROP COLUMN "total";
