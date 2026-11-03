-- Generated PostgreSQL DDL for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE "order" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  placed_at TIMESTAMPTZ NOT NULL
);

COMMENT ON TABLE "order" IS 'An order of the shop.';

CREATE TABLE order_line (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  product_id UUID,
  quantity BIGINT NOT NULL,
  note TEXT
);

COMMENT ON TABLE order_line IS 'A line of an order. Whatever writes it, a change to its product or its
quantity enqueues the order''s OrderChanged, as does a new line or a
deleted one, once it names a product.';

CREATE TABLE product (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL
);

COMMENT ON TABLE product IS 'A product an order''s line may name.';

CREATE TABLE order_changed_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  line_id UUID,
  priority TEXT DEFAULT 'low' NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  CONSTRAINT ck_order_changed_queue_priority CHECK (priority IN ('low', 'high'))
);

COMMENT ON TABLE order_changed_queue IS 'Sent when an order''s lines change: a newer change replaces the one that
waits, and the worker handles one order''s changes at a time.';

CREATE TABLE order_placed_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  placed_at TIMESTAMPTZ NOT NULL,
  priority TEXT DEFAULT 'low' NOT NULL,
  quantity BIGINT NOT NULL,
  note TEXT,
  ship_to JSONB NOT NULL,
  tags TEXT[] DEFAULT '{}' NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  CONSTRAINT ck_order_placed_queue_priority CHECK (priority IN ('low', 'high')),
  CONSTRAINT ck_order_placed_queue_note CHECK (length(note) <= 200)
);

COMMENT ON TABLE order_placed_queue IS 'Placed when an order is: the worker that fulfils orders handles it.';

CREATE TABLE ping_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  sent_at TIMESTAMPTZ,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE ping_queue IS 'Sent when nobody asked for anything in particular.';

CREATE TABLE stock_checked_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  sku TEXT NOT NULL,
  quantity BIGINT NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE stock_checked_queue IS 'Sent when a stock count is asked for: one waits per SKU, and a later ask
while it waits adds nothing.';

CREATE TABLE sync_requested_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  account_id UUID NOT NULL,
  reason TEXT,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE sync_requested_queue IS 'Sent when an account''s records are to be synced: every request is kept,
and the worker syncs one account at a time.';

-- Add foreign key constraints

ALTER TABLE order_line
  ADD CONSTRAINT fk_order_line_order_id
  FOREIGN KEY (order_id)
  REFERENCES "order"(id)
  ON DELETE CASCADE;

ALTER TABLE order_line
  ADD CONSTRAINT fk_order_line_product_id
  FOREIGN KEY (product_id)
  REFERENCES product(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_order_changed_queue_due_at ON order_changed_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_order_changed_queue_claim_expires_at ON order_changed_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_order_changed_queue_updated_at ON order_changed_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX uq_order_changed_queue_ready_key ON order_changed_queue USING BTREE (order_id) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_order_changed_queue_claimed_key ON order_changed_queue USING BTREE (order_id) WHERE state = 'claimed';

CREATE INDEX idx_order_placed_queue_due_at ON order_placed_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_order_placed_queue_claim_expires_at ON order_placed_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_order_placed_queue_updated_at ON order_placed_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_ping_queue_due_at ON ping_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ping_queue_claim_expires_at ON ping_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ping_queue_updated_at ON ping_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_stock_checked_queue_due_at ON stock_checked_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_stock_checked_queue_claim_expires_at ON stock_checked_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_stock_checked_queue_updated_at ON stock_checked_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX uq_stock_checked_queue_ready_key ON stock_checked_queue USING BTREE (sku) WHERE state = 'ready';

CREATE INDEX idx_sync_requested_queue_due_at ON sync_requested_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_sync_requested_queue_claim_expires_at ON sync_requested_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_sync_requested_queue_updated_at ON sync_requested_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_sync_requested_queue_ready_key ON sync_requested_queue USING BTREE (account_id) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_sync_requested_queue_claimed_key ON sync_requested_queue USING BTREE (account_id) WHERE state = 'claimed';

-- Create the functions and triggers that enqueue a queue's message after a
-- write to a table's rows, whatever writes it

CREATE OR REPLACE FUNCTION order_line_enqueue_order_changed() RETURNS trigger AS $$
BEGIN
  -- Enqueues a message of OrderChanged after a write to order_line (D80).
  IF TG_OP = 'UPDATE' AND NOT (OLD.product_id IS DISTINCT FROM NEW.product_id OR OLD.quantity IS DISTINCT FROM NEW.quantity) THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'DELETE' THEN
    IF OLD.product_id IS NOT NULL THEN
      INSERT INTO order_changed_queue (order_id, line_id, priority) VALUES (OLD.order_id, OLD.id, 'low')
        ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = now();
    END IF;
    RETURN NULL;
  END IF;
  IF NEW.product_id IS NOT NULL THEN
    INSERT INTO order_changed_queue (order_id, line_id, priority) VALUES (NEW.order_id, NEW.id, 'low')
      ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = now();
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_order_line_enqueue_order_changed
  AFTER INSERT OR UPDATE OR DELETE ON order_line
  FOR EACH ROW EXECUTE FUNCTION order_line_enqueue_order_changed();
