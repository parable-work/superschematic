-- Generated PostgreSQL DDL for schema: fixture-timers-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE invoice (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  number TEXT NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  payment TEXT NOT NULL,
  payment_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE invoice IS 'An invoice with two Workflows, whose Timers names the one it reads:
an issued invoice unpaid for thirty days is overdue.';

CREATE TABLE invoice_timers_deadlines (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  invoice_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  base_at TIMESTAMPTZ,
  armed_from TEXT,
  due_at TIMESTAMPTZ,
  fired BOOLEAN,
  fired_at TIMESTAMPTZ,
  outcome TEXT
);

COMMENT ON TABLE invoice_timers_deadlines IS 'One row per deadline started on the row, keyed by the deadline''s name: the time it starts from, the fact it started from, when it is due (null once until cancelled it), whether and when it fired, and what its then met.';

CREATE TABLE ticket (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  visit_at TIMESTAMPTZ,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE ticket IS 'A support ticket (D103): its status is a Workflow, and Timers holds it to
its SLA. The first answer is due 1.5 seconds after the create, and a
ticket still open then escalates; one left waiting on its customer an
hour closes; and a reminder falls due a day before a visit.';

CREATE TABLE ticket_timers_deadlines (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  base_at TIMESTAMPTZ,
  armed_from TEXT,
  due_at TIMESTAMPTZ,
  fired BOOLEAN,
  fired_at TIMESTAMPTZ,
  outcome TEXT
);

COMMENT ON TABLE ticket_timers_deadlines IS 'One row per deadline started on the row, keyed by the deadline''s name: the time it starts from, the fact it started from, when it is due (null once until cancelled it), whether and when it fired, and what its then met.';

CREATE TABLE invoice_change_queue (
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

COMMENT ON TABLE invoice_change_queue IS 'The writes of Invoice''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE invoice_deadline_queue (
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

COMMENT ON TABLE invoice_deadline_queue IS 'The deadlines Invoice''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

CREATE TABLE ticket_change_queue (
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

COMMENT ON TABLE ticket_change_queue IS 'The writes of Ticket''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE ticket_deadline_queue (
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

COMMENT ON TABLE ticket_deadline_queue IS 'The deadlines Ticket''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

-- Add foreign key constraints

ALTER TABLE invoice_timers_deadlines
  ADD CONSTRAINT fk_invoice_timers_deadlines_invoice_id
  FOREIGN KEY (invoice_id)
  REFERENCES invoice(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_timers_deadlines
  ADD CONSTRAINT fk_ticket_timers_deadlines_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_invoice_timers_deadlines_invoice_name ON invoice_timers_deadlines USING BTREE (invoice_id, "name");

CREATE UNIQUE INDEX idx_ticket_timers_deadlines_ticket_name ON ticket_timers_deadlines USING BTREE (ticket_id, "name");

CREATE INDEX idx_invoice_change_queue_due_at ON invoice_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_invoice_change_queue_claim_expires_at ON invoice_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_invoice_change_queue_updated_at ON invoice_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_invoice_change_queue_ready_key ON invoice_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_invoice_change_queue_claimed_key ON invoice_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_invoice_deadline_queue_due_at ON invoice_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_invoice_deadline_queue_claim_expires_at ON invoice_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_invoice_deadline_queue_updated_at ON invoice_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_invoice_deadline_queue_row_key_use_id_name ON invoice_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_invoice_deadline_queue_row_key ON invoice_deadline_queue USING BTREE (row_key);

CREATE INDEX idx_ticket_change_queue_due_at ON ticket_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ticket_change_queue_claim_expires_at ON ticket_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ticket_change_queue_updated_at ON ticket_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_ticket_change_queue_ready_key ON ticket_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_ticket_change_queue_claimed_key ON ticket_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_ticket_deadline_queue_due_at ON ticket_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ticket_deadline_queue_claim_expires_at ON ticket_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ticket_deadline_queue_updated_at ON ticket_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_ticket_deadline_queue_row_key_use_id_name ON ticket_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_ticket_deadline_queue_row_key ON ticket_deadline_queue USING BTREE (row_key);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION invoice_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_invoice_bump_version
  BEFORE UPDATE ON invoice
  FOR EACH ROW EXECUTE FUNCTION invoice_bump_version();

CREATE OR REPLACE FUNCTION ticket_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_bump_version
  BEFORE UPDATE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_bump_version();

-- Create the functions and triggers that write a table's change queue: a
-- message for each write of a row its behaviors hear, whoever writes it

CREATE OR REPLACE FUNCTION "invoice_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of InvoiceChange for each write of a row of invoice
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."number" IS DISTINCT FROM NEW."number" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at" OR OLD."payment" IS DISTINCT FROM NEW."payment" OR OLD."payment_entered_at" IS DISTINCT FROM NEW."payment_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "invoice_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('number', NEW."number", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at", 'payment', NEW."payment", 'payment_entered_at', NEW."payment_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "invoice_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('number', OLD."number", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at", 'payment', OLD."payment", 'payment_entered_at', OLD."payment_entered_at"), jsonb_build_object('number', NEW."number", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at", 'payment', NEW."payment", 'payment_entered_at', NEW."payment_entered_at"), written, written);
  ELSE
    INSERT INTO "invoice_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('number', OLD."number", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at", 'payment', OLD."payment", 'payment_entered_at', OLD."payment_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_invoice_changes
  AFTER INSERT OR UPDATE OR DELETE ON invoice
  FOR EACH ROW EXECUTE FUNCTION invoice_changes();

CREATE OR REPLACE FUNCTION "ticket_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of TicketChange for each write of a row of ticket
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."title" IS DISTINCT FROM NEW."title" OR OLD."visit_at" IS DISTINCT FROM NEW."visit_at" OR OLD."status" IS DISTINCT FROM NEW."status" OR OLD."status_entered_at" IS DISTINCT FROM NEW."status_entered_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('title', NEW."title", 'visit_at', NEW."visit_at", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'visit_at', OLD."visit_at", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), jsonb_build_object('title', NEW."title", 'visit_at', NEW."visit_at", 'status', NEW."status", 'status_entered_at', NEW."status_entered_at"), written, written);
  ELSE
    INSERT INTO "ticket_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'visit_at', OLD."visit_at", 'status', OLD."status", 'status_entered_at', OLD."status_entered_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_changes
  AFTER INSERT OR UPDATE OR DELETE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_changes();
