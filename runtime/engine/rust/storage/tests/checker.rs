//! The behavior SQL checker: the refusals of `behavior-sql.test.ts`, on
//! both databases' tokens, and what Postgres refuses besides.

use superschematic_engine_storage::{sql_refusal, Dialect, SqlMode};

const PREFIX: &str = "bhv_test_sneaky__";
const BOTH: [Dialect; 2] = [Dialect::Sqlite, Dialect::Postgres];

fn refusal(sql: &str, mode: SqlMode, dialect: Dialect) -> Option<String> {
    sql_refusal(sql, PREFIX, mode, dialect, &["app".to_owned()])
}

fn refused(sql: &str, mode: SqlMode, dialect: Dialect) -> String {
    refusal(sql, mode, dialect).unwrap_or_else(|| panic!("{dialect}: {sql} was not refused"))
}

#[test]
fn a_behavior_reads_and_writes_its_own_tables() {
    for dialect in BOTH {
        for (sql, mode) in [
            (
                "INSERT INTO bhv_test_sneaky__notes (id, body) VALUES ('n1', 'a desk'); ",
                SqlMode::Write,
            ),
            (
                "INSERT INTO \"BHV_TEST_SNEAKY__SEARCH\" (body) VALUES (?)",
                SqlMode::Write,
            ),
            (
                "SELECT body FROM bhv_test_sneaky__notes -- engine_instances is only a comment",
                SqlMode::Read,
            ),
            (
                "/* engine_events */ SELECT body FROM bhv_test_sneaky__search WHERE bhv_test_sneaky__search MATCH ?",
                SqlMode::Read,
            ),
            (
                "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3) SELECT i FROM n",
                SqlMode::Read,
            ),
            ("UPDATE bhv_test_sneaky__notes SET body = ?", SqlMode::Write),
            ("VALUES (1)", SqlMode::Read),
            (
                "SELECT t.id FROM bhv_test_sneaky__notes AS t WHERE t.body = ?",
                SqlMode::Read,
            ),
        ] {
            assert_eq!(refusal(sql, mode, dialect), None, "{dialect}: {sql}");
        }
    }
}

#[test]
fn sql_naming_anything_outside_the_behaviors_storage_is_refused() {
    let outside = [
        ("SELECT data FROM engine_instances", "engine_instances"),
        (
            "UPDATE engine_instances SET bhv_test_counter__count = 99",
            "engine_instances",
        ),
        ("SELECT * FROM \"ENGINE_EVENTS\"", "ENGINE_EVENTS"),
        ("SELECT * FROM [engine_schemas]", "engine_schemas"),
        ("SELECT * FROM `engine_behaviors`", "engine_behaviors"),
        ("SELECT * FROM 'engine_instances'", "engine_instances"),
        ("SELECT * FROM main.engine_migrations", "engine_migrations"),
        (
            "DELETE FROM bhv_test_counter__history",
            "bhv_test_counter__history",
        ),
        ("SELECT name FROM sqlite_master", "sqlite_master"),
        ("SELECT * FROM pragma_table_info('x')", "pragma_table_info"),
        (
            "SELECT body FROM bhv_test_sneaky__notes WHERE body = 'engine_like'",
            "engine_like",
        ),
    ];
    for dialect in BOTH {
        for (sql, name) in outside {
            assert_eq!(
                refused(sql, SqlMode::Write, dialect),
                format!(
                    "it names {name}, which is not the behavior's own storage (bhv_test_sneaky__*)"
                ),
                "{dialect}: {sql}"
            );
        }
        let others = [
            (
                "PRAGMA foreign_keys = OFF",
                "a write runs SELECT, VALUES, WITH, INSERT, UPDATE, DELETE or REPLACE, not PRAGMA",
            ),
            ("ATTACH DATABASE 'other.db' AS other", "not ATTACH"),
            ("BEGIN", "not BEGIN"),
            ("SAVEPOINT mine", "not SAVEPOINT"),
            ("COMMIT", "not COMMIT"),
            ("CREATE TABLE bhv_test_sneaky__more (x TEXT)", "not CREATE"),
            ("DROP TABLE bhv_test_sneaky__notes", "not DROP"),
            (
                "INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n2'); DELETE FROM bhv_test_sneaky__notes",
                "the SQL holds more than one statement; run one at a time",
            ),
            ("SELECT load_extension('evil')", "it calls load_extension"),
            ("", "the SQL holds no statement"),
            ("  -- only a comment\n", "the SQL holds no statement"),
        ];
        for (sql, message) in others {
            let refusal = refused(sql, SqlMode::Write, dialect);
            assert!(refusal.contains(message), "{dialect}: {sql}: {refusal}");
        }
    }
}

