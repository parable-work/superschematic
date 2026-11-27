-- Generated PostgreSQL DDL for schema: fixture-variant-field-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE step (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  kind TEXT NOT NULL,
  result JSONB,
  CONSTRAINT ck_step_kind CHECK (kind IN ('verify', 'review', 'note')),
  CONSTRAINT ck_step_result CHECK (CASE WHEN kind = 'verify' THEN COALESCE(jsonb_typeof(result), 'null') = 'null' OR COALESCE(jsonb_typeof(result) = 'object' AND jsonb_typeof(result -> 'passed') = 'boolean' AND (COALESCE(jsonb_typeof(result -> 'checks'), 'null') = 'null' OR jsonb_typeof(result -> 'checks') = 'array'), false) WHEN kind = 'review' THEN COALESCE(jsonb_typeof(result), 'null') = 'null' OR COALESCE(jsonb_typeof(result) = 'object' AND (jsonb_typeof(result -> 'verdict') = 'string' AND (result ->> 'verdict') IN ('approved', 'changes_requested')) AND (COALESCE(jsonb_typeof(result -> 'notes'), 'null') = 'null' OR jsonb_typeof(result -> 'notes') = 'string'), false) ELSE COALESCE(jsonb_typeof(result), 'null') = 'null' END)
);

COMMENT ON TABLE step IS 'One step of a run.';

-- Add foreign key constraints

-- Create indexes
