-- Migration plan for tasks-db (sqlite)
-- from:    288da563a4c63dd5f6805bd32a1509d3501ad56808f1feefc1b419c76c5190d8
-- to:      c0386a186512d9f720437b2b2512ae0cdac9b297981d04995a5d641ebeb9ca50
-- between: d709077e86ae51c5261ddbd645ac68d3022b605a4af5ddf50a9a326955e3d355 (expanded: the model between the phases)
-- plan:    6d385d9c8129d0c4610bfd0d398c1a366ef04c934eb236271030ecc396a3e1a2
-- renames: task.title=task.summary
-- 5 steps (3 expand, 2 contract), 7 hazards (2 destructive, 2 blocking, 2 compat, 1 copy-table)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, renameColumn table/task/column/summary
-- hazard compat:table/task/column/summary
ALTER TABLE "task" RENAME COLUMN "title" TO "summary";

-- Step 2, expand, addColumn table/project/column/status
ALTER TABLE "project" ADD COLUMN "status" TEXT DEFAULT 'active' NOT NULL CHECK ("status" IN ('active', 'archived'));

-- Step 3, expand, copyTable table/comment
-- hazard destructive:table/task/column/remind_before
-- hazard blocking:table/comment
-- hazard compat:table/task/column/remind_before
-- hazard copy-table:table/comment
PRAGMA defer_foreign_keys = ON;
CREATE TABLE "_new_task" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "project_id" TEXT NOT NULL,
  "summary" TEXT NOT NULL,
  "done" INTEGER NOT NULL,
  "due_at" TEXT,
  "remind_before" TEXT DEFAULT '[]' NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("project_id") REFERENCES "project" ("id") ON DELETE CASCADE
);
CREATE TABLE "_new_comment" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "task_id" TEXT NOT NULL,
  "body" TEXT NOT NULL,
  "mentions" TEXT DEFAULT '[]' NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "legacy_id" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("task_id") REFERENCES "_new_task" ("id") ON DELETE CASCADE
);
INSERT INTO "_new_task" ("id", "project_id", "summary", "done", "due_at", "remind_before", "created_at")
SELECT "id", "project_id", "summary", "done", "due_at", CASE WHEN "task"."remind_before" IS NOT NULL THEN (SELECT json_group_array(CAST("_element"."value" AS INTEGER) ORDER BY "_element"."key") FROM json_each("task"."remind_before") AS "_element") END, "created_at"
FROM "task";
INSERT INTO "_new_comment" ("id", "task_id", "body", "created_at", "legacy_id")
SELECT "id", "task_id", "body", "created_at", "legacy_id"
FROM "comment";
DROP TABLE "comment";
DROP TABLE "task";
ALTER TABLE "_new_task" RENAME TO "task";
ALTER TABLE "_new_comment" RENAME TO "comment";
CREATE INDEX "idx_task_project_due_at" ON "task" ("project_id", "due_at");
CREATE INDEX "idx_task_summary_lower" ON "task" ((lower(summary)));

-- Contract. Runs after the new servers roll out.

-- Step 4, contract, createTrigger trigger/project/trg_project_status_transition_guard
CREATE TRIGGER "trg_project_status_transition_guard" BEFORE UPDATE OF "status" ON "project" FOR EACH ROW
WHEN NEW."status" IS NOT OLD."status" AND (
  OLD."status" IN ('archived')
  OR NOT COALESCE(
    (OLD."status" IS 'active' AND NEW."status" IN ('archived')), 0))
BEGIN
  SELECT RAISE(ABORT, 'project.status cannot make this move: its state machine does not list it');
END;

-- Step 5, contract, dropColumn table/comment/column/legacy_id
-- hazard destructive:table/comment/column/legacy_id
-- hazard blocking:table/comment/column/legacy_id
ALTER TABLE "comment" DROP COLUMN "legacy_id";
