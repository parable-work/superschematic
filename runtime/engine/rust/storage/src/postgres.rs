//! Postgres through the synchronous `postgres` crate over tokio-postgres
//! 0.7, the client the version graph's Rust engine uses (D58).
//!
//! The engine's tables live in a schema of their own (`engine` unless
//! configured), created when the storage opens unless it exists, so a user
//! without CREATE on the database opens a schema an administrator made,
//! and every connection searches that schema alone (`search_path`), so a
//! behavior's unqualified name reaches nothing else in the database.
//!
//! The storage acts as its schema's owner. When another role owns the
//! schema, one the session's user is a member of, every connection acts
//! as that role (`SET ROLE`, a startup option after the first), so what
//! the engine creates in it, a table, an index or a text search
//! configuration, is the owner's whichever process creates it, and every
//! process that opens it as a member may change it later: a migration
//! the engine runs at open, a behavior's at a publish (D58). A deploy's
//! migration job makes the schema such a role's, and grants the role to
//! every deployable that opens the engine, each as its own user.
//!
//! Connections come from a
//! small pool (`pool_max`, 4 by default), each preparing a statement once
//! per text: its `?` placeholders numbered `$1..$n`
//! ([`crate::postgres_placeholders`]), then reused.
//!
//! A value binds to the type Postgres inferred for its parameter: an
//! integer to an integer, a double or a boolean (0 or 1); a real to a
//! double, or to an integer when it is integral (3.0 binds as 3); a text
//! or a number to anything else in its text form, which the server reads
//! (NUMERIC, JSON, JSONB, a date); a blob to BYTEA alone. A column reads
//! back as one of the same values: integers, booleans (0 or 1) and NUMERIC
//! with no fraction as integers, doubles and NUMERIC with one as reals,
//! text, JSON and JSONB as text, BYTEA as a blob, `void` as null. Any
//! other column type is refused, naming the cast that reads it.
//!
//! The listener is one more connection per [`Db`], which LISTENs on the
//! engine's channel and moves the [`Notifier`]'s epoch for each commit any
//! process announces ([`crate::Tx::notify`]).

use std::collections::HashMap;
use std::error::Error as StdError;
use std::fmt::Write as _;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::Arc;
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use bytes::BytesMut;
use postgres::fallible_iterator::FallibleIterator;
use postgres::types::{to_sql_checked, Format, FromSql, IsNull, Kind, ToSql, Type};
use postgres::{Client, Config, NoTls, Statement};

use crate::conn::{Conn, Failure, Outcome, Pool, RawConn};
use crate::db::{Backend, Db, DEFAULT_BUSY_TIMEOUT};
use crate::dialect::Dialect;
use crate::error::{DbCode, Error, Result};
use crate::ident::{postgres_identifier, quote_identifier};
use crate::notifier::Notifier;
use crate::tokens::{postgres_placeholders, statement_changes_tables, statement_writes};
use crate::value::SqlValue;

/// How a Postgres storage opens.
#[derive(Clone, Debug)]
pub struct PostgresOptions {
    /// The schema the engine's tables live in, created if absent:
    /// lowercase letters, digits and `_`, starting with a letter or `_`,
    /// at most 63 bytes, not `pg_`, and not `public`, where a database's
    /// own tables are.
    pub schema: String,
    /// How long a write waits for another writer's lock (`lock_timeout`)
    /// or for a pooled connection.
    pub busy_timeout: Duration,
    /// The most connections the pool opens.
    pub pool_max: usize,
    /// Whether to keep a listener connection that moves the notifier's
    /// epoch for commits in other processes.
    pub listen: bool,
    /// How long opening a connection may take, unless the URL says
    /// (`connect_timeout`): the socket, the startup handshake and the
    /// login, end to end. A server that accepts the socket and never
    /// answers (a port a proxy forwards to nothing yet) is refused once it
    /// passes, as one that refuses the socket is at once; so is a pool's
    /// wait for a new connection.
    pub connect_timeout: Duration,
    /// How long a migration at start waits for the lock its process holds
    /// while it migrates (another process migrating first) and for the
    /// tables it changes (an open read).
    pub migration_timeout: Duration,
    /// How long the server lets one of the pool's sessions sit idle inside
    /// a transaction before it ends the session
    /// (`idle_in_transaction_session_timeout`), so a process that stops
    /// mid-transaction (frozen, paused, cut off without the network
    /// telling) holds its namespace's lock no longer than this; `None`
    /// leaves the server's own setting. The engine's transactions are
    /// busy with statements, so only code that waits between two of them
    /// (a behavior's, a host's policy) meets it, and its call fails as a
    /// lost connection.
    pub idle_in_transaction_timeout: Option<Duration>,
}

