-- Generated PostgreSQL DDL for schema: fixture-vectors-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS vector;


-- Create tables (without foreign key constraints)

CREATE TABLE ledger_line (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  amount BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL,
  rate NUMERIC(10,4) NOT NULL,
  confidence NUMERIC(5,2),
  quantity NUMERIC,
  booked_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE ledger_line IS 'A line of a ledger: a signed amount in its currency''s minor units, a
debit negative, and the exchange rate it was booked at, exact.';

CREATE TABLE passage (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  topic TEXT,
  embedding vector(3) NOT NULL,
  title_embedding vector(3),
  rates NUMERIC(6,3)[],
  deleted_at TIMESTAMPTZ
);

COMMENT ON TABLE passage IS 'A document a nearest-neighbour search finds, by a small embedding: by
cosine distance through an HNSW index, and by Euclidean distance through
an IVFFlat one, which a migration builds inside its step.';

CREATE TABLE text_embedding (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  text TEXT NOT NULL,
  embedding vector(1024) NOT NULL
);

COMMENT ON TABLE text_embedding IS 'A text and its embedding, 1024 numbers, found by cosine distance.';

-- Add foreign key constraints

-- Create indexes

CREATE INDEX idx_passage_embedding ON passage USING HNSW (embedding vector_cosine_ops);

CREATE INDEX idx_passage_embedding_l2 ON passage USING IVFFLAT (embedding vector_l2_ops) WITH (lists = 4);

CREATE INDEX idx_text_embedding_embedding ON text_embedding USING HNSW (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
