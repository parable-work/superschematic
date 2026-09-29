/*
The engine's own storage, as forward-only migrations under the owner
`engine`. A shipped migration is never edited; a change to these tables
is a new migration at the end of the list.

engine_schemas holds every schema document by namespace, name and
version. Version 0 is the name's draft and 1, 2, 3, ... its published
versions; the live version is the highest. document is the canonical JSON
the loader writes and hash its SHA-256.
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
  ],
};