/// The schema the engine's tables live in unless configured.
pub const DEFAULT_SCHEMA: &str = "engine";

/// The most connections a Postgres storage opens unless configured.
pub const DEFAULT_POOL_MAX: usize = 4;

/// How long a session may sit idle inside a transaction unless configured.
pub const DEFAULT_IDLE_IN_TRANSACTION_TIMEOUT: Duration = Duration::from_secs(60);

impl Default for PostgresOptions {
    fn default() -> Self {
        PostgresOptions {
            schema: DEFAULT_SCHEMA.to_owned(),
            busy_timeout: DEFAULT_BUSY_TIMEOUT,
            pool_max: DEFAULT_POOL_MAX,
            listen: true,
            connect_timeout: Duration::from_secs(10),
            migration_timeout: Duration::from_secs(60),
            idle_in_transaction_timeout: Some(DEFAULT_IDLE_IN_TRANSACTION_TIMEOUT),
        }
    }
}

/// How many prepared statements a connection keeps; past it, it forgets
/// them all and prepares again.
const STATEMENT_CACHE: usize = 512;

/// How often the listener looks up from its connection to see whether the
/// storage closed.
const LISTEN_POLL: Duration = Duration::from_millis(100);

impl Db {
    /// Opens the storage on the Postgres database `url` names (a
    /// `postgres://` URL or a libpq key-value string; TLS is not yet
    /// supported, so `sslmode=disable`, or a socket the platform secures),
    /// creating its schema if absent.
    pub fn open_postgres(url: &str, options: PostgresOptions) -> Result<Db> {
        check_schema(&options.schema)?;
        if options.pool_max == 0 {
            return Err(Error::usage("pool_max must be at least 1"));
        }
        let schema = options.schema.clone();
        let mut config: Config = url.parse().map_err(pg_error)?;
        if config.get_connect_timeout().is_none() {
            config.connect_timeout(options.connect_timeout);
        }
        // The bound of a whole connect: tokio-postgres holds its
        // connect_timeout to the socket alone, so a server that takes the
        // socket and never answers the startup message would hold a
        // connect, and the pool's wait behind it, for good.
        let deadline = config
            .get_connect_timeout()
            .copied()
            .unwrap_or(options.connect_timeout);
        let listen_config = config.clone();
        // Every execution is planned for its values: the engine's reads
        // are range reads whose plan turns on them (a partial index on one
        // schema's rows, a value that names one instance), and a generic
        // plan, which Postgres switches a prepared statement to after five
        // runs, can scan the whole table in position order instead. A
        // statement is still parsed once (Postgres 12 and later).
        let mut session = format!("-c search_path={schema} -c plan_cache_mode=force_custom_plan");
        if let Some(idle) = options.idle_in_transaction_timeout {
            let _ = write!(
                session,
                " -c idle_in_transaction_session_timeout={}",
                idle.as_millis().max(1)
            );
        }
        let options_text = match config.get_options() {
            Some(existing) => format!("{existing} {session}"),
            None => session,
        };
        config.options(&options_text);
        let mut first = PgConn::connect(&config, deadline)?;
        if let Some(owner) = open_schema(&mut first.client, &schema)? {
            // The first connection takes the owner's role now; every
            // later one, the pool's and the lease's, as it starts.
            act_as(&mut first.client, &schema, &owner)?;
            config.options(&format!(
                "{options_text} -c role={}",
                startup_option_value(&owner)
            ));
        }
        let schemas = first
            .client
            .query("SELECT nspname FROM pg_catalog.pg_namespace", &[])
            .map_err(pg_error)?
            .iter()
            .map(|row| row.get::<_, String>(0).to_lowercase())
            .collect();
        let lease_config = config.clone();
        let pool = Pool::new(
            options.pool_max,
            vec![first],
            Box::new(move || PgConn::connect(&config, deadline)),
            <PgConn as RawConn>::healthy,
        );
        let notifier = Arc::new(Notifier::new());
        let channel = postgres_identifier(&format!("superschematic_engine_{schema}")).into_owned();
        let mut closers: Vec<Box<dyn FnOnce() + Send + Sync>> = Vec::new();
        if options.listen {
            closers.push(listen(
                listen_config,
                deadline,
                channel.clone(),
                Arc::clone(&notifier),
            ));
        }
        Ok(Db::new(
            Box::new(PostgresBackend {
                label: format!("Postgres schema {schema}"),
                schema,
                schemas,
                channel,
                pool,
                lease_config,
                deadline,
            }),
            options.busy_timeout,
            options.migration_timeout,
            notifier,
            closers,
        ))
    }
}

