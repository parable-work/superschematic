-- Generated PostgreSQL DDL for schema: fixture-approvals-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE note (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  author UUID NOT NULL,
  pinned BOOLEAN NOT NULL,
  views DOUBLE PRECISION NOT NULL,
  created_by UUID NOT NULL,
  deleted_at TIMESTAMPTZ,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL,
  UNIQUE (slug)
);

COMMENT ON TABLE note IS 'A note a person drafts and publishes, kept by its slug (D104). It keeps
no sub-table, so it has upserts, and an upsert''s update of the note its
slug holds runs through the core as an update does: its author stays who
its create named, and pinned and views only go up, unless the caller
holds notes.admin; while it is a draft only its creator edits its body.
A deleted note is kept, deletedAt stamped, and an upsert of its slug
finds none.';

CREATE TABLE task (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  "owner" UUID NOT NULL,
  reviewer UUID,
  plan TEXT,
  proof TEXT,
  urgent BOOLEAN NOT NULL,
  attempts DOUBLE PRECISION NOT NULL,
  created_by UUID NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE task IS 'A task a person plans and its owner proves (D104). Its plan is approved
before it is proved, and that approval survives later edits of the plan;
its proof is approved against the version its reviewer read, and a change
of the proof clears that approval. While it is planned only its creator
edits the plan, and while it is working only its reviewer does; it enters
proving only with a proof. Its owner stays who its create named, and
urgent and attempts only go up, unless the caller holds tasks.admin.';

CREATE TABLE task_approvals_sign_offs (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  task_id UUID NOT NULL,
  number TEXT NOT NULL,
  state TEXT,
  subject TEXT,
  note TEXT,
  at TIMESTAMPTZ,
  consumed_at TIMESTAMPTZ
);

COMMENT ON TABLE task_approvals_sign_offs IS 'One row per sign-off on the row, keyed by its number, the order it was given in: the gated state, the subject who signed, why, when, and when it was consumed, absent while it counts. A withdrawal deletes its row; consumed ones stay for listSignOffs.';

-- Add foreign key constraints

ALTER TABLE task_approvals_sign_offs
  ADD CONSTRAINT fk_task_approvals_sign_offs_task_id
  FOREIGN KEY (task_id)
  REFERENCES task(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_task_approvals_sign_offs_task_number ON task_approvals_sign_offs USING BTREE (task_id, number);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION note_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_note_bump_version
  BEFORE UPDATE ON note
  FOR EACH ROW EXECUTE FUNCTION note_bump_version();

CREATE OR REPLACE FUNCTION task_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_task_bump_version
  BEFORE UPDATE ON task
  FOR EACH ROW EXECUTE FUNCTION task_bump_version();
