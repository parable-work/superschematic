/*
The engine's own storage, as forward-only migrations under the owner
`engine`. A shipped migration is never edited; a change to these tables
is a new migration at the end of the list.

engine_schemas holds every schema document by namespace, name and
version. Version 0 is the name's draft and 1, 2, 3, ... its published
versions; the live version is the highest. document is the canonical JSON
the loader writes and hash its SHA-256.

engine_instances holds instances by namespace, schema and id, with their
fields as JSON. position orders them by creation and is never reused, so a
list cursor stays valid across deletes. engine_events is the event log;
cursor orders it globally and seq orders one instance's events, and
triggers refuse an update or a delete of an event. engine_events_publish
indexes the publish events alone, which every namespace that looks names
up in a shared namespace reads from it.

engine_behaviors records the key of each behavior whose storage the file
holds: the behavior's columns on engine_instances and its tables are named
bhv_<key>__<name> (behaviors/storage.ts), and its migrations are in the
ledger under its own name.

engine_references holds the references behaviors record from one
instance to another in the same namespace (instances/references.ts): by
target, for the guards and hooks a change of the target runs, and by
source, for a behavior's own list and for dropping them when the source
is deleted.

engine_subscriptions and engine_schedules are the runner's
(runner/runner.ts): one row per behavior's reactions on a schema in a
namespace, with the cursor of the last event they handled and the state
of their retries, and one per behavior's schedule on a schema in a
namespace, with the time of its last run and of its next. The event log
records the cause of an event the runner's work wrote: the behavior, the
event it reacted to or the schedule that ran, and its depth, 0 for a
caller's change.
*/

import type { MigrationSet } from './storage/migrations.js';

export const ENGINE_OWNER = 'engine';

