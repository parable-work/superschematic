//! The engine's own storage, as forward-only migrations under the owner
//! `engine` (a port of `runtime/engine/typescript/src/migrations.ts`, whose
//! comment says what each table holds). A shipped migration is never
//! edited; a change to these tables is a new migration at the end of the
//! list.
//!
//! Each migration is written once for each database under one version and
//! name. SQLite's statements are the TypeScript engine's, verbatim, so a
//! file it wrote opens and migrates and one written here opens there.
//! Postgres's build the same tables in BIGINT, DOUBLE PRECISION and TEXT,
//! with identity columns for SQLite's AUTOINCREMENT keys, JSON checks that
//! cast (`::json`), named CHECK constraints a later migration can replace
//! where SQLite has to copy the event log, and PL/pgSQL functions for its
//! append-only triggers. Instance data and events stay JSON text on both,
//! so their key order and number text survive (D58).
//!
//! Migration 10 builds the indexes of the unique fields of every version
//! published before it from the schema documents, which is the engine's
//! code, not the storage's: [`engine_migrations`] takes it as a function.

use crate::db::Tx;
use crate::dialect::Sql;
use crate::error::Result;
use crate::migrations::{Migration, MigrationSet};

/// The owner of the engine's own migrations.
pub const ENGINE_OWNER: &str = "engine";

