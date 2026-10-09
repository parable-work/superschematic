-- Generated SQLite DDL for schema: fixture-user-model-db
-- This file is auto-generated. Do not edit manually.
-- It is the migration plan from an empty database to model 26e7befbfde24d10c968ea59d22e632658d21fc394ced423809794992132aeb6.

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
  PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "user_email_key" ON "user" ("email");

CREATE TABLE "user_credential" (
  "id" TEXT DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))) NOT NULL,
  "user_id" TEXT NOT NULL,
  "password_hash" TEXT NOT NULL,
  "password_changed_at" TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
  "disabled_at" TEXT,
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

CREATE INDEX "idx_session_user" ON "session" ("user_id");

CREATE UNIQUE INDEX "uq_user_role_grant_user_role" ON "user_role_grant" ("user_id", "role_id");