fn check_schema(schema: &str) -> Result<()> {
    let bytes = schema.as_bytes();
    let valid = !bytes.is_empty()
        && bytes.len() <= 63
        && (bytes[0].is_ascii_lowercase() || bytes[0] == b'_')
        && bytes
            .iter()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || *b == b'_')
        && !schema.starts_with("pg_");
    if !valid {
        return Err(Error::usage(format!(
            "the engine's Postgres schema {schema:?} is lowercase letters, digits and _, starting with a letter or _, at most 63 bytes, and not pg_"
        )));
    }
    // The engine's tables live in a schema of their own (D58): the one a
    // database's own tables are in would put them in a behavior's reach.
    if schema == "public" || schema == "information_schema" {
        return Err(Error::usage(format!(
            "the engine's Postgres schema is one of its own, not {schema}"
        )));
    }
    Ok(())
}

// open_schema creates the engine's schema unless it exists, and returns
// the role the storage acts as in it: its owner, when that is another
// role the session's user is a member of, else none, the user itself.
// Postgres checks CREATE on the database before it reads IF NOT EXISTS,
// so a schema that exists is looked up first, and a user without that
// privilege opens a schema an administrator made for it.
fn open_schema(client: &mut Client, schema: &str) -> Result<Option<String>> {
    let owner = |client: &mut Client| -> Result<Option<(String, bool, bool)>> {
        let row = client
            .query_opt(
                "SELECT r.rolname::text, r.rolname = current_user, pg_catalog.pg_has_role(n.nspowner, 'MEMBER')
                 FROM pg_catalog.pg_namespace AS n
                 JOIN pg_catalog.pg_roles AS r ON r.oid = n.nspowner
                 WHERE n.nspname = $1",
                &[&schema],
            )
            .map_err(pg_error)?;
        Ok(row.map(|row| (row.get(0), row.get(1), row.get(2))))
    };
    let found = match owner(client)? {
        Some(found) => found,
        None => {
            create_schema(client, schema)?;
            owner(client)?.ok_or_else(|| Error::Usage {
                message: format!(
                    "the engine's Postgres schema {schema} is missing just after it was created"
                ),
            })?
        }
    };
    Ok(match found {
        (role, false, true) => Some(role),
        _ => None,
    })
}

// create_schema creates the engine's schema unless it exists. Two
// processes that create it at once can collide on the catalog's unique
// index, which leaves it created.
fn create_schema(client: &mut Client, schema: &str) -> Result<()> {
    match client.batch_execute(&format!("CREATE SCHEMA IF NOT EXISTS {schema}")) {
        Ok(()) => Ok(()),
        Err(error) => match error.code().map(|code| code.code()) {
            Some("23505" | "42P06") => Ok(()),
            _ => Err(pg_error(error)),
        },
    }
}

// act_as makes the connection act as owner, the schema's, a role its user
// is a member of. One whose membership does not let it SET ROLE, since
// Postgres 16 a grant WITH SET FALSE, is refused, saying so.
fn act_as(client: &mut Client, schema: &str, owner: &str) -> Result<()> {
    client
        .batch_execute(&format!("SET ROLE {}", quote_identifier(owner)))
        .map_err(|error| match pg_error(error) {
            Error::Database { code, message } => Error::Database {
                code,
                message: format!(
                    "the engine acts as the owner of its schema {schema}, {owner}, and cannot: {message}; grant {owner} to the engine's user WITH SET TRUE"
                ),
            },
            other => other,
        })
}

// startup_option_value is value as a startup option's `-c name=value`
// takes it: a backslash before each space and backslash.
fn startup_option_value(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for c in value.chars() {
        if c == ' ' || c == '\\' {
            out.push('\\');
        }
        out.push(c);
    }
    out
}

struct PostgresBackend {
    label: String,
    schema: String,
    schemas: Vec<String>,
    channel: String,
    pool: Pool<PgConn>,
    /// What a lease's own connection opens with, and how long it may take.
    lease_config: Config,
    deadline: Duration,
}

// A lease's session: the connection that holds the advisory lock.
struct PgLease {
    client: Client,
    key: i64,
}

impl crate::db::LeaseHolder for PgLease {
    fn held(&mut self) -> bool {
        !self.client.is_closed() && self.client.is_valid(LEASE_CHECK).is_ok()
    }
}

impl Drop for PgLease {
    fn drop(&mut self) {
        // Closing the session lets go of the lock in any case; the unlock
        // lets go of it at once on a connection that closes slowly.
        let _ = self
            .client
            .batch_execute(&format!("SELECT pg_advisory_unlock({})", self.key));
    }
}

/// How long a lease's check of its connection waits for an answer.
const LEASE_CHECK: Duration = Duration::from_secs(5);

