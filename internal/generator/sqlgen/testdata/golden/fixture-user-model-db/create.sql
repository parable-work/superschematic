-- Generated PostgreSQL DDL for schema: fixture-user-model-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

CREATE TABLE oauth_client (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  client_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  "name" TEXT NOT NULL,
  uri TEXT,
  redirect_uris TEXT[] DEFAULT '{}' NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  refreshed_at TIMESTAMPTZ,
  fresh_until TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  UNIQUE (client_id),
  CONSTRAINT ck_oauth_client_client_id CHECK (length(client_id) >= 1)
);

COMMENT ON TABLE oauth_client IS 'An OAuth client: a metadata document''s, cached, or one that registered.';

CREATE TABLE oauth_code (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  client_id UUID NOT NULL,
  user_id UUID,
  grant_id UUID,
  redirect_uri TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  state TEXT,
  scope TEXT,
  resource TEXT NOT NULL,
  nonce TEXT NOT NULL,
  code_hash VARCHAR(64),
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  used_at TIMESTAMPTZ,
  UNIQUE (code_hash)
);

COMMENT ON TABLE oauth_code IS 'An OAuth authorization request: pending while its user consents, then the code its client redeems once.';

CREATE TABLE oauth_grant (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  client_id UUID NOT NULL,
  permissions TEXT[],
  resource TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ
);

COMMENT ON TABLE oauth_grant IS 'An OAuth grant: the user who gave it, the client that holds it, the permissions it narrows them to, and when it ends.';

CREATE TABLE oauth_token (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  grant_id UUID NOT NULL,
  kind TEXT NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash)
);

COMMENT ON TABLE oauth_token IS 'An OAuth access or refresh token: the grant that issued it, the SHA-256 of its text, and when it ends.';

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

CREATE TABLE user_api_key (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  token_hash VARCHAR(64) NOT NULL,
  permissions TEXT[],
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  expires_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (token_hash),
  CONSTRAINT ck_user_api_key_name CHECK (length("name") >= 1 AND length("name") <= 100)
);

COMMENT ON TABLE user_api_key IS 'An API key: the user it calls as, its name, the SHA-256 of its token, and when it ends.';

CREATE TABLE user_credential (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  user_id UUID NOT NULL,
  password_hash TEXT NOT NULL,
  password_changed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  disabled_at TIMESTAMPTZ,
  pending_at TIMESTAMPTZ,
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

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_client_id
  FOREIGN KEY (client_id)
  REFERENCES oauth_client(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_grant_id
  FOREIGN KEY (grant_id)
  REFERENCES oauth_grant(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_grant
  ADD CONSTRAINT fk_oauth_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_grant
  ADD CONSTRAINT fk_oauth_grant_client_id
  FOREIGN KEY (client_id)
  REFERENCES oauth_client(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_token
  ADD CONSTRAINT fk_oauth_token_grant_id
  FOREIGN KEY (grant_id)
  REFERENCES oauth_grant(id)
  ON DELETE CASCADE;

ALTER TABLE "session"
  ADD CONSTRAINT fk_session_user_id
  FOREIGN KEY (user_id)
  REFERENCES "user"(id)
  ON DELETE CASCADE;

ALTER TABLE user_api_key
  ADD CONSTRAINT fk_user_api_key_user_id
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

CREATE INDEX idx_oauth_grant_user ON oauth_grant USING BTREE (user_id);

CREATE INDEX idx_oauth_token_grant ON oauth_token USING BTREE (grant_id);

CREATE INDEX idx_session_user ON "session" USING BTREE (user_id);

CREATE INDEX idx_user_api_key_user ON user_api_key USING BTREE (user_id);

CREATE UNIQUE INDEX uq_user_role_grant_user_role ON user_role_grant USING BTREE (user_id, role_id);

-- Seed the roles a UserRole table's @seedRoles declares: a role of the
-- name that exists keeps its permissions unless it has none

INSERT INTO role ("name", permissions) VALUES ('member', ARRAY['identity.roles.read']::TEXT[])
ON CONFLICT ("name") DO UPDATE SET permissions = EXCLUDED.permissions WHERE cardinality(role.permissions) = 0;
