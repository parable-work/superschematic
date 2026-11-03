-- Generated PostgreSQL DDL for schema: fixture-reactions-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE project (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE project IS 'A project (D103): its status is a Workflow, which its tasks move. It is
done once every task is done with success, and failed once one fails. The
rules hear Task''s writes through task.project, whoever makes them.';

CREATE TABLE task (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  priority TEXT,
  project_id UUID NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE task IS 'A task of a project (D103): a task that starts moves its project to
active, and one marked urgent starts.';

CREATE TABLE project_change_queue (
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

COMMENT ON TABLE project_change_queue IS 'The writes of Project''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE task_change_queue (
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

COMMENT ON TABLE task_change_queue IS 'The writes of Task''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

-- Add foreign key constraints

ALTER TABLE task
  ADD CONSTRAINT fk_task_project_id
  FOREIGN KEY (project_id)
  REFERENCES project(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_project_change_queue_due_at ON project_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_project_change_queue_claim_expires_at ON project_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_project_change_queue_updated_at ON project_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_project_change_queue_ready_key ON project_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_project_change_queue_claimed_key ON project_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_task_change_queue_due_at ON task_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_task_change_queue_claim_expires_at ON task_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_task_change_queue_updated_at ON task_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_task_change_queue_ready_key ON task_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_task_change_queue_claimed_key ON task_change_queue USING BTREE (row_key) WHERE state = 'claimed';

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION project_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_project_bump_version
  BEFORE UPDATE ON project
  FOR EACH ROW EXECUTE FUNCTION project_bump_version();

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

-- Create the functions and triggers that write a table's change queue: a
-- message for each write of a row its behaviors hear, whoever writes it

CREATE OR REPLACE FUNCTION "project_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of ProjectChange for each write of a row of project
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."name" IS DISTINCT FROM NEW."name" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "project_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('name', NEW."name", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "project_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('name', OLD."name", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), jsonb_build_object('name', NEW."name", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSE
    INSERT INTO "project_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('name', OLD."name", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_project_changes
  AFTER INSERT OR UPDATE OR DELETE ON project
  FOR EACH ROW EXECUTE FUNCTION project_changes();

CREATE OR REPLACE FUNCTION "task_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of TaskChange for each write of a row of task
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."title" IS DISTINCT FROM NEW."title" OR OLD."priority" IS DISTINCT FROM NEW."priority" OR OLD."project_id" IS DISTINCT FROM NEW."project_id" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "task_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('title', NEW."title", 'priority', NEW."priority", 'project_id', NEW."project_id", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "task_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'priority', OLD."priority", 'project_id', OLD."project_id", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), jsonb_build_object('title', NEW."title", 'priority', NEW."priority", 'project_id', NEW."project_id", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSE
    INSERT INTO "task_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'priority', OLD."priority", 'project_id', OLD."project_id", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_task_changes
  AFTER INSERT OR UPDATE OR DELETE ON task
  FOR EACH ROW EXECUTE FUNCTION task_changes();
