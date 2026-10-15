//! The version graph inside a behavior's call (D32, D58): the call's SQL as
//! the transaction the version graph's fixed layout runs in.
//!
//! The version graph's Rust engine (`superschematic-versiongraph-engine`)
//! keeps a graph in the fixed layout's nine tables, in either dialect, and
//! reaches them through a synchronous transaction its host holds
//! ([`HostTx`]). [`GraphSql`] is that transaction over a hook's `sql`: each
//! statement the layout runs goes through the behavior's SQL handle, so the
//! engine's checks hold it to the behavior's own tables, one statement at a
//! time, with no trigger and no transaction control, and it runs in the
//! call's transaction, so a savepoint around an invoked operation rolls the
//! graph back with the rest. The handle numbers the `?` placeholders for
//! Postgres and binds each value as the server infers its parameter, which
//! the layout's statements type as `TEXT` or `BIGINT`. [`run`] runs one
//! graph operation over it synchronously, on the calling thread, with the
//! call's time as the layout's clock and the engine's UUID source as its
//! ids', so a replay's seeded engine draws the graph's ids in the order the
//! TypeScript engine's recording drew them.
//!
//! `begin`, `commit` and `rollback` issue nothing, as the TypeScript
//! adapter's `callerTransaction` does: an operation that fails leaves what
//! it wrote for the engine to roll back with the call, or with the
//! savepoint of the call that invoked it. A statement the engine refuses
//! (the transaction aborted, the lock waited past the busy timeout) is
//! kept, and [`run`] returns it rather than the layout's word for it, so the
//! caller meets the engine's own error.

use std::cell::{Cell, RefCell};
use std::future::Future;
use std::sync::Arc;

use superschematic_engine::behaviors::{SqlReader, SqlWriter};
use superschematic_engine::storage::{SqlValue, SQLITE_CONSTRAINT_UNIQUE};
use superschematic_engine::{Error, UuidSource};
use superschematic_versiongraph_engine::fixed::{
    ClientError, Clock, FixedLayout, HostClient, HostTx, IdSource, SqlValue as GraphValue,
};
use superschematic_versiongraph_engine::{ready, Engine as GraphEngine, EngineOn};

thread_local! {
    // The time, in microseconds since the Unix epoch, the operation [`run`]
    // runs on this thread writes: the call's.
    static NOW: Cell<i64> = const { Cell::new(0) };
    // Where the ids it writes come from: the engine's.
    static UUIDS: RefCell<Option<UuidSource>> = const { RefCell::new(None) };
}

/// The clock a [`FixedLayout`] a behavior binds reads: the time of the call
/// whose operation [`run`] is running on the thread, in microseconds. A
/// layout's clock is fixed when it is built, and a behavior builds one per
/// graph and keeps it across calls, each with a time of its own.
pub fn call_clock() -> Clock {
    Arc::new(|| NOW.with(Cell::get))
}

/// The id source a [`FixedLayout`] a behavior binds draws from: the UUID
/// source of the engine whose call [`run`] is running on the thread, random
/// ids outside one.
pub fn call_ids() -> IdSource {
    Arc::new(|| {
        Ok(UUIDS.with(|uuids| {
            uuids
                .borrow()
                .as_ref()
                .map_or_else(|| UuidSource::random().next_bytes(), UuidSource::next_bytes)
        }))
    })
}

/// What one graph operation runs with: the call's SQL (`writer` `None` in a
/// read, which refuses a statement that writes), its time in epoch
/// milliseconds, and the engine's UUID source.
#[derive(Clone)]
pub struct GraphCall<'a> {
    pub reader: SqlReader<'a>,
    pub writer: Option<SqlWriter<'a>>,
    pub now_ms: i64,
    pub uuids: UuidSource,
}

/// Polls a graph operation's future once and returns its result: over
/// [`GraphSql`] every storage call is ready at once, so an operation runs to
/// its end ([`ready`]).
pub fn sync<T, F>(future: F) -> Result<T, superschematic_versiongraph_engine::Error>
where
    F: Future<Output = Result<T, superschematic_versiongraph_engine::Error>>,
{
    ready(future)?
}

/// What a graph operation run over the call's SQL failed with: the graph
/// engine's error, or a statement the behavior engine refused, which is
/// the call's to report as it is.
#[derive(Debug)]
pub enum GraphFailure {
    Graph(superschematic_versiongraph_engine::Error),
    Engine(Error),
}

