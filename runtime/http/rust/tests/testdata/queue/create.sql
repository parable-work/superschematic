-- Generated PostgreSQL DDL for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE "order" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  placed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE "order" IS 'An order of the shop.';

CREATE TABLE order_placed_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  placed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  priority TEXT NOT NULL,
  quantity BIGINT NOT NULL,
  note TEXT,
  ship_to TEXT NOT NULL,
  tags TEXT[] DEFAULT '{}' NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
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

-- Add foreign key constraints

-- Create indexes

CREATE INDEX idx_order_placed_queue_due_at ON order_placed_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_order_placed_queue_claim_expires_at ON order_placed_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ping_queue_due_at ON ping_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ping_queue_claim_expires_at ON ping_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';
