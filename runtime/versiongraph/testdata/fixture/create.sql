-- Generated PostgreSQL DDL for schema: fixture-version-graph-db
-- This file is auto-generated. Do not edit manually.

-- Enable required PostgreSQL extensions
-- Note: pgcrypto is only needed for PostgreSQL < 13 (for gen_random_uuid)
-- PostgreSQL 13+ has gen_random_uuid() built-in
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- Create tables (without foreign key constraints)

CREATE TABLE cover (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  photo_url TEXT NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE cover IS 'The recipe''s cover photo: at most one per ref.';

CREATE TABLE ingredient (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  step_key UUID NOT NULL,
  quantity TEXT NOT NULL,
  substitutes JSONB NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE ingredient IS 'An ingredient one step uses.';

CREATE TABLE note (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  reply_to UUID,
  body TEXT NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE note IS 'A cook''s note, threaded under another note.';

CREATE TABLE recipe (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL
);

COMMENT ON TABLE recipe IS 'A recipe: the stable identity its steps and ingredients are versioned under.';

CREATE TABLE recipe_commit (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  root_id UUID NOT NULL,
  ref_id UUID NOT NULL,
  parent_commit_id UUID,
  message TEXT,
  schema_epoch BIGINT NOT NULL,
  content_hash TEXT NOT NULL,
  "sequence" BIGINT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL
);

COMMENT ON TABLE recipe_commit IS 'A commit of the Recipe version graph: the exact row versions one ref sealed.';

CREATE TABLE recipe_patch (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  commit_id UUID NOT NULL,
  entity_kind TEXT NOT NULL,
  entity_key UUID NOT NULL,
  entity_id UUID NOT NULL,
  entity_version BIGINT NOT NULL,
  operation TEXT NOT NULL
);

COMMENT ON TABLE recipe_patch IS 'One entity a commit of the Recipe version graph changed, pinned to the row version it sealed.';

CREATE TABLE recipe_ref (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  root_id UUID NOT NULL,
  parent_ref_id UUID,
  base_commit_id UUID,
  head_commit_id UUID,
  "name" TEXT NOT NULL,
  sealed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_by UUID NOT NULL,
  deleted_at TIMESTAMPTZ,
  deleted_by UUID,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE recipe_ref IS 'A line of the Recipe version graph: a primary line when parentRef is null, else a change set.';

CREATE TABLE recipe_release (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  root_id UUID NOT NULL,
  commit_id UUID NOT NULL,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_by UUID NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE recipe_release IS 'The released commit of one root of the Recipe version graph; its history is the release log.';

CREATE TABLE recipe_snapshot_entry (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  commit_id UUID NOT NULL,
  entity_kind TEXT NOT NULL,
  entity_key UUID NOT NULL,
  entity_id UUID NOT NULL,
  entity_version BIGINT NOT NULL
);

COMMENT ON TABLE recipe_snapshot_entry IS 'One entity of a snapshotted commit of the Recipe version graph, pinned to the row version its tree holds.';

CREATE TABLE step (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  position BIGINT NOT NULL,
  instruction TEXT NOT NULL,
  timings JSONB NOT NULL,
  scratch TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  created_by UUID NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_by UUID NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE step IS 'One step of a recipe, ordered by position; updatedBy names its row''s writer.';

CREATE TABLE tasting (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  taster UUID NOT NULL,
  salty BOOLEAN NOT NULL,
  score DOUBLE PRECISION NOT NULL,
  servings BIGINT NOT NULL,
  tasted_on DATE DEFAULT CURRENT_DATE NOT NULL,
  tasted_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  served_at TIME DEFAULT CURRENT_TIME NOT NULL,
  rested INTERVAL NOT NULL,
  verdict TEXT NOT NULL,
  remarks JSONB NOT NULL,
  tags TEXT[] DEFAULT '{}' NOT NULL,
  helpers UUID[] DEFAULT '{}' NOT NULL,
  bites JSONB NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE tasting IS 'A tasting of the recipe. Its columns hold a value of every class a
descriptor names.';

CREATE TABLE utensil (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  recipe_id UUID NOT NULL,
  "name" TEXT NOT NULL,
  entity_key UUID DEFAULT gen_random_uuid() NOT NULL,
  ref_id UUID NOT NULL,
  deleted_on_ref BOOLEAN NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
);

COMMENT ON TABLE utensil IS 'A utensil the recipe needs, keyed by a plain UUID rather than an
AutoGenerate one.';

-- Create history tables for versioned entities

CREATE TABLE cover_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_cover_history_id_version ON cover_history (id, _version);
CREATE INDEX idx_cover_history_id_recorded ON cover_history (id, recorded_at);

COMMENT ON TABLE cover_history IS 'Version history for cover';

CREATE TABLE ingredient_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_ingredient_history_id_version ON ingredient_history (id, _version);
CREATE INDEX idx_ingredient_history_id_recorded ON ingredient_history (id, recorded_at);
-- The prune function filters on recorded_at alone; the index above leads with
-- the row key and cannot serve it, so retention would seq-scan the whole
-- history table on every pass.
CREATE INDEX idx_ingredient_history_recorded ON ingredient_history (recorded_at);

COMMENT ON TABLE ingredient_history IS 'Version history for ingredient';

CREATE TABLE note_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_note_history_id_version ON note_history (id, _version);
CREATE INDEX idx_note_history_id_recorded ON note_history (id, recorded_at);

COMMENT ON TABLE note_history IS 'Version history for note';

CREATE TABLE recipe_ref_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_recipe_ref_history_id_version ON recipe_ref_history (id, _version);
CREATE INDEX idx_recipe_ref_history_id_recorded ON recipe_ref_history (id, recorded_at);

COMMENT ON TABLE recipe_ref_history IS 'Version history for recipe_ref';

CREATE TABLE recipe_release_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_recipe_release_history_id_version ON recipe_release_history (id, _version);
CREATE INDEX idx_recipe_release_history_id_recorded ON recipe_release_history (id, recorded_at);

COMMENT ON TABLE recipe_release_history IS 'Version history for recipe_release';

CREATE TABLE step_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_step_history_id_version ON step_history (id, _version);
CREATE INDEX idx_step_history_id_recorded ON step_history (id, recorded_at);
-- The prune function filters on recorded_at alone; the index above leads with
-- the row key and cannot serve it, so retention would seq-scan the whole
-- history table on every pass.
CREATE INDEX idx_step_history_recorded ON step_history (recorded_at);

COMMENT ON TABLE step_history IS 'Version history for step';

CREATE TABLE tasting_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_tasting_history_id_version ON tasting_history (id, _version);
CREATE INDEX idx_tasting_history_id_recorded ON tasting_history (id, recorded_at);

COMMENT ON TABLE tasting_history IS 'Version history for tasting';

CREATE TABLE utensil_history (
  history_id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  id UUID NOT NULL,
  _version BIGINT NOT NULL,
  operation TEXT NOT NULL,
  data JSONB NOT NULL,
  recorded_at TIMESTAMPTZ DEFAULT clock_timestamp() NOT NULL
);

CREATE UNIQUE INDEX uq_utensil_history_id_version ON utensil_history (id, _version);
CREATE INDEX idx_utensil_history_id_recorded ON utensil_history (id, recorded_at);

COMMENT ON TABLE utensil_history IS 'Version history for utensil';

-- Add foreign key constraints

ALTER TABLE cover
  ADD CONSTRAINT fk_cover_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE cover
  ADD CONSTRAINT fk_cover_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE ingredient
  ADD CONSTRAINT fk_ingredient_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE ingredient
  ADD CONSTRAINT fk_ingredient_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE note
  ADD CONSTRAINT fk_note_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE note
  ADD CONSTRAINT fk_note_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_commit
  ADD CONSTRAINT fk_recipe_commit_root_id
  FOREIGN KEY (root_id)
  REFERENCES recipe(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_commit
  ADD CONSTRAINT fk_recipe_commit_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_commit
  ADD CONSTRAINT fk_recipe_commit_parent_commit_id
  FOREIGN KEY (parent_commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_patch
  ADD CONSTRAINT fk_recipe_patch_commit_id
  FOREIGN KEY (commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_ref
  ADD CONSTRAINT fk_recipe_ref_root_id
  FOREIGN KEY (root_id)
  REFERENCES recipe(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_ref
  ADD CONSTRAINT fk_recipe_ref_parent_ref_id
  FOREIGN KEY (parent_ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_ref
  ADD CONSTRAINT fk_recipe_ref_base_commit_id
  FOREIGN KEY (base_commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_ref
  ADD CONSTRAINT fk_recipe_ref_head_commit_id
  FOREIGN KEY (head_commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_release
  ADD CONSTRAINT fk_recipe_release_root_id
  FOREIGN KEY (root_id)
  REFERENCES recipe(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_release
  ADD CONSTRAINT fk_recipe_release_commit_id
  FOREIGN KEY (commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE recipe_snapshot_entry
  ADD CONSTRAINT fk_recipe_snapshot_entry_commit_id
  FOREIGN KEY (commit_id)
  REFERENCES recipe_commit(id)
  ON DELETE RESTRICT;

ALTER TABLE step
  ADD CONSTRAINT fk_step_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE step
  ADD CONSTRAINT fk_step_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE tasting
  ADD CONSTRAINT fk_tasting_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE tasting
  ADD CONSTRAINT fk_tasting_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

ALTER TABLE utensil
  ADD CONSTRAINT fk_utensil_recipe_id
  FOREIGN KEY (recipe_id)
  REFERENCES recipe(id)
  ON DELETE CASCADE;

ALTER TABLE utensil
  ADD CONSTRAINT fk_utensil_ref_id
  FOREIGN KEY (ref_id)
  REFERENCES recipe_ref(id)
  ON DELETE RESTRICT;

-- Create indexes

CREATE UNIQUE INDEX uq_cover_entity_ref ON cover USING BTREE (entity_key, ref_id);

CREATE UNIQUE INDEX uq_ingredient_entity_ref ON ingredient USING BTREE (entity_key, ref_id);

CREATE UNIQUE INDEX uq_note_entity_ref ON note USING BTREE (entity_key, ref_id);

CREATE UNIQUE INDEX uq_recipe_commit_root_sequence ON recipe_commit USING BTREE (root_id, "sequence");

CREATE UNIQUE INDEX uq_recipe_patch_entity ON recipe_patch USING BTREE (commit_id, entity_kind, entity_key);

CREATE INDEX idx_recipe_patch_entity_version ON recipe_patch USING BTREE (entity_id, entity_version);

CREATE UNIQUE INDEX uq_recipe_ref_root_name ON recipe_ref USING BTREE (root_id, "name") WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX uq_recipe_release_root ON recipe_release USING BTREE (root_id);

CREATE UNIQUE INDEX uq_recipe_snapshot_entry_entity ON recipe_snapshot_entry USING BTREE (commit_id, entity_kind, entity_key);

CREATE INDEX idx_recipe_snapshot_entry_entity_version ON recipe_snapshot_entry USING BTREE (entity_id, entity_version);

CREATE UNIQUE INDEX uq_step_entity_ref ON step USING BTREE (entity_key, ref_id);

CREATE UNIQUE INDEX uq_tasting_entity_ref ON tasting USING BTREE (entity_key, ref_id);

CREATE UNIQUE INDEX uq_utensil_entity_ref ON utensil USING BTREE (entity_key, ref_id);

-- Create history capture functions and triggers

CREATE OR REPLACE FUNCTION cover_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version.
    INSERT INTO cover_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO cover_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_cover_bump_version
  BEFORE UPDATE ON cover
  FOR EACH ROW EXECUTE FUNCTION cover_capture_history();

CREATE TRIGGER trg_cover_capture_history_write
  AFTER INSERT OR UPDATE ON cover
  FOR EACH ROW EXECUTE FUNCTION cover_capture_history();

CREATE TRIGGER trg_cover_capture_history_delete
  AFTER DELETE ON cover
  FOR EACH ROW EXECUTE FUNCTION cover_capture_history();

CREATE OR REPLACE FUNCTION ingredient_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version.
    INSERT INTO ingredient_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO ingredient_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ingredient_bump_version
  BEFORE UPDATE ON ingredient
  FOR EACH ROW EXECUTE FUNCTION ingredient_capture_history();

CREATE TRIGGER trg_ingredient_capture_history_write
  AFTER INSERT OR UPDATE ON ingredient
  FOR EACH ROW EXECUTE FUNCTION ingredient_capture_history();

CREATE TRIGGER trg_ingredient_capture_history_delete
  AFTER DELETE ON ingredient
  FOR EACH ROW EXECUTE FUNCTION ingredient_capture_history();

-- max_rows caps one call so a caller can drain a backlog in committed chunks
-- instead of one unbounded DELETE; NULL means no cap. The retention filter uses
-- now() (STABLE, folded once per statement and indexable) rather than
-- clock_timestamp() (VOLATILE, re-evaluated per row).
CREATE OR REPLACE FUNCTION ingredient_prune_history(retention_days INTEGER DEFAULT 365, max_rows INTEGER DEFAULT NULL) RETURNS BIGINT AS $$
DECLARE
  deleted_count BIGINT;
BEGIN
  WITH candidates AS (
    SELECT h.history_id
    FROM ingredient_history h
    WHERE h.recorded_at < now() - make_interval(days => retention_days)
      AND EXISTS (
        SELECT 1
        FROM ingredient_history newer
        WHERE newer.id = h.id
          AND newer._version > h._version
      )
      AND NOT EXISTS (
        SELECT 1
        FROM recipe_patch pin_1
        WHERE pin_1.entity_id = h.id
          AND pin_1.entity_version = h._version
      )
      AND NOT EXISTS (
        SELECT 1
        FROM recipe_snapshot_entry pin_2
        WHERE pin_2.entity_id = h.id
          AND pin_2.entity_version = h._version
      )
    LIMIT max_rows
  ),
  deleted AS (
    DELETE FROM ingredient_history h
    USING candidates c
    WHERE h.history_id = c.history_id
    RETURNING 1
  )
  SELECT count(*) INTO deleted_count FROM deleted;
  RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION note_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version.
    INSERT INTO note_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO note_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_note_bump_version
  BEFORE UPDATE ON note
  FOR EACH ROW EXECUTE FUNCTION note_capture_history();

CREATE TRIGGER trg_note_capture_history_write
  AFTER INSERT OR UPDATE ON note
  FOR EACH ROW EXECUTE FUNCTION note_capture_history();

CREATE TRIGGER trg_note_capture_history_delete
  AFTER DELETE ON note
  FOR EACH ROW EXECUTE FUNCTION note_capture_history();

CREATE OR REPLACE FUNCTION recipe_ref_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version. Its deleted_by is the actor a hard delete set in
    -- superschematic.history_actor_id for the statement, else the row's own value.
    INSERT INTO recipe_ref_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1,
        'deleted_by', COALESCE(
          NULLIF(current_setting('superschematic.history_actor_id', true), '')::UUID,
          OLD.deleted_by
        )
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO recipe_ref_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_recipe_ref_bump_version
  BEFORE UPDATE ON recipe_ref
  FOR EACH ROW EXECUTE FUNCTION recipe_ref_capture_history();

CREATE TRIGGER trg_recipe_ref_capture_history_write
  AFTER INSERT OR UPDATE ON recipe_ref
  FOR EACH ROW EXECUTE FUNCTION recipe_ref_capture_history();

CREATE TRIGGER trg_recipe_ref_capture_history_delete
  AFTER DELETE ON recipe_ref
  FOR EACH ROW EXECUTE FUNCTION recipe_ref_capture_history();

CREATE OR REPLACE FUNCTION recipe_release_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version. Its updated_by is the actor a hard delete set in
    -- superschematic.history_actor_id for the statement, else the row's own value.
    INSERT INTO recipe_release_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1,
        'updated_by', COALESCE(
          NULLIF(current_setting('superschematic.history_actor_id', true), '')::UUID,
          OLD.updated_by
        )
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO recipe_release_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_recipe_release_bump_version
  BEFORE UPDATE ON recipe_release
  FOR EACH ROW EXECUTE FUNCTION recipe_release_capture_history();

CREATE TRIGGER trg_recipe_release_capture_history_write
  AFTER INSERT OR UPDATE ON recipe_release
  FOR EACH ROW EXECUTE FUNCTION recipe_release_capture_history();

CREATE TRIGGER trg_recipe_release_capture_history_delete
  AFTER DELETE ON recipe_release
  FOR EACH ROW EXECUTE FUNCTION recipe_release_capture_history();

CREATE OR REPLACE FUNCTION step_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version. Its updated_by is the actor a hard delete set in
    -- superschematic.history_actor_id for the statement, else the row's own value.
    -- Every image leaves out scratch.
    INSERT INTO step_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      (to_jsonb(OLD) - ARRAY['scratch']) || jsonb_build_object(
        '_version', OLD._version + 1,
        'updated_by', COALESCE(
          NULLIF(current_setting('superschematic.history_actor_id', true), '')::UUID,
          OLD.updated_by
        )
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO step_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW) - ARRAY['scratch']);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_step_bump_version
  BEFORE UPDATE ON step
  FOR EACH ROW EXECUTE FUNCTION step_capture_history();

CREATE TRIGGER trg_step_capture_history_write
  AFTER INSERT OR UPDATE ON step
  FOR EACH ROW EXECUTE FUNCTION step_capture_history();

CREATE TRIGGER trg_step_capture_history_delete
  AFTER DELETE ON step
  FOR EACH ROW EXECUTE FUNCTION step_capture_history();

-- max_rows caps one call so a caller can drain a backlog in committed chunks
-- instead of one unbounded DELETE; NULL means no cap. The retention filter uses
-- now() (STABLE, folded once per statement and indexable) rather than
-- clock_timestamp() (VOLATILE, re-evaluated per row).
CREATE OR REPLACE FUNCTION step_prune_history(retention_days INTEGER DEFAULT 365, max_rows INTEGER DEFAULT NULL) RETURNS BIGINT AS $$
DECLARE
  deleted_count BIGINT;
BEGIN
  WITH candidates AS (
    SELECT h.history_id
    FROM step_history h
    WHERE h.recorded_at < now() - make_interval(days => retention_days)
      AND EXISTS (
        SELECT 1
        FROM step_history newer
        WHERE newer.id = h.id
          AND newer._version > h._version
      )
      AND NOT EXISTS (
        SELECT 1
        FROM recipe_patch pin_1
        WHERE pin_1.entity_id = h.id
          AND pin_1.entity_version = h._version
      )
      AND NOT EXISTS (
        SELECT 1
        FROM recipe_snapshot_entry pin_2
        WHERE pin_2.entity_id = h.id
          AND pin_2.entity_version = h._version
      )
    LIMIT max_rows
  ),
  deleted AS (
    DELETE FROM step_history h
    USING candidates c
    WHERE h.history_id = c.history_id
    RETURNING 1
  )
  SELECT count(*) INTO deleted_count FROM deleted;
  RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION tasting_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version.
    INSERT INTO tasting_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO tasting_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tasting_bump_version
  BEFORE UPDATE ON tasting
  FOR EACH ROW EXECUTE FUNCTION tasting_capture_history();

CREATE TRIGGER trg_tasting_capture_history_write
  AFTER INSERT OR UPDATE ON tasting
  FOR EACH ROW EXECUTE FUNCTION tasting_capture_history();

CREATE TRIGGER trg_tasting_capture_history_delete
  AFTER DELETE ON tasting
  FOR EACH ROW EXECUTE FUNCTION tasting_capture_history();

CREATE OR REPLACE FUNCTION utensil_capture_history() RETURNS trigger AS $$
BEGIN
  IF (TG_WHEN = 'BEFORE') THEN
    -- BEFORE UPDATE: the stored row takes the next version.
    NEW._version := OLD._version + 1;
    RETURN NEW;
  END IF;
  IF (TG_OP = 'DELETE') THEN
    -- Tombstone at OLD._version + 1: keeps (key, _version) strictly monotonic so
    -- the unique history index holds and the latest history row for a deleted key
    -- is the DELETE. The data payload is the pre-delete image at the tombstone's
    -- version.
    INSERT INTO utensil_history (id, _version, operation, data)
    VALUES (
      OLD.id,
      OLD._version + 1,
      'DELETE',
      to_jsonb(OLD) || jsonb_build_object(
        '_version', OLD._version + 1
      )
    );
    RETURN OLD;
  END IF;
  -- AFTER INSERT OR UPDATE: record the row as stored, so an INSERT ... ON
  -- CONFLICT DO UPDATE records the one UPDATE it made.
  INSERT INTO utensil_history (id, _version, operation, data)
  VALUES (NEW.id, NEW._version, TG_OP, to_jsonb(NEW));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_utensil_bump_version
  BEFORE UPDATE ON utensil
  FOR EACH ROW EXECUTE FUNCTION utensil_capture_history();

CREATE TRIGGER trg_utensil_capture_history_write
  AFTER INSERT OR UPDATE ON utensil
  FOR EACH ROW EXECUTE FUNCTION utensil_capture_history();

CREATE TRIGGER trg_utensil_capture_history_delete
  AFTER DELETE ON utensil
  FOR EACH ROW EXECUTE FUNCTION utensil_capture_history();
