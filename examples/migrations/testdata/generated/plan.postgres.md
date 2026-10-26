## Migration plan for `tasks-db` (postgres)

11 steps (10 expand, 1 contract), 6 hazards (1 destructive, 1 blocking, 2 compat, 1 data-dependent, 1 raw-sql).

- From: `5114b56a847c1f1aead0821a376bc6f01951f6e79419bb66fff1bec1de0dd120`
- To: `0817bc633d2835099a5949a496bdec7642a6a362784fef790a4ab093677fd609`
- Between the phases (expanded): `2f2872f14d662868e0ad94843dd05ecaadcf7b8b5488ce5dcb5c73add64d5750`
- Plan: `a2e0e9f5de91d97c4331be32d6cf21af1799ea9d9d75cba4cbeec73783e65782`
- Renames: `task.title=task.summary`

| Class | Subject | Reader | Reason | ID |
| --- | --- | --- | --- | --- |
| compat | `table/task/column/summary` |  | task.title is renamed to task.summary (Task.summary): servers built from the previous version still use the old name. | `compat:table/task/column/summary` |
| blocking | `table/task/column/remind_before` |  | The change rewrites task and rebuilds its indexes under an ACCESS EXCLUSIVE lock. | `blocking:table/task/column/remind_before` |
| compat | `table/task/column/remind_before` |  | Task.remindBefore changes from TEXT[] to BIGINT[]: servers built from the previous version still read and write TEXT[]. | `compat:table/task/column/remind_before` |
| data-dependent | `table/task/column/remind_before` |  | Task.remindBefore changes from TEXT[] to BIGINT[]: the cast fails on a value BIGINT[] cannot hold. | `data-dependent:table/task/column/remind_before` |
| raw-sql | `rawSql/project_open_tasks` |  | The raw SQL project_open_tasks (Project) runs for the first time; the planner cannot judge what it does. | `raw-sql:rawSql/project_open_tasks` |
| destructive | `table/comment/column/legacy_id` |  | Dropping comment.legacy_id deletes the values of Comment.legacyId. | `destructive:table/comment/column/legacy_id` |

### Expand

Runs before the new servers roll out.

**1. renameColumn** `table/task/column/summary`. Hazards: compat.

```sql
ALTER TABLE task RENAME COLUMN title TO summary;
```

**2. addColumn** `table/comment/column/mentions`.

```sql
ALTER TABLE "comment" ADD COLUMN mentions TEXT[] DEFAULT '{}' NOT NULL;
```

**3. addColumn** `table/project/column/status`.

```sql
ALTER TABLE project ADD COLUMN status TEXT DEFAULT 'active' NOT NULL;
```

**4. dropNotNull** `table/comment/column/legacy_id`.

```sql
ALTER TABLE "comment" ALTER COLUMN legacy_id DROP NOT NULL;
```

**5. alterColumnType** `table/task/column/remind_before`. Hazards: blocking, compat, data-dependent.

```sql
ALTER TABLE task
  ALTER COLUMN remind_before DROP DEFAULT,
  ALTER COLUMN remind_before TYPE BIGINT[] USING remind_before::BIGINT[],
  ALTER COLUMN remind_before SET DEFAULT '{}';
```

**6. createIndex** `table/task/index/idx_task_project_due_at`. Runs outside a transaction.

```sql
CREATE INDEX CONCURRENTLY idx_task_project_due_at ON task USING BTREE (project_id, due_at);
```

**7. createIndex** `table/task/index/idx_task_summary_lower`. Runs outside a transaction.

```sql
CREATE INDEX CONCURRENTLY idx_task_summary_lower ON task USING BTREE ((lower(summary)));
```

**8. addCheck** `table/project/constraint/ck_project_status`.

```sql
ALTER TABLE project ADD CONSTRAINT ck_project_status CHECK (status IN ('active', 'archived')) NOT VALID;
```

**9. validateCheck** `table/project/constraint/ck_project_status`.

```sql
ALTER TABLE project VALIDATE CONSTRAINT ck_project_status;
```

**10. runRawSQL** `rawSql/project_open_tasks`. Hazards: raw-sql.

```sql
DROP MATERIALIZED VIEW IF EXISTS project_open_tasks;
CREATE MATERIALIZED VIEW IF NOT EXISTS project_open_tasks AS
      SELECT project_id, count(*) AS open_tasks FROM task WHERE NOT done GROUP BY project_id;
CREATE UNIQUE INDEX IF NOT EXISTS project_open_tasks_project ON project_open_tasks (project_id);
```

### Contract

Runs after the new servers roll out.

**11. dropColumn** `table/comment/column/legacy_id`. Hazards: destructive.

```sql
ALTER TABLE "comment" DROP COLUMN legacy_id;
```
