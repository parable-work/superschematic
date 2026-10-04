## Migration plan for `edge-db` (sqlite)

1 step (1 expand, 0 contract), 2 hazards (1 blocking, 1 copy-table).

- From: `a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1`
- To: `b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2`
- Plan: `c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3`

| Class | Subject | Reader | Reason | ID |
| --- | --- | --- | --- | --- |
| blocking | `table/reading` |  | Reading.value changes from INTEGER to REAL: SQLite rebuilds the table. | `blocking:table/reading` |
| copy-table | `table/reading` |  | SQLite's ALTER TABLE cannot retype a column. | `copy-table:table/reading` |

### Expand

Runs before the new servers roll out.

**1. copyTable** `table/reading`. Hazards: blocking, copy-table. Runs with foreign keys off, checked before its commit.

```sql
CREATE TABLE "reading_new" ("id" TEXT NOT NULL PRIMARY KEY, "value" REAL NOT NULL);
INSERT INTO "reading_new" ("id", "value") SELECT "id", "value" FROM "reading";
DROP TABLE "reading";
ALTER TABLE "reading_new" RENAME TO "reading";
```