impl Backend for PostgresBackend {
    fn dialect(&self) -> Dialect {
        Dialect::Postgres
    }

    fn acquire(&self, timeout: Duration) -> Result<Box<dyn Conn + '_>> {
        Ok(Box::new(self.pool.get(timeout)?))
    }

    fn label(&self) -> &str {
        &self.label
    }

    fn postgres_schema(&self) -> Option<&str> {
        Some(&self.schema)
    }

    fn schemas(&self) -> &[String] {
        &self.schemas
    }

    fn channel(&self) -> Option<&str> {
        Some(&self.channel)
    }

    fn stale_retries(&self) -> usize {
        self.pool.max()
    }

    fn try_lease(&self, key: i64) -> Result<Option<crate::db::Lease>> {
        let mut client = connect_within(&self.lease_config, self.deadline)?;
        let taken: bool = client
            .query_one("SELECT pg_try_advisory_lock($1)", &[&key])
            .map_err(pg_error)?
            .get(0);
        Ok(taken.then(|| crate::db::Lease::of(Box::new(PgLease { client, key }))))
    }
}

#[derive(Clone)]
struct Prepared {
    statement: Statement,
    columns: Arc<[String]>,
    /// Whether it changes rows, so the rows Postgres reports count.
    writes: bool,
    /// Whether it changes tables (CREATE, ALTER, DROP), after which the
    /// connection prepares every statement afresh.
    ddl: bool,
}

struct PgConn {
    client: Client,
    statements: HashMap<String, Prepared>,
}

impl PgConn {
    fn connect(config: &Config, deadline: Duration) -> Result<PgConn> {
        let client = connect_within(config, deadline)?;
        Ok(PgConn {
            client,
            statements: HashMap::new(),
        })
    }

    fn prepare(&mut self, sql: &str) -> Result<Prepared, Failure> {
        if let Some(prepared) = self.statements.get(sql) {
            return Ok(prepared.clone());
        }
        let text = postgres_placeholders(sql).map_err(Failure::refused)?;
        let statement = self
            .client
            .prepare(&text)
            .map_err(|error| Failure::reached(pg_error(error)))?;
        let prepared = Prepared {
            columns: statement
                .columns()
                .iter()
                .map(|column| column.name().to_owned())
                .collect(),
            writes: statement_writes(&text, Dialect::Postgres),
            ddl: statement_changes_tables(&text, Dialect::Postgres),
            statement,
        };
        if prepared.ddl {
            return Ok(prepared);
        }
        if self.statements.len() >= STATEMENT_CACHE {
            self.statements.clear();
        }
        self.statements.insert(sql.to_owned(), prepared.clone());
        Ok(prepared)
    }
}

impl RawConn for PgConn {
    fn control(&mut self, sql: &str) -> Result<()> {
        self.client.batch_execute(sql).map_err(pg_error)
    }

    fn batch(&mut self, sql: &str) -> Result<()> {
        // DDL can change what a prepared statement returns (SELECT * after
        // ADD COLUMN), which Postgres refuses to replan; prepare afresh.
        self.statements.clear();
        self.client.batch_execute(sql).map_err(pg_error)
    }

    fn statement(
        &mut self,
        sql: &str,
        params: &[SqlValue],
        _read_only: bool,
    ) -> Result<Outcome, Failure> {
        let prepared = self.prepare(sql)?;
        let types = prepared.statement.params();
        if types.len() != params.len() {
            return Err(Failure::refused(Error::usage(format!(
                "the statement binds {} parameters and was given {}: {}",
                types.len(),
                params.len(),
                sql.trim()
            ))));
        }
        let bound = params
            .iter()
            .zip(types)
            .enumerate()
            .map(|(index, (value, ty))| PgParam::new(index, value, ty))
            .collect::<Result<Vec<_>>>()
            .map_err(Failure::refused)?;
        let width = prepared.columns.len();
        let mut rows = Vec::new();
        let ran = self.read_rows(&prepared, &bound, width, &mut rows);
        if prepared.ddl {
            // A table changed: what a statement prepared before returns
            // may have too (SELECT * after ADD COLUMN).
            self.statements.clear();
        }
        let affected = match ran {
            Ok(affected) => affected,
            Err(failure) => {
                // Postgres refuses to replan a statement whose result
                // changed shape (0A000); prepare it afresh next time.
                if failure.error.sqlstate() == Some("0A000") {
                    self.statements.remove(sql);
                }
                return Err(failure);
            }
        };
        Ok(Outcome {
            columns: prepared.columns,
            rows,
            changes: if prepared.writes { affected } else { 0 },
            last_insert_rowid: None,
        })
    }

    fn healthy(&self) -> bool {
        !self.client.is_closed()
    }
}

