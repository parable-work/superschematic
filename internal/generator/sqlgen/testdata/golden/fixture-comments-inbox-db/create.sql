-- Generated PostgreSQL DDL for schema: fixture-comments-inbox-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;


-- Create tables (without foreign key constraints)

CREATE TABLE account (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  email CITEXT NOT NULL,
  UNIQUE (email)
);

COMMENT ON TABLE account IS 'A person who signs in.';

CREATE TABLE delivery (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  notice UUID,
  purpose TEXT DEFAULT 'notice' NOT NULL,
  recipient UUID,
  channel_kind TEXT NOT NULL,
  slot TEXT,
  address TEXT,
  attempt BIGINT DEFAULT 1 NOT NULL,
  state TEXT DEFAULT 'queued' NOT NULL,
  provider_message_id TEXT,
  detail TEXT,
  queued_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  due_at TIMESTAMPTZ,
  sent_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  read_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  acknowledged_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ
);

COMMENT ON TABLE delivery IS 'Generated from schema type: Delivery';

CREATE TABLE notice (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipient UUID NOT NULL,
  recipient_kind TEXT DEFAULT 'user' NOT NULL,
  reference BIGINT NOT NULL,
  event_id UUID NOT NULL,
  event_kind TEXT NOT NULL,
  urgency TEXT NOT NULL,
  subject_table TEXT,
  subject_key TEXT,
  title TEXT NOT NULL,
  body TEXT,
  payload JSONB,
  route UUID,
  state TEXT DEFAULT 'unread' NOT NULL,
  escalation BIGINT DEFAULT 0 NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  read_at TIMESTAMPTZ,
  acknowledged_at TIMESTAMPTZ,
  acknowledged_via TEXT,
  digested_at TIMESTAMPTZ
);

COMMENT ON TABLE notice IS 'The notifier''s tables, which the build fills (D153).';

CREATE TABLE notice_route (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  event_kind TEXT NOT NULL,
  subject_table TEXT,
  filter JSONB,
  recipients JSONB NOT NULL,
  urgency TEXT,
  escalation JSONB,
  enabled BOOLEAN DEFAULT true NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID
);

COMMENT ON TABLE notice_route IS 'Generated from schema type: NoticeRoute';

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
  "name" CITEXT NOT NULL,
  permissions TEXT[] DEFAULT '{}' NOT NULL,
  UNIQUE ("name")
);

COMMENT ON TABLE role IS 'A named set of permissions.';

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

