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
triggers refuse an update or a delete of an event.
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
  ],
};
