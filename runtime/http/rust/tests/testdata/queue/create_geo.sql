-- Generated PostgreSQL DDL for schema: fixture-queue-geo-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE delivery_planned_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  drop_at POINT NOT NULL,
  wait INTERVAL NOT NULL,
  meet_at POINT,
  grace INTERVAL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE delivery_planned_queue IS 'Planned when a courier takes an order: where to leave it, and how long
the courier waits there. Postgres holds a location as a POINT and a
duration as an INTERVAL.';

-- Add foreign key constraints

-- Create indexes

CREATE INDEX idx_delivery_planned_queue_due_at ON delivery_planned_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_delivery_planned_queue_claim_expires_at ON delivery_planned_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';
