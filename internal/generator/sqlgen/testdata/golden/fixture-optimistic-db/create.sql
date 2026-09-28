-- Generated PostgreSQL DDL for schema: fixture-optimistic-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE shelf (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  label TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_by UUID NOT NULL,
  deleted_at TIMESTAMPTZ,
  deleted_by UUID,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE shelf IS 'A shelf of the pantry. A delete sets deletedAt.';

CREATE TABLE stock (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  shelf_id UUID NOT NULL,
  ingredient TEXT NOT NULL,
  quantity BIGINT NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE stock IS 'How much of one ingredient a shelf holds. A delete removes the row.';

-- Add foreign key constraints

ALTER TABLE stock
  ADD CONSTRAINT fk_stock_shelf_id
  FOREIGN KEY (shelf_id)
  REFERENCES shelf(id)
  ON DELETE CASCADE;

-- Create indexes

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION shelf_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_shelf_bump_version
  BEFORE UPDATE ON shelf
  FOR EACH ROW EXECUTE FUNCTION shelf_bump_version();

CREATE OR REPLACE FUNCTION stock_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_stock_bump_version
  BEFORE UPDATE ON stock
  FOR EACH ROW EXECUTE FUNCTION stock_bump_version();
