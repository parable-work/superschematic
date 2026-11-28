//! A database's repositories on either dialect (#618): what the notifier
//! (D153), the inbound path and parties (D157) and the agent dispatcher
//! (D158) run over, on Postgres through a pool's connections and
//! transactions, and on a SQLite file through its reading connections and
//! a write a task holds across its awaits ([`HeldWrite`]).
//!
//! [`Store`] is the database: a Postgres [`Tables`](super::postgres::Tables)
//! or a SQLite [`Tables`](super::sqlite::Tables). [`Store::client`] answers
//! a [`StoreClient`], a pool's connection or the file, and
//! [`StoreClient::transaction`] a [`StoreTx`], a Postgres transaction or a
//! held write. Each hands out a [`Db`], which opens
//! [`StoreRepositories`] whose repositories answer as the Postgres ones
//! do, with the same members: on Postgres they are the Postgres
//! repositories; on SQLite each statement runs on the held write's
//! connection, or for a client outside a transaction a read on a reading
//! connection and a write in a held write of its own. A SQLite statement
//! never waits, so a task holds the file's write lock only as long as its
//! statements and the callbacks it runs between them take.
//!
//! `C` is the Postgres client a [`Db`] is over: a pool's connection, a
//! transaction, or the caller's own; on SQLite it is [`PoolClient`] or
//! [`PoolTransaction`], whichever the code it runs through names.

#[cfg(any(feature = "sqlite", not(feature = "postgres")))]
use std::marker::PhantomData;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use serde::Serialize;
use serde_json::Value;

use super::rowfeatures::{Attachment, BeginFile, FileOnto, FileRef, Reference, RowRef, Upload};
#[cfg(feature = "sqlite")]
use super::Table;
use super::{
    AccessCaller, Caller, Definition, Error, FieldKeys, FindOptions, Found, PageOptions, RowPage,
    Session, Values, WriteOptions,
};
use crate::access::{ActiveGrantsFilter, IssueGrant, IssuedGrant};
use crate::bucket::Bucket;
use crate::queue::{QueueDefinition, QueueError};

#[cfg(feature = "postgres")]
use super::postgres;
#[cfg(feature = "sqlite")]
use super::sqlite::{self, rusqlite::Connection, Database, HeldWrite};

/// What a Postgres client a [`Db`] runs on is: a pool's connection, a
/// transaction, the caller's own client.
#[cfg(feature = "postgres")]
pub trait PgClient: postgres::deadpool_postgres::GenericClient + Sync {}
#[cfg(feature = "postgres")]
impl<C: postgres::deadpool_postgres::GenericClient + Sync> PgClient for C {}

/// A client the core's statements and a row's references run on: a
/// pool's connection or a transaction.
#[cfg(feature = "postgres")]
pub trait PgCore: postgres::CoreClient {}
#[cfg(feature = "postgres")]
impl<C: postgres::CoreClient> PgCore for C {}

/// No Postgres client: the runtime is built without its postgres feature.
#[cfg(not(feature = "postgres"))]
#[derive(Debug)]
pub struct NoPostgres<'t>(PhantomData<&'t ()>);

/// What a Postgres client a [`Db`] runs on is; none without the postgres
/// feature.
#[cfg(not(feature = "postgres"))]
pub trait PgClient: Sync {}
#[cfg(not(feature = "postgres"))]
impl PgClient for NoPostgres<'_> {}
/// A client the core's statements run on; none without the postgres
/// feature.
#[cfg(not(feature = "postgres"))]
pub trait PgCore: PgClient {}
#[cfg(not(feature = "postgres"))]
impl PgCore for NoPostgres<'_> {}

/// A pool's connection, the client a [`StoreClient`] hands out.
#[cfg(feature = "postgres")]
pub type PoolClient = postgres::deadpool_postgres::Client;
/// A pool's connection; none without the postgres feature.
#[cfg(not(feature = "postgres"))]
pub type PoolClient = NoPostgres<'static>;
/// A transaction on a pool's connection, the client a [`StoreTx`] hands
/// out.
#[cfg(feature = "postgres")]
pub type PoolTransaction<'t> = postgres::deadpool_postgres::Transaction<'t>;
/// A transaction; none without the postgres feature.
#[cfg(not(feature = "postgres"))]
pub type PoolTransaction<'t> = NoPostgres<'t>;

/// The repositories a transaction hands out, as a callback of the app
/// (a reply action, an agent's tool) is given them.
pub type TxRepositories<'a, 't> = StoreRepositories<'a, PoolTransaction<'t>>;

/// A database's tables on either dialect.
#[derive(Clone)]
pub enum Store {
    #[cfg(feature = "postgres")]
    Postgres(postgres::Tables),
    #[cfg(feature = "sqlite")]
    Sqlite(sqlite::Tables),
}

impl std::fmt::Debug for Store {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(if self.is_sqlite() {
            "Store::Sqlite"
        } else {
            "Store::Postgres"
        })
    }
}

#[cfg(feature = "postgres")]
impl From<postgres::Tables> for Store {
    fn from(tables: postgres::Tables) -> Self {
        Store::Postgres(tables)
    }
}

#[cfg(feature = "sqlite")]
impl From<sqlite::Tables> for Store {
    fn from(tables: sqlite::Tables) -> Self {
        Store::Sqlite(tables)
    }
}

