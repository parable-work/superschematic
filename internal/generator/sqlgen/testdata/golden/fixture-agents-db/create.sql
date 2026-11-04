-- Generated PostgreSQL DDL for schema: fixture-agents-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE account (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  workspace_id UUID NOT NULL,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE account IS 'An account an agent runs on (D104): forty questions an hour and a million
tokens over any day, each charged to its workspace too. The provider''s own
count of its tokens, read every few minutes, replaces what the account
counted until the provider''s period rolls over (observe, for callers who
hold accounts.observe). Usage that takes it past a cap moves it out, until
a person moves it back.';

CREATE TABLE account_budget_buckets (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  account_id UUID NOT NULL,
  bucket TEXT NOT NULL,
  meter TEXT,
  starts_at TIMESTAMPTZ,
  used BIGINT
);

COMMENT ON TABLE account_budget_buckets IS 'A rolling meter''s usage, a sixtieth of its period each, keyed by the meter and the bucket''s start; a write deletes the ones that left the window.';

CREATE TABLE account_budget_holds (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  account_id UUID NOT NULL,
  hold TEXT NOT NULL,
  meter TEXT,
  inner_type TEXT,
  inner_key TEXT,
  amount BIGINT
);

COMMENT ON TABLE account_budget_holds IS 'What the row, a scope, holds of a meter for each row inside it, keyed by the meter, that row''s type and its key.';

CREATE TABLE account_budget_meters (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  account_id UUID NOT NULL,
  meter TEXT NOT NULL,
  used BIGINT,
  reserved BIGINT,
  limit_value BIGINT,
  period_start TIMESTAMPTZ,
  reservation BIGINT,
  token BIGINT,
  directed BIGINT,
  scope_type TEXT,
  scope_key TEXT,
  observed BIGINT,
  observed_limit BIGINT,
  observed_at TIMESTAMPTZ,
  observed_until TIMESTAMPTZ
);

COMMENT ON TABLE account_budget_meters IS 'One row per meter the row has written, keyed by the meter''s name: what it has used in its current period, what it has reserved (its own reservation and what it holds for the rows inside it), the limit setLimit gave it, the start of the period its used counts from, its own reservation with the lease token it was made under, the token onExceeded last directed under, the scope that holds its reservation, and the usage a provider last reported, with its limit, when, and until when it holds.';

CREATE TABLE job (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  revision BIGINT NOT NULL,
  lease_holder TEXT,
  lease_token BIGINT,
  lease_acquired_at TIMESTAMPTZ,
  lease_renewed_at TIMESTAMPTZ,
  lease_expires_at TIMESTAMPTZ,
  lease_expiries BIGINT,
  lease_ended JSONB,
  retries_total BIGINT,
  retries_class_attempts JSONB,
  retries_best_score DOUBLE PRECISION,
  retries_exhausted BOOLEAN,
  retries_stuck BOOLEAN,
  retries_retry_at TIMESTAMPTZ,
  retries_failing_since TIMESTAMPTZ,
  retries_keyed_by TEXT,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE job IS 'A job a worker takes (D104). The worker leases it with a fencing token and
presents the token with every write while it holds it, so a process whose
lease lapsed cannot write after another took the job. A lease the worker
stops renewing expires when its deadline falls due and moves the job back
to queued; the third expiry fails it. A timeout waits a minute before the
job runs again, twice as long after each next one up to ten minutes, and
then moves it from waiting back to queued; a build failure is retried once
per revision; a job that has failed for half an hour gives up; and at most
four attempts start in an hour. Its revision is not named version, which a
table that composes behaviors keeps its own version in (_version).';

CREATE TABLE job_lease_directives (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  job_id UUID NOT NULL,
  number TEXT NOT NULL,
  token BIGINT,
  "name" TEXT,
  "data" JSONB,
  dedupe_key TEXT,
  sent_by TEXT,
  sent_at TIMESTAMPTZ,
  acknowledged_at TIMESTAMPTZ
);

COMMENT ON TABLE job_lease_directives IS 'One row per directive sent to the holder of the row''s lease, keyed by its number in the lease: the token it was sent under, what it asks, its data, the dedupeKey it was sent with, who sent it and when, and when the holder acknowledged it. Every end of a lease deletes its directives, so none reaches the next holder.';

CREATE TABLE job_retries_attempts (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  job_id UUID NOT NULL,
  number TEXT NOT NULL,
  at TIMESTAMPTZ,
  failure TEXT,
  score DOUBLE PRECISION,
  kept BOOLEAN,
  signature TEXT,
  predicates JSONB,
  detail JSONB,
  subject TEXT,
  keyed_by TEXT
);

COMMENT ON TABLE job_retries_attempts IS 'One row per attempt the row recorded, keyed by its number, the order they were recorded in: when, its failure class (absent for a success), its score, whether its result was kept, its signature, its predicates and its detail, who recorded it, and the keyField value it counted under. The window, stuckAfter''s streak, keepBest''s neverRegress and a backoff''s wait read it.';

CREATE TABLE "session" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  device UUID NOT NULL,
  agent TEXT NOT NULL,
  presence_deadline TIMESTAMPTZ,
  presence_last_beat_at TIMESTAMPTZ,
  presence_missed BOOLEAN,
  presence_released JSONB,
  status TEXT NOT NULL,
  status_entered_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE "session" IS 'A session a device opens for an agent (D104). Only the device''s principal
beats it, and it stays live three minutes after its last beat, and fifteen
while its agent is in a turn, counted from the later of the beat and the
turn''s start. A session not beaten in time is missed: it moves to missing,
and the leases its device holds on jobs are expired, but for one the device
renewed since its last beat. Its next beat brings it back to idle.';

CREATE TABLE workspace (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  "name" TEXT NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE workspace IS 'A workspace whose accounts'' usage rolls up into its own caps (D104): sixty
questions an hour, and two million tokens over any day.';

CREATE TABLE workspace_budget_buckets (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  workspace_id UUID NOT NULL,
  bucket TEXT NOT NULL,
  meter TEXT,
  starts_at TIMESTAMPTZ,
  used BIGINT
);

COMMENT ON TABLE workspace_budget_buckets IS 'A rolling meter''s usage, a sixtieth of its period each, keyed by the meter and the bucket''s start; a write deletes the ones that left the window.';

CREATE TABLE workspace_budget_holds (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  workspace_id UUID NOT NULL,
  hold TEXT NOT NULL,
  meter TEXT,
  inner_type TEXT,
  inner_key TEXT,
  amount BIGINT
);

COMMENT ON TABLE workspace_budget_holds IS 'What the row, a scope, holds of a meter for each row inside it, keyed by the meter, that row''s type and its key.';

CREATE TABLE workspace_budget_meters (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  workspace_id UUID NOT NULL,
  meter TEXT NOT NULL,
  used BIGINT,
  reserved BIGINT,
  limit_value BIGINT,
  period_start TIMESTAMPTZ,
  reservation BIGINT,
  token BIGINT,
  directed BIGINT,
  scope_type TEXT,
  scope_key TEXT,
  observed BIGINT,
  observed_limit BIGINT,
  observed_at TIMESTAMPTZ,
  observed_until TIMESTAMPTZ
);

COMMENT ON TABLE workspace_budget_meters IS 'One row per meter the row has written, keyed by the meter''s name: what it has used in its current period, what it has reserved (its own reservation and what it holds for the rows inside it), the limit setLimit gave it, the start of the period its used counts from, its own reservation with the lease token it was made under, the token onExceeded last directed under, the scope that holds its reservation, and the usage a provider last reported, with its limit, when, and until when it holds.';

CREATE TABLE job_deadline_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  "name" TEXT NOT NULL,
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

COMMENT ON TABLE job_deadline_queue IS 'The deadlines Job''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

CREATE TABLE session_deadline_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  row_key UUID NOT NULL,
  use_id TEXT NOT NULL,
  "name" TEXT NOT NULL,
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

COMMENT ON TABLE session_deadline_queue IS 'The deadlines Session''s behaviors arm, one message per row, use and deadline, due when it falls due (D103).';

-- Add foreign key constraints

ALTER TABLE account
  ADD CONSTRAINT fk_account_workspace_id
  FOREIGN KEY (workspace_id)
  REFERENCES workspace(id)
  ON DELETE CASCADE;

ALTER TABLE account_budget_buckets
  ADD CONSTRAINT fk_account_budget_buckets_account_id
  FOREIGN KEY (account_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE account_budget_holds
  ADD CONSTRAINT fk_account_budget_holds_account_id
  FOREIGN KEY (account_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE account_budget_meters
  ADD CONSTRAINT fk_account_budget_meters_account_id
  FOREIGN KEY (account_id)
  REFERENCES account(id)
  ON DELETE CASCADE;

ALTER TABLE job_lease_directives
  ADD CONSTRAINT fk_job_lease_directives_job_id
  FOREIGN KEY (job_id)
  REFERENCES job(id)
  ON DELETE CASCADE;

ALTER TABLE job_retries_attempts
  ADD CONSTRAINT fk_job_retries_attempts_job_id
  FOREIGN KEY (job_id)
  REFERENCES job(id)
  ON DELETE CASCADE;

ALTER TABLE workspace_budget_buckets
  ADD CONSTRAINT fk_workspace_budget_buckets_workspace_id
  FOREIGN KEY (workspace_id)
  REFERENCES workspace(id)
  ON DELETE CASCADE;

ALTER TABLE workspace_budget_holds
  ADD CONSTRAINT fk_workspace_budget_holds_workspace_id
  FOREIGN KEY (workspace_id)
  REFERENCES workspace(id)
  ON DELETE CASCADE;

ALTER TABLE workspace_budget_meters
  ADD CONSTRAINT fk_workspace_budget_meters_workspace_id
  FOREIGN KEY (workspace_id)
  REFERENCES workspace(id)
  ON DELETE CASCADE;

-- Create indexes

CREATE UNIQUE INDEX idx_account_budget_buckets_account_bucket ON account_budget_buckets USING BTREE (account_id, bucket);

CREATE UNIQUE INDEX idx_account_budget_holds_account_hold ON account_budget_holds USING BTREE (account_id, hold);

CREATE UNIQUE INDEX idx_account_budget_meters_account_meter ON account_budget_meters USING BTREE (account_id, meter);

CREATE UNIQUE INDEX idx_job_lease_directives_job_number ON job_lease_directives USING BTREE (job_id, number);

CREATE UNIQUE INDEX idx_job_retries_attempts_job_number ON job_retries_attempts USING BTREE (job_id, number);

CREATE UNIQUE INDEX idx_workspace_budget_buckets_workspace_bucket ON workspace_budget_buckets USING BTREE (workspace_id, bucket);

CREATE UNIQUE INDEX idx_workspace_budget_holds_workspace_hold ON workspace_budget_holds USING BTREE (workspace_id, hold);

CREATE UNIQUE INDEX idx_workspace_budget_meters_workspace_meter ON workspace_budget_meters USING BTREE (workspace_id, meter);

CREATE INDEX idx_job_deadline_queue_due_at ON job_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_job_deadline_queue_claim_expires_at ON job_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_job_deadline_queue_updated_at ON job_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_job_deadline_queue_row_key_use_id_name ON job_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_job_deadline_queue_row_key ON job_deadline_queue USING BTREE (row_key);

CREATE INDEX idx_session_deadline_queue_due_at ON session_deadline_queue USING BTREE (due_at) WHERE state = 'ready';

CREATE INDEX idx_session_deadline_queue_claim_expires_at ON session_deadline_queue USING BTREE (claim_expires_at) WHERE state = 'claimed';

CREATE INDEX idx_session_deadline_queue_updated_at ON session_deadline_queue USING BTREE (updated_at) WHERE state = 'done';

CREATE UNIQUE INDEX idx_session_deadline_queue_row_key_use_id_name ON session_deadline_queue USING BTREE (row_key, use_id, "name") WHERE state IN ('ready', 'claimed');

CREATE INDEX idx_session_deadline_queue_row_key ON session_deadline_queue USING BTREE (row_key);

-- Create version bump functions and triggers for optimistic entities

CREATE OR REPLACE FUNCTION account_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_account_bump_version
  BEFORE UPDATE ON account
  FOR EACH ROW EXECUTE FUNCTION account_bump_version();

CREATE OR REPLACE FUNCTION job_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_job_bump_version
  BEFORE UPDATE ON job
  FOR EACH ROW EXECUTE FUNCTION job_bump_version();

CREATE OR REPLACE FUNCTION session_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_session_bump_version
  BEFORE UPDATE ON "session"
  FOR EACH ROW EXECUTE FUNCTION session_bump_version();

CREATE OR REPLACE FUNCTION workspace_bump_version() RETURNS trigger AS $$
BEGIN
  -- BEFORE UPDATE: the stored row takes the next version.
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_workspace_bump_version
  BEFORE UPDATE ON workspace
  FOR EACH ROW EXECUTE FUNCTION workspace_bump_version();