CREATE TABLE ticket (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  "owner" UUID NOT NULL,
  audience TEXT DEFAULT 'everyone' NOT NULL,
  audience_named UUID[],
  agent_access TEXT DEFAULT 'followRole' NOT NULL,
  effective_audience UUID[],
  effective_agents TEXT DEFAULT 'followRole' NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE ticket IS 'A ticket, which its owner may keep to themselves (D156). Its
conversation emits its mentions and its replies (#713), which the
notifier raises into its inbox.';

CREATE TABLE ticket_comments_conversations (
  conversation TEXT NOT NULL PRIMARY KEY,
  last_message BIGINT,
  last_reaction BIGINT,
  last_stand BIGINT,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE ticket_comments_conversations IS 'On a table, one row per row that has had a message, kept for the table as a whole: the greatest message id, reaction number and stand the row''s conversation has given. Every write of a conversation moves its row''s version, which holds two writes of one conversation apart; a delete of the row leaves it, so a row re-created with the key numbers on from it and never reuses an id.';

CREATE TABLE ticket_comments_deliveries (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  delivery TEXT NOT NULL,
  message BIGINT,
  subject TEXT,
  mention BOOLEAN,
  reply BOOLEAN,
  state TEXT,
  due_at TIMESTAMPTZ,
  token TEXT,
  claimed_by TEXT,
  attempts BIGINT,
  error TEXT,
  created_at TIMESTAMPTZ,
  settled_at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_deliveries IS 'On a table, one row per message and subject that should hear of it, keyed by the message and the subject: each subject a message that comes to stand mentions and each follower of its thread but its author, and each subject an edit adds a mention of. A notifier or an agent dispatcher outside the core claims and settles them with claimDeliveries and settleDeliveries; the core sends nothing. The inbox reads its mentions from them. A delete or a purge of the message deletes them but the claimed ones, and so does a delete of the row.';

CREATE TABLE ticket_comments_drafts (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  chunk TEXT NOT NULL,
  message BIGINT,
  chunk_offset BIGINT,
  text TEXT
);

COMMENT ON TABLE ticket_comments_drafts IS 'On a table, one row per chunk a non-empty appendDraft added to a composing message''s draft, keyed by the message and the chunk''s offset: the draft is its chunks in offset order. A finish, an interrupt, a delete or a purge of the message deletes them, and so does a delete of the row.';

CREATE TABLE ticket_comments_edits (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  edit TEXT NOT NULL,
  message BIGINT,
  number BIGINT,
  body TEXT,
  blocks JSONB,
  mentions JSONB,
  by TEXT,
  for_subject TEXT,
  at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_edits IS 'On a table, one row per edit of a message, keyed by the message and the edit''s number: what the message held before the edit, who made it and when. A delete or a purge of the message deletes them, and so does a delete of the row.';

CREATE TABLE ticket_comments_follows (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  follow TEXT NOT NULL,
  thread BIGINT,
  subject TEXT,
  followed BOOLEAN,
  read_up_to BIGINT,
  updated_at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_follows IS 'On a table, one row per subject and thread it has state on, keyed by the thread and the subject: whether it follows the thread, and its read marker on the thread''s replies. A message that stands makes its author follow its thread, and its mentions too, but not a role:<name>; follow sets it, and markRead with a thread moves its marker. A purge of the thread, and a delete of the row, deletes them.';

CREATE TABLE ticket_comments_messages (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  message TEXT NOT NULL,
  thread BIGINT,
  quotes BIGINT,
  author TEXT,
  for_subject TEXT,
  via TEXT,
  state TEXT,
  body TEXT,
  blocks JSONB,
  mentions JSONB,
  created_at TIMESTAMPTZ,
  posted_at TIMESTAMPTZ,
  stand BIGINT,
  edited_at TIMESTAMPTZ,
  edits BIGINT,
  pinned BOOLEAN,
  client_id TEXT,
  resolved_by TEXT,
  resolved_for TEXT,
  resolved_at TIMESTAMPTZ,
  draft_length BIGINT,
  last_append_at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_messages IS 'On a table, one row per message of the row''s conversation, keyed by its id: its thread and the message it quotes, its author, its state, its body, blocks and mentions, when it was posted and last edited, how many edits it has had, whether it is pinned, the clientId it was posted with, and for a top-level message who resolved its thread and when. A deleted message keeps its row; a purged one loses it. A delete of the row deletes them.';

CREATE TABLE ticket_comments_reactions (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  reaction TEXT NOT NULL,
  message BIGINT,
  emoji TEXT,
  subject TEXT,
  for_subject TEXT,
  at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_reactions IS 'On a table, one row per reaction a subject holds on a message, keyed by its number on the row, which orders reactions: the message, the emoji, the subject and when. unreact deletes the caller''s; a delete or a purge of the message deletes them, and so does a delete of the row.';

CREATE TABLE ticket_comments_reads (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  ticket_id UUID NOT NULL,
  subject TEXT NOT NULL,
  up_to BIGINT,
  read_at TIMESTAMPTZ
);

COMMENT ON TABLE ticket_comments_reads IS 'On a table, one row per subject that has a read marker on the row''s conversation, keyed by the subject: the greatest stand of the top-level messages it has read, and when it last moved. markRead and a subject''s own top-level message move it, never back. A delete of the row deletes them.';

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

CREATE TABLE notice_work_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  step TEXT NOT NULL,
  event JSONB,
  notice UUID,
  delivery UUID,
  recipient UUID,
  subscription UUID,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE notice_work_queue IS 'Messages of the queue NoticeWork, one row each.';

CREATE TABLE ticket_event_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  at BIGINT NOT NULL,
  payload JSONB,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

COMMENT ON TABLE ticket_event_queue IS 'The events Ticket''s behaviors emit about its rows, one message per event, written in the command''s transaction, which a behavior worker hands to the project''s handler (D104).';

-- Add foreign key constraints

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_client_id
  FOREIGN KEY (client_id)
  REFERENCES oauth_client(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_user_id
  FOREIGN KEY (user_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_code
  ADD CONSTRAINT fk_oauth_code_grant_id
  FOREIGN KEY (grant_id)
  REFERENCES oauth_grant(id)
  ON DELETE CASCADE;

ALTER TABLE oauth_grant
  ADD CONSTRAINT fk_oauth_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES account(id)
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
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_deliveries
  ADD CONSTRAINT fk_ticket_comments_deliveries_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_drafts
  ADD CONSTRAINT fk_ticket_comments_drafts_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_edits
  ADD CONSTRAINT fk_ticket_comments_edits_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_follows
  ADD CONSTRAINT fk_ticket_comments_follows_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_messages
  ADD CONSTRAINT fk_ticket_comments_messages_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_reactions
  ADD CONSTRAINT fk_ticket_comments_reactions_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE ticket_comments_reads
  ADD CONSTRAINT fk_ticket_comments_reads_ticket_id
  FOREIGN KEY (ticket_id)
  REFERENCES ticket(id)
  ON DELETE CASCADE;

ALTER TABLE user_api_key
  ADD CONSTRAINT fk_user_api_key_user_id
  FOREIGN KEY (user_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE user_credential
  ADD CONSTRAINT fk_user_credential_user_id
  FOREIGN KEY (user_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_user_id
  FOREIGN KEY (user_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE user_role_grant
  ADD CONSTRAINT fk_user_role_grant_role_id
  FOREIGN KEY (role_id)
  REFERENCES role(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE INDEX idx_delivery_notice ON delivery USING BTREE (notice);

CREATE INDEX idx_delivery_provider_message ON delivery USING BTREE (provider_message_id);

CREATE INDEX idx_delivery_ended ON delivery USING BTREE (ended_at);

CREATE UNIQUE INDEX uq_notice_reference ON notice USING BTREE (recipient, reference);

CREATE UNIQUE INDEX uq_notice_event ON notice USING BTREE (event_id, recipient);

CREATE INDEX idx_notice_waiting ON notice USING BTREE (recipient, state);

CREATE INDEX idx_notice_route_event_kind ON notice_route USING BTREE (event_kind);

CREATE INDEX idx_oauth_grant_user ON oauth_grant USING BTREE (user_id);

CREATE INDEX idx_oauth_token_grant ON oauth_token USING BTREE (grant_id);

CREATE INDEX idx_session_user ON "session" USING BTREE (user_id);

CREATE INDEX idx_ticket_effective_audience ON ticket USING GIN (effective_audience);

CREATE UNIQUE INDEX idx_ticket_comments_deliveries_ticket_delivery ON ticket_comments_deliveries USING BTREE (ticket_id, delivery);

CREATE UNIQUE INDEX idx_ticket_comments_drafts_ticket_chunk ON ticket_comments_drafts USING BTREE (ticket_id, chunk);

CREATE UNIQUE INDEX idx_ticket_comments_edits_ticket_edit ON ticket_comments_edits USING BTREE (ticket_id, edit);

CREATE UNIQUE INDEX idx_ticket_comments_follows_ticket_follow ON ticket_comments_follows USING BTREE (ticket_id, follow);

CREATE UNIQUE INDEX idx_ticket_comments_messages_ticket_message ON ticket_comments_messages USING BTREE (ticket_id, message);

CREATE INDEX idx_ticket_comments_messages_blocks ON ticket_comments_messages USING GIN (blocks jsonb_path_ops);

CREATE UNIQUE INDEX idx_ticket_comments_reactions_ticket_reaction ON ticket_comments_reactions USING BTREE (ticket_id, reaction);

CREATE UNIQUE INDEX idx_ticket_comments_reads_ticket_subject ON ticket_comments_reads USING BTREE (ticket_id, subject);

CREATE INDEX idx_user_api_key_user ON user_api_key USING BTREE (user_id);

CREATE UNIQUE INDEX uq_user_role_grant_user_role ON user_role_grant USING BTREE (user_id, role_id);

CREATE INDEX idx_notice_work_queue_due_at ON notice_work_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_notice_work_queue_claim_expires_at ON notice_work_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_notice_work_queue_updated_at ON notice_work_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_ticket_event_queue_due_at ON ticket_event_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_ticket_event_queue_claim_expires_at ON ticket_event_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_ticket_event_queue_updated_at ON ticket_event_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE INDEX idx_ticket_event_queue_ready_key ON ticket_event_queue USING BTREE (row_key) WHERE state = 'ready';

CREATE UNIQUE INDEX uq_ticket_event_queue_claimed_key ON ticket_event_queue USING BTREE (row_key) WHERE state = 'claimed';

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION ticket_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_bump_version
  BEFORE UPDATE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_bump_version();

CREATE OR REPLACE FUNCTION ticket_comments_conversations_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_comments_conversations_bump_version
  BEFORE UPDATE ON ticket_comments_conversations
  FOR EACH ROW EXECUTE FUNCTION ticket_comments_conversations_bump_version();

-- Create the functions and triggers that keep an @audience table's
-- effective audience: the row's audience resolved, through its parent for
-- inherit, whatever writes the row, and again for each child that inherits
-- when a parent's changes

CREATE OR REPLACE FUNCTION ticket_audience() RETURNS trigger AS $$
DECLARE
  parent_audience UUID[];
  parent_agents TEXT;
  has_parent BOOLEAN := FALSE;
  own UUID[];
BEGIN
  -- The @audience of Ticket (D156): the row's audience resolved into
  -- effective_audience and effective_agents, whatever writes the row.
  IF NEW.audience IS NULL OR NEW.audience NOT IN ('everyone', 'private', 'named') THEN
    RAISE EXCEPTION 'ticket.audience is none of everyone, private, named: %', NEW.audience
      USING ERRCODE = 'check_violation', CONSTRAINT = 'ticket_audience';
  END IF;
  IF NEW.agent_access IS NULL OR NEW.agent_access NOT IN ('followRole', 'readOnly', 'none') THEN
    RAISE EXCEPTION 'ticket.agent_access is none of followRole, readOnly, none: %', NEW.agent_access
      USING ERRCODE = 'check_violation', CONSTRAINT = 'ticket_audience';
  END IF;
  own := array_remove(ARRAY[NEW."owner"]::UUID[], NULL);
  NEW.effective_audience := CASE NEW.audience
    WHEN 'everyone' THEN NULL
    WHEN 'private' THEN own
    WHEN 'named' THEN own || array_remove(COALESCE(NEW.audience_named, '{}'::UUID[]), NULL)
    -- inherit: the parent's, or the owner's alone with no parent.
    ELSE CASE WHEN has_parent THEN parent_audience ELSE own END
  END;
  NEW.effective_agents := NEW.agent_access;
  -- An inherited agent access is the least open of the two.
  IF has_parent AND array_position(ARRAY['followRole', 'readOnly', 'none'], parent_agents) > array_position(ARRAY['followRole', 'readOnly', 'none'], NEW.agent_access) THEN
    NEW.effective_agents := parent_agents;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ticket_audience
  BEFORE INSERT OR UPDATE ON ticket
  FOR EACH ROW EXECUTE FUNCTION ticket_audience();