impl Store {
    /// The definition of the database's tables.
    pub fn definition(&self) -> Result<&'static Definition, Error> {
        match self {
            #[cfg(feature = "postgres")]
            Store::Postgres(tables) => tables.definition(),
            #[cfg(feature = "sqlite")]
            Store::Sqlite(tables) => tables.definition(),
        }
    }

    /// The keys of the database's `Encrypted<T>` fields, when given.
    pub fn keys(&self) -> Option<&Arc<dyn FieldKeys>> {
        match self {
            #[cfg(feature = "postgres")]
            Store::Postgres(tables) => tables.keys(),
            #[cfg(feature = "sqlite")]
            Store::Sqlite(tables) => tables.keys(),
        }
    }

    /// Whether the database is a SQLite file.
    pub fn is_sqlite(&self) -> bool {
        #[cfg(feature = "sqlite")]
        if matches!(self, Store::Sqlite(_)) {
            return true;
        }
        false
    }

    /// The Postgres tables, on Postgres.
    #[cfg(feature = "postgres")]
    pub fn postgres(&self) -> Option<&postgres::Tables> {
        match self {
            Store::Postgres(tables) => Some(tables),
            #[cfg(feature = "sqlite")]
            Store::Sqlite(_) => None,
        }
    }

    /// The SQLite tables, on a SQLite file.
    #[cfg(feature = "sqlite")]
    pub fn sqlite(&self) -> Option<&sqlite::Tables> {
        match self {
            Store::Sqlite(tables) => Some(tables),
            #[cfg(feature = "postgres")]
            Store::Postgres(_) => None,
        }
    }

    /// A connection of the pool, or the file.
    pub async fn client(&self) -> Result<StoreClient, Error> {
        match self {
            #[cfg(feature = "postgres")]
            Store::Postgres(tables) => tables
                .pool()
                .get()
                .await
                .map(StoreClient::Postgres)
                .map_err(|err| Error::Database(format!("the database is unreachable: {err}"))),
            #[cfg(feature = "sqlite")]
            Store::Sqlite(tables) => Ok(StoreClient::Sqlite(Arc::clone(tables.database()))),
        }
    }
}

/// A connection of a Postgres pool, or a SQLite file.
#[allow(clippy::large_enum_variant)]
pub enum StoreClient {
    #[cfg(feature = "postgres")]
    Postgres(PoolClient),
    #[cfg(feature = "sqlite")]
    Sqlite(Arc<Database>),
}

impl StoreClient {
    /// The client as its statements run on it: each its own on SQLite.
    pub fn db(&self) -> Db<'_, PoolClient> {
        match self {
            #[cfg(feature = "postgres")]
            StoreClient::Postgres(client) => Db::Postgres(client),
            #[cfg(feature = "sqlite")]
            StoreClient::Sqlite(database) => Db::Sqlite(LiteDb::Auto(database), PhantomData),
        }
    }

    /// A transaction: a Postgres one, or a held write of the file.
    pub async fn transaction(&mut self) -> Result<StoreTx<'_>, Error> {
        match self {
            #[cfg(feature = "postgres")]
            StoreClient::Postgres(client) => client
                .transaction()
                .await
                .map(StoreTx::Postgres)
                .map_err(|err| postgres::failure("BEGIN", &err)),
            #[cfg(feature = "sqlite")]
            StoreClient::Sqlite(database) => {
                Ok(StoreTx::Sqlite(database.begin_async().await?, PhantomData))
            }
        }
    }
}

/// A transaction: on Postgres, on a pool's connection; on SQLite, a held
/// write.
pub enum StoreTx<'a> {
    #[cfg(feature = "postgres")]
    Postgres(PoolTransaction<'a>),
    #[cfg(feature = "sqlite")]
    Sqlite(HeldWrite, PhantomData<&'a ()>),
}

impl<'a> StoreTx<'a> {
    /// The transaction as its statements run in it.
    pub fn db(&self) -> Db<'_, PoolTransaction<'a>> {
        match self {
            #[cfg(feature = "postgres")]
            StoreTx::Postgres(tx) => Db::Postgres(tx),
            #[cfg(feature = "sqlite")]
            StoreTx::Sqlite(held, _) => Db::Sqlite(LiteDb::Held(held), PhantomData),
        }
    }

    /// Commits it.
    pub async fn commit(self) -> Result<(), Error> {
        match self {
            #[cfg(feature = "postgres")]
            StoreTx::Postgres(tx) => tx
                .commit()
                .await
                .map_err(|err| postgres::failure("COMMIT", &err)),
            #[cfg(feature = "sqlite")]
            StoreTx::Sqlite(held, _) => held.commit(),
        }
    }
}

/// Where a SQLite statement runs: a reading connection or a held write of
/// its own (a client outside a transaction), or a held write. Both are
/// shared across a task's awaits, so a part's futures stay `Send`; work
/// in a caller's own rusqlite transaction goes through the parts' `_sqlite`
/// members, which take its connection.
#[cfg(feature = "sqlite")]
#[derive(Clone, Copy)]
pub enum LiteDb<'c> {
    Auto(&'c Arc<Database>),
    Held(&'c HeldWrite),
}

#[cfg(feature = "sqlite")]
impl LiteDb<'_> {
    /// Runs `work` where the statements run: a write's on the writing
    /// connection, in a transaction of its own outside a held write. A
    /// SQLite statement never waits, so it runs at once; a write outside a
    /// held write waits its turn for the writing connection, up to the
    /// file's busy timeout.
    fn run<T, E: From<Error>>(
        self,
        write: bool,
        work: impl FnOnce(&Connection) -> Result<T, E>,
    ) -> Result<T, E> {
        match self {
            LiteDb::Auto(database) if write => database.write(work),
            LiteDb::Auto(database) => database.read(work),
            LiteDb::Held(held) => held.with(work),
        }
    }
}

/// A client a part's statements run on: a Postgres client, or where a
/// SQLite statement runs.
pub enum Db<'c, C> {
    #[cfg(feature = "postgres")]
    Postgres(&'c C),
    #[cfg(feature = "sqlite")]
    Sqlite(LiteDb<'c>, PhantomData<fn() -> C>),
}

impl<C> Clone for Db<'_, C> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<C> Copy for Db<'_, C> {}

/// What converts into a [`Db`]: a Db, or a Postgres client.
pub trait IntoDb<'c> {
    type Client: PgClient + 'c;
    fn into_db(self) -> Db<'c, Self::Client>;
}

impl<'c, C: PgClient + 'c> IntoDb<'c> for Db<'c, C> {
    type Client = C;
    fn into_db(self) -> Db<'c, C> {
        self
    }
}