#[test]
fn a_read_runs_select_values_and_with_select_only() {
    for dialect in BOTH {
        for (sql, message) in [
            (
                "INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n9') RETURNING id",
                "a read runs SELECT, VALUES or WITH ... SELECT, not INSERT",
            ),
            ("DELETE FROM bhv_test_sneaky__notes RETURNING id", "not DELETE"),
            (
                "WITH gone AS (SELECT id FROM bhv_test_sneaky__notes) DELETE FROM bhv_test_sneaky__notes WHERE id IN (SELECT id FROM gone) RETURNING id",
                "not WITH ... DELETE",
            ),
            (
                "REPLACE INTO bhv_test_sneaky__notes (id) VALUES ('n9') RETURNING id",
                "not REPLACE",
            ),
            ("WITH x AS (SELECT 1)", "its WITH clause is followed by no statement"),
        ] {
            let refusal = refused(sql, SqlMode::Read, dialect);
            assert!(refusal.contains(message), "{dialect}: {sql}: {refusal}");
        }
    }
}

#[test]
fn a_migration_creates_only_tables_and_indexes_of_its_own() {
    let both = [
        (
            "CREATE TRIGGER bhv_test_sneaky__t AFTER INSERT ON bhv_test_sneaky__notes BEGIN SELECT 1; END",
            "the SQL holds more than one statement",
        ),
        (
            "CREATE TRIGGER bhv_test_sneaky__t AFTER INSERT ON bhv_test_sneaky__notes EXECUTE FUNCTION f()",
            "a migration may CREATE only tables and indexes of its own, not CREATE TRIGGER",
        ),
        ("CREATE VIEW bhv_test_sneaky__v AS SELECT 1", "not CREATE VIEW"),
        ("CREATE TEMP TABLE bhv_test_sneaky__t (x)", "not CREATE TEMP TABLE"),
        (
            "ALTER TABLE engine_instances ADD COLUMN x TEXT",
            "it names engine_instances",
        ),
        ("PRAGMA writable_schema = ON", "not PRAGMA"),
        (
            "CREATE INDEX bhv_test_sneaky___index_x ON bhv_test_sneaky__notes (body)",
            "it names bhv_test_sneaky___index_x, which is under bhv_test_sneaky___",
        ),
        ("CREATE OR REPLACE FUNCTION f() RETURNS int", "not CREATE OR REPLACE"),
        ("DROP VIEW bhv_test_sneaky__v", "not DROP VIEW"),
    ];
    for dialect in BOTH {
        for (sql, message) in both {
            let refusal = refused(sql, SqlMode::Migrate, dialect);
            assert!(refusal.contains(message), "{dialect}: {sql}: {refusal}");
        }
        for sql in [
            "CREATE TABLE bhv_test_sneaky__t (id TEXT PRIMARY KEY)",
            "CREATE UNIQUE INDEX bhv_test_sneaky__t_id ON bhv_test_sneaky__t (id)",
            "ALTER TABLE bhv_test_sneaky__t ADD COLUMN body TEXT",
            "DROP INDEX bhv_test_sneaky__t_id",
            "DROP TABLE bhv_test_sneaky__t",
            "INSERT INTO bhv_test_sneaky__t (id) VALUES (?)",
        ] {
            assert_eq!(
                refusal(sql, SqlMode::Migrate, dialect),
                None,
                "{dialect}: {sql}"
            );
        }
    }
    // A virtual table is fts5, under a name of its own: no other module, no
    // other name.
    let sqlite = [
        (
            "CREATE VIRTUAL TABLE bhv_test_sneaky__boxes USING rtree(id, lo, hi)",
            "a migration creates a virtual table only with the fts5 module, not rtree",
        ),
        (
            "CREATE VIRTUAL TABLE bhv_test_sneaky__old USING fts4(body)",
            "only with the fts5 module, not fts4",
        ),
        ("CREATE VIRTUAL TABLE bhv_test_sneaky__stat USING dbstat", "not dbstat"),
        ("CREATE VIRTUAL TABLE bhv_test_sneaky__bare", "under a name of its own"),
        (
            "CREATE VIRTUAL TABLE notes_index USING fts5(body)",
            "a migration creates a virtual table only under a name of its own (bhv_test_sneaky__*, unqualified)",
        ),
        (
            "CREATE VIRTUAL TABLE main.bhv_test_sneaky__more USING fts5(body)",
            "only under a name of its own",
        ),
        (
            "CREATE VIRTUAL TABLE temp.bhv_test_sneaky__more USING fts5(body)",
            "only under a name of its own",
        ),
        (
            "CREATE VIRTUAL TABLE bhv_test_sneaky__mirror USING fts5(data, content='engine_instances')",
            "it names engine_instances",
        ),
    ];
    for (sql, message) in sqlite {
        let refusal = refused(sql, SqlMode::Migrate, Dialect::Sqlite);
        assert!(refusal.contains(message), "{sql}: {refusal}");
    }
    for sql in [
        "CREATE VIRTUAL TABLE IF NOT EXISTS bhv_test_sneaky__titles USING fts5(title)",
        "CREATE VIRTUAL TABLE \"bhv_test_sneaky__bodies\" USING FTS5(body, tokenize = 'unicode61 remove_diacritics 2')",
    ] {
        assert_eq!(refusal(sql, SqlMode::Migrate, Dialect::Sqlite), None, "{sql}");
        assert_eq!(
            refused(sql, SqlMode::Migrate, Dialect::Postgres),
            "Postgres has no virtual tables; full-text search is the engine's own (Search)"
        );
    }
}

