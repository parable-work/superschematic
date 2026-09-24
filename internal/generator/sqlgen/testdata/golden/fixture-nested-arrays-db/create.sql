-- Generated PostgreSQL DDL for schema: fixture-nested-arrays-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE board (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  labels JSONB NOT NULL,
  states JSONB NOT NULL,
  walls JSONB NOT NULL,
  scores JSONB
);

COMMENT ON TABLE board IS 'A game board whose columns are lists of lists.';

-- Add foreign key constraints

-- Create indexes