#[cfg(feature = "postgres")]
impl<'c, C: PgClient> IntoDb<'c> for &'c C {
    type Client = C;
    fn into_db(self) -> Db<'c, C> {
        Db::Postgres(self)
    }
}

/// The wall time of `at` in `zone` (an IANA name), its minutes of the day,
/// its second and its date, by the zone database the runtime bundles.
#[cfg(feature = "sqlite")]
pub fn wall_in_zone(at: SystemTime, zone: &str) -> Result<(u32, u32, String), Error> {
    let timestamp = jiff::Timestamp::try_from(at)
        .map_err(|err| Error::Invalid(format!("the time {at:?}: {err}")))?;
    let tz = jiff::tz::TimeZone::get(zone)
        .map_err(|err| Error::Invalid(format!("the time zone {zone}: {err}")))?;
    let civil = timestamp.to_zoned(tz).datetime();
    Ok((
        u32::try_from(civil.hour()).unwrap_or(0) * 60 + u32::try_from(civil.minute()).unwrap_or(0),
        u32::try_from(civil.second()).unwrap_or(0),
        format!(
            "{:04}-{:02}-{:02}",
            civil.year(),
            civil.month(),
            civil.day()
        ),
    ))
}

impl<'c, C: PgClient> Db<'c, C> {
    /// Whether the statements run on SQLite.
    pub fn is_sqlite(self) -> bool {
        #[cfg(feature = "sqlite")]
        if matches!(self, Db::Sqlite(..)) {
            return true;
        }
        false
    }

