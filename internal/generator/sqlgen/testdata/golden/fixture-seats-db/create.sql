-- Generated PostgreSQL DDL for schema: fixture-seats-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE problem (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  agent UUID,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE problem IS 'A record of work (D104). It has one accountable person, whom the next to
take the seat displaces; any number of responsible agents, the record''s
own agent always among them; and at most five handles, which any caller
may claim and only the agent that claimed one, or a person who may
release another''s, releases. Only a responsible subject starts and
submits the record, only the accountable person closes it or sends it
back, and in review only the accountable person edits its title.';

CREATE TABLE problem_seats_occupants (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  problem_id UUID NOT NULL,
  number TEXT NOT NULL,
  seat TEXT,
  subject TEXT,
  seated_at TIMESTAMPTZ,
  seated_by TEXT
);

COMMENT ON TABLE problem_seats_occupants IS 'One row per subject seated on the row, keyed by its number, the order they were seated in: the seat, the subject, and when and by whom it was seated. A leave or a displacement deletes its row; the subject a seat''s fromField holds needs none.';

CREATE TABLE sandbox (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  observed TEXT,
  observed_observed_at TIMESTAMPTZ,
  observed_drift JSONB,
  observed_reconciled_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE sandbox IS 'A sandbox a provider runs (D104). Its status follows the state the
provider reports, through observe, each step a transition its Workflow
allows as the observer, so a stop needs sandboxes.stop of the observer
too; when no report has reached it for five minutes its status moves to
unreachable, and any report brings it back.';

CREATE TABLE sandbox_deadline_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  "name" TEXT NOT NULL,
  at BIGINT NOT NULL,
  payload JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE sandbox_deadline_queue IS 'The deadlines Sandbox''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

-- Add foreign key constraints

ALTER TABLE problem_seats_occupants
  ADD CONSTRAINT fk_problem_seats_occupants_problem_id
  FOREIGN KEY (problem_id)
  REFERENCES problem(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_problem_seats_occupants_problem_number ON problem_seats_occupants USING BTREE (problem_id, number);

CREATE INDEX idx_sandbox_deadline_queue_due_at ON sandbox_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_sandbox_deadline_queue_claim_expires_at ON sandbox_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_sandbox_deadline_queue_updated_at ON sandbox_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_sandbox_deadline_queue_row_key_use_id_name ON sandbox_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_sandbox_deadline_queue_row_key ON sandbox_deadline_queue USING BTREE (row_key);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION problem_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_problem_bump_version
  BEFORE UPDATE ON problem
  FOR EACH ROW EXECUTE FUNCTION problem_bump_version();

CREATE OR REPLACE FUNCTION sandbox_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sandbox_bump_version
  BEFORE UPDATE ON sandbox
  FOR EACH ROW EXECUTE FUNCTION sandbox_bump_version();
