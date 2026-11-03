-- Generated PostgreSQL DDL for schema: fixture-workflow-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE "order" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  placed_at TIMESTAMPTZ,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE "order" IS 'An order a customer places, which moves from draft through paid to
shipped. It stays a table: its status is a Workflow, whose fields are
its columns (D103), which an index names as it names its own.';

CREATE TABLE shipment (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  reference TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  payment_status TEXT NOT NULL,
  payment_status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL,
  UNIQUE (reference)
);

COMMENT ON TABLE shipment IS 'A shipment of goods, which is packed and sent while its payment settles
apart: two Workflows, one on each of two fields.';

-- Add foreign key constraints

-- Create indexes

CREATE INDEX idx_order_status_placed_at ON "order" USING BTREE (status, placed_at);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION order_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_order_bump_version
  BEFORE UPDATE ON "order"
  FOR EACH ROW EXECUTE FUNCTION order_bump_version();

CREATE OR REPLACE FUNCTION shipment_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_shipment_bump_version
  BEFORE UPDATE ON shipment
  FOR EACH ROW EXECUTE FUNCTION shipment_bump_version();
