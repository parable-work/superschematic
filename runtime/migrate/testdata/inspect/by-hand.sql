-- A SQLite file an app built by hand before it had a schema (D116), which
-- byhand-db (internal/sqlmigrate/testdata/services/byhand) adopts: its
-- columns in another order, a UNIQUE written in a table's definition, a
-- column and a foreign key the schema has and the file has not, and a
-- column, an index, a table, a trigger and a view the schema has not.
CREATE TABLE customer (
  name TEXT NOT NULL,
  id TEXT NOT NULL PRIMARY KEY,
  email VARCHAR(255) NOT NULL UNIQUE,
  active INTEGER NOT NULL DEFAULT TRUE,
  nickname TEXT
);

CREATE TABLE "order" (
  id TEXT NOT NULL PRIMARY KEY,
  customer_id TEXT NOT NULL,
  total REAL NOT NULL,
  state TEXT NOT NULL DEFAULT 'open' CHECK (state in ('open','shipped')),
  tags TEXT NOT NULL DEFAULT '[]',
  placed_at TEXT NOT NULL
);

CREATE INDEX order_by_total ON "order" (total);

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  order_id TEXT,
  what TEXT
);

CREATE TRIGGER order_audit AFTER UPDATE ON "order"
BEGIN
  INSERT INTO audit_log (order_id, what) VALUES (NEW.id, 'updated');
END;

CREATE VIEW order_totals AS
  SELECT customer_id, sum(total) AS total FROM "order" GROUP BY customer_id;

INSERT INTO customer (name, id, email, nickname) VALUES ('Ada', 'c1', 'ada@example.com', 'ada');
INSERT INTO "order" (id, customer_id, total, placed_at) VALUES ('o1', 'c1', 12.5, '2026-01-02T03:04:05.000Z');