impl PgConn {
    // read_rows runs a prepared statement and reads every row into rows,
    // returning the rows Postgres reports it affected.
    fn read_rows(
        &mut self,
        prepared: &Prepared,
        bound: &[PgParam],
        width: usize,
        rows: &mut Vec<Vec<SqlValue>>,
    ) -> Result<u64, Failure> {
        {
            let mut iter = self
                .client
                .query_raw(&prepared.statement, bound.iter())
                .map_err(|error| Failure::reached(pg_error(error)))?;
            loop {
                let row = match iter.next() {
                    Ok(Some(row)) => row,
                    Ok(None) => break,
                    Err(error) => return Err(Failure::reached(pg_error(error))),
                };
                let mut values = Vec::with_capacity(width);
                for index in 0..width {
                    let value: PgValue = row
                        .try_get(index)
                        .map_err(|error| Failure::reached(read_error(error)))?;
                    values.push(value.0);
                }
                rows.push(values);
            }
            Ok(iter.rows_affected().unwrap_or(0))
        }
    }
}

/// A value encoded for the type Postgres inferred for its parameter.
#[derive(Debug)]
enum Encoded {
    Null,
    Binary(Vec<u8>),
    /// The value's text form, which the server reads with the type's input
    /// function.
    Text(String),
}

#[derive(Debug)]
struct PgParam(Encoded);

impl PgParam {
    fn new(index: usize, value: &SqlValue, ty: &Type) -> Result<PgParam> {
        encode(value, ty)
            .map(PgParam)
            .map_err(|reason| Error::Value {
                message: format!(
                    "SQL parameter {} is {} ({value}), which Postgres types {}: {reason}",
                    index + 1,
                    value.kind(),
                    ty.name()
                ),
            })
    }
}

fn encode(value: &SqlValue, ty: &Type) -> Result<Encoded, &'static str> {
    let binary = |bytes: &[u8]| Ok(Encoded::Binary(bytes.to_vec()));
    match value {
        SqlValue::Null => Ok(Encoded::Null),
        SqlValue::Integer(n) => encode_integer(*n, ty),
        SqlValue::Real(x) => match *ty {
            Type::INT2 | Type::INT4 | Type::INT8 | Type::OID => {
                // An integral real binds as its integer (JSON's 3.0 is 3).
                if x.fract() != 0.0 || x.abs() >= 9.2e18 {
                    return Err("a real with a fraction or past 2^63 binds to no integer");
                }
                encode_integer(*x as i64, ty)
            }
            Type::FLOAT4 => binary(&(*x as f32).to_be_bytes()),
            Type::FLOAT8 => binary(&x.to_be_bytes()),
            Type::BOOL | Type::BYTEA => Err("bind an integer 0 or 1 to a boolean, a blob to bytea"),
            _ => Ok(Encoded::Text(format!("{x:?}"))),
        },
        SqlValue::Text(text) => match *ty {
            Type::BYTEA => Err("bind a blob to bytea"),
            _ => Ok(Encoded::Text(text.clone())),
        },
        SqlValue::Blob(bytes) => match *ty {
            Type::BYTEA => binary(bytes),
            _ => Err("a blob binds to bytea alone"),
        },
    }
}

fn encode_integer(n: i64, ty: &Type) -> Result<Encoded, &'static str> {
    let binary = |bytes: &[u8]| Ok(Encoded::Binary(bytes.to_vec()));
    const RANGE: &str = "it is out of the type's range";
    match *ty {
        Type::BOOL => match n {
            0 | 1 => binary(&[n as u8]),
            _ => Err("a boolean binds 0 or 1"),
        },
        Type::INT2 => binary(&i16::try_from(n).map_err(|_| RANGE)?.to_be_bytes()),
        Type::INT4 => binary(&i32::try_from(n).map_err(|_| RANGE)?.to_be_bytes()),
        Type::INT8 => binary(&n.to_be_bytes()),
        Type::OID => binary(&u32::try_from(n).map_err(|_| RANGE)?.to_be_bytes()),
        Type::FLOAT4 => binary(&(n as f32).to_be_bytes()),
        Type::FLOAT8 => binary(&(n as f64).to_be_bytes()),
        Type::BYTEA => Err("bind a blob to bytea"),
        _ => Ok(Encoded::Text(n.to_string())),
    }
}

impl ToSql for PgParam {
    fn to_sql(
        &self,
        _ty: &Type,
        out: &mut BytesMut,
    ) -> Result<IsNull, Box<dyn StdError + Sync + Send>> {
        match &self.0 {
            Encoded::Null => return Ok(IsNull::Yes),
            Encoded::Binary(bytes) => out.extend_from_slice(bytes),
            Encoded::Text(text) => out.extend_from_slice(text.as_bytes()),
        }
        Ok(IsNull::No)
    }

