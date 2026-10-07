-- Migration plan for tasks-db (postgres)
-- from:    5114b56a847c1f1aead0821a376bc6f01951f6e79419bb66fff1bec1de0dd120
-- to:      cd286b327e6cf2f11d124a5294db88ef6370e2b19d3e419def0ac833f962cc24
-- between: 23d1d979eff87184c600b9bb0dfb13eb850c430a7677da58423ed46521d4044d (expanded: the model between the phases)
-- plan:    9356e2dc27a8f63adcff9fd5cfa5a717a6b35326cf0ec7858c54927f01c672d3
-- renames: task.title=task.summary
-- 6 steps (5 expand, 1 contract), 5 hazards (1 destructive, 1 blocking, 2 compat, 1 data-dependent)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, renameColumn table/task/column/summary
-- hazard compat:table/task/column/summary
ALTER TABLE task RENAME COLUMN title TO summary;

-- Step 2, expand, addColumn table/comment/column/mentions
ALTER TABLE "comment" ADD COLUMN mentions TEXT[] DEFAULT '{}' NOT NULL;

-- Step 3, expand, dropNotNull table/comment/column/legacy_id
ALTER TABLE "comment" ALTER COLUMN legacy_id DROP NOT NULL;

-- Step 4, expand, alterColumnType table/task/column/remind_before
-- hazard blocking:table/task/column/remind_before
-- hazard compat:table/task/column/remind_before
-- hazard data-dependent:table/task/column/remind_before
ALTER TABLE task
  ALTER COLUMN remind_before DROP DEFAULT,
  ALTER COLUMN remind_before TYPE BIGINT[] USING remind_before::BIGINT[],
  ALTER COLUMN remind_before SET DEFAULT '{}';

-- Step 5, expand, createIndex table/task/index/idx_task_project_due_at
-- not in a transaction: each statement runs on its own
-- recovery: DROP INDEX CONCURRENTLY IF EXISTS idx_task_project_due_at;
CREATE INDEX CONCURRENTLY idx_task_project_due_at ON task USING BTREE (project_id, due_at);

-- Contract. Runs after the new servers roll out.

-- Step 6, contract, dropColumn table/comment/column/legacy_id
-- hazard destructive:table/comment/column/legacy_id
ALTER TABLE "comment" DROP COLUMN legacy_id;