    /// The repositories of `definition` over the client, acting as no
    /// one.
    pub fn repositories(
        self,
        definition: &'c Definition,
    ) -> Result<StoreRepositories<'c, C>, Error> {
        let inner = match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                Inner::Postgres(postgres::Repositories::new(client, definition)?)
            }
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => {
                if definition.dialect != super::Dialect::Sqlite {
                    return Err(Error::Definition(format!(
                        "the ORM's definition is {}'s, and the database is a SQLite file: open its sqlite::Tables with the types crate's orm::sqlite_definition",
                        definition.dialect
                    )));
                }
                Inner::Sqlite(
                    LiteRepos {
                        db: lite,
                        definition,
                        session: Session::default(),
                        rules: None,
                    },
                    PhantomData,
                )
            }
        };
        Ok(StoreRepositories { db: self, inner })
    }

    /// Writes `message` into `queue` on the client, in its transaction
    /// when it is one, due once `delay` has passed (D80).
    pub async fn enqueue<M: Serialize + Sync + ?Sized>(
        self,
        queue: &QueueDefinition,
        message: &M,
        delay: Duration,
    ) -> Result<(), QueueError> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => crate::queue::enqueue_in(client, queue, message, delay).await,
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => lite
                .run(true, |connection| {
                    crate::queue::sqlite::enqueue_sqlite_in(connection, queue, message, delay)
                        .map_err(Queued)
                })
                .map_err(|Queued(err)| err),
        }
    }

    /// Runs `sql`, statements of no parameters and no rows: a savepoint,
    /// its release or its rollback.
    pub async fn batch_execute(self, sql: &str) -> Result<(), Error> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .batch_execute(sql)
                .await
                .map_err(|err| postgres::failure(sql, &err)),
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => lite.run(true, |connection| {
                connection
                    .execute_batch(sql)
                    .map_err(|err| sqlite::failure(sql, &err))
            }),
        }
    }

    /// The active grants `filter` reads (D156), as the system.
    pub async fn active_grants(
        self,
        definition: &Definition,
        filter: &ActiveGrantsFilter,
    ) -> Result<Vec<Values>, Error> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => crate::access::active_grants(client, definition, filter).await,
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => lite.run(false, |connection| {
                crate::access::sqlite::active_grants(connection, definition, filter)
            }),
        }
    }

    /// Why `grant` is not active at `now`, or none when it is (D156).
    pub async fn grant_ended(
        self,
        definition: &Definition,
        grant: &Values,
        now: SystemTime,
    ) -> Result<Option<&'static str>, Error> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                crate::access::grant_ended(client, definition, grant, now).await
            }
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => lite.run(false, |connection| {
                crate::access::sqlite::grant_ended(connection, definition, grant, now)
            }),
        }
    }

    /// Revokes the grant `id` at `at`, as the system.
    pub async fn revoke_grant(
        self,
        definition: &Definition,
        id: &str,
        at: SystemTime,
    ) -> Result<(), Error> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => crate::access::revoke_grant(client, definition, id, at).await,
            #[cfg(feature = "sqlite")]
            Db::Sqlite(lite, _) => lite.run(true, |connection| {
                crate::access::sqlite::revoke_grant(connection, definition, id, at)
            }),
        }
    }

    /// The wall time of `at` in `zone`: its minutes of the day, its
    /// second and its date. Postgres reads its own zone database; on
    /// SQLite the runtime's bundled one does.
    pub async fn wall_of(self, at: SystemTime, zone: &str) -> Result<(u32, u32, String), Error> {
        match self {
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let at = super::rfc3339_millis(at);
                let sql = "SELECT to_char(($1::text)::timestamptz AT TIME ZONE $2::text, 'HH24:MI:SS YYYY-MM-DD')";
                let row = client
                    .query_one(sql, &[&at, &zone])
                    .await
                    .map_err(|err| postgres::failure(sql, &err))?;
                let wall: String = row.get(0);
                let (time, date) = wall.split_once(' ').unwrap_or((&wall, ""));
                let parts: Vec<u32> = time.split(':').filter_map(|p| p.parse().ok()).collect();
                let (hour, minute, second) = (
                    parts.first().copied().unwrap_or(0),
                    parts.get(1).copied().unwrap_or(0),
                    parts.get(2).copied().unwrap_or(0),
                );
                Ok((hour * 60 + minute, second, date.to_owned()))
            }
            #[cfg(feature = "sqlite")]
            Db::Sqlite(..) => wall_in_zone(at, zone),
        }
    }
}

/// A queue's error carried through a SQLite run, whose errors convert
/// from the ORM's.
#[cfg(feature = "sqlite")]
struct Queued(QueueError);

#[cfg(feature = "sqlite")]
impl From<Error> for Queued {
    fn from(err: Error) -> Self {
        Queued(QueueError::Failed(err.to_string()))
    }
}

/// The SQLite repositories' settings, which open them on each statement's
/// connection.
#[cfg(feature = "sqlite")]
#[doc(hidden)]
pub struct LiteRepos<'c> {
    db: LiteDb<'c>,
    definition: &'c Definition,
    session: Session<'c>,
    rules: Option<&'c dyn sqlite::Rules>,
}

#[cfg(feature = "sqlite")]
impl LiteRepos<'_> {
    fn run<T>(
        &self,
        write: bool,
        work: impl FnOnce(&sqlite::Repositories<'_>) -> Result<T, Error>,
    ) -> Result<T, Error> {
        self.db.run(write, |connection| {
            let mut repositories = sqlite::Repositories::new(connection, self.definition)?
                .with_session(self.session.clone());
            if let Some(rules) = self.rules {
                repositories = repositories.with_rules(rules);
            }
            work(&repositories)
        })
    }
}

enum Inner<'c, C> {
    #[cfg(feature = "postgres")]
    Postgres(postgres::Repositories<'c, C>),
    #[cfg(feature = "sqlite")]
    Sqlite(LiteRepos<'c>, PhantomData<fn() -> C>),
}

/// The repositories of a database on a [`Db`], acting as a user or as no
/// one: the Postgres repositories' members, on either dialect.
pub struct StoreRepositories<'c, C> {
    db: Db<'c, C>,
    inner: Inner<'c, C>,
}

/// Changes the repositories on either dialect: `$pg` the Postgres
/// repositories `$r`, `$lite` the SQLite session `$s`.
macro_rules! edit {
    ($self:ident, |$r:ident| $pg:expr, |$s:ident, $d:ident| $lite:expr) => {{
        $self.inner = match $self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres($r) => Inner::Postgres($pg),
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(mut repositories, marker) => {
                let $d = repositories.definition;
                let $s = &mut repositories.session;
                $lite;
                Inner::Sqlite(repositories, marker)
            }
        };
        $self
    }};
}

impl<'c, C: PgClient> StoreRepositories<'c, C> {
    /// The repositories acting as `user` (D87); `None` acts as no one.
    #[must_use]
    pub fn acting(mut self, user: Option<String>) -> Self {
        edit!(self, |r| r.acting(user), |s, _d| s.user = user)
    }

    /// The repositories acting as the database's system actor (D87),
    /// which reads every row (D156).
    #[must_use]
    pub fn as_system(mut self) -> Self {
        edit!(self, |r| r.as_system(), |s, d| {
            s.user = Some(d.system_actor_id().to_owned());
            s.access = None;
        })
    }

