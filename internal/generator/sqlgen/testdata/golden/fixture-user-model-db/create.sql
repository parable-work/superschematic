-- Generated PostgreSQL DDL for schema: fixture-user-model-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

CREATE TABLE role (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  permissions TEXT[] DEFAULT '{}' NOT NULL,
  UNIQUE ("name")
);

COMMENT ON TABLE role IS 'A named set of permissions users are granted.';

CREATE TABLE "session" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_seen_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash)
);

COMMENT ON TABLE "session" IS 'A signed-in session: the user it signs in, the SHA-256 of its token, and when it ends.';

CREATE TABLE "user" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  email CITEXT NOT NULL,
  display_name VARCHAR(80) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  UNIQUE (email)
);

COMMENT ON TABLE "user" IS 'A person who signs in with their email.';

CREATE TABLE user_credential (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  password_hash TEXT NOT NULL,
  password_changed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  disabled_at TIMESTAMPTZ,
  UNIQUE (user_id)
);

COMMENT ON TABLE user_credential IS 'A user''s password hash, kept apart from the user row.';

CREATE TABLE user_role_grant (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  role_id UUID NOT NULL,
  granted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE user_role_grant IS 'A role a user holds.';

-- Add foreign key constraints

ALTER TABLE "session"
  ADD CONSTRAINT fk_session_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_credential
  ADD CONSTRAINT fk_user_credential_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_role_id
  FOREIGN KEY (role_id)
  REFERENCES role(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_session_user ON "session" USING BTREE (user_id);

CREATE UNIQUE INDEX uq_user_role_grant_user_role ON user_role_grant USING BTREE (user_id, role_id);