export const engineMigrations: MigrationSet = {
  owner: ENGINE_OWNER,
  migrations: [
    {
      version: 1,
      name: 'schema versions',
      up(storage) {
        storage.exec(`
CREATE TABLE engine_schemas (
  namespace    TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  version      INTEGER NOT NULL CHECK (version >= 0),
  document     TEXT    NOT NULL CHECK (json_valid(document)),
  hash         TEXT    NOT NULL,
  defined_at   INTEGER NOT NULL,
  published_at INTEGER,
  PRIMARY KEY (namespace, name, version),
  CHECK ((version = 0) = (published_at IS NULL))
) STRICT;

CREATE INDEX engine_schemas_name ON engine_schemas (name, namespace);
`);
      },
    },
    {
      version: 2,
      name: 'instances and the event log',
      up(storage) {
        storage.exec(`
ALTER TABLE engine_schemas ADD COLUMN defined_by TEXT;
ALTER TABLE engine_schemas ADD COLUMN published_by TEXT;

CREATE TABLE engine_instances (
  position         INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace        TEXT    NOT NULL,
  schema           TEXT    NOT NULL,
  id               TEXT    NOT NULL,
  schema_namespace TEXT    NOT NULL,
  version          INTEGER NOT NULL CHECK (version >= 1),
  seq              INTEGER NOT NULL CHECK (seq >= 1),
  data             TEXT    NOT NULL CHECK (json_valid(data) AND json_type(data) = 'object'),
  created_at       INTEGER NOT NULL,
  created_by       TEXT    NOT NULL,
  updated_at       INTEGER NOT NULL,
  updated_by       TEXT    NOT NULL,
  UNIQUE (namespace, schema, id)
) STRICT;

CREATE INDEX engine_instances_list ON engine_instances (namespace, schema, position);

CREATE TABLE engine_events (
  cursor      INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'publish')),
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT,
  seq         INTEGER CHECK (seq >= 1),
  version     INTEGER NOT NULL CHECK (version >= 1),
  actor       TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  change      TEXT    CHECK (change IS NULL OR json_valid(change)),
  CHECK ((kind = 'publish') = (instance_id IS NULL)),
  CHECK ((instance_id IS NULL) = (seq IS NULL))
) STRICT;

CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
CREATE INDEX engine_events_namespace ON engine_events (namespace, cursor);
CREATE INDEX engine_events_schema ON engine_events (namespace, schema, cursor);

CREATE TRIGGER engine_events_no_update BEFORE UPDATE ON engine_events
BEGIN SELECT RAISE(ABORT, 'engine_events is append-only'); END;
CREATE TRIGGER engine_events_no_delete BEFORE DELETE ON engine_events
BEGIN SELECT RAISE(ABORT, 'engine_events is append-only'); END;
`);
      },
    },
    {
      version: 3,
      name: 'publish events by namespace',
      up(storage) {
        storage.exec(`
CREATE INDEX engine_events_publish ON engine_events (namespace, cursor) WHERE kind = 'publish';
`);
      },
    },
    {
      version: 4,
      name: 'operation events and behavior storage',
      // SQLite cannot change a CHECK constraint, so the event log is copied
      // into a table whose kind CHECK admits operation. The copy keeps every
      // cursor, so the autoincrement sequence continues from the last one;
      // dropping the old table fires none of its triggers and drops its
      // indexes, so every index and trigger of migrations 2 and 3 is made
      // again on the copy.
      up(storage) {
        storage.exec(`
CREATE TABLE engine_events_next (
  cursor      INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish')),
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT,
  seq         INTEGER CHECK (seq >= 1),
  version     INTEGER NOT NULL CHECK (version >= 1),
  actor       TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  change      TEXT    CHECK (change IS NULL OR json_valid(change)),
  CHECK ((kind = 'publish') = (instance_id IS NULL)),
  CHECK ((instance_id IS NULL) = (seq IS NULL))
) STRICT;

INSERT INTO engine_events_next (cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change)
SELECT cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change FROM engine_events ORDER BY cursor;

DROP TABLE engine_events;
ALTER TABLE engine_events_next RENAME TO engine_events;

CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
CREATE INDEX engine_events_namespace ON engine_events (namespace, cursor);
CREATE INDEX engine_events_schema ON engine_events (namespace, schema, cursor);
CREATE INDEX engine_events_publish ON engine_events (namespace, cursor) WHERE kind = 'publish';

CREATE TRIGGER engine_events_no_update BEFORE UPDATE ON engine_events
BEGIN SELECT RAISE(ABORT, 'engine_events is append-only'); END;
CREATE TRIGGER engine_events_no_delete BEFORE DELETE ON engine_events
BEGIN SELECT RAISE(ABORT, 'engine_events is append-only'); END;

CREATE TABLE engine_behaviors (
  name       TEXT    PRIMARY KEY,
  key        TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
) STRICT;
`);
      },
    },
    {
      version: 5,
      name: 'references between instances',
      up(storage) {
        storage.exec(`
CREATE TABLE engine_references (
  namespace     TEXT NOT NULL,
  target_schema TEXT NOT NULL,
  target_id     TEXT NOT NULL,
  source_schema TEXT NOT NULL,
  source_id     TEXT NOT NULL,
  behavior      TEXT NOT NULL,
  key           TEXT NOT NULL,
  PRIMARY KEY (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
) STRICT;

CREATE INDEX engine_references_source ON engine_references (namespace, source_schema, source_id, behavior);
`);
      },
    },
    {
      version: 6,
      name: 'reactions, schedules and causes',
      up(storage) {
        storage.exec(`
CREATE TABLE engine_subscriptions (
  behavior       TEXT    NOT NULL,
  namespace      TEXT    NOT NULL,
  schema         TEXT    NOT NULL,
  cursor         INTEGER NOT NULL CHECK (cursor >= 0),
  halted         INTEGER NOT NULL DEFAULT 0 CHECK (halted IN (0, 1)),
  attempts       INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  retry_at       INTEGER,
  failed_cursor  INTEGER,
  failed_at      INTEGER,
  error          TEXT,
  skipped        INTEGER NOT NULL DEFAULT 0 CHECK (skipped >= 0),
  skipped_cursor INTEGER,
  skipped_reason TEXT CHECK (skipped_reason IS NULL OR skipped_reason IN ('depth', 'resume')),
  PRIMARY KEY (behavior, namespace, schema)
) STRICT;

CREATE TABLE engine_schedules (
  behavior    TEXT    NOT NULL,
  schedule    TEXT    NOT NULL,
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  last_run_at INTEGER,
  next_run_at INTEGER NOT NULL,
  failures    INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
  error       TEXT,
  PRIMARY KEY (behavior, schedule, namespace, schema)
) STRICT;

ALTER TABLE engine_events ADD COLUMN cause_behavior TEXT;
ALTER TABLE engine_events ADD COLUMN cause_event INTEGER;
ALTER TABLE engine_events ADD COLUMN cause_schedule TEXT;
ALTER TABLE engine_events ADD COLUMN depth INTEGER NOT NULL DEFAULT 0 CHECK (depth >= 0);
`);
      },
    },
  ],
};