/// Runs one graph operation over the call's SQL, on the calling thread:
/// `op` calls an operation of the [`EngineOn`] it is given and polls it
/// with [`sync`]. While it runs, a layout built with [`call_clock`] and
/// [`call_ids`] reads the call's time and draws from its UUID source.
pub fn run<T>(
    engine: &GraphEngine,
    layout: &Arc<FixedLayout>,
    call: &GraphCall<'_>,
    op: impl FnOnce(EngineOn<'_>) -> Result<T, superschematic_versiongraph_engine::Error>,
) -> Result<T, GraphFailure> {
    let mut host = GraphSql::new(call.reader, call.writer);
    let result = {
        let storage = layout.bind(HostClient::new(&mut host));
        let _call = CallState::set(call.now_ms.saturating_mul(1000), call.uuids.clone());
        op(engine.on(&storage))
    };
    host.settle(result)
}

// CallState sets the thread's call time and UUID source and puts back the
// ones before them when it goes, however the operation ends.
struct CallState {
    micros: i64,
    uuids: Option<UuidSource>,
}

impl CallState {
    fn set(micros: i64, uuids: UuidSource) -> Self {
        CallState {
            micros: NOW.with(|now| now.replace(micros)),
            uuids: UUIDS.with(|source| source.replace(Some(uuids))),
        }
    }
}

impl Drop for CallState {
    fn drop(&mut self) {
        NOW.with(|now| now.set(self.micros));
        let before = self.uuids.take();
        UUIDS.with(|source| *source.borrow_mut() = before);
    }
}

/// A hook's SQL as the transaction the fixed layout runs in ([`HostTx`]).
pub struct GraphSql<'a> {
    reader: SqlReader<'a>,
    writer: Option<SqlWriter<'a>>,
    /// The engine's error behind the last statement that failed.
    failure: Option<Error>,
}

impl<'a> GraphSql<'a> {
    /// The call's SQL: `writer` runs the statements that write, and is
    /// `None` in a read, which refuses them.
    pub fn new(reader: SqlReader<'a>, writer: Option<SqlWriter<'a>>) -> Self {
        GraphSql {
            reader,
            writer,
            failure: None,
        }
    }

    /// The result of an operation run over it: a failure of its storage
    /// that the engine refused a statement for is the engine's error.
    pub fn settle<T>(
        self,
        result: Result<T, superschematic_versiongraph_engine::Error>,
    ) -> Result<T, GraphFailure> {
        match (result, self.failure) {
            (Ok(value), _) => Ok(value),
            (Err(superschematic_versiongraph_engine::Error::Storage(_)), Some(failure)) => {
                Err(GraphFailure::Engine(failure))
            }
            (Err(error), _) => Err(GraphFailure::Graph(error)),
        }
    }

    // refused keeps the engine's error and gives the layout its own: with
    // SQLite's result code for a unique index, which the layout reads as a
    // name already taken.
    fn refused(&mut self, error: Error) -> ClientError {
        let code = match &error {
            Error::Storage(storage) if storage.is_unique() => Some(SQLITE_CONSTRAINT_UNIQUE),
            Error::Storage(storage) => storage.sqlite_code(),
            _ => None,
        };
        let client = ClientError {
            code,
            source: error.to_string().into(),
        };
        self.failure = Some(error);
        client
    }
}

impl HostTx for GraphSql<'_> {
    fn query(
        &mut self,
        sql: &str,
        args: &[GraphValue],
    ) -> Result<Vec<Vec<GraphValue>>, ClientError> {
        let params: Vec<SqlValue> = args.iter().map(engine_value).collect();
        match self.reader.all(sql, &params) {
            Ok(rows) => Ok(rows
                .into_iter()
                .map(|row| row.into_values().into_iter().map(graph_value).collect())
                .collect()),
            Err(error) => Err(self.refused(error)),
        }
    }

    fn execute(&mut self, sql: &str, args: &[GraphValue]) -> Result<u64, ClientError> {
        let Some(writer) = self.writer else {
            return Err(ClientError::new(
                "a read of the graph ran a statement that writes",
            ));
        };
        let params: Vec<SqlValue> = args.iter().map(engine_value).collect();
        match writer.run(sql, &params) {
            Ok(outcome) => Ok(outcome.changes),
            Err(error) => Err(self.refused(error)),
        }
    }
}

/// A layout's argument as the behavior engine binds it.
pub fn engine_value(value: &GraphValue) -> SqlValue {
    match value {
        GraphValue::Null => SqlValue::Null,
        GraphValue::Int(n) => SqlValue::Integer(*n),
        GraphValue::Real(x) => SqlValue::Real(*x),
        GraphValue::Text(text) => SqlValue::Text(text.clone()),
        GraphValue::Blob(bytes) => SqlValue::Blob(bytes.clone()),
    }
}

/// A column the behavior engine read, as the layout reads it.
pub fn graph_value(value: SqlValue) -> GraphValue {
    match value {
        SqlValue::Null => GraphValue::Null,
        SqlValue::Integer(n) => GraphValue::Int(n),
        SqlValue::Real(x) => GraphValue::Real(x),
        SqlValue::Text(text) => GraphValue::Text(text),
        SqlValue::Blob(bytes) => GraphValue::Blob(bytes),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Each storage class crosses unchanged in both directions.
    #[test]
    fn values_cross_unchanged() {
        for value in [
            GraphValue::Null,
            GraphValue::Int(-7),
            GraphValue::Real(1.5),
            GraphValue::Text("a'b".into()),
            GraphValue::Blob(vec![0, 255]),
        ] {
            assert_eq!(graph_value(engine_value(&value)), value);
        }
    }

    /// The clock reads the time run sets, in microseconds, and the time
    /// before it once the operation ends.
    #[test]
    fn the_clock_reads_the_calls_time() {
        let clock = call_clock();
        NOW.with(|now| now.set(5));
        {
            let _call = CallState::set(1_800_000_000_000 * 1000, UuidSource::random());
            assert_eq!(clock(), 1_800_000_000_000_000);
        }
        assert_eq!(clock(), 5);
    }
}