/// The engine's migrations, in order. `unique_indexes` is migration 10:
/// it creates the indexes of each live version's unique fields, or fails
/// when the stored instances break one, which rolls the migration back.
pub fn engine_migrations(
    unique_indexes: impl Fn(&Tx<'_>) -> Result<()> + Send + Sync + 'static,
) -> MigrationSet {
    let step =
        |version, name, sqlite, postgres| Migration::sql(version, name, Sql::per(sqlite, postgres));
    MigrationSet::new(
        ENGINE_OWNER,
        vec![
            step(1, "schema versions", SQLITE_1, POSTGRES_1),
            step(2, "instances and the event log", SQLITE_2, POSTGRES_2),
            step(3, "publish events by namespace", SQLITE_3, POSTGRES_3),
            step(
                4,
                "operation events and behavior storage",
                SQLITE_4,
                POSTGRES_4,
            ),
            step(5, "references between instances", SQLITE_5, POSTGRES_5),
            step(6, "reactions, schedules and causes", SQLITE_6, POSTGRES_6),
            step(
                7,
                "define events and calling services",
                SQLITE_7,
                POSTGRES_7,
            ),
            step(8, "what a reference hears", SQLITE_8, POSTGRES_8),
            step(9, "the value store", SQLITE_9, POSTGRES_9),
            Migration::new(10, "unique fields and indexes", unique_indexes),
            step(11, "namespaces a create makes", SQLITE_11, POSTGRES_11),
            step(12, "event log retention", SQLITE_12, POSTGRES_12),
        ],
    )
}

const SQLITE_1: &str = "
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
";

const POSTGRES_1: &str = "
CREATE TABLE engine_schemas (
  namespace    TEXT   NOT NULL,
  name         TEXT   NOT NULL,
  version      BIGINT NOT NULL CHECK (version >= 0),
  document     TEXT   NOT NULL CHECK (document::json IS NOT NULL),
  hash         TEXT   NOT NULL,
  defined_at   BIGINT NOT NULL,
  published_at BIGINT,
  PRIMARY KEY (namespace, name, version),
  CHECK ((version = 0) = (published_at IS NULL))
);

CREATE INDEX engine_schemas_name ON engine_schemas (name, namespace);
";

const SQLITE_2: &str = "
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
";

// SQLite's AUTOINCREMENT keys are identity columns, which never reuse a
// value; Postgres's do not roll back with a transaction, so a cursor that
// a rolled-back write took is a gap. The append-only triggers call a
// function, pinned to the engine's schema (search_path FROM CURRENT).
const POSTGRES_2: &str = "
ALTER TABLE engine_schemas ADD COLUMN defined_by TEXT, ADD COLUMN published_by TEXT;

CREATE TABLE engine_instances (
  position         BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  namespace        TEXT   NOT NULL,
  schema           TEXT   NOT NULL,
  id               TEXT   NOT NULL,
  schema_namespace TEXT   NOT NULL,
  version          BIGINT NOT NULL CHECK (version >= 1),
  seq              BIGINT NOT NULL CHECK (seq >= 1),
  data             TEXT   NOT NULL CHECK (json_typeof(data::json) = 'object'),
  created_at       BIGINT NOT NULL,
  created_by       TEXT   NOT NULL,
  updated_at       BIGINT NOT NULL,
  updated_by       TEXT   NOT NULL,
  UNIQUE (namespace, schema, id)
);

CREATE INDEX engine_instances_list ON engine_instances (namespace, schema, position);

CREATE TABLE engine_events (
  cursor      BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  kind        TEXT   NOT NULL CONSTRAINT engine_events_kind CHECK (kind IN ('create', 'update', 'delete', 'publish')),
  namespace   TEXT   NOT NULL,
  schema      TEXT   NOT NULL,
  instance_id TEXT,
  seq         BIGINT CHECK (seq >= 1),
  version     BIGINT NOT NULL CHECK (version >= 1),
  actor       TEXT   NOT NULL,
  at          BIGINT NOT NULL,
  change      TEXT   CHECK (change IS NULL OR change::json IS NOT NULL),
  CONSTRAINT engine_events_instance_kind CHECK ((kind = 'publish') = (instance_id IS NULL)),
  CONSTRAINT engine_events_instance_seq CHECK ((instance_id IS NULL) = (seq IS NULL))
);

CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
CREATE INDEX engine_events_namespace ON engine_events (namespace, cursor);
CREATE INDEX engine_events_schema ON engine_events (namespace, schema, cursor);

CREATE FUNCTION engine_events_append_only() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  RAISE EXCEPTION 'engine_events is append-only';
END
$$;

CREATE TRIGGER engine_events_no_update BEFORE UPDATE ON engine_events
FOR EACH ROW EXECUTE FUNCTION engine_events_append_only();
CREATE TRIGGER engine_events_no_delete BEFORE DELETE ON engine_events
FOR EACH ROW EXECUTE FUNCTION engine_events_append_only();
";

const SQLITE_3: &str = "
CREATE INDEX engine_events_publish ON engine_events (namespace, cursor) WHERE kind = 'publish';
";

const POSTGRES_3: &str = SQLITE_3;

// SQLite cannot change a CHECK constraint, so the event log is copied into
// a table whose kind CHECK admits operation. The copy keeps every cursor,
// so the autoincrement sequence continues from the last one; dropping the
// old table fires none of its triggers and drops its indexes, so every
// index and trigger of migrations 2 and 3 is made again on the copy.
const SQLITE_4: &str = "
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
";

// Postgres replaces the named CHECK in place.
const POSTGRES_4: &str = "
ALTER TABLE engine_events
  DROP CONSTRAINT engine_events_kind,
  ADD CONSTRAINT engine_events_kind CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish'));

CREATE TABLE engine_behaviors (
  name       TEXT   PRIMARY KEY,
  key        TEXT   NOT NULL UNIQUE,
  created_at BIGINT NOT NULL
);
";

const SQLITE_5: &str = "
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
";

// Postgres has no rowid, the order SQLite reads references in (the order
// they were recorded), so the table keeps it in an identity of its own.
const POSTGRES_5: &str = "
CREATE TABLE engine_references (
  position      BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
  namespace     TEXT NOT NULL,
  target_schema TEXT NOT NULL,
  target_id     TEXT NOT NULL,
  source_schema TEXT NOT NULL,
  source_id     TEXT NOT NULL,
  behavior      TEXT NOT NULL,
  key           TEXT NOT NULL,
  PRIMARY KEY (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
);

CREATE INDEX engine_references_source ON engine_references (namespace, source_schema, source_id, behavior);
";

const SQLITE_6: &str = "
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
";

// halted stays an integer 0 or 1 rather than a boolean, so it reads back
// as the same value from either database.
const POSTGRES_6: &str = "
CREATE TABLE engine_subscriptions (
  behavior       TEXT   NOT NULL,
  namespace      TEXT   NOT NULL,
  schema         TEXT   NOT NULL,
  cursor         BIGINT NOT NULL CHECK (cursor >= 0),
  halted         BIGINT NOT NULL DEFAULT 0 CHECK (halted IN (0, 1)),
  attempts       BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  retry_at       BIGINT,
  failed_cursor  BIGINT,
  failed_at      BIGINT,
  error          TEXT,
  skipped        BIGINT NOT NULL DEFAULT 0 CHECK (skipped >= 0),
  skipped_cursor BIGINT,
  skipped_reason TEXT CHECK (skipped_reason IS NULL OR skipped_reason IN ('depth', 'resume')),
  PRIMARY KEY (behavior, namespace, schema)
);

CREATE TABLE engine_schedules (
  behavior    TEXT   NOT NULL,
  schedule    TEXT   NOT NULL,
  namespace   TEXT   NOT NULL,
  schema      TEXT   NOT NULL,
  last_run_at BIGINT,
  next_run_at BIGINT NOT NULL,
  failures    BIGINT NOT NULL DEFAULT 0 CHECK (failures >= 0),
  error       TEXT,
  PRIMARY KEY (behavior, schedule, namespace, schema)
);

ALTER TABLE engine_events
  ADD COLUMN cause_behavior TEXT,
  ADD COLUMN cause_event BIGINT,
  ADD COLUMN cause_schedule TEXT,
  ADD COLUMN depth BIGINT NOT NULL DEFAULT 0 CHECK (depth >= 0);
";

// As migration 4 did: SQLite cannot change a CHECK constraint, so the log
// is copied, cursors and all, into a table whose kind CHECK admits define,
// whose version is null for a define alone, and which has the service
// column; every index and trigger is made again.
const SQLITE_7: &str = "
CREATE TABLE engine_events_next (
  cursor         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind           TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish', 'define')),
  namespace      TEXT    NOT NULL,
  schema         TEXT    NOT NULL,
  instance_id    TEXT,
  seq            INTEGER CHECK (seq >= 1),
  version        INTEGER CHECK (version >= 1),
  actor          TEXT    NOT NULL,
  at             INTEGER NOT NULL,
  change         TEXT    CHECK (change IS NULL OR json_valid(change)),
  cause_behavior TEXT,
  cause_event    INTEGER,
  cause_schedule TEXT,
  depth          INTEGER NOT NULL DEFAULT 0 CHECK (depth >= 0),
  service        TEXT,
  CHECK ((kind IN ('publish', 'define')) = (instance_id IS NULL)),
  CHECK ((instance_id IS NULL) = (seq IS NULL)),
  CHECK ((kind = 'define') = (version IS NULL))
) STRICT;

INSERT INTO engine_events_next
  (cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change, cause_behavior, cause_event, cause_schedule, depth)
SELECT cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change, cause_behavior, cause_event, cause_schedule, depth
FROM engine_events ORDER BY cursor;

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
";

const POSTGRES_7: &str = "
ALTER TABLE engine_events
  DROP CONSTRAINT engine_events_kind,
  DROP CONSTRAINT engine_events_instance_kind,
  ALTER COLUMN version DROP NOT NULL,
  ADD COLUMN service TEXT,
  ADD CONSTRAINT engine_events_kind CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish', 'define')),
  ADD CONSTRAINT engine_events_instance_kind CHECK ((kind IN ('publish', 'define')) = (instance_id IS NULL)),
  ADD CONSTRAINT engine_events_define_version CHECK ((kind = 'define') = (version IS NULL));
";

// A reference recorded before hears every change, as it did.
const SQLITE_8: &str = "
ALTER TABLE engine_references ADD COLUMN hears TEXT;
ALTER TABLE engine_references ADD COLUMN crosses REAL;

CREATE INDEX engine_references_hears ON engine_references (namespace, target_schema, target_id, hears, crosses);
";

const POSTGRES_8: &str = "
ALTER TABLE engine_references ADD COLUMN hears TEXT, ADD COLUMN crosses DOUBLE PRECISION;

CREATE INDEX engine_references_hears ON engine_references (namespace, target_schema, target_id, hears, crosses);
";

// A row or event written before keeps its values inline and has no
// value_refs: an instance's row moves its large members to the store at
// its next write, and an event is never rewritten.
const SQLITE_9: &str = "
CREATE TABLE engine_payloads (
  hash  TEXT    PRIMARY KEY,
  value TEXT    NOT NULL CHECK (json_valid(value)),
  bytes INTEGER NOT NULL CHECK (bytes >= 0)
) STRICT;

CREATE TABLE engine_payload_holders (
  hash      TEXT NOT NULL,
  namespace TEXT NOT NULL,
  schema    TEXT NOT NULL,
  holder    TEXT NOT NULL,
  id        TEXT NOT NULL,
  key       TEXT NOT NULL,
  PRIMARY KEY (hash, namespace, schema, holder, id, key)
) STRICT;

CREATE INDEX engine_payload_holders_holder ON engine_payload_holders (namespace, schema, holder, id, key);

ALTER TABLE engine_instances ADD COLUMN value_refs TEXT CHECK (value_refs IS NULL OR json_valid(value_refs));
ALTER TABLE engine_events ADD COLUMN value_refs TEXT CHECK (value_refs IS NULL OR json_valid(value_refs));
";

const POSTGRES_9: &str = "
CREATE TABLE engine_payloads (
  hash  TEXT   PRIMARY KEY,
  value TEXT   NOT NULL CHECK (value::json IS NOT NULL),
  bytes BIGINT NOT NULL CHECK (bytes >= 0)
);

CREATE TABLE engine_payload_holders (
  hash      TEXT NOT NULL,
  namespace TEXT NOT NULL,
  schema    TEXT NOT NULL,
  holder    TEXT NOT NULL,
  id        TEXT NOT NULL,
  key       TEXT NOT NULL,
  PRIMARY KEY (hash, namespace, schema, holder, id, key)
);

CREATE INDEX engine_payload_holders_holder ON engine_payload_holders (namespace, schema, holder, id, key);

ALTER TABLE engine_instances ADD COLUMN value_refs TEXT CHECK (value_refs IS NULL OR value_refs::json IS NOT NULL);
ALTER TABLE engine_events ADD COLUMN value_refs TEXT CHECK (value_refs IS NULL OR value_refs::json IS NOT NULL);
";

const SQLITE_11: &str = "
CREATE TABLE engine_namespaces (
  name        TEXT    PRIMARY KEY,
  created_at  INTEGER NOT NULL,
  created_by  TEXT    NOT NULL,
  archived_at INTEGER,
  archived_by TEXT,
  CHECK ((archived_at IS NULL) = (archived_by IS NULL))
) STRICT;
";

const POSTGRES_11: &str = "
CREATE TABLE engine_namespaces (
  name        TEXT   PRIMARY KEY,
  created_at  BIGINT NOT NULL,
  created_by  TEXT   NOT NULL,
  archived_at BIGINT,
  archived_by TEXT,
  CHECK ((archived_at IS NULL) = (archived_by IS NULL))
);
";

// Every publish event is still in the log when this runs, so each version
// gets its publish's cursor; one a version-1 file stored with no event
// keeps null, from which a subscription starts at 0, as it did. The delete
// trigger is made again, letting through only an event at or before its
// namespace's floor.
const SQLITE_12: &str = "
ALTER TABLE engine_schemas ADD COLUMN published_cursor INTEGER;

UPDATE engine_schemas SET published_cursor = (
  SELECT MAX(events.cursor) FROM engine_events AS events
  WHERE events.kind = 'publish' AND events.namespace = engine_schemas.namespace
    AND events.schema = engine_schemas.name AND events.version = engine_schemas.version
) WHERE version > 0;

CREATE TABLE engine_log_floors (
  namespace     TEXT    PRIMARY KEY,
  floor         INTEGER NOT NULL CHECK (floor >= 0),
  publish_floor INTEGER NOT NULL DEFAULT 0 CHECK (publish_floor >= 0),
  pruned        INTEGER NOT NULL DEFAULT 0 CHECK (pruned >= 0)
) STRICT;

CREATE TABLE engine_event_bases (
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT    NOT NULL,
  seq         INTEGER NOT NULL CHECK (seq >= 1),
  data        TEXT    CHECK (data IS NULL OR (json_valid(data) AND json_type(data) = 'object')),
  value_refs  TEXT    CHECK (value_refs IS NULL OR json_valid(value_refs)),
  PRIMARY KEY (namespace, schema, instance_id)
) STRICT;

DROP TRIGGER engine_events_no_delete;
CREATE TRIGGER engine_events_no_delete BEFORE DELETE ON engine_events
WHEN OLD.cursor > COALESCE((SELECT floor FROM engine_log_floors WHERE namespace = OLD.namespace), 0)
BEGIN SELECT RAISE(ABORT, 'engine_events is append-only'); END;
";

// A Postgres trigger's WHEN cannot read a table, so the function reads the
// floor.
const POSTGRES_12: &str = "
ALTER TABLE engine_schemas ADD COLUMN published_cursor BIGINT;

UPDATE engine_schemas SET published_cursor = (
  SELECT MAX(events.cursor) FROM engine_events AS events
  WHERE events.kind = 'publish' AND events.namespace = engine_schemas.namespace
    AND events.schema = engine_schemas.name AND events.version = engine_schemas.version
) WHERE version > 0;

CREATE TABLE engine_log_floors (
  namespace     TEXT   PRIMARY KEY,
  floor         BIGINT NOT NULL CHECK (floor >= 0),
  publish_floor BIGINT NOT NULL DEFAULT 0 CHECK (publish_floor >= 0),
  pruned        BIGINT NOT NULL DEFAULT 0 CHECK (pruned >= 0)
);

CREATE TABLE engine_event_bases (
  namespace   TEXT   NOT NULL,
  schema      TEXT   NOT NULL,
  instance_id TEXT   NOT NULL,
  seq         BIGINT NOT NULL CHECK (seq >= 1),
  data        TEXT   CHECK (data IS NULL OR json_typeof(data::json) = 'object'),
  value_refs  TEXT   CHECK (value_refs IS NULL OR value_refs::json IS NOT NULL),
  PRIMARY KEY (namespace, schema, instance_id)
);

CREATE FUNCTION engine_events_no_delete() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  IF OLD.cursor > COALESCE((SELECT f.floor FROM engine_log_floors AS f WHERE f.namespace = OLD.namespace), 0) THEN
    RAISE EXCEPTION 'engine_events is append-only';
  END IF;
  RETURN OLD;
END
$$;

DROP TRIGGER engine_events_no_delete ON engine_events;
CREATE TRIGGER engine_events_no_delete BEFORE DELETE ON engine_events
FOR EACH ROW EXECUTE FUNCTION engine_events_no_delete();
";
