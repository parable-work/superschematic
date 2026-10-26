-- Generated PostgreSQL DDL for schema: fixture-ddl-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE category (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  label TEXT NOT NULL,
  kind TEXT NOT NULL,
  active BOOLEAN DEFAULT false NOT NULL,
  sort_order DOUBLE PRECISION DEFAULT 0 NOT NULL,
  parent_id UUID,
  CONSTRAINT ck_category_label CHECK (length(label) >= 1 AND length(label) <= 60),
  CONSTRAINT ck_category_kind CHECK (kind IN ('personal', 'business', 'accounting')),
  CONSTRAINT ck_category_sort_order CHECK (sort_order >= 0 AND sort_order <= 1000)
);

COMMENT ON TABLE category IS 'A category of spending. A category may sit under another, and deleting
the parent leaves its children at the top level. A label is unique
whatever its case, and the active categories are looked up by kind.';

CREATE TABLE line_item (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  category_id UUID,
  amount_cents BIGINT NOT NULL,
  memo TEXT DEFAULT 'it''s unexplained' NOT NULL,
  confidence DOUBLE PRECISION,
  posted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  external_id TEXT,
  merchant JSONB,
  CONSTRAINT ck_line_item_confidence CHECK (confidence >= 0 AND confidence <= 1)
);

COMMENT ON TABLE line_item IS 'A line of spending. Deleting its category leaves the line uncategorized.
The dashboard reads each category''s total from a materialized view the
raw SQL keeps, keyed by its category so it refreshes concurrently. Lines
are read by when they were posted; a migration builds that index inside
its step, as the table is small. A line a bank import posted holds the
bank''s id for it, unique among such lines, which the import upserts by.';

-- Add foreign key constraints

ALTER TABLE category
  ADD CONSTRAINT fk_category_parent_id
  FOREIGN KEY (parent_id)
  REFERENCES category(id)
  ON DELETE SET NULL;

ALTER TABLE line_item
  ADD CONSTRAINT fk_line_item_category_id
  FOREIGN KEY (category_id)
  REFERENCES category(id)
  ON DELETE SET NULL;

-- Create indexes

CREATE UNIQUE INDEX uq_category_label_lower ON category USING BTREE ((lower(label)));

CREATE INDEX idx_category_active_kind ON category USING BTREE (kind) WHERE active;

CREATE INDEX idx_line_item_posted_at ON line_item USING BTREE (posted_at);

CREATE UNIQUE INDEX uq_line_item_external ON line_item USING BTREE (external_id) WHERE external_id IS NOT NULL;

-- Raw SQL the schema declares (@rawSql), after every object above

-- category_totals (LineItem)
DROP MATERIALIZED VIEW IF EXISTS category_totals;
CREATE MATERIALIZED VIEW IF NOT EXISTS category_totals AS
      SELECT category_id, sum(amount_cents) AS total_cents, count(*) AS line_count
      FROM line_item
      GROUP BY category_id;
COMMENT ON MATERIALIZED VIEW category_totals IS 'Each category''s total, for the dashboard.';

-- category_totals_key (LineItem, after category_totals)
CREATE UNIQUE INDEX IF NOT EXISTS category_totals_category ON category_totals (category_id);
