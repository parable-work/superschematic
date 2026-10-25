-- Migration plan for tasks-db (postgres)
-- from:    5114b56a847c1f1aead0821a376bc6f01951f6e79419bb66fff1bec1de0dd120
-- to:      0817bc633d2835099a5949a496bdec7642a6a362784fef790a4ab093677fd609
-- between: 2f2872f14d662868e0ad94843dd05ecaadcf7b8b5488ce5dcb5c73add64d5750 (expanded: the model between the phases)
-- plan:    a2e0e9f5de91d97c4331be32d6cf21af1799ea9d9d75cba4cbeec73783e65782
-- renames: task.title=task.summary
-- 11 steps (10 expand, 1 contract), 6 hazards (1 destructive, 1 blocking, 2 compat, 1 data-dependent, 1 raw-sql)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, renameColumn table/task/column/summary
-- hazard compat:table/task/column/summary
ALTER TABLE task RENAME COLUMN title TO summary;

-- Step 2, expand, addColumn table/comment/column/mentions
ALTER TABLE "comment" ADD COLUMN mentions TEXT[] DEFAULT '{}' NOT NULL;

-- Step 3, expand, addColumn table/project/column/status
ALTER TABLE project ADD COLUMN status TEXT DEFAULT 'active' NOT NULL;

-- Step 4, expand, dropNotNull table/comment/column/legacy_id
ALTER TABLE "comment" ALTER COLUMN legacy_id DROP NOT NULL;

-- Step 5, expand, alterColumnType table/task/column/remind_before
-- hazard blocking:table/task/column/remind_before
-- hazard compat:table/task/column/remind_before
-- hazard data-dependent:table/task/column/remind_before
ALTER TABLE task
  ALTER COLUMN remind_before DROP DEFAULT,
  ALTER COLUMN remind_before TYPE BIGINT[] USING remind_before::BIGINT[],
  ALTER COLUMN remind_before SET DEFAULT '{}';

-- Step 6, expand, createIndex table/task/index/idx_task_project_due_at
-- not in a transaction: each statement runs on its own
-- recovery: DROP INDEX CONCURRENTLY IF EXISTS idx_task_project_due_at;
CREATE INDEX CONCURRENTLY idx_task_project_due_at ON task USING BTREE (project_id, due_at);

-- Step 7, expand, createIndex table/task/index/idx_task_summary_lower
-- not in a transaction: each statement runs on its own
-- recovery: DROP INDEX CONCURRENTLY IF EXISTS idx_task_summary_lower;
CREATE INDEX CONCURRENTLY idx_task_summary_lower ON task USING BTREE ((lower(summary)));

-- Step 8, expand, addCheck table/project/constraint/ck_project_status
ALTER TABLE project ADD CONSTRAINT ck_project_status CHECK (status IN ('active', 'archived')) NOT VALID;

-- Step 9, expand, validateCheck table/project/constraint/ck_project_status
ALTER TABLE project VALIDATE CONSTRAINT ck_project_status;

-- Step 10, expand, runRawSQL rawSql/project_open_tasks
-- hazard raw-sql:rawSql/project_open_tasks
DROP MATERIALIZED VIEW IF EXISTS project_open_tasks;
CREATE MATERIALIZED VIEW IF NOT EXISTS project_open_tasks AS
      SELECT project_id, count(*) AS open_tasks FROM task WHERE NOT done GROUP BY project_id;
CREATE UNIQUE INDEX IF NOT EXISTS project_open_tasks_project ON project_open_tasks (project_id);

-- Contract. Runs after the new servers roll out.

-- Step 11, contract, dropColumn table/comment/column/legacy_id
-- hazard destructive:table/comment/column/legacy_id
ALTER TABLE "comment" DROP COLUMN legacy_id;
