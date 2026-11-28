-- Generated PostgreSQL DDL for schema: fixture-recurrence-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE contract (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  ends_on DATE NOT NULL
);

COMMENT ON TABLE contract IS 'An agreement with an end date, which a filing''s relative recurrence
counts back from. Its end is on the calendar.';

CREATE TABLE filing (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  holiday_calendar TEXT,
  contract_id UUID,
  recurrence_recurrence JSONB,
  recurrence_next_due_at TIMESTAMPTZ,
  recurrence_last_done_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE filing IS 'Something that must be done again and again (D152): on a calendar rule,
a while after it was last done, or a while before its contract ends. Each
occurrence opens two weeks before it is due, reminds three days before,
and is missed a day after. A filing''s due dates move to the next business
day of its holiday calendar. Each occurrence is on the calendar.';

CREATE TABLE filing_recurrence_occurrences (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  filing_id UUID NOT NULL,
  "key" TEXT NOT NULL,
  due TEXT,
  due_at TIMESTAMPTZ,
  opens_at TIMESTAMPTZ,
  remind_at TIMESTAMPTZ,
  overdue_at TIMESTAMPTZ,
  state TEXT,
  revision BIGINT,
  shifted TEXT,
  moved BOOLEAN,
  changed_at TIMESTAMPTZ,
  reminded_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  completed_by_table TEXT,
  completed_by_key TEXT,
  completed_by_principal TEXT,
  note TEXT
);

COMMENT ON TABLE filing_recurrence_occurrences IS 'One row per occurrence of the row''s recurrence, keyed by its nominal wall date-time before any adjustment: its due and open times, its reminder and overdue times, its state, the revision of the recurrence that planned it, and what and who completed it. Upcoming ones up to horizonDays ahead; finished ones until historyDays after their due.';

CREATE TABLE holiday (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  calendar TEXT NOT NULL,
  "date" DATE,
  weekday TEXT,
  "name" TEXT NOT NULL
);

COMMENT ON TABLE holiday IS 'A day a holiday calendar closes (D152). The app fills these rows; core
ships no holidays. A filing''s due date moves off the days of the calendar
its holidayCalendar names. A row with a weekday and no date closes that
day every week, and a calendar with such rows takes them as its weekend
in place of Saturday and Sunday (#729).';

CREATE TABLE filing_change_queue (
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

COMMENT ON TABLE filing_change_queue IS 'The writes of Filing''s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).';

CREATE TABLE filing_deadline_queue (
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

COMMENT ON TABLE filing_deadline_queue IS 'The deadlines Filing''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

CREATE TABLE filing_event_queue (
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

COMMENT ON TABLE filing_event_queue IS 'The events Filing''s behaviors emit about its rows, one message per event, written in the command''s transaction, which a behavior worker hands to the project''s handler (D104).';

-- Add foreign key constraints

ALTER TABLE filing
  ADD CONSTRAINT fk_filing_contract_id
  FOREIGN KEY (contract_id)
  REFERENCES contract(id)
  ON DELETE CASCADE;

ALTER TABLE filing_recurrence_occurrences
  ADD CONSTRAINT fk_filing_recurrence_occurrences_filing_id
  FOREIGN KEY (filing_id)
  REFERENCES filing(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_filing_recurrence_occurrences_filing_key ON filing_recurrence_occurrences USING BTREE (filing_id, "key");

CREATE INDEX idx_filing_change_queue_due_at ON filing_change_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_filing_change_queue_claim_expires_at ON filing_change_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_filing_change_queue_updated_at ON filing_change_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_filing_change_queue_ready_key ON filing_change_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_filing_change_queue_claimed_key ON filing_change_queue USING BTREE (row_key) WHERE state = 'claimed';

CREATE INDEX idx_filing_deadline_queue_due_at ON filing_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_filing_deadline_queue_claim_expires_at ON filing_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_filing_deadline_queue_updated_at ON filing_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_filing_deadline_queue_row_key_use_id_name ON filing_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_filing_deadline_queue_row_key ON filing_deadline_queue USING BTREE (row_key);

CREATE INDEX idx_filing_event_queue_due_at ON filing_event_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_filing_event_queue_claim_expires_at ON filing_event_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_filing_event_queue_updated_at ON filing_event_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_filing_event_queue_ready_key ON filing_event_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_filing_event_queue_claimed_key ON filing_event_queue USING BTREE (row_key) WHERE state = 'claimed';

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION filing_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_filing_bump_version
  BEFORE UPDATE ON filing
  FOR EACH ROW EXECUTE FUNCTION filing_bump_version();

-- Create the functions and triggers that write a table's change queue: a
-- message for each write of a row its behaviors hear, whoever writes it

CREATE OR REPLACE FUNCTION "filing_changes"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of FilingChange for each write of a row of filing
  -- its behaviors hear, whoever makes it (D103).
  IF TG_OP = 'UPDATE' AND NOT (OLD."title" IS DISTINCT FROM NEW."title" OR OLD."holiday_calendar" IS DISTINCT FROM NEW."holiday_calendar" OR OLD."contract_id" IS DISTINCT FROM NEW."contract_id" OR OLD."recurrence_recurrence" IS DISTINCT FROM NEW."recurrence_recurrence" OR OLD."recurrence_next_due_at" IS DISTINCT FROM NEW."recurrence_next_due_at" OR OLD."recurrence_last_done_at" IS DISTINCT FROM NEW."recurrence_last_done_at") THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'INSERT' THEN
    INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'insert', floor(extract(epoch FROM written) * 1000)::bigint, NULL, jsonb_build_object('title', NEW."title", 'holiday_calendar', NEW."holiday_calendar", 'contract_id', NEW."contract_id", 'recurrence_recurrence', NEW."recurrence_recurrence", 'recurrence_next_due_at', NEW."recurrence_next_due_at", 'recurrence_last_done_at', NEW."recurrence_last_done_at"), written, written);
  ELSIF TG_OP = 'UPDATE' THEN
    INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (NEW."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'holiday_calendar', OLD."holiday_calendar", 'contract_id', OLD."contract_id", 'recurrence_recurrence', OLD."recurrence_recurrence", 'recurrence_next_due_at', OLD."recurrence_next_due_at", 'recurrence_last_done_at', OLD."recurrence_last_done_at"), jsonb_build_object('title', NEW."title", 'holiday_calendar', NEW."holiday_calendar", 'contract_id', NEW."contract_id", 'recurrence_recurrence', NEW."recurrence_recurrence", 'recurrence_next_due_at', NEW."recurrence_next_due_at", 'recurrence_last_done_at', NEW."recurrence_last_done_at"), written, written);
  ELSE
    INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
    VALUES (OLD."id", 'delete', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', OLD."title", 'holiday_calendar', OLD."holiday_calendar", 'contract_id', OLD."contract_id", 'recurrence_recurrence', OLD."recurrence_recurrence", 'recurrence_next_due_at', OLD."recurrence_next_due_at", 'recurrence_last_done_at', OLD."recurrence_last_done_at"), NULL, written, written);
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_filing_changes
  AFTER INSERT OR UPDATE OR DELETE ON filing
  FOR EACH ROW EXECUTE FUNCTION filing_changes();

-- Create the functions and triggers that write a recurring table's change
-- queue when the date its relative recurrence reads, on another table's
-- row, moves (#626)

CREATE OR REPLACE FUNCTION "contract_filing_dates"() RETURNS trigger AS $$
DECLARE
  written timestamptz := clock_timestamp();
BEGIN
  -- Writes a message of FilingChange for each row of filing whose relation
  -- names the row of contract whose date its relative recurrence reads,
  -- when that date moves, whoever writes it (#626).
  IF NOT (OLD."ends_on" IS DISTINCT FROM NEW."ends_on") THEN
    RETURN NULL;
  END IF;
  INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")
  SELECT h."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object('title', h."title", 'holiday_calendar', h."holiday_calendar", 'contract_id', h."contract_id", 'recurrence_recurrence', h."recurrence_recurrence", 'recurrence_next_due_at', h."recurrence_next_due_at", 'recurrence_last_done_at', h."recurrence_last_done_at"), jsonb_build_object('title', h."title", 'holiday_calendar', h."holiday_calendar", 'contract_id', h."contract_id", 'recurrence_recurrence', h."recurrence_recurrence", 'recurrence_next_due_at', h."recurrence_next_due_at", 'recurrence_last_done_at', h."recurrence_last_done_at"), written, written
  FROM "filing" h
  WHERE h."contract_id" = NEW."id";
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_contract_filing_dates
  AFTER UPDATE ON contract
  FOR EACH ROW EXECUTE FUNCTION contract_filing_dates();
