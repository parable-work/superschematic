-- Generated PostgreSQL DDL for schema: fixture-steps-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE batch (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE batch IS 'A batch of steps (D104''s step 4): running until every step is done, which
its rollup done gates, or failed. It counts its steps, counts them by
status and sums their estimates, each computed at a read of the batch
from the steps that point at it, the rows its steps field lists.';

CREATE TABLE step (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  estimate BIGINT,
  batch_id UUID NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE step IS 'One step of a batch (D104''s step 4): blocked by the steps it comes after,
which hold up its start and its finish until each is done, a step that
failed blocking until it is removed.';

CREATE TABLE step_dependencies_blockers (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  step_id UUID NOT NULL,
  number TEXT NOT NULL,
  blocker_type TEXT,
  blocker_key TEXT,
  added_by TEXT,
  added_at TIMESTAMPTZ
);

COMMENT ON TABLE step_dependencies_blockers IS 'On a table, one row per blocker of the row, keyed by its number on the row, the order they were added in: the blocker''s table and key, and who added it when. removeBlocker deletes its row, and the row''s delete deletes them all.';

-- Add foreign key constraints

ALTER TABLE step
  ADD CONSTRAINT fk_step_batch_id
  FOREIGN KEY (batch_id)
  REFERENCES batch(id)
  ON DELETE CASCADE;

ALTER TABLE step_dependencies_blockers
  ADD CONSTRAINT fk_step_dependencies_blockers_step_id
  FOREIGN KEY (step_id)
  REFERENCES step(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_step_dependencies_blockers_step_number ON step_dependencies_blockers USING BTREE (step_id, number);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION batch_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_batch_bump_version
  BEFORE UPDATE ON batch
  FOR EACH ROW EXECUTE FUNCTION batch_bump_version();

CREATE OR REPLACE FUNCTION step_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_step_bump_version
  BEFORE UPDATE ON step
  FOR EACH ROW EXECUTE FUNCTION step_bump_version();