    /// The repositories keeping every read and write to what `caller`
    /// sees (D156).
    #[must_use]
    pub fn with_access(mut self, caller: AccessCaller) -> Self {
        edit!(self, |r| r.with_access(caller), |s, _d| s.access =
            Some(caller))
    }

    /// The repositories with the keys of the database's `Encrypted<T>`
    /// fields (D85).
    #[must_use]
    pub fn with_keys(mut self, keys: &'c dyn FieldKeys) -> Self {
        edit!(self, |r| r.with_keys(keys), |s, _d| s.keys = Some(keys))
    }

    /// The repositories running each behavior's command as `caller`
    /// (D103).
    #[must_use]
    pub fn with_caller(mut self, caller: Caller) -> Self {
        edit!(self, |r| r.with_caller(caller), |s, _d| s.caller =
            Some(caller))
    }

    /// The client the repositories run on.
    pub fn db(&self) -> Db<'c, C> {
        self.db
    }

    /// The definition of the database the repositories serve.
    pub fn definition(&self) -> &'c Definition {
        match &self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.definition(),
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => repositories.definition,
        }
    }

    /// The session the repositories act in.
    pub fn session(&self) -> &Session<'c> {
        match &self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.session(),
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => &repositories.session,
        }
    }

    /// The repository of the table type `name`.
    pub fn table(&self, name: &str) -> Result<StoreRepository<'_, C>, Error> {
        Ok(match &self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => StoreRepository::Postgres(repositories.table(name)?),
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => StoreRepository::Sqlite(
                repositories,
                repositories.definition.table(name)?,
                PhantomData,
            ),
        })
    }

    /// Makes a grant (D156) and its token, written through the
    /// repositories as they are.
    pub async fn issue_grant(&self, input: &IssueGrant) -> Result<IssuedGrant, Error> {
        match &self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => crate::access::issue_grant(repositories, input).await,
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => {
                repositories.run(true, |r| crate::access::sqlite::issue_grant(r, input))
            }
        }
    }
}

impl<C: PgClient> StoreRepositories<'_, C> {
    /// Runs the MCP search tool (D155) as the repositories read: on SQLite
    /// over the FTS5 table beside the index (D186).
    pub async fn call_search_tool(
        &self,
        principal: Option<&crate::auth::Principal>,
        args: &serde_json::Map<String, Value>,
        options: &super::search_tables::SearchOptions,
    ) -> Result<super::search_tables::SearchResult, Error> {
        match &self.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => {
                super::search_tables::call_search_tool(repositories, principal, args, options).await
            }
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => repositories.run(false, |r| {
                super::search_tables::sqlite::call_search_tool(r, principal, args, options)
            }),
        }
    }
}

impl<'c, 't> StoreRepositories<'c, PoolTransaction<'t>> {
    /// The repositories running the behaviors of the tables that compose
    /// them through `store`'s core (D103): a transaction's.
    #[must_use]
    pub fn with_rules_of(mut self, store: &'c Store) -> Self {
        self.inner = match (self.inner, store) {
            #[cfg(feature = "postgres")]
            (Inner::Postgres(repositories), Store::Postgres(tables)) => {
                Inner::Postgres(match tables.rules() {
                    Some(rules) => repositories.with_rules(rules.as_ref()),
                    None => repositories,
                })
            }
            #[cfg(feature = "sqlite")]
            (Inner::Sqlite(mut repositories, marker), Store::Sqlite(tables)) => {
                repositories.rules = tables
                    .rules()
                    .map(|rules| rules.as_ref() as &dyn sqlite::Rules);
                Inner::Sqlite(repositories, marker)
            }
            #[cfg(all(feature = "postgres", feature = "sqlite"))]
            (inner, _) => inner,
        };
        self
    }
}

impl<'c, C: PgCore> StoreRepositories<'c, C> {
    /// The references of the database's rows (D154).
    pub fn references(&self) -> Result<StoreReferences<'_, 'c, C>, Error> {
        if self.definition().references.is_none() {
            return Err(Error::Invalid(
                "the database keeps no references (@references or Comments)".to_owned(),
            ));
        }
        Ok(StoreReferences(self))
    }

    /// The files of the database's rows (D154).
    pub fn attachments(&self) -> Result<StoreAttachments<'_, 'c, C>, Error> {
        if self.definition().attachments.is_none() {
            return Err(Error::Invalid(
                "the database holds no files (@attachments)".to_owned(),
            ));
        }
        Ok(StoreAttachments(self))
    }
}

/// The answer of work on a SQLite connection that may await a bucket:
/// what a bucket that answers at once (a local or in-memory one) gives.
#[cfg(feature = "sqlite")]
fn ready<T>(work: impl std::future::Future<Output = Result<T, Error>>) -> Result<T, Error> {
    use std::task::{Context, Poll, Waker};
    match std::pin::pin!(work)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(out) => out,
        Poll::Pending => Err(Error::Definition(
            "a file's bucket waited inside a transaction on the SQLite file: give a bucket that answers at once there (#618)".to_owned(),
        )),
    }
}

/// The reference operations of [`StoreRepositories`] (D154).
pub struct StoreReferences<'r, 'c, C>(&'r StoreRepositories<'c, C>);

