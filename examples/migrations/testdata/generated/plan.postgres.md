## Migration plan for `tasks-db` (postgres)

6 steps (5 expand, 1 contract), 5 hazards (1 destructive, 1 blocking, 2 compat, 1 data-dependent).

- From: `5114b56a847c1f1aead0821a376bc6f01951f6e79419bb66fff1bec1de0dd120`
- To: `cd286b327e6cf2f11d124a5294db88ef6370e2b19d3e419def0ac833f962cc24`
- Between the phases (expanded): `23d1d979eff87184c600b9bb0dfb13eb850c430a7677da58423ed46521d4044d`
- Plan: `9356e2dc27a8f63adcff9fd5cfa5a717a6b35326cf0ec7858c54927f01c672d3`
- Renames: `task.title=task.summary`

| Class | Subject | Reader | Reason | ID |
| --- | --- | --- | --- | --- |
| compat | `table/task/column/summary` |  | task.title is renamed to task.summary (Task.summary): servers built from the previous version still use the old name. | `compat:table/task/column/summary` |
| blocking | `table/task/column/remind_before` |  | The change rewrites task and rebuilds its indexes under an ACCESS EXCLUSIVE lock. | `blocking:table/task/column/remind_before` |
| compat | `table/task/column/remind_before` |  | Task.remindBefore changes from TEXT[] to BIGINT[]: servers built from the previous version still read and write TEXT[]. | `compat:table/task/column/remind_before` |
| data-dependent | `table/task/column/remind_before` |  | Task.remindBefore changes from TEXT[] to BIGINT[]: the cast fails on a value BIGINT[] cannot hold. | `data-dependent:table/task/column/remind_before` |
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

**3. dropNotNull** `table/comment/column/legacy_id`.

```sql
ALTER TABLE "comment" ALTER COLUMN legacy_id DROP NOT NULL;
```

**4. alterColumnType** `table/task/column/remind_before`. Hazards: blocking, compat, data-dependent.

```sql
ALTER TABLE task
  ALTER COLUMN remind_before DROP DEFAULT,
  ALTER COLUMN remind_before TYPE BIGINT[] USING remind_before::BIGINT[],
  ALTER COLUMN remind_before SET DEFAULT '{}';
```

**5. createIndex** `table/task/index/idx_task_project_due_at`. Runs outside a transaction.

```sql
CREATE INDEX CONCURRENTLY idx_task_project_due_at ON task USING BTREE (project_id, due_at);
```

### Contract

Runs after the new servers roll out.

**6. dropColumn** `table/comment/column/legacy_id`. Hazards: destructive.

```sql
ALTER TABLE "comment" DROP COLUMN legacy_id;
```