    fn accepts(_ty: &Type) -> bool {
        true
    }

    fn encode_format(&self, _ty: &Type) -> Format {
        match self.0 {
            Encoded::Text(_) => Format::Text,
            _ => Format::Binary,
        }
    }

    to_sql_checked!();
}

/// A column's value, read whatever its type.
struct PgValue(SqlValue);

impl<'a> FromSql<'a> for PgValue {
    fn from_sql(ty: &Type, raw: &'a [u8]) -> Result<Self, Box<dyn StdError + Sync + Send>> {
        decode(ty, raw).map(PgValue).map_err(Into::into)
    }

    fn from_sql_null(_ty: &Type) -> Result<Self, Box<dyn StdError + Sync + Send>> {
        Ok(PgValue(SqlValue::Null))
    }

    fn accepts(_ty: &Type) -> bool {
        true
    }
}

fn decode(ty: &Type, raw: &[u8]) -> Result<SqlValue, String> {
    let fixed = |width: usize| -> Result<&[u8], String> {
        if raw.len() == width {
            Ok(raw)
        } else {
            Err(format!("a {} value of {} bytes", ty.name(), raw.len()))
        }
    };
    let text = |bytes: &[u8]| SqlValue::Text(String::from_utf8_lossy(bytes).into_owned());
    Ok(match *ty {
        Type::BOOL => SqlValue::Integer(i64::from(fixed(1)?[0] != 0)),
        Type::INT2 => SqlValue::Integer(i64::from(i16::from_be_bytes(array(fixed(2)?)))),
        Type::INT4 => SqlValue::Integer(i64::from(i32::from_be_bytes(array(fixed(4)?)))),
        Type::INT8 => SqlValue::Integer(i64::from_be_bytes(array(fixed(8)?))),
        Type::OID => SqlValue::Integer(i64::from(u32::from_be_bytes(array(fixed(4)?)))),
        Type::FLOAT4 => SqlValue::Real(f64::from(f32::from_be_bytes(array(fixed(4)?)))),
        Type::FLOAT8 => SqlValue::Real(f64::from_be_bytes(array(fixed(8)?))),
        Type::NUMERIC => numeric(raw)?,
        Type::TEXT | Type::VARCHAR | Type::BPCHAR | Type::NAME | Type::UNKNOWN | Type::JSON | Type::CHAR => {
            text(raw)
        }
        Type::JSONB => match raw.split_first() {
            Some((1, json)) => text(json),
            _ => return Err("a jsonb value of an unknown version".to_owned()),
        },
        Type::BYTEA => SqlValue::Blob(raw.to_vec()),
        Type::UUID => SqlValue::Text(uuid(fixed(16)?)),
        Type::VOID => SqlValue::Null,
        _ if matches!(ty.kind(), Kind::Enum(_)) => text(raw),
        _ => {
            return Err(format!(
                "the storage reads no column of type {}; cast it in the statement (::text, ::bigint, ::double precision)",
                ty.name()
            ))
        }
    })
}

fn array<const N: usize>(bytes: &[u8]) -> [u8; N] {
    let mut out = [0_u8; N];
    out.copy_from_slice(bytes);
    out
}

fn uuid(bytes: &[u8]) -> String {
    let hex = crate::ident::hex(bytes);
    format!(
        "{}-{}-{}-{}-{}",
        &hex[..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..]
    )
}

// numeric reads Postgres's binary NUMERIC: an integer when it has no
// fraction digits and fits, a real otherwise (SUM of integers is NUMERIC,
// as SQLite's is an integer; AVG has a fraction, as SQLite's is a real).
fn numeric(raw: &[u8]) -> Result<SqlValue, String> {
    let word = |index: usize| -> Result<u16, String> {
        raw.get(index * 2..index * 2 + 2)
            .map(|bytes| u16::from_be_bytes([bytes[0], bytes[1]]))
            .ok_or_else(|| "a truncated numeric value".to_owned())
    };
    let digits = usize::from(word(0)?);
    let weight = i64::from(word(1)? as i16);
    let sign = word(2)?;
    let scale = usize::from(word(3)?);
    let digit = |k: i64| -> Result<u16, String> {
        if k < 0 || k >= digits as i64 {
            Ok(0)
        } else {
            word(4 + k as usize)
        }
    };
    match sign {
        0xC000 => return Ok(SqlValue::Real(f64::NAN)),
        0xD000 => return Ok(SqlValue::Real(f64::INFINITY)),
        0xF000 => return Ok(SqlValue::Real(f64::NEG_INFINITY)),
        0x0000 | 0x4000 => {}
        _ => return Err("a numeric value of an unknown sign".to_owned()),
    }
    let mut out = String::new();
    if sign == 0x4000 {
        out.push('-');
    }
    if weight < 0 {
        out.push('0');
    } else {
        for k in 0..=weight {
            let d = digit(k)?;
            let _ = if k == 0 {
                write!(out, "{d}")
            } else {
                write!(out, "{d:04}")
            };
        }
    }
    if scale == 0 {
        return Ok(out.parse::<i64>().map_or_else(
            |_| SqlValue::Real(out.parse().unwrap_or(f64::NAN)),
            SqlValue::Integer,
        ));
    }
    let mut fraction = String::new();
    let mut k = weight + 1;
    while fraction.len() < scale {
        let _ = write!(fraction, "{:04}", digit(k)?);
        k += 1;
    }
    fraction.truncate(scale);
    out.push('.');
    out.push_str(&fraction);
    out.parse()
        .map(SqlValue::Real)
        .map_err(|_| format!("a numeric value {out} that is no number"))
}