impl<C: PgCore> StoreReferences<'_, '_, C> {
    /// Makes `from` point at `to` with `role`.
    pub async fn add(&self, from: &RowRef, to: &RowRef, role: &str) -> Result<Reference, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.references()?.add(from, to, role).await,
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => {
                repositories.run(true, |r| r.references()?.add(from, to, role))
            }
        }
    }

    /// The rows that point at `row`, as the reader reads, at most `limit`.
    pub async fn referenced_by(
        &self,
        row: &RowRef,
        limit: Option<u32>,
    ) -> Result<super::rowfeatures::Backlinks, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => {
                repositories.references()?.referenced_by(row, limit).await
            }
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => {
                repositories.run(false, |r| r.references()?.referenced_by(row, limit))
            }
        }
    }

    /// The references `row` holds, as the reader reads.
    pub async fn of(&self, row: &RowRef) -> Result<Vec<Reference>, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.references()?.of(row).await,
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => repositories.run(false, |r| r.references()?.of(row)),
        }
    }
}

/// The attachment operations of [`StoreRepositories`] (D154).
pub struct StoreAttachments<'r, 'c, C>(&'r StoreRepositories<'c, C>);

impl<C: PgCore> StoreAttachments<'_, '_, C> {
    /// Starts an upload of a file to its row.
    pub async fn begin(
        &self,
        input: &BeginFile,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<(Attachment, Upload), Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => {
                repositories
                    .attachments()?
                    .begin(input, bucket, expires_in)
                    .await
            }
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => repositories.run(true, |r| {
                ready(async { r.attachments()?.begin(input, bucket, expires_in).await })
            }),
        }
    }

    /// Checks an upload's bytes and makes the file ready.
    pub async fn complete(&self, file: &FileRef, bucket: &dyn Bucket) -> Result<Attachment, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => {
                repositories.attachments()?.complete(file, bucket).await
            }
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => repositories.run(true, |r| {
                ready(async { r.attachments()?.complete(file, bucket).await })
            }),
        }
    }

    /// The files of `row`.
    pub async fn list(&self, row: &RowRef) -> Result<Vec<Attachment>, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.attachments()?.list(row).await,
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => {
                repositories.run(false, |r| r.attachments()?.list(row))
            }
        }
    }

    /// Files a file onto another row.
    pub async fn file(&self, input: &FileOnto) -> Result<Attachment, Error> {
        match &self.0.inner {
            #[cfg(feature = "postgres")]
            Inner::Postgres(repositories) => repositories.attachments()?.file(input).await,
            #[cfg(feature = "sqlite")]
            Inner::Sqlite(repositories, _) => {
                repositories.run(true, |r| r.attachments()?.file(input))
            }
        }
    }
}

/// The repository of one table on a [`Db`].
pub enum StoreRepository<'r, C> {
    #[cfg(feature = "postgres")]
    Postgres(postgres::Repository<'r, C>),
    #[cfg(feature = "sqlite")]
    #[doc(hidden)]
    Sqlite(&'r LiteRepos<'r>, &'r Table, PhantomData<fn() -> C>),
}

/// Runs a repository's member on either dialect: `$write` says whether
/// it writes, for SQLite's connection.
macro_rules! on_table {
    ($self:ident, $write:expr, |$r:ident| $call:expr) => {
        match $self {
            #[cfg(feature = "postgres")]
            StoreRepository::Postgres($r) => $call.await,
            #[cfg(feature = "sqlite")]
            StoreRepository::Sqlite(repositories, table, _) => {
                repositories.run($write, |repositories| {
                    let $r = repositories.table(&table.name)?;
                    $call
                })
            }
        }
    };
}

impl<C: PgClient> StoreRepository<'_, C> {
    /// The live row whose key is `key`; [`Error::NotFound`] when none.
    pub async fn get_one(&self, key: &Value) -> Result<Values, Error> {
        on_table!(self, false, |r| r.get_one(key))
    }

    /// The first row `filter` finds, or none.
    pub async fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Option<Values>, Error> {
        on_table!(self, false, |r| r.find_one(filter, options))
    }

    /// The rows `filter` finds.
    pub async fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Found<Values>, Error> {
        on_table!(self, false, |r| r.find_many(filter, options))
    }

    /// A page of the rows `filter` finds.
    pub async fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<RowPage<Values>, Error> {
        on_table!(self, false, |r| r.find_page(filter, options))
    }

    /// How many live rows `filter` finds.
    pub async fn count(&self, filter: Option<&Values>) -> Result<u64, Error> {
        on_table!(self, false, |r| r.count(filter))
    }

    /// Inserts a row.
    pub async fn create_one(&self, input: &Values) -> Result<Values, Error> {
        on_table!(self, true, |r| r.create_one(input))
    }

    /// Sets the members `update` sets on the live row whose key is `key`.
    pub async fn update_one(
        &self,
        key: &Value,
        update: &Values,
        options: &WriteOptions,
    ) -> Result<Values, Error> {
        on_table!(self, true, |r| r.update_one(key, update, options))
    }

    /// Sets the members `update` sets on the live rows `filter` finds.
    pub async fn update_many(
        &self,
        filter: Option<&Values>,
        update: &Values,
    ) -> Result<u64, Error> {
        on_table!(self, true, |r| r.update_many(filter, update))
    }

    /// Deletes the live row whose key is `key`.
    pub async fn delete_one(&self, key: &Value, options: &WriteOptions) -> Result<(), Error> {
        on_table!(self, true, |r| r.delete_one(key, options))
    }

    /// Removes the row whose key is `key`, deleted or not.
    pub async fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        on_table!(self, true, |r| r.hard_delete_one(key))
    }

    /// Runs `operation` of the behavior under `member` on the row `key`
    /// through the core (D103).
    pub async fn operation(
        &self,
        member: &str,
        operation: &str,
        key: &Value,
        params: &Value,
        options: &WriteOptions,
    ) -> Result<Value, Error> {
        on_table!(self, true, |r| r
            .operation(member, operation, key, params, options))
    }

    /// Runs `operation` of the behavior under `member` on the table as a
    /// whole (D104).
    pub async fn table_operation(
        &self,
        member: &str,
        operation: &str,
        params: &Value,
    ) -> Result<Value, Error> {
        on_table!(self, true, |r| r.table_operation(member, operation, params))
    }
}

