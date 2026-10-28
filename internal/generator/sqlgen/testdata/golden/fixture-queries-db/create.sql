-- Generated PostgreSQL DDL for schema: fixture-queries-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE account (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" VARCHAR(80) NOT NULL
);

COMMENT ON TABLE account IS 'An account a line is booked to.';

CREATE TABLE category (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" VARCHAR(80) NOT NULL,
  parent_id UUID,
  budget NUMERIC(12,2),
  budget_since DATE
);

COMMENT ON TABLE category IS 'A category, which may sit under a parent; the dashboard rolls a
sub-category''s totals up to it. A budget is exact, and starts on a day.';

CREATE TABLE line_item (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "date" TIMESTAMPTZ NOT NULL,
  "name" TEXT NOT NULL,
  amount BIGINT NOT NULL,
  account_id UUID NOT NULL,
  category_id UUID,
  transfer_group_id UUID
);

COMMENT ON TABLE line_item IS 'A line of the ledger: a signed amount in minor units, a debit negative.';

CREATE TABLE transfer_group (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  note TEXT
);

COMMENT ON TABLE transfer_group IS 'A group of lines that move money between two accounts, which no total
counts.';

-- Add foreign key constraints

ALTER TABLE category
  ADD CONSTRAINT fk_category_parent_id
  FOREIGN KEY (parent_id)
  REFERENCES category(id)
  ON DELETE RESTRICT;

ALTER TABLE line_item
  ADD CONSTRAINT fk_line_item_account_id
  FOREIGN KEY (account_id)
  REFERENCES account(id)
  ON DELETE RESTRICT;

ALTER TABLE line_item
  ADD CONSTRAINT fk_line_item_category_id
  FOREIGN KEY (category_id)
  REFERENCES category(id)
  ON DELETE SET NULL;

ALTER TABLE line_item
  ADD CONSTRAINT fk_line_item_transfer_group_id
  FOREIGN KEY (transfer_group_id)
  REFERENCES transfer_group(id)
  ON DELETE SET NULL;

-- Create indexes

CREATE INDEX idx_category_parent ON category USING BTREE (parent_id);

CREATE INDEX idx_line_item_date ON line_item USING BTREE ("date");

CREATE INDEX idx_line_item_category ON line_item USING BTREE (category_id);

-- Create projection views: read-only relations over the tables above. A
-- setting a view binds without optional makes current_setting raise while
-- it is unset, so the view fails closed.

CREATE VIEW line_list WITH (security_barrier = true) AS
-- Projection column order is the published Arrow contract.
SELECT -- noqa: ST06
  base.id,
  base."date",
  base."name",
  base.account_id,
  account."name" AS account_name,
  base.category_id,
  category."name" AS category_name
FROM line_item AS base
  INNER JOIN account AS account ON base.account_id = account.id
  LEFT JOIN category AS category ON base.category_id = category.id
;

COMMENT ON VIEW line_list IS 'A line with its account''s and its category''s names, flat: a plain view the dashboard''s line list reads. The lock holds a query over it to the projection''s model, its joins and its columns (D95).';
