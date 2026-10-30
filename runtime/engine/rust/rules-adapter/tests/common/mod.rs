//! What the adapter's tests on real databases share: the layout rule's
//! names as the generators write them, a parameter each dialect binds, the
//! `@optimistic` triggers sqlgen writes, a Postgres schema of a test's own
//! on the database `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL` names, and the
//! toy behaviors the tests register beside Workflow.

// Each test file uses some of what is here.
#![allow(dead_code)]

use serde_json::{json, Value};
use superschematic_rules::{
    Behavior, ConfigError, ConfigTarget, Cx, Declaration, Hook, Refusal, UseSpec,
};
use superschematic_rules_adapter::{
    Column, Convert, Layout, Operation, OwnedField, Table, TypeSpec, Use,
};

/// The variable that names the Postgres the tests run against; the
/// Postgres tests are skipped without it.
pub const RULES_DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL";

/// A field's name in snake case as the generators write it
/// (`codegen.ToSnakeCase`, the core README's layout rule): a `_` before each
/// capital that follows a lowercase letter or a digit, each `-` a `_`, the
/// whole lowercased. `enteredAt` is `entered_at`, `lastIPAddress`
/// `last_ipaddress`.
pub fn snake(name: &str) -> String {
    let mut out = String::new();
    let mut previous: Option<char> = None;
    for c in name.chars() {
        if c.is_ascii_uppercase()
            && previous.is_some_and(|p| p.is_ascii_lowercase() || p.is_ascii_digit())
        {
            out.push('_');
        }
        out.push(if c == '-' {
            '_'
        } else {
            c.to_ascii_lowercase()
        });
        previous = Some(c);
    }
    out
}

/// A behavior's storage key: its name lowercased, each run of other
/// characters one `_`, none at either end (behaviorlayout's `StorageKey`).
pub fn storage_key(behavior: &str) -> String {
    let mut out = String::new();
    for c in behavior.to_ascii_lowercase().chars() {
        if c.is_ascii_lowercase() || c.is_ascii_digit() {
            out.push(c);
        } else if !out.ends_with('_') {
            out.push('_');
        }
    }
    out.trim_matches('_').to_owned()
}

/// A column's name in camel case: the IR field a type anchor's owned
/// column takes here.
pub fn camel(column: &str) -> String {
    let mut out = String::new();
    let mut upper = false;
    for c in column.chars() {
        if c == '_' {
            upper = true;
        } else if upper {
            out.push(c.to_ascii_uppercase());
            upper = false;
        } else {
            out.push(c);
        }
    }
    out
}

/// A value a test's own SQL binds.
#[derive(Clone, Debug)]
pub enum Param {
    Null,
    Text(String),
    Int(i64),
}

impl Param {
    pub fn text(value: impl Into<String>) -> Param {
        Param::Text(value.into())
    }
}

