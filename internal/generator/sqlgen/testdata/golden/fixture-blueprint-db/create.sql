-- Generated PostgreSQL DDL for schema: fixture-blueprint-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE batch (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  topic TEXT,
  flags TEXT[],
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE batch IS 'A batch of jobs (D149): its create stamps a job per step of its template,
each blocked by the jobs of the steps it comes after, and audit only for
a batch whose flags include audit, its report then waiting on index.
Every job copies the batch''s topic. The batch holds no state of the
stamp: a read lists its children, the jobs that point at it.';

CREATE TABLE job (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  step TEXT,
  topic TEXT,
  batch_id UUID NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE job IS 'One job of a batch (D149): the step it was stamped as and its topic stay
as the stamp set them, and it waits to run, and to finish, until the
jobs of the steps before it are done.';

CREATE TABLE job_dependencies_blockers (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  job_id UUID NOT NULL,
  number TEXT NOT NULL,
  blocker_type TEXT,
  blocker_key TEXT,
  added_by TEXT,
  added_at TIMESTAMPTZ
);

COMMENT ON TABLE job_dependencies_blockers IS 'On a table, one row per blocker of the row, keyed by its number on the row, the order they were added in: the blocker''s table and key, and who added it when. removeBlocker deletes its row, and the row''s delete deletes them all.';

-- Add foreign key constraints

ALTER TABLE job
  ADD CONSTRAINT fk_job_batch_id
  FOREIGN KEY (batch_id)
  REFERENCES batch(id)
  ON DELETE CASCADE;

ALTER TABLE job_dependencies_blockers
  ADD CONSTRAINT fk_job_dependencies_blockers_job_id
  FOREIGN KEY (job_id)
  REFERENCES job(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_job_dependencies_blockers_job_number ON job_dependencies_blockers USING BTREE (job_id, number);

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

CREATE OR REPLACE FUNCTION job_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_job_bump_version
  BEFORE UPDATE ON job
  FOR EACH ROW EXECUTE FUNCTION job_bump_version();
