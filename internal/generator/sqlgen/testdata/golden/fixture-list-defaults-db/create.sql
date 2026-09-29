-- Generated PostgreSQL DDL for schema: fixture-list-defaults-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE tasting (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  tasted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  tasted_on DATE DEFAULT CURRENT_DATE NOT NULL,
  poured_at TIME DEFAULT CURRENT_TIME NOT NULL,
  retasted_at TIMESTAMPTZ[] DEFAULT '{}' NOT NULL,
  retasted_on DATE[] DEFAULT '{}' NOT NULL,
  repoured_at TIME[] DEFAULT '{}' NOT NULL,
  restocked_at TIMESTAMPTZ[] DEFAULT '{}' NOT NULL,
  flights JSONB NOT NULL,
  opened_at JSONB NOT NULL,
  bottled_at JSONB NOT NULL
);

COMMENT ON TABLE tasting IS 'A tasting whose required columns hold temporal values. A required
temporal scalar defaults to the current date or time; a required list of
one defaults to an empty array; a list of lists, a map or a JSON field
holding one has no default.';

-- Add foreign key constraints

-- Create indexes