/// A Postgres failure as the storage's error.
/// Opens a connection, refusing one that has not opened within `deadline`
/// as a connection failure. The connect runs on a thread of its own, since
/// the synchronous client cannot be cancelled: past the deadline it is
/// left to finish or fail, and a connection it opens then is closed as its
/// result is dropped.
fn connect_within(config: &Config, deadline: Duration) -> Result<Client> {
    let (sender, receiver) = mpsc::sync_channel(1);
    let connecting = config.clone();
    thread::Builder::new()
        .name("engine-storage-connect".to_owned())
        .spawn(move || {
            let _ = sender.send(connecting.connect(NoTls));
        })
        .map_err(|error| Error::Connection {
            message: format!("could not start a thread to connect to Postgres: {error}"),
        })?;
    match receiver.recv_timeout(deadline) {
        Ok(connected) => connected.map_err(pg_error),
        Err(RecvTimeoutError::Timeout) => Err(Error::Connection {
            message: format!(
                "connecting to Postgres took longer than the connect timeout ({} ms)",
                deadline.as_millis()
            ),
        }),
        Err(RecvTimeoutError::Disconnected) => Err(Error::Connection {
            message: "the thread connecting to Postgres ended without a connection".to_owned(),
        }),
    }
}

fn pg_error(error: postgres::Error) -> Error {
    if let Some(db) = error.as_db_error() {
        let state = db.code().code().to_owned();
        let message = db.message().to_owned();
        return match state.as_str() {
            // lock_not_available (a lock_timeout), deadlock_detected,
            // serialization_failure: another writer held what this one
            // needed; query_canceled: the server's statement_timeout, most
            // often a wait for a lock too, or an administrator's cancel.
            "55P03" | "40P01" | "40001" | "57014" => Error::Busy {
                code: Some(DbCode::Postgres(state)),
                message,
            },
            // The server ended the session: connection_exception (class
            // 08), admin_shutdown (pg_terminate_backend, a smart or fast
            // shutdown), crash_shutdown, cannot_connect_now (starting or
            // in recovery), idle_session_timeout and
            // idle_in_transaction_session_timeout. Its transaction is
            // gone with it.
            code if code.starts_with("08")
                || matches!(code, "57P01" | "57P02" | "57P03" | "57P05" | "25P03") =>
            {
                Error::Connection {
                    message: format!("{message} (SQLSTATE {state})"),
                }
            }
            "23505" => Error::Unique {
                index: db.constraint().map(str::to_owned),
                code: DbCode::Postgres(state),
                message,
            },
            _ => Error::Database {
                code: DbCode::Postgres(state),
                message,
            },
        };
    }
    Error::Connection {
        message: error.to_string(),
    }
}

// read_error is a column the storage could not read, its reason in the
// error's source.
fn read_error(error: postgres::Error) -> Error {
    let reason = error
        .source()
        .map_or_else(|| error.to_string(), ToString::to_string);
    Error::Value { message: reason }
}

// listen starts the listener thread and returns what stops it.
fn listen(
    config: Config,
    deadline: Duration,
    channel: String,
    notifier: Arc<Notifier>,
) -> Box<dyn FnOnce() + Send + Sync> {
    let stop = Arc::new(AtomicBool::new(false));
    let thread: JoinHandle<()> = {
        let stop = Arc::clone(&stop);
        thread::Builder::new()
            .name("engine-storage-listen".to_owned())
            .spawn(move || listen_loop(&config, deadline, &channel, &notifier, &stop))
            .expect("start the storage's listener thread")
    };
    Box::new(move || {
        stop.store(true, Ordering::SeqCst);
        let _ = thread.join();
    })
}

