-- Generated SQLite DDL for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.
-- It is the migration plan from an empty database to model 90c61cb2c00fe96df81e8992aa373929c9d0b5c4c745cf0ce3eb1063594ab606.

CREATE TABLE "order" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "placed_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  PRIMARY KEY ("id")
);

CREATE TABLE "order_placed_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "order_id" TEXT NOT NULL,
  "placed_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "priority" TEXT NOT NULL,
  "quantity" INTEGER NOT NULL,
  "note" TEXT,
  "ship_to" TEXT NOT NULL,
  "tags" TEXT DEFAULT '[]' NOT NULL,
  "state" TEXT DEFAULT 'ready' NOT NULL,
  "attempts" INTEGER DEFAULT 0 NOT NULL,
  "due_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "claim_expires_at" TEXT,
  "claim_token" TEXT,
  "last_error" TEXT,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "updated_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  PRIMARY KEY ("id")
);

CREATE TABLE "ping_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "sent_at" TEXT,
  "state" TEXT DEFAULT 'ready' NOT NULL,
  "attempts" INTEGER DEFAULT 0 NOT NULL,
  "due_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "claim_expires_at" TEXT,
  "claim_token" TEXT,
  "last_error" TEXT,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "updated_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  PRIMARY KEY ("id")
);

CREATE INDEX "idx_order_placed_queue_claim_expires_at" ON "order_placed_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_order_placed_queue_due_at" ON "order_placed_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_order_placed_queue_updated_at" ON "order_placed_queue" ("updated_at") WHERE state = 'done';

CREATE INDEX "idx_ping_queue_claim_expires_at" ON "ping_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_ping_queue_due_at" ON "ping_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_ping_queue_updated_at" ON "ping_queue" ("updated_at") WHERE state = 'done';