macro_rules! on_queue {
    ($self:ident, |$q:ident| $call:expr) => {
        match $self {
            #[cfg(feature = "postgres")]
            PartQueue::Postgres($q) => $call,
            #[cfg(feature = "sqlite")]
            PartQueue::Sqlite($q) => $call,
        }
    };
}

/// A part's queue as a worker claims it (D80, D116): the Postgres queue
/// over the pool, or the SQLite queue over a connection of its own to the
/// file, which takes no slot and reads no next due time (D145).
pub enum PartQueue<M> {
    #[cfg(feature = "postgres")]
    Postgres(crate::queue::PostgresQueue<M>),
    #[cfg(feature = "sqlite")]
    Sqlite(crate::queue::sqlite::SqliteQueue<M>),
}

impl<M> Clone for PartQueue<M> {
    fn clone(&self) -> Self {
        match self {
            #[cfg(feature = "postgres")]
            PartQueue::Postgres(queue) => PartQueue::Postgres(queue.clone()),
            #[cfg(feature = "sqlite")]
            PartQueue::Sqlite(queue) => PartQueue::Sqlite(queue.clone()),
        }
    }
}

impl<M> std::fmt::Debug for PartQueue<M> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            #[cfg(feature = "postgres")]
            PartQueue::Postgres(queue) => queue.fmt(f),
            #[cfg(feature = "sqlite")]
            PartQueue::Sqlite(queue) => queue.fmt(f),
        }
    }
}

impl<M: serde::de::DeserializeOwned + Send + 'static> PartQueue<M> {
    /// The claims of the queue `definition` describes in `store`'s
    /// database; on SQLite it opens a connection of its own to the file,
    /// and refuses a descriptor with no statements there.
    pub fn of(store: &Store, definition: QueueDefinition) -> Result<Self, QueueError> {
        match store {
            #[cfg(feature = "postgres")]
            Store::Postgres(tables) => Ok(PartQueue::Postgres(crate::queue::PostgresQueue::new(
                tables.pool().clone(),
                definition,
            ))),
            #[cfg(feature = "sqlite")]
            Store::Sqlite(tables) => {
                crate::queue::sqlite::SqliteQueue::open(tables.database().path(), definition)
                    .map(PartQueue::Sqlite)
            }
        }
    }

    /// The queue's descriptor.
    pub fn definition(&self) -> &QueueDefinition {
        on_queue!(self, |q| q.definition())
    }

    /// Claims up to `n` due messages for `lease`.
    pub async fn claim(
        &self,
        n: usize,
        lease: Duration,
    ) -> Result<Vec<crate::queue::Claim<M>>, QueueError> {
        on_queue!(self, |q| q.claim(n, lease).await)
    }

    /// Marks a claimed message done.
    pub async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        on_queue!(self, |q| q.complete(id, token).await)
    }

    /// Records `cause` on a claimed message; answers whether it is dead.
    pub async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        on_queue!(self, |q| q.fail(id, token, cause).await)
    }

    /// Gives a claimed message back, due at once.
    pub async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        on_queue!(self, |q| q.release(id, token).await)
    }
}

#[async_trait::async_trait]
impl<M: serde::de::DeserializeOwned + Send + 'static> crate::worker::WorkerQueue for PartQueue<M> {
    type Message = M;

    fn name(&self) -> &str {
        on_queue!(self, |q| q.name())
    }

    fn lease(&self) -> Duration {
        on_queue!(self, |q| q.lease())
    }

    async fn claim(
        &self,
        n: usize,
        lease: Duration,
    ) -> Result<Vec<crate::queue::Claim<M>>, QueueError> {
        on_queue!(self, |q| q.claim(n, lease).await)
    }

    async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        on_queue!(self, |q| q.extend(id, token, lease).await)
    }

    async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        on_queue!(self, |q| q.complete(id, token).await)
    }

    async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        on_queue!(self, |q| q.fail(id, token, cause).await)
    }

    async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        on_queue!(self, |q| q.release(id, token).await)
    }

    async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        on_queue!(self, |q| q.prune(limit).await)
    }

    async fn slot(&self, slots: usize) -> Result<Option<crate::queue::Slot>, QueueError> {
        match self {
            #[cfg(feature = "postgres")]
            PartQueue::Postgres(queue) => queue.slot(slots).await,
            #[cfg(feature = "sqlite")]
            PartQueue::Sqlite(_) => {
                let _ = slots;
                Ok(Some(crate::queue::Slot::none()))
            }
        }
    }

    async fn next_due(&self) -> Result<Option<Duration>, QueueError> {
        match self {
            #[cfg(feature = "postgres")]
            PartQueue::Postgres(queue) => queue.next_due().await,
            #[cfg(feature = "sqlite")]
            PartQueue::Sqlite(_) => Ok(None),
        }
    }
}

