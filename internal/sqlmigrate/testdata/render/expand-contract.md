## Migration plan for `shop-db` (postgres)

6 steps (4 expand, 2 contract), 4 hazards (1 destructive, 1 compat, 1 data-dependent, 1 api-breaking).

- From: `a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1`
- To: `b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2`
- Plan: `c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3`
- Renames: `purchase=order`, `order.amount=order.total_cents`

| Class | Subject | Reader | Reason | ID |
| --- | --- | --- | --- | --- |
| compat | `table/order` |  | purchase is renamed to order: a server built from the previous version still names purchase. | `compat:table/order` |
| data-dependent | `table/order/column/note` |  | Order.note becomes required: the step fails while a row has no note. | `data-dependent:table/order/column/note` |
| destructive | `table/order/column/total` |  | Order.total is dropped with its data. | `destructive:table/order/column/total` |
| api-breaking | `table/order/column/total` | `shop-api/OrderView.total` | shop-api reads order.total through OrderView.total \| a pipe stays in its cell. | `api-breaking:table/order/column/total@shop-api/OrderView.total` |

### Expand

Runs before the new servers roll out.

**1. renameTable** `table/order`. Hazards: compat.

```sql
ALTER TABLE "purchase" RENAME TO "order";
```

**2. addColumn** `table/order/column/note`.

```sql
ALTER TABLE "order" ADD COLUMN "note" TEXT;
```

**3. createIndex** `table/order/index/order_note_idx`. Runs outside a transaction.

```sql
CREATE INDEX CONCURRENTLY "order_note_idx" ON "order" ("note");
```

**4. replaceFunction** `function/order_history_capture`.

```sql
CREATE OR REPLACE FUNCTION "order_history_capture"() RETURNS TRIGGER AS $$
BEGIN
  INSERT INTO "order_history" SELECT NEW.*;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
```

### Contract

Runs after the new servers roll out.

**5. setNotNull** `table/order/column/note`. Hazards: data-dependent.

```sql
ALTER TABLE "order" VALIDATE CONSTRAINT "order_note_not_null";
ALTER TABLE "order" ALTER COLUMN "note" SET NOT NULL;
ALTER TABLE "order" DROP CONSTRAINT "order_note_not_null";
```

**6. dropColumn** `table/order/column/total`. Hazards: destructive, api-breaking.

```sql
ALTER TABLE "order" DROP COLUMN "total";
```
