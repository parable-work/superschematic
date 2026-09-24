-- Generated PostgreSQL DDL for schema: fixture-projection
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

CREATE TABLE channel (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  account UUID NOT NULL,
  "name" VARCHAR(80) NOT NULL,
  handle CITEXT NOT NULL
);

COMMENT ON TABLE channel IS 'A release channel of an app: the line a preference row resolves against.';

CREATE TABLE preference (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ,
  account UUID NOT NULL,
  channel_id UUID NOT NULL,
  branch_id UUID,
  "commit" UUID,
  scope TEXT NOT NULL,
  user_id UUID,
  slot_key UUID NOT NULL,
  handle CITEXT NOT NULL,
  value JSONB NOT NULL,
  revision BIGINT NOT NULL,
  tags CITEXT[] DEFAULT '{}' NOT NULL,
  archived_at TIMESTAMPTZ,
  hidden BOOLEAN NOT NULL
);

COMMENT ON TABLE preference IS 'One stored preference value for a slot, scoped to an app, a team, or a
user.';

-- Add foreign key constraints

ALTER TABLE preference
  ADD CONSTRAINT fk_preference_channel_id
  FOREIGN KEY (channel_id)
  REFERENCES channel(id)
  ON DELETE CASCADE;

ALTER TABLE preference
  ADD CONSTRAINT fk_preference_branch_id
  FOREIGN KEY (branch_id)
  REFERENCES channel(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_preference_account_slot_key ON preference USING BTREE (account, slot_key);

-- Create projection views: read-only relations over the tables above. A
-- setting a view binds without optional makes current_setting raise while
-- it is unset, so the view fails closed.

CREATE SCHEMA IF NOT EXISTS app;

CREATE VIEW app.preferences WITH (security_barrier = true) AS
-- Projection column order is the published Arrow contract.
SELECT DISTINCT ON (base.slot_key) -- noqa: ST06
  base.slot_key,
  base.handle,
  base.scope,
  base.user_id,
  base.channel_id,
  channel.handle AS channel_handle,
  branch."name" AS branch_name,
  base.value,
  base.revision,
  base.tags,
  base.updated_at
FROM preference AS base
  INNER JOIN channel AS channel ON base.channel_id = channel.id
  LEFT JOIN channel AS branch ON base.branch_id = branch.id
WHERE base.account = current_setting('app.account_id')::uuid
  AND base.channel_id = current_setting('app.channel_id')::uuid
  AND (base.scope IS DISTINCT FROM 'user' OR base.user_id = current_setting('app.user_id')::uuid)
  AND (base.scope IS DISTINCT FROM 'app' OR (base.branch_id = NULLIF(current_setting('app.branch_id', true), '')::uuid OR base."commit" = NULLIF(current_setting('app.commit_id', true), '')::uuid))
  AND base.archived_at IS null
  AND base.hidden = false
  AND (base.scope IS DISTINCT FROM 'user' OR base.user_id IS NOT null)
ORDER BY base.slot_key ASC, CASE base.scope WHEN 'user' THEN 0 WHEN 'team' THEN 1 WHEN 'app' THEN 2 ELSE 3 END ASC, base.branch_id ASC NULLS LAST;

COMMENT ON VIEW app.preferences IS 'Preference values visible to the current account, channel and user: app and team rows for the selected channel, and user rows only for the user whose session runs the query. App rows resolve by the branch or the released commit the caller names (either setting may be left unset), and one row per slot survives: user over team over app, and a branch row before the released commit''s.';
COMMENT ON COLUMN app.preferences.slot_key IS 'Stable identity of the slot the value belongs to.';