/// The text `Date.prototype.toISOString` writes for epoch milliseconds,
/// which a SQLite date-time column holds and a Postgres `TIMESTAMPTZ` reads.
pub fn iso(millis: i64) -> String {
    let days = millis.div_euclid(86_400_000);
    let of_day = millis.rem_euclid(86_400_000);
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

/// The `@optimistic` trigger sqlgen writes on Postgres, which bumps
/// `_version` on every update of a table.
pub fn postgres_bump(table: &str) -> [String; 2] {
    [
        format!(
            "CREATE OR REPLACE FUNCTION \"{table}_bump_version\"() RETURNS trigger AS $$\nBEGIN\n  NEW._version := OLD._version + 1;\n  RETURN NEW;\nEND;\n$$ LANGUAGE plpgsql"
        ),
        format!(
            "CREATE TRIGGER \"trg_{table}_bump_version\" BEFORE UPDATE ON \"{table}\" FOR EACH ROW EXECUTE FUNCTION \"{table}_bump_version\"()"
        ),
    ]
}

/// The `@optimistic` trigger sqlgen writes on SQLite
/// (`internal/sqlmigrate`'s `sqliteBumpTrigger`): after an update that left
/// `_version` as it was, it moves it on.
pub fn sqlite_bump(table: &str) -> String {
    format!(
        "CREATE TRIGGER \"trg_{table}_bump_version\" AFTER UPDATE ON \"{table}\" FOR EACH ROW WHEN NEW._version = OLD._version BEGIN UPDATE \"{table}\" SET _version = OLD._version + 1 WHERE rowid = NEW.rowid; END"
    )
}

/// The column types of a test table in a dialect: text, a timestamp, a
/// version, a key, a number, a boolean, JSON.
pub struct ColumnTypes {
    pub text: &'static str,
    pub timestamp: &'static str,
    pub version: &'static str,
    pub key: &'static str,
    pub number: &'static str,
    pub boolean: &'static str,
    pub json: &'static str,
}

pub fn column_types(postgres: bool) -> ColumnTypes {
    if postgres {
        ColumnTypes {
            text: "TEXT",
            timestamp: "TIMESTAMPTZ",
            version: "BIGINT DEFAULT 1 NOT NULL",
            key: "TEXT PRIMARY KEY",
            number: "DOUBLE PRECISION",
            boolean: "BOOLEAN",
            json: "JSONB",
        }
    } else {
        ColumnTypes {
            text: "TEXT",
            timestamp: "TEXT",
            version: "INTEGER DEFAULT 1 NOT NULL",
            key: "TEXT PRIMARY KEY",
            number: "REAL",
            boolean: "INTEGER",
            json: "TEXT",
        }
    }
}

/// The nth placeholder in a dialect.
pub fn p(postgres: bool, n: usize) -> String {
    if postgres {
        format!("${n}")
    } else {
        format!("?{n}")
    }
}

// ---- the tables the store tests run on ----

pub fn flow() -> Value {
    json!({
        "states": ["draft", "paid", "shipped"],
        "initial": "draft",
        "transitions": [
            {"from": "draft", "to": "paid"},
            {"from": "paid", "to": "shipped", "permission": "orders.ship"}
        ]
    })
}

fn owned(name: &str, field: &str, column: &str, type_name: &str, value: bool) -> OwnedField {
    OwnedField {
        name: name.into(),
        column: Column::new(field, column, type_name),
        value,
    }
}

/// A table written as sqlgen writes one that composes Workflow on status:
/// the owned columns `status` and `status_entered_at` beside the own
/// `title`, and `_version`; its key a string.
pub fn order_table(type_name: &str, table: &str) -> Table {
    let mut entered = owned(
        "enteredAt",
        "statusEnteredAt",
        "status_entered_at",
        "Temporal.DateTime",
        false,
    );
    entered.column = entered.column.converted(Convert::EpochMillis);
    Table {
        type_name: type_name.into(),
        table: table.into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: vec![Column::new("title", "title", "string")],
        uses: vec![Use {
            id: "status".into(),
            behavior: "Workflow".into(),
            anchor: Some("status".into()),
            fields: vec![owned("status", "status", "status", "string", true), entered],
            operations: vec![Operation {
                name: "transition".into(),
                writes: true,
            }],
        }],
        spec: TypeSpec::new(type_name).with(UseSpec::on_field("status", "Workflow", flow())),
    }
}

/// A room key no other layout's toy locks: `Room/r1/<pid>/<nanos>/<n>`. An
/// advisory lock is the whole database's, not a schema's, so a test that
/// checks a lock is free after its transaction would fail while another
/// test, in this process or another on the same database, held a key of
/// one name.
pub fn room() -> String {
    static ROOMS: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or(0);
    format!(
        "Room/r1/{}/{nanos}/{}",
        std::process::id(),
        ROOMS.fetch_add(1, std::sync::atomic::Ordering::SeqCst)
    )
}

/// Order, Ticket (soft-deletable, its live rows NULL in `deleted_at`), Item
/// (Order's shape with a UUID key) and Booking (no behavior of Workflow's,
/// the toy's rule across rows on its create), its room a [`room`] of its
/// own.
pub fn store_layout() -> Layout {
    store_layout_in(&room())
}

/// [`store_layout`] with Booking's create locking `room`.
pub fn store_layout_in(room: &str) -> Layout {
    let ticket = Table {
        live: Some(superschematic_rules_adapter::Live {
            column: "deleted_at".into(),
        }),
        ..order_table("Ticket", "ticket")
    };
    let item = Table {
        key: Column::new("id", "id", "Identity.UUID"),
        ..order_table("Item", "item")
    };
    let booking = Table {
        type_name: "Booking".into(),
        table: "booking".into(),
        key: Column::new("id", "id", "string"),
        version: "_version".into(),
        live: None,
        own: Vec::new(),
        uses: Vec::new(),
        spec: TypeSpec::new("Booking").with(UseSpec::on_type("OneRoom", json!({"room": room}))),
    };
    let loud = Table {
        type_name: "Loud".into(),
        table: "loud".into(),
        spec: TypeSpec::new("Loud").with(UseSpec::on_type("OneRoom", json!({"emit": true}))),
        ..booking.clone()
    };
    let timed = Table {
        type_name: "Timed".into(),
        table: "timed".into(),
        spec: TypeSpec::new("Timed").with(UseSpec::on_type("OneRoom", json!({"arm": true}))),
        ..booking.clone()
    };
    Layout::new([
        order_table("Order", "order"),
        ticket,
        item,
        booking,
        loud,
        timed,
    ])
}

/// The tables of [`store_layout`] in a dialect, each with its trigger.
pub fn store_ddl(postgres: bool) -> Vec<String> {
    let t = column_types(postgres);
    let mut ddl = Vec::new();
    for (table, extra) in [
        ("order", ""),
        ("ticket", ", deleted_at TIMESTAMPTZ"),
        ("item", ""),
    ] {
        let extra = if postgres {
            extra.to_owned()
        } else {
            extra.replace("TIMESTAMPTZ", t.timestamp)
        };
        ddl.push(format!(
            "CREATE TABLE \"{table}\" (id {}, title {} NOT NULL, status {}, status_entered_at {}{extra}, _version {})",
            t.key, t.text, t.text, t.timestamp, t.version
        ));
    }
    for table in ["booking", "loud", "timed"] {
        ddl.push(format!(
            "CREATE TABLE \"{table}\" (id {}, _version {})",
            t.key, t.version
        ));
    }
    for table in ["order", "ticket", "item", "booking", "loud", "timed"] {
        if postgres {
            ddl.extend(postgres_bump(table));
        } else {
            ddl.push(sqlite_bump(table));
        }
    }
    ddl
}

/// A behavior of the tests' own whose create is a rule across rows, as two
/// bookings of one room are: it takes the lock its config's `room` names
/// (`Room/r1` when it names none), reads under it whether its type has
/// rows, and lets its row be created only when there is none, refusing
/// `conflict` otherwise. Only the row it creates is written, so only the
/// lock holds two such commands apart. A config of `{"emit": true}` emits
/// an event on a create instead, and one of `{"arm": true}` arms a
/// deadline: writes the adapter does not take yet.
pub struct OneRoom;

impl Behavior for OneRoom {
    type Config = Value;

    fn declaration(&self) -> Declaration {
        Declaration::new("OneRoom")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value, ConfigError> {
        Ok(raw.clone())
    }

    fn initialize(&self, cx: &Cx<'_, Value>, _params: &Value) -> Hook<()> {
        if cx.config().get("emit") == Some(&Value::Bool(true)) {
            return cx.emit("created", json!({}));
        }
        if cx.config().get("arm") == Some(&Value::Bool(true)) {
            return cx.arm("expire", cx.now() + 1000, Value::Null);
        }
        let room = cx
            .config()
            .get("room")
            .and_then(Value::as_str)
            .unwrap_or("Room/r1");
        cx.lock(room)?;
        if cx.has_rows(cx.type_name())? {
            return Err(
                Refusal::new(superschematic_rules::Code::Conflict, "room r1 is booked").into(),
            );
        }
        Ok(())
    }
}

/// Workflow, and the toy.
pub fn core() -> superschematic_rules::Core {
    superschematic_rules_behaviors::core().with(OneRoom)
}

#[cfg(feature = "sqlite")]
pub fn sqlite_params(values: &[Param]) -> Vec<rusqlite::types::Value> {
    values
        .iter()
        .map(|value| match value {
            Param::Null => rusqlite::types::Value::Null,
            Param::Text(text) => rusqlite::types::Value::Text(text.clone()),
            Param::Int(n) => rusqlite::types::Value::Integer(*n),
        })
        .collect()
}

/// A SQLite file of a test's own under the temporary directory, removed
/// with its WAL files when dropped.
#[cfg(feature = "sqlite")]
pub struct SqliteFile(std::path::PathBuf);

#[cfg(feature = "sqlite")]
impl SqliteFile {
    pub fn new() -> Self {
        static FILES: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|since| since.as_nanos())
            .unwrap_or(0);
        let n = FILES.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        SqliteFile(
            std::env::temp_dir().join(format!("rules_rs_{}_{nanos}_{n}.db", std::process::id())),
        )
    }

    /// A connection to the file that waits up to ten seconds for a lock.
    pub fn open(&self) -> rusqlite::Connection {
        let conn = rusqlite::Connection::open(&self.0).expect("SQLite opens");
        conn.busy_timeout(std::time::Duration::from_secs(10))
            .expect("a busy timeout");
        conn
    }
}

#[cfg(feature = "sqlite")]
impl Drop for SqliteFile {
    fn drop(&mut self) {
        for suffix in ["", "-wal", "-shm"] {
            let _ = std::fs::remove_file(format!("{}{suffix}", self.0.display()));
        }
    }
}

#[cfg(feature = "postgres")]
pub mod pg {
    //! A Postgres schema of a test's own, and parameters sent as text.

    use std::future::Future;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use bytes::BytesMut;
    use tokio_postgres::types::{to_sql_checked, Format, IsNull, ToSql, Type};
    use tokio_postgres::{Client, NoTls};

    use super::Param;

    /// The database the variable names, or `None`, with the line that says
    /// so, when it is unset.
    pub fn dsn(skipping: &str) -> Option<String> {
        match std::env::var(super::RULES_DATABASE_VARIABLE) {
            Ok(url) if !url.is_empty() => Some(url),
            _ => {
                println!("{skipping}");
                None
            }
        }
    }

    /// A connection, its search path the schema's when one is named.
    pub async fn connect(url: &str, schema: Option<&str>) -> Client {
        let (client, connection) = tokio_postgres::connect(url, NoTls)
            .await
            .expect("Postgres connects");
        tokio::spawn(async move {
            let _ = connection.await;
        });
        if let Some(schema) = schema {
            client
                .batch_execute(&format!("SET search_path TO \"{schema}\""))
                .await
                .expect("the search path is set");
        }
        client
    }

    /// A schema of a test's own on the database.
    #[derive(Clone, Debug)]
    pub struct Schema {
        pub url: String,
        pub name: String,
    }

    impl Schema {
        /// A connection whose search path is the schema.
        pub async fn client(&self) -> Client {
            connect(&self.url, Some(&self.name)).await
        }
    }

    static SCHEMAS: AtomicUsize = AtomicUsize::new(0);

    /// Runs body on a schema of its own, `rules_rs_<pid>_<nanos>_<n>`,
    /// created first and dropped after, whether body passes or panics.
    pub async fn on_schema<F, Fut>(url: String, body: F)
    where
        F: FnOnce(Schema) -> Fut,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|since| since.as_nanos())
            .unwrap_or(0);
        let name = format!(
            "rules_rs_{}_{nanos}_{}",
            std::process::id(),
            SCHEMAS.fetch_add(1, Ordering::SeqCst)
        );
        let admin = connect(&url, None).await;
        admin
            .batch_execute(&format!("CREATE SCHEMA \"{name}\""))
            .await
            .expect("the schema is created");
        let outcome = tokio::spawn(body(Schema {
            url,
            name: name.clone(),
        }))
        .await;
        admin
            .batch_execute(&format!("DROP SCHEMA \"{name}\" CASCADE"))
            .await
            .expect("the schema is dropped");
        if let Err(error) = outcome {
            if error.is_panic() {
                std::panic::resume_unwind(error.into_panic());
            }
            panic!("the test's task did not finish: {error}");
        }
    }

    /// A parameter sent as text, which Postgres reads into the type it
    /// meets, or NULL.
    #[derive(Debug)]
    pub struct Text(Option<String>);

    impl ToSql for Text {
        fn to_sql(
            &self,
            _ty: &Type,
            out: &mut BytesMut,
        ) -> Result<IsNull, Box<dyn std::error::Error + Sync + Send>> {
            match &self.0 {
                Some(text) => {
                    out.extend_from_slice(text.as_bytes());
                    Ok(IsNull::No)
                }
                None => Ok(IsNull::Yes),
            }
        }

        fn accepts(_ty: &Type) -> bool {
            true
        }

        fn encode_format(&self, _ty: &Type) -> Format {
            Format::Text
        }

        to_sql_checked!();
    }

    pub fn texts(values: &[Param]) -> Vec<Text> {
        values
            .iter()
            .map(|value| {
                Text(match value {
                    Param::Null => None,
                    Param::Text(text) => Some(text.clone()),
                    Param::Int(n) => Some(n.to_string()),
                })
            })
            .collect()
    }

    pub fn refs(values: &[Text]) -> Vec<&(dyn ToSql + Sync)> {
        values
            .iter()
            .map(|value| value as &(dyn ToSql + Sync))
            .collect()
    }
}
