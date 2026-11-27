-- Generated SQLite DDL for schema: fixture-queue-db
-- This file is auto-generated. Do not edit manually.
-- It is the migration plan from an empty database to model c425ef1ae67d89b50843cb9c722d3ccb914fc519150685bd50d12fa7ef7a7ff0.

CREATE TABLE "order" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "placed_at" TEXT NOT NULL,
  PRIMARY KEY ("id")
);

CREATE TABLE "order_changed_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "order_id" TEXT NOT NULL,
  "line_id" TEXT,
  "priority" TEXT DEFAULT 'low' NOT NULL CHECK ("priority" IN ('low', 'high')),
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

CREATE TABLE "order_line" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "order_id" TEXT NOT NULL,
  "product_id" TEXT,
  "quantity" INTEGER NOT NULL,
  "note" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("order_id") REFERENCES "order" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("product_id") REFERENCES "product" ("id") ON DELETE CASCADE
);

CREATE TABLE "order_placed_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "order_id" TEXT NOT NULL,
  "placed_at" TEXT NOT NULL,
  "priority" TEXT DEFAULT 'low' NOT NULL CHECK ("priority" IN ('low', 'high')),
  "quantity" INTEGER NOT NULL,
  "note" TEXT CHECK (length("note") <= 200),
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

CREATE TABLE "product" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "name" TEXT NOT NULL,
  PRIMARY KEY ("id")
);

CREATE TABLE "stock_checked_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "sku" TEXT NOT NULL,
  "quantity" INTEGER NOT NULL,
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

CREATE TABLE "stock_moved_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "warehouse" TEXT NOT NULL,
  "sku" TEXT NOT NULL,
  "quantity" INTEGER NOT NULL,
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

CREATE TABLE "sync_requested_queue" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "account_id" TEXT NOT NULL,
  "reason" TEXT,
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

CREATE INDEX "idx_order_changed_queue_claim_expires_at" ON "order_changed_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_order_changed_queue_due_at" ON "order_changed_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_order_changed_queue_updated_at" ON "order_changed_queue" ("updated_at") WHERE state = 'done';

CREATE UNIQUE INDEX "uq_order_changed_queue_claimed_key" ON "order_changed_queue" ("order_id") WHERE state = 'claimed';

CREATE UNIQUE INDEX "uq_order_changed_queue_ready_key" ON "order_changed_queue" ("order_id") WHERE state = 'ready';

CREATE INDEX "idx_order_placed_queue_claim_expires_at" ON "order_placed_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_order_placed_queue_due_at" ON "order_placed_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_order_placed_queue_updated_at" ON "order_placed_queue" ("updated_at") WHERE state = 'done';

CREATE INDEX "idx_ping_queue_claim_expires_at" ON "ping_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_ping_queue_due_at" ON "ping_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_ping_queue_updated_at" ON "ping_queue" ("updated_at") WHERE state = 'done';

CREATE INDEX "idx_stock_checked_queue_claim_expires_at" ON "stock_checked_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_stock_checked_queue_due_at" ON "stock_checked_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_stock_checked_queue_updated_at" ON "stock_checked_queue" ("updated_at") WHERE state = 'done';

CREATE UNIQUE INDEX "uq_stock_checked_queue_ready_key" ON "stock_checked_queue" ("sku") WHERE state = 'ready';

CREATE INDEX "idx_stock_moved_queue_claim_expires_at" ON "stock_moved_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_stock_moved_queue_due_at" ON "stock_moved_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_stock_moved_queue_updated_at" ON "stock_moved_queue" ("updated_at") WHERE state = 'done';

CREATE UNIQUE INDEX "uq_stock_moved_queue_claimed_key" ON "stock_moved_queue" ("warehouse", "sku") WHERE state = 'claimed';

CREATE UNIQUE INDEX "uq_stock_moved_queue_ready_key" ON "stock_moved_queue" ("warehouse", "sku") WHERE state = 'ready';

CREATE INDEX "idx_sync_requested_queue_claim_expires_at" ON "sync_requested_queue" ("claim_expires_at") WHERE state = 'claimed';

CREATE INDEX "idx_sync_requested_queue_due_at" ON "sync_requested_queue" ("due_at") WHERE state = 'ready';

CREATE INDEX "idx_sync_requested_queue_ready_key" ON "sync_requested_queue" ("account_id") WHERE state = 'ready';

CREATE INDEX "idx_sync_requested_queue_updated_at" ON "sync_requested_queue" ("updated_at") WHERE state = 'done';

CREATE UNIQUE INDEX "uq_sync_requested_queue_claimed_key" ON "sync_requested_queue" ("account_id") WHERE state = 'claimed';

CREATE TRIGGER "trg_order_line_enqueue_order_changed_delete" AFTER DELETE ON "order_line" FOR EACH ROW
WHEN OLD."product_id" IS NOT NULL
BEGIN
  INSERT INTO "order_changed_queue" ("order_id", "line_id", "priority") VALUES (OLD."order_id", OLD."id", 'low')
    ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
END;

CREATE TRIGGER "trg_order_line_enqueue_order_changed_insert" AFTER INSERT ON "order_line" FOR EACH ROW
WHEN NEW."product_id" IS NOT NULL
BEGIN
  INSERT INTO "order_changed_queue" ("order_id", "line_id", "priority") VALUES (NEW."order_id", NEW."id", 'low')
    ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
END;

CREATE TRIGGER "trg_order_line_enqueue_order_changed_update" AFTER UPDATE ON "order_line" FOR EACH ROW
WHEN (OLD."product_id" IS NOT NEW."product_id" OR OLD."quantity" IS NOT NEW."quantity") AND NEW."product_id" IS NOT NULL
BEGIN
  INSERT INTO "order_changed_queue" ("order_id", "line_id", "priority") VALUES (NEW."order_id", NEW."id", 'low')
    ON CONFLICT (order_id) WHERE state = 'ready' DO UPDATE SET line_id = excluded.line_id, priority = excluded.priority, due_at = excluded.due_at, attempts = 0, last_error = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
END;