fn listen_loop(
    config: &Config,
    deadline: Duration,
    channel: &str,
    notifier: &Notifier,
    stop: &AtomicBool,
) {
    let mut backoff = Duration::from_millis(50);
    while !stop.load(Ordering::SeqCst) {
        let mut client = match connect_within(config, deadline) {
            Ok(client) => client,
            Err(_) => {
                pause(backoff, stop);
                backoff = (backoff * 2).min(Duration::from_secs(5));
                continue;
            }
        };
        if client
            .batch_execute(&format!("LISTEN {}", quote_identifier(channel)))
            .is_err()
        {
            pause(backoff, stop);
            continue;
        }
        backoff = Duration::from_millis(50);
        notifier.set_listening(true);
        // A notification may have gone by while no listener was connected.
        notifier.bump();
        while !stop.load(Ordering::SeqCst) {
            let next = client.notifications().timeout_iter(LISTEN_POLL).next();
            match next {
                Ok(Some(_)) => {
                    notifier.bump();
                }
                Ok(None) if client.is_closed() => break,
                Ok(None) => {}
                Err(_) => break,
            }
        }
        notifier.set_listening(false);
    }
}

// pause sleeps for up to duration, waking early when the storage closes.
fn pause(duration: Duration, stop: &AtomicBool) {
    let deadline = Instant::now() + duration;
    while !stop.load(Ordering::SeqCst) {
        let now = Instant::now();
        if now >= deadline {
            return;
        }
        thread::sleep((deadline - now).min(LISTEN_POLL));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn values_bind_to_the_inferred_type() {
        let bound = |value: SqlValue, ty: &Type| encode(&value, ty);
        assert!(
            matches!(bound(SqlValue::Real(3.0), &Type::INT8), Ok(Encoded::Binary(b)) if b == 3_i64.to_be_bytes())
        );
        assert!(bound(SqlValue::Real(3.5), &Type::INT8).is_err());
        assert!(
            matches!(bound(SqlValue::Integer(1), &Type::BOOL), Ok(Encoded::Binary(b)) if b == [1])
        );
        assert!(bound(SqlValue::Integer(2), &Type::BOOL).is_err());
        assert!(bound(SqlValue::Integer(70_000), &Type::INT2).is_err());
        assert!(
            matches!(bound(SqlValue::Integer(7), &Type::NUMERIC), Ok(Encoded::Text(t)) if t == "7")
        );
        assert!(
            matches!(bound(SqlValue::Text("7".into()), &Type::INT8), Ok(Encoded::Text(t)) if t == "7")
        );
        assert!(matches!(
            bound(SqlValue::Text("{}".into()), &Type::JSONB),
            Ok(Encoded::Text(_))
        ));
        assert!(bound(SqlValue::Text("x".into()), &Type::BYTEA).is_err());
        assert!(bound(SqlValue::Blob(vec![1]), &Type::TEXT).is_err());
        assert!(matches!(
            bound(SqlValue::Null, &Type::INT8),
            Ok(Encoded::Null)
        ));
    }

    #[test]
    fn numerics_read_as_integers_or_reals() {
        // 12.5: one base-10000 digit before the point, one after.
        let raw = [0, 2, 0, 0, 0, 0, 0, 1, 0, 12, 0x13, 0x88];
        assert_eq!(numeric(&raw).unwrap(), SqlValue::Real(12.5));
        // 10000: one digit at weight 1, no fraction.
        assert_eq!(
            numeric(&[0, 1, 0, 1, 0, 0, 0, 0, 0, 1]).unwrap(),
            SqlValue::Integer(10_000)
        );
        // -0.0001.
        assert_eq!(
            numeric(&[0, 1, 0xff, 0xff, 0x40, 0, 0, 4, 0, 1]).unwrap(),
            SqlValue::Real(-0.0001)
        );
        // Zero has no digits.
        assert_eq!(
            numeric(&[0, 0, 0, 0, 0, 0, 0, 0]).unwrap(),
            SqlValue::Integer(0)
        );
        assert!(
            matches!(numeric(&[0, 0, 0, 0, 0xC0, 0, 0, 0]).unwrap(), SqlValue::Real(x) if x.is_nan())
        );
        assert!(numeric(&[0, 1]).is_err());
    }

    #[test]
    fn a_schema_name_is_checked() {
        assert!(check_schema("engine").is_ok());
        assert!(check_schema("_shop_2").is_ok());
        for bad in [
            "",
            "Engine",
            "pg_engine",
            "a-b",
            "1st",
            "public",
            "information_schema",
        ] {
            assert!(check_schema(bad).is_err(), "{bad}");
        }
    }
}
