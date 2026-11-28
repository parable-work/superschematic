//! Development mode's store in a Postgres schema (D117): the same fixed
//! tables as SQLite's, in a schema of their own, created when the store
//! opens and never after. Each request runs in one transaction that takes
//! the schema's advisory lock first, so it holds the store's one writer
//! from its start, as an IMMEDIATE transaction holds SQLite's; a unit
//! applies in a savepoint, whole or not at all. Rows and sub-table rows
//! read in their keys' order by code point (`COLLATE "C"`), as SQLite's
//! do. The statements are the layout's ([`crate::layout`]), SQLite's too.

use postgres::types::{ToSql, Type};
use postgres::Client;
use serde_json::Value;
use superschematic_engine_pg::tls_for;
use superschematic_rules::{Refusal, SharedRow, SubRow};
use superschematic_rules_handler::{
    Declarations, DueDeadline, QueuedChange, QueuedEvent, Store, StoreAnswer, StoreRead, Unit,
};

use crate::layout::{Arg, Cell, Cells, Db, Layout};
use crate::LAYOUT_VERSION;

/// The development layout in a schema of a Postgres database.
pub struct PgStore {
    client: Client,
    schema: String,
    layout: Layout,
    open: bool,
}

fn failed(error: postgres::Error) -> Refusal {
    Refusal::internal(format!("the development store failed: {error}"))
}

/// Whether a name may be a schema's: a letter or `_`, then letters,
/// digits and `_`, at most 63 bytes.
fn is_identifier(name: &str) -> bool {
    let mut chars = name.chars();
    name.len() <= 63
        && chars
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

// pg_args is a statement's values as the postgres crate binds them.
fn pg_args<'a>(args: &'a [Arg<'_>]) -> Vec<&'a (dyn ToSql + Sync)> {
    args.iter()
        .map(|arg| match arg {
            Arg::Text(text) => text as &(dyn ToSql + Sync),
            Arg::Owned(text) => text,
            Arg::Int(n) => n,
        })
        .collect()
}

impl Db for Client {
    fn execute(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<u64, Refusal> {
        Client::execute(self, sql, &pg_args(args)).map_err(failed)
    }

    fn query(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<Vec<Cells>, Refusal> {
        let rows = Client::query(self, sql, &pg_args(args)).map_err(failed)?;
        rows.iter()
            .map(|row| {
                let cells = row
                    .columns()
                    .iter()
                    .enumerate()
                    .map(|(at, column)| {
                        // A BIGINT column, a COUNT or MAX of one, or an
                        // integer literal; TEXT otherwise.
                        match *column.type_() {
                            Type::INT8 => row
                                .try_get::<_, Option<i64>>(at)
                                .map(|n| n.map_or(Cell::Null, Cell::Int)),
                            Type::INT4 => row
                                .try_get::<_, Option<i32>>(at)
                                .map(|n| n.map_or(Cell::Null, |n| Cell::Int(n.into()))),
                            _ => row
                                .try_get::<_, Option<String>>(at)
                                .map(|text| text.map_or(Cell::Null, Cell::Text)),
                        }
                        .map_err(failed)
                    })
                    .collect::<Result<_, Refusal>>()?;
                Ok(Cells(cells))
            })
            .collect()
    }

    fn batch(&mut self, sql: &str) -> Result<(), Refusal> {
        self.batch_execute(sql).map_err(failed)
    }
}

impl PgStore {
    /// The store in a schema of the database a URL names, its tables
    /// created when they are not there, for the behaviors `declarations`
    /// declares. It connects with the TLS the URL's `sslmode` asks for
    /// ([`superschematic_engine_pg::read_ssl_mode`]).
    pub fn open(url: &str, schema: &str, declarations: &Declarations) -> Result<Self, Refusal> {
        if !is_identifier(schema) {
            return Err(Refusal::invalid_argument(format!(
                "\"{schema}\" is no schema name: a letter or _, then letters, digits and _"
            )));
        }
        let (url, tls) = tls_for(url).map_err(|error| Refusal::internal(error.to_string()))?;
        let mut client = Client::connect(&url, tls).map_err(failed)?;
        let layout = Layout::postgres(schema, declarations);
        client
            .batch_execute(&format!(
                "CREATE SCHEMA IF NOT EXISTS \"{schema}\";\n{}",
                layout.create_tables()
            ))
            .map_err(failed)?;
        client
            .execute(&layout.record_version(), &[&LAYOUT_VERSION])
            .map_err(failed)?;
        for table in layout.create_sub_tables() {
            client.batch_execute(&table).map_err(failed)?;
        }
        Ok(PgStore {
            client,
            schema: schema.to_owned(),
            layout,
            open: false,
        })
    }

    /// Deletes every row of every table of the store: a fresh store in a
    /// schema that held one.
    pub fn clear(&mut self) -> Result<(), Refusal> {
        self.client
            .batch_execute(&format!("TRUNCATE {}", self.layout.tables().join(", ")))
            .map_err(failed)
    }

    /// Drops the store's schema and everything in it.
    pub fn drop_schema(mut self) -> Result<(), Refusal> {
        let s = self.schema.clone();
        self.client
            .batch_execute(&format!("DROP SCHEMA IF EXISTS \"{s}\" CASCADE"))
            .map_err(failed)
    }

    /// A row's rows of one use's sub-table, in key order.
    pub fn sub_rows(
        &mut self,
        type_name: &str,
        id: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<SubRow>, Refusal> {
        self.layout
            .sub_rows(&mut self.client, type_name, id, use_id, table)
    }

    /// The row of a key of a use's shared table, with its version.
    pub fn shared_row(
        &mut self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Result<Option<SharedRow>, Refusal> {
        self.layout
            .shared_row(&mut self.client, type_name, use_id, table, key)
    }

    /// A row's armed deadlines: each its use, name, time and payload, by
    /// use and name.
    pub fn armed(
        &mut self,
        type_name: &str,
        id: &str,
    ) -> Result<Vec<(String, String, i64, Value)>, Refusal> {
        self.layout.armed(&mut self.client, type_name, id)
    }
}

impl Store for PgStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        self.layout.due(&mut self.client, now)
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        self.layout.queued(&mut self.client)
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        self.layout.events(&mut self.client, after)
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.client.batch_execute("BEGIN").map_err(failed)?;
        self.open = true;
        let key = format!("superschematic-dev:{}", self.schema);
        self.client
            .execute(
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
                &[&key],
            )
            .map_err(failed)?;
        Ok(())
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.open = false;
        self.client.batch_execute("COMMIT").map_err(failed)
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if !self.open {
            return Ok(());
        }
        self.open = false;
        self.client.batch_execute("ROLLBACK").map_err(failed)
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        self.layout.read(&mut self.client, read)
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        self.layout.apply(&mut self.client, unit)
    }
}
