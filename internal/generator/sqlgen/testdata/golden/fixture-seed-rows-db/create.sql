-- Generated PostgreSQL DDL for schema: fixture-seed-rows-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

CREATE TABLE region (
  code CITEXT NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL
);

COMMENT ON TABLE region IS 'A region, keyed by the code each row gives as the table''s key.';

CREATE TABLE reminder (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  member UUID,
  kind TEXT DEFAULT '*' NOT NULL,
  lead_minutes BIGINT NOT NULL,
  quiet_start TIME
);

COMMENT ON TABLE reminder IS 'A reminder''s lead time for a member, or for everyone: a row of no
member is the default each member without one of their own takes. No
unique constraint holds the rows, so the seed names the fields that
identify one, a null member among them.';

CREATE TABLE tier (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  slug CITEXT NOT NULL,
  title TEXT NOT NULL,
  seats BIGINT NOT NULL,
  listed BOOLEAN NOT NULL,
  features TEXT[] DEFAULT '{}' NOT NULL,
  limits JSONB,
  note TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  UNIQUE (slug)
);

COMMENT ON TABLE tier IS 'A tier a workspace subscribes to. The rows are keyed by slug, the
unique field every row gives; each holds text, a number, a boolean, a
list, JSON and a null, and leaves its key and createdAt to their
columns'' defaults.';

-- Add foreign key constraints

-- Create indexes

-- Seed the rows each table's @seedRows declares: a row whose key the
-- table holds already is left as it is

INSERT INTO region (code, "name")
SELECT 'eu'::CITEXT, 'Europe'::TEXT
WHERE NOT EXISTS (SELECT 1 FROM region WHERE code = 'eu'::CITEXT)
ON CONFLICT DO NOTHING;

INSERT INTO region (code, "name")
SELECT 'us'::CITEXT, 'United States'::TEXT
WHERE NOT EXISTS (SELECT 1 FROM region WHERE code = 'us'::CITEXT)
ON CONFLICT DO NOTHING;

INSERT INTO reminder (member, kind, lead_minutes, quiet_start)
SELECT NULL::UUID, '*'::TEXT, 30::BIGINT, '22:00'::TIME
WHERE NOT EXISTS (SELECT 1 FROM reminder WHERE member IS NULL AND kind = '*'::TEXT)
ON CONFLICT DO NOTHING;

INSERT INTO reminder (member, kind, lead_minutes)
SELECT NULL::UUID, 'deadline'::TEXT, 120::BIGINT
WHERE NOT EXISTS (SELECT 1 FROM reminder WHERE member IS NULL AND kind = 'deadline'::TEXT)
ON CONFLICT DO NOTHING;

INSERT INTO tier (slug, title, seats, listed, features)
SELECT 'free'::CITEXT, 'Free'::TEXT, 1::BIGINT, TRUE::BOOLEAN, ARRAY['inbox']::TEXT[]
WHERE NOT EXISTS (SELECT 1 FROM tier WHERE slug = 'free'::CITEXT)
ON CONFLICT DO NOTHING;

INSERT INTO tier (slug, title, seats, listed, features, limits)
SELECT 'team'::CITEXT, 'Team'::TEXT, 10::BIGINT, TRUE::BOOLEAN, ARRAY['inbox', 'calendar']::TEXT[], '{"exports":true,"storageGb":50}'::JSONB
WHERE NOT EXISTS (SELECT 1 FROM tier WHERE slug = 'team'::CITEXT)
ON CONFLICT DO NOTHING;

INSERT INTO tier (slug, title, seats, listed, features, note)
SELECT 'internal'::CITEXT, 'Staff''s own'::TEXT, 0::BIGINT, FALSE::BOOLEAN, '{}'::TEXT[], NULL::TEXT
WHERE NOT EXISTS (SELECT 1 FROM tier WHERE slug = 'internal'::CITEXT)
ON CONFLICT DO NOTHING;