/// The grants' [`Authenticator`](crate::auth::Authenticator) of a
/// [`Store`] (D156): the Postgres `GrantAuthenticator` or the SQLite one.
pub enum StoreGrants {
    #[cfg(feature = "postgres")]
    Postgres(crate::access::GrantAuthenticator),
    #[cfg(feature = "sqlite")]
    Sqlite(crate::access::sqlite::GrantAuthenticator),
}

impl StoreGrants {
    /// The grants of `store`'s database; refuses one without the
    /// `@accessGrants` table or the UserRole table.
    pub fn new(store: Store) -> Result<Self, Error> {
        match store {
            #[cfg(feature = "postgres")]
            Store::Postgres(tables) => {
                crate::access::GrantAuthenticator::new(tables).map(StoreGrants::Postgres)
            }
            #[cfg(feature = "sqlite")]
            Store::Sqlite(tables) => {
                crate::access::sqlite::GrantAuthenticator::new(tables).map(StoreGrants::Sqlite)
            }
        }
    }

    /// The authenticator taking an agent run's token too (D158).
    pub(crate) fn admitting_runs(self) -> Self {
        match self {
            #[cfg(feature = "postgres")]
            StoreGrants::Postgres(grants) => StoreGrants::Postgres(grants.admitting_runs()),
            #[cfg(feature = "sqlite")]
            StoreGrants::Sqlite(grants) => StoreGrants::Sqlite(grants.admitting_runs()),
        }
    }

    /// The time a grant is held to, the clock's unless set.
    #[must_use]
    pub fn with_clock(self, now: Arc<dyn Fn() -> SystemTime + Send + Sync>) -> Self {
        match self {
            #[cfg(feature = "postgres")]
            StoreGrants::Postgres(grants) => StoreGrants::Postgres(grants.with_clock(now)),
            #[cfg(feature = "sqlite")]
            StoreGrants::Sqlite(grants) => StoreGrants::Sqlite(grants.with_clock(now)),
        }
    }

    /// The authenticator establishing every caller that presents no
    /// grant's token as `inner` does.
    #[must_use]
    pub fn with_inner(self, inner: Arc<dyn crate::auth::Authenticator>) -> Self {
        match self {
            #[cfg(feature = "postgres")]
            StoreGrants::Postgres(grants) => StoreGrants::Postgres(grants.with_inner(inner)),
            #[cfg(feature = "sqlite")]
            StoreGrants::Sqlite(grants) => StoreGrants::Sqlite(grants.with_inner(inner)),
        }
    }
}

macro_rules! on_grants {
    ($self:ident, |$g:ident| $call:expr) => {
        match $self {
            #[cfg(feature = "postgres")]
            StoreGrants::Postgres($g) => $call,
            #[cfg(feature = "sqlite")]
            StoreGrants::Sqlite($g) => $call,
        }
    };
}

#[async_trait::async_trait]
impl crate::auth::Authenticator for StoreGrants {
    async fn authenticate(
        &self,
        request: &http::request::Parts,
    ) -> Result<Option<crate::auth::Principal>, crate::ApiError> {
        on_grants!(self, |g| g.authenticate(request).await)
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        on_grants!(self, |g| g.permits(held, required))
    }

    async fn called(
        &self,
        principal: Option<&crate::auth::Principal>,
        operation: &str,
        method: &http::Method,
    ) -> Result<(), crate::ApiError> {
        on_grants!(self, |g| g.called(principal, operation, method).await)
    }
}

#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use std::time::{Duration, UNIX_EPOCH};

    use super::wall_in_zone;

    #[test]
    fn a_wall_time_is_read_in_its_zone_across_a_change_of_the_clocks() {
        // 2027-03-10T23:30:00Z, Greenwich time in London.
        let winter = UNIX_EPOCH + Duration::from_secs(1_804_721_400);
        assert_eq!(
            wall_in_zone(winter, "Europe/London").unwrap(),
            (23 * 60 + 30, 0, "2027-03-10".to_owned())
        );
        // 2027-07-01T12:00:05Z, British Summer Time.
        let summer = UNIX_EPOCH + Duration::from_secs(1_814_443_205);
        assert_eq!(
            wall_in_zone(summer, "Europe/London").unwrap(),
            (13 * 60, 5, "2027-07-01".to_owned())
        );
        assert_eq!(
            wall_in_zone(winter, "America/New_York").unwrap(),
            (18 * 60 + 30, 0, "2027-03-10".to_owned())
        );
        assert!(wall_in_zone(winter, "Nowhere/Else").is_err());
    }
}