#[test]
fn postgres_refuses_its_catalog_other_schemas_and_what_reaches_past_the_tables() {
    let refusals = [
        ("SELECT relname FROM pg_class", SqlMode::Read, "it names pg_class, which is not the behavior's own storage"),
        ("SELECT * FROM \"PG_PROC\"", SqlMode::Read, "it names PG_PROC"),
        ("SELECT * FROM 'pg_settings'", SqlMode::Read, "it names pg_settings"),
        ("SELECT pg_sleep(10)", SqlMode::Read, "it names pg_sleep"),
        (
            "SELECT * FROM information_schema.tables",
            SqlMode::Read,
            "it names information_schema, which is not the behavior's own storage",
        ),
        (
            "SELECT * FROM public.users",
            SqlMode::Read,
            "it names public.users, an object of schema public; a behavior names its own tables unqualified",
        ),
        ("SELECT * FROM \"public\".\"users\"", SqlMode::Read, "it names public.users"),
        ("SELECT * FROM APP.orders", SqlMode::Read, "it names APP.orders, an object of schema APP"),
        (
            "INSERT INTO bhv_test_sneaky__t SELECT * FROM postgres.public.users",
            SqlMode::Write,
            "it names public.users",
        ),
        (
            "SELECT set_config('search_path', 'public', true)",
            SqlMode::Read,
            "it calls set_config, which reaches past the behavior's own storage",
        ),
        ("SELECT current_setting('search_path')", SqlMode::Read, "it calls current_setting"),
        ("SELECT query_to_xml('select 1', true, true, '')", SqlMode::Read, "it calls query_to_xml"),
        ("SELECT table_to_xmlschema('t', true, true, '')", SqlMode::Read, "it calls table_to_xmlschema"),
        ("SELECT * FROM ts_stat('select v from t')", SqlMode::Read, "it calls ts_stat"),
        ("SELECT dblink_connect('x')", SqlMode::Read, "it calls dblink_connect"),
        ("SELECT lo_import('/etc/passwd')", SqlMode::Read, "it calls lo_import"),
        (
            "WITH gone AS (DELETE FROM bhv_test_sneaky__notes RETURNING id) SELECT count(*) FROM gone",
            SqlMode::Read,
            "it nests DELETE in another statement; on Postgres a statement that changes rows runs on its own",
        ),
        (
            "WITH moved AS (INSERT INTO bhv_test_sneaky__a SELECT 1 RETURNING *) SELECT * FROM moved",
            SqlMode::Write,
            "it nests INSERT in another statement",
        ),
        (
            "SELECT * INTO bhv_test_sneaky__copy FROM bhv_test_sneaky__notes",
            SqlMode::Write,
            "SELECT ... INTO creates a table",
        ),
        ("COPY bhv_test_sneaky__notes TO '/tmp/x'", SqlMode::Write, "not COPY"),
        ("DO $$ BEGIN PERFORM 1; END $$", SqlMode::Migrate, "not DO"),
        ("SET search_path = public", SqlMode::Write, "not SET"),
        ("SET LOCAL statement_timeout = 0", SqlMode::Migrate, "not SET"),
        ("LISTEN bhv_test_sneaky__x", SqlMode::Write, "not LISTEN"),
        ("NOTIFY bhv_test_sneaky__x", SqlMode::Write, "not NOTIFY"),
        ("RESET ALL", SqlMode::Write, "not RESET"),
        ("SHOW search_path", SqlMode::Read, "not SHOW"),
        ("TRUNCATE bhv_test_sneaky__notes", SqlMode::Write, "not TRUNCATE"),
        ("MERGE INTO bhv_test_sneaky__a USING bhv_test_sneaky__b ON true WHEN MATCHED THEN DELETE", SqlMode::Write, "not MERGE"),
        (
            "ALTER TABLE bhv_test_sneaky__notes SET SCHEMA public",
            SqlMode::Migrate,
            "a migration may not move a table to another schema",
        ),
        ("CREATE UNLOGGED TABLE bhv_test_sneaky__x (a int)", SqlMode::Migrate, "not CREATE UNLOGGED TABLE"),
        ("SELECT $$engine_events$$", SqlMode::Read, "it names engine_events"),
        ("SELECT E'it\\'s', 'engine_x'", SqlMode::Read, "it names engine_x"),
    ];
    for (sql, mode, message) in refusals {
        let refusal = refused(sql, mode, Dialect::Postgres);
        assert!(refusal.contains(message), "{sql}: {refusal}");
    }
    // What only Postgres refuses, SQLite lets through: it has no such
    // catalog, schemas or functions, and refuses the syntax itself.
    for sql in [
        "SELECT pg_x FROM bhv_test_sneaky__t",
        "SELECT set_config('a', 'b', true)",
        "SELECT t.id FROM bhv_test_sneaky__t AS t",
    ] {
        assert_eq!(refusal(sql, SqlMode::Read, Dialect::Sqlite), None, "{sql}");
    }
    // A comment or a dollar-quoted body hides nothing on Postgres, and a
    // subscript is not a quoted name.
    for sql in [
        "SELECT tags[1] FROM bhv_test_sneaky__t /* pg_class /* nested */ engine_x */",
        "SELECT body FROM bhv_test_sneaky__t WHERE body = $1 AND id = ?::text",
        "SELECT jsonb_exists(data::jsonb, 'k') FROM bhv_test_sneaky__t FOR UPDATE",
        "INSERT INTO bhv_test_sneaky__t (id) VALUES (?) ON CONFLICT (id) DO UPDATE SET n = bhv_test_sneaky__t.n + 1",
    ] {
        assert_eq!(refusal(sql, SqlMode::Write, Dialect::Postgres), None, "{sql}");
    }
}
