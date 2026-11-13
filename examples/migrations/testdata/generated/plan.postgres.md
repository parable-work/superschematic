## Migration plan for `tasks-db` (postgres)

13 steps (11 expand, 2 contract), 6 hazards (1 destructive, 1 blocking, 2 compat, 1 data-dependent, 1 raw-sql).

- From: `5114b56a847c1f1aead0821a376bc6f01951f6e79419bb66fff1bec1de0dd120`
- To: `cfb8597dfc0750c84a20f5bb196960b4a7f4abb424af76251d981c21ba2acca1`
- Between the phases (expanded): `d82ab463bc0dc48148b886c2d7bb03c500988adc8d3f9e75bb9451c77cf7824b`
- Plan: `a4c8cd4414214c3e6417b37449f720fb259c13f05e3fa68e668c5ceb38ca2b4a`
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

**10. createFunction** `function/project_status_transition_guard`.

```sql
CREATE OR REPLACE FUNCTION project_status_transition_guard() RETURNS trigger AS $$
BEGIN
  -- The @stateMachine of Project.status (D117): an update that leaves the
  -- column's value is no move, and any other must be one the machine lists.
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.status IN ('archived') THEN
    RAISE EXCEPTION 'project.status is terminal at %: it cannot move to %', OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF (OLD.status = 'active' AND NEW.status IN ('archived'))
  THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'project.status cannot move from % to %', OLD.status, NEW.status
    USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;
```

**11. runRawSQL** `rawSql/project_open_tasks`. Hazards: raw-sql.

```sql
DROP MATERIALIZED VIEW IF EXISTS project_open_tasks;
CREATE MATERIALIZED VIEW IF NOT EXISTS project_open_tasks AS
      SELECT project_id, count(*) AS open_tasks FROM task WHERE NOT done GROUP BY project_id;
CREATE UNIQUE INDEX IF NOT EXISTS project_open_tasks_project ON project_open_tasks (project_id);
```

### Contract

Runs after the new servers roll out.

**12. createTrigger** `trigger/project/trg_project_status_transition_guard`.

```sql
CREATE TRIGGER trg_project_status_transition_guard
  BEFORE UPDATE OF status ON project
  FOR EACH ROW EXECUTE FUNCTION project_status_transition_guard();
```

**13. dropColumn** `table/comment/column/legacy_id`. Hazards: destructive.

```sql
ALTER TABLE "comment" DROP COLUMN legacy_id;
```
