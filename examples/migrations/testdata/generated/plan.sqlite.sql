-- Migration plan for tasks-db (sqlite)
-- from:    288da563a4c63dd5f6805bd32a1509d3501ad56808f1feefc1b419c76c5190d8
-- to:      1c45865ddcb6c5199a6527a5ff24f2d1606f6bce578f88208022df1ca4337b8d
-- between: 64c7eaa310f2bb3d987cfd26f6fea030955996ab52957462ef47996a86c871cd (expanded: the model between the phases)
-- plan:    01966f0f87eb965d67fd5b1b065cb7be784406f4eda0758e7b36119195f2caaf
-- renames: task.title=task.summary
-- 3 steps (2 expand, 1 contract), 7 hazards (2 destructive, 2 blocking, 2 compat, 1 copy-table)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, renameColumn table/task/column/summary
-- hazard compat:table/task/column/summary
ALTER TABLE "task" RENAME COLUMN "title" TO "summary";

-- Step 2, expand, copyTable table/comment
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

-- Contract. Runs after the new servers roll out.

-- Step 3, contract, dropColumn table/comment/column/legacy_id
-- hazard destructive:table/comment/column/legacy_id
-- hazard blocking:table/comment/column/legacy_id
ALTER TABLE "comment" DROP COLUMN "legacy_id";
