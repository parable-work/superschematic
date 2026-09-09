-- Generated PostgreSQL DDL for schema: fixture-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;


-- Create tables (without foreign key constraints)

CREATE TABLE tenant (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" VARCHAR(80) NOT NULL,
  slug CITEXT NOT NULL,
  email TEXT NOT NULL,
  status TEXT NOT NULL,
  is_active BOOLEAN NOT NULL,
  seat_count DOUBLE PRECISION NOT NULL,
  metadata JSONB NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL,
  UNIQUE (slug)
);

-- Add search_text generated column for trigram search
ALTER TABLE tenant ADD COLUMN search_text TEXT GENERATED ALWAYS AS (
  COALESCE("name", '')
) STORED;

COMMENT ON TABLE tenant IS 'A tenant of the platform.';

CREATE TABLE tenant_user (
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  tenant_id UUID NOT NULL,
  display_name TEXT,
  deleted_at TIMESTAMPTZ,
  deleted_by UUID,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE tenant_user IS 'A soft-deletable tenant membership.';

-- Create history tables for versioned entities

CREATE TABLE tenant_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_tenant_history_id_version ON tenant_history (id, _version);
CREATE INDEX idx_tenant_history_id_recorded ON tenant_history (id, recorded_at);

COMMENT ON TABLE tenant_history IS 'Version history for tenant';

CREATE TABLE tenant_user_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_tenant_user_history_id_version ON tenant_user_history (id, _version);
CREATE INDEX idx_tenant_user_history_id_recorded ON tenant_user_history (id, recorded_at);

COMMENT ON TABLE tenant_user_history IS 'Version history for tenant_user';

-- Add foreign key constraints

ALTER TABLE tenant_user
  ADD CONSTRAINT fk_tenant_user_tenant_id
  FOREIGN KEY (tenant_id)
  REFERENCES tenant(id)
  ON DELETE RESTRICT;

-- Create indexes

CREATE UNIQUE INDEX uq_tenant_slug ON tenant USING BTREE (slug);

CREATE INDEX idx_tenant_search_trgm ON tenant USING GIN (search_text gin_trgm_ops);

CREATE UNIQUE INDEX idx_tenant_user_tenant_display_name ON tenant_user USING BTREE (tenant_id, display_name) WHERE deleted_at IS NULL;

-- Create history capture functions and triggers

CREATE OR REPLACE FUNCTION tenant_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image.
    INSERT INTO tenant_history (id, _version, operation, data)
    VALUES (OLD.id, OLD._version + 1, 'DELETE', to_jsonb(OLD));
    RETURN OLD;
  END IF;
  IF (TG_OP = 'UPDATE') THEN
    NEW._version := OLD._version + 1;
  END IF;
  INSERT INTO tenant_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tenant_capture_history_write
  BEFORE INSERT OR UPDATE ON tenant
  FOR EACH ROW EXECUTE FUNCTION tenant_capture_history();

CREATE TRIGGER trg_tenant_capture_history_delete
  AFTER DELETE ON tenant
  FOR EACH ROW EXECUTE FUNCTION tenant_capture_history();

CREATE OR REPLACE FUNCTION tenant_user_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image.
    INSERT INTO tenant_user_history (id, _version, operation, data)
    VALUES (OLD.id, OLD._version + 1, 'DELETE', to_jsonb(OLD));
    RETURN OLD;
  END IF;
  IF (TG_OP = 'UPDATE') THEN
    NEW._version := OLD._version + 1;
  END IF;
  INSERT INTO tenant_user_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tenant_user_capture_history_write
  BEFORE INSERT OR UPDATE ON tenant_user
  FOR EACH ROW EXECUTE FUNCTION tenant_user_capture_history();

CREATE TRIGGER trg_tenant_user_capture_history_delete
  AFTER DELETE ON tenant_user
  FOR EACH ROW EXECUTE FUNCTION tenant_user_capture_history();
