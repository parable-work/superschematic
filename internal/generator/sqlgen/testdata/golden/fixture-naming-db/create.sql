-- Generated PostgreSQL DDL for schema: fixture-naming-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;


-- Create tables (without foreign key constraints)

CREATE TABLE "budgetCategory" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "displayName" TEXT NOT NULL,
  kind TEXT NOT NULL,
  "createdAt" TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  CONSTRAINT "ck_budgetCategory_displayName" CHECK (length("displayName") >= 1 AND length("displayName") <= 60),
  CONSTRAINT "ck_budgetCategory_kind" CHECK (kind IN ('personal', 'business'))
);

COMMENT ON TABLE "budgetCategory" IS 'A category of spending, in the table the application named
budgetCategory.';

CREATE TABLE "lineItem" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "amountCents" BIGINT NOT NULL,
  "postedOn" DATE DEFAULT CURRENT_DATE NOT NULL,
  memo TEXT,
  "externalId" TEXT,
  "categoryId" UUID,
  "createdAt" TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  "updatedAt" TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  "deletedAt" TIMESTAMPTZ
);

-- Add search_text generated column for trigram search
ALTER TABLE "lineItem" ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE(memo, '')
) STORED;

COMMENT ON TABLE "lineItem" IS 'A line of spending, in the application''s lineItem table: its columns
keep their camel-case names, its category''s foreign key among them, and
the bank''s id of a line is unique among its category''s. A line from the
bank enqueues a LinePosted when it is written and when its amount
changes; the trigger is named after both tables, and the plan that adds
it enqueues the lines from the bank already there. A line written
without the day it was posted on is posted on the day it is written.';

CREATE TABLE "ledgerNote" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "bodyText" TEXT NOT NULL,
  "updatedAt" TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  "updatedBy" UUID NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE "ledgerNote" IS 'A note on the ledger, whose every version a history keeps: its history
table, functions and triggers are named after its table.';

CREATE TABLE "linePosted" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "lineId" UUID NOT NULL,
  "amountCents" BIGINT NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE "linePosted" IS 'Sent when a line from the bank is posted or its amount changes, in the
application''s linePosted table: a newer posting of a line replaces the
one that waits.';

-- Create history tables for versioned entities

CREATE TABLE "ledgerNote_history" (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX "uq_ledgerNote_history_id_version" ON "ledgerNote_history" (id, _version);
CREATE INDEX "idx_ledgerNote_history_id_recorded" ON "ledgerNote_history" (id, recorded_at);

COMMENT ON TABLE "ledgerNote_history" IS 'Version history for ledgerNote';

-- Add foreign key constraints

ALTER TABLE "lineItem"
  ADD CONSTRAINT "fk_lineItem_categoryId"
  FOREIGN KEY ("categoryId")
  REFERENCES "budgetCategory"(id)
  ON DELETE SET NULL;

-- Create indexes

CREATE INDEX "idx_lineItem_postedOn" ON "lineItem" USING BTREE ("postedOn");

CREATE UNIQUE INDEX "uq_lineItem_external" ON "lineItem" USING BTREE ("categoryId", "externalId") WHERE "deletedAt" IS NULL;

CREATE INDEX "idx_lineItem_search_trgm" ON "lineItem" USING GIN (search_text gin_trgm_ops);

CREATE INDEX "idx_linePosted_due_at" ON "linePosted" USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX "idx_linePosted_claim_expires_at" ON "linePosted" USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX "idx_linePosted_updated_at" ON "linePosted" USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX "uq_linePosted_ready_key" ON "linePosted" USING BTREE ("lineId") WHERE state = 'ready';

-- Create history capture functions and triggers

CREATE OR REPLACE FUNCTION "ledgerNote_capture_history"() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version. Its updatedBy is the actor a hard delete set in
    -- superschematic.history_actor_id for the statement, else the row's own value.
    INSERT INTO "ledgerNote_history" (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1,
        'updatedBy', COALESCE(
          NULLIF(current_setting('superschematic.history_actor_id', true), '')::UUID,
          OLD."updatedBy"
        )
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO "ledgerNote_history" (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER "trg_ledgerNote_bump_version"
  BEFORE UPDATE ON "ledgerNote"
  FOR EACH ROW EXECUTE FUNCTION "ledgerNote_capture_history"();

CREATE TRIGGER "trg_ledgerNote_capture_history_write"
  AFTER INSERT OR UPDATE ON "ledgerNote"
  FOR EACH ROW EXECUTE FUNCTION "ledgerNote_capture_history"();

CREATE TRIGGER "trg_ledgerNote_capture_history_delete"
  AFTER DELETE ON "ledgerNote"
  FOR EACH ROW EXECUTE FUNCTION "ledgerNote_capture_history"();

-- Create the functions and triggers that enqueue a queue's message after a
-- write to a table's rows, whatever writes it

CREATE OR REPLACE FUNCTION "lineItem_enqueue_linePosted"() RETURNS trigger AS $$
BEGIN
  -- Enqueues a message of LinePosted after a write to lineItem (D80).
  IF TG_OP = 'UPDATE' AND NOT (OLD."amountCents" IS DISTINCT FROM NEW."amountCents") THEN
    RETURN NULL;
  END IF;
  IF NEW."externalId" IS NOT NULL THEN
    INSERT INTO "linePosted" ("lineId", "amountCents") VALUES (NEW.id, NEW."amountCents")
      ON CONFLICT ("lineId") WHERE state = 'ready' DO UPDATE SET "amountCents" = excluded."amountCents", due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = now();
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER "trg_lineItem_enqueue_linePosted"
  AFTER INSERT OR UPDATE ON "lineItem"
  FOR EACH ROW EXECUTE FUNCTION "lineItem_enqueue_linePosted"();
