-- Generated PostgreSQL DDL for schema: fixture-schedules-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE problem (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE problem IS 'A record of work an agent wakes on (D104). The agent sets wake-ups on it:
after a delay, at an interval, or on a cron line in a time zone, a minute
to thirty days apart, at most ten a record, one live name each. Each wake
reaches the app''s handler, which tells the agent and reports told,
resumed, held or missed; a missed one is tried again five minutes on. A
record that is done or dropped finishes its wake-ups.';

CREATE TABLE problem_schedules_schedules (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  problem_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  kind TEXT,
  spec TEXT,
  time_zone TEXT,
  state TEXT,
  set_at TIMESTAMPTZ,
  next_at TIMESTAMPTZ,
  retry_at TIMESTAMPTZ,
  runs BIGINT,
  attempt BIGINT,
  last_at TIMESTAMPTZ,
  last_outcome TEXT,
  finished_at TIMESTAMPTZ,
  ended_by TEXT
);

COMMENT ON TABLE problem_schedules_schedules IS 'One row per schedule of the row, keyed by its name: its kind and spec, its time zone, its state, when it was set, its next run and the retry of its latest one, how many runs it has had and which attempt of the latest fired last and when, the outcome reported of it, and when and why it finished. A finished schedule stays until its name is set again, or until the row keeps more than max finished ones, when those finished longest ago go.';

CREATE TABLE problem_change_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  write TEXT NOT NULL,
  at BIGINT NOT NULL,
  before JSONB,
  "after" JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE problem_change_queue IS 'The writes of Problem''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE problem_deadline_queue (
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

COMMENT ON TABLE problem_deadline_queue IS 'The deadlines Problem''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

CREATE TABLE problem_event_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  kind TEXT NOT NULL,
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

COMMENT ON TABLE problem_event_queue IS 'The events Problem''s behaviors emit about its rows, one message per event, written in the command''s transaction, which a behavior worker hands to the project''s handler (D104).';

-- Add foreign key constraints

ALTER TABLE problem_schedules_schedules
  ADD CONSTRAINT fk_problem_schedules_schedules_problem_id
  FOREIGN KEY (problem_id)
  REFERENCES problem(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_problem_schedules_schedules_problem_name ON problem_schedules_schedules USING BTREE (problem_id, "name");

CREATE INDEX idx_problem_change_queue_due_at ON problem_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_problem_change_queue_claim_expires_at ON problem_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_problem_change_queue_updated_at ON problem_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_problem_change_queue_ready_key ON problem_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_problem_change_queue_claimed_key ON problem_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_problem_deadline_queue_due_at ON problem_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_problem_deadline_queue_claim_expires_at ON problem_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_problem_deadline_queue_updated_at ON problem_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_problem_deadline_queue_row_key_use_id_name ON problem_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_problem_deadline_queue_row_key ON problem_deadline_queue USING BTREE (row_key);

CREATE INDEX idx_problem_event_queue_due_at ON problem_event_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_problem_event_queue_claim_expires_at ON problem_event_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_problem_event_queue_updated_at ON problem_event_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_problem_event_queue_ready_key ON problem_event_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_problem_event_queue_claimed_key ON problem_event_queue USING BTREE (row_key) WHERE state = 'claimed';

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

-- Create the functions and triggers that write a table's change queue: a
-- message for each write of a row its behaviors hear, whoever writes it

CREATE OR REPLACE FUNCTION "problem_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of ProblemChange for each write of a row of problem
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."title" IS DISTINCT FROM NEW."title" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "problem_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('title', NEW."title", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "problem_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), jsonb_build_object('title', NEW."title", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSE
    INSERT INTO "problem_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_problem_changes
  AFTER INSERT OR UPDATE OR DELETE ON problem
  FOR EACH ROW EXECUTE FUNCTION problem_changes();
