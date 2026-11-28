-- Generated SQLite DDL for schema: fixture-user-model-db
-- This file is auto-generated. Do not edit manually.
-- It is the migration plan from an empty database to model 55cc48dde839918c7077aff528c1434d5ed9a13ba2750321e5f3baeace147b45.

CREATE TABLE "oauth_client" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "client_id" TEXT NOT NULL CHECK (length("client_id") >= 1),
  "kind" TEXT NOT NULL,
  "name" TEXT NOT NULL,
  "uri" TEXT,
  "redirect_uris" TEXT DEFAULT '[]' NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "refreshed_at" TEXT,
  "fresh_until" TEXT,
  "last_used_at" TEXT,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "oauth_client_client_id_key" ON "oauth_client" ("client_id");

CREATE TABLE "oauth_code" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "client_id" TEXT NOT NULL,
  "user_id" TEXT,
  "grant_id" TEXT,
  "redirect_uri" TEXT NOT NULL,
  "code_challenge" TEXT NOT NULL,
  "state" TEXT,
  "scope" TEXT,
  "resource" TEXT NOT NULL,
  "nonce" TEXT NOT NULL,
  "code_hash" TEXT,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "expires_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "used_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("client_id") REFERENCES "oauth_client" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("grant_id") REFERENCES "oauth_grant" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);
CREATE UNIQUE INDEX "oauth_code_code_hash_key" ON "oauth_code" ("code_hash");

CREATE TABLE "oauth_grant" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "client_id" TEXT NOT NULL,
  "permissions" TEXT,
  "resource" TEXT NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "expires_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "last_used_at" TEXT,
  "revoked_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("client_id") REFERENCES "oauth_client" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);

CREATE TABLE "oauth_token" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "grant_id" TEXT NOT NULL,
  "kind" TEXT NOT NULL,
  "token_hash" TEXT NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "expires_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "used_at" TEXT,
  "revoked_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("grant_id") REFERENCES "oauth_grant" ("id") ON DELETE CASCADE
);
CREATE UNIQUE INDEX "oauth_token_token_hash_key" ON "oauth_token" ("token_hash");

CREATE TABLE "role" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "name" TEXT NOT NULL,
  "permissions" TEXT DEFAULT '[]' NOT NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "role_name_key" ON "role" ("name");

CREATE TABLE "session" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "token_hash" TEXT NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "expires_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "last_seen_at" TEXT,
  "revoked_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);
CREATE UNIQUE INDEX "session_token_hash_key" ON "session" ("token_hash");

CREATE TABLE "user" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "email" TEXT COLLATE NOCASE NOT NULL,
  "display_name" TEXT NOT NULL,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "active" INTEGER DEFAULT 0 NOT NULL,
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "user_email_key" ON "user" ("email");

CREATE TABLE "user_api_key" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "name" TEXT NOT NULL CHECK (length("name") >= 1 AND length("name") <= 100),
  "token_hash" TEXT NOT NULL,
  "permissions" TEXT,
  "created_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "expires_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "last_used_at" TEXT,
  "revoked_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);
CREATE UNIQUE INDEX "user_api_key_token_hash_key" ON "user_api_key" ("token_hash");

CREATE TABLE "user_credential" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "password_hash" TEXT NOT NULL,
  "password_changed_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "disabled_at" TEXT,
  "pending_at" TEXT,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);
CREATE UNIQUE INDEX "user_credential_user_id_key" ON "user_credential" ("user_id");

CREATE TABLE "user_role_grant" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "role_id" TEXT NOT NULL,
  "granted_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  PRIMARY KEY ("id"),
  FOREIGN KEY ("role_id") REFERENCES "role" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("user_id") REFERENCES "user" ("id") ON DELETE CASCADE
);

CREATE INDEX "idx_oauth_grant_user" ON "oauth_grant" ("user_id");

CREATE INDEX "idx_oauth_token_grant" ON "oauth_token" ("grant_id");

CREATE INDEX "idx_session_user" ON "session" ("user_id");

CREATE INDEX "idx_user_api_key_user" ON "user_api_key" ("user_id");

CREATE UNIQUE INDEX "uq_user_role_grant_user_role" ON "user_role_grant" ("user_id", "role_id");
