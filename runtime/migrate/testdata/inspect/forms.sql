-- The forms a file built by hand may take that the compiler's read of it
-- (sqlmigrate.ReadDatabase, D116) tells apart: declared types by their
-- affinity, defaults, collations, generated columns, checks in and out of
-- the schema's forms, foreign keys with what the model has no form of,
-- indexes over columns and over expressions, table options, and the
-- objects a schema's raw SQL makes beside those nothing declares.
CREATE TABLE parent (
  id INTEGER PRIMARY KEY,
  code TEXT NOT NULL COLLATE NOCASE,
  label VARCHAR(40) COLLATE RTRIM,
  UNIQUE (code, label)
);

CREATE TABLE child (
  id TEXT PRIMARY KEY,
  parent_id BIGINT REFERENCES parent ON UPDATE CASCADE ON DELETE SET NULL,
  other_id INTEGER REFERENCES missing DEFERRABLE INITIALLY DEFERRED,
  amount DECIMAL(10,2) DEFAULT 0 CHECK (amount >= 0 AND amount <= 100.5),
  title TEXT NOT NULL DEFAULT ('untitled') CHECK (length(title) >= 1 AND length(title) <= 80),
  kind TEXT CHECK (kind IN ('a', 'b''c')),
  flag BOOLEAN DEFAULT FALSE,
  made TEXT DEFAULT CURRENT_TIMESTAMP,
  payload,
  ratio DOUBLE PRECISION,
  body TEXT,
  search_text TEXT GENERATED ALWAYS AS (coalesce(title, '') || ' ' || coalesce(body, '')) STORED,
  CHECK (amount < ratio),
  CONSTRAINT kind_or_flag CHECK (kind IS NOT NULL OR flag)
);

CREATE INDEX child_by_parent ON child (parent_id DESC);
CREATE UNIQUE INDEX child_title_lower ON child (lower(title)) WHERE kind = 'a';
CREATE INDEX child_title_nocase ON child (title COLLATE NOCASE);
CREATE INDEX child_kind_title ON child (kind, lower(title));
CREATE INDEX child_body ON child (body);

CREATE TABLE strict_log (a INTEGER PRIMARY KEY, b TEXT) STRICT;

CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT) WITHOUT ROWID;

CREATE TABLE seq (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT UNIQUE ON CONFLICT REPLACE);

CREATE VIRTUAL TABLE child_fts USING fts5(title, body);

CREATE VIRTUAL TABLE stray_fts USING fts5(body);

CREATE TRIGGER child_fts_insert AFTER INSERT ON child
BEGIN
  INSERT INTO child_fts (rowid, title, body) VALUES (NEW.rowid, NEW.title, NEW.body);
END;

CREATE TRIGGER "trg_child_bump_version" AFTER UPDATE ON "child" FOR EACH ROW WHEN NEW.amount = OLD.amount BEGIN UPDATE "child" SET amount = OLD.amount + 1 WHERE rowid = NEW.rowid; END;

CREATE VIEW child_titles AS SELECT id, title FROM child;
