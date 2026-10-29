-- Generated PostgreSQL DDL for schema: fixture-vault-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;


-- Create tables (without foreign key constraints)

CREATE TABLE api_credential (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  service TEXT NOT NULL,
  secret TEXT NOT NULL,
  rotated_at TIMESTAMPTZ,
  UNIQUE (service)
);

COMMENT ON TABLE api_credential IS 'A service''s credential, whose key the code that creates it chooses or the ORM generates.';

CREATE TABLE plaid_item (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  item_id TEXT NOT NULL,
  institution TEXT NOT NULL,
  access_token TEXT NOT NULL,
  webhook_secret TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  deleted_at TIMESTAMPTZ,
  UNIQUE (item_id)
);

-- Add search_text generated column for trigram search
ALTER TABLE plaid_item ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE(institution, '')
) STORED;

COMMENT ON TABLE plaid_item IS 'A bank link: the access token is a long-lived bank credential, kept encrypted.';

-- Add foreign key constraints

-- Create indexes

CREATE INDEX idx_plaid_item_search_trgm ON plaid_item USING GIN (search_text gin_trgm_ops);
