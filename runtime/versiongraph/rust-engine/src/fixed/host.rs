//! The fixed layout inside a synchronous transaction a host holds (D58):
//! [`HostTx`], what a host implements over its transaction, and
//! [`HostClient`], which makes it the layout's [`Client`]. Every future the
//! client returns is ready when first polled, so [`crate::Engine::run_on`]
//! runs an operation over it on the calling thread, inside the host's
//! transaction.

use std::cell::{RefCell, RefMut};
use std::thread::{self, ThreadId};

use async_trait::async_trait;

use super::{Client, ClientError, Conn, SqlValue};

/// A synchronous transaction a host holds, which the fixed layout runs its
/// statements in: D58's engine implements it over the transaction of the
/// call a behavior runs in.
///
/// A statement's placeholders are `?`, each taking the next argument in
/// order, one argument for each; the layout's statements in its dialect
/// hold no other `?` outside quotes, so a host on Postgres numbers them
/// `$1`, `$2`, ... in order. Arguments and columns are [`SqlValue`]s, as
/// [`SqlValue`] says for each dialect: on Postgres every parameter is
/// `TEXT` or `BIGINT`, and every column read is text or an integer.
///
/// `begin`, `commit` and `rollback` bound each engine operation. By
/// default they issue nothing, and the operation runs in the host's
/// transaction itself with no transaction control, as the TypeScript
/// adapter's `callerTransaction` does for a D16 behavior: the host rolls
/// back what an operation that failed wrote, as D16's engine rolls back a
/// call that fails. A host that goes on after an operation fails, as it
/// may after a version conflict, runs each in a savepoint of its own
/// instead (on Postgres, an operation whose statement failed leaves the
/// transaction refusing every statement until it rolls back to one).
pub trait HostTx {
    /// Runs a statement and returns its rows, each as its columns' values,
    /// in the order the statement selects them.
    fn query(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError>;
    /// Runs a statement that returns no rows, and returns how many rows it
    /// changed.
    fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError>;
    /// Begins the transaction one engine operation runs in, inside the
    /// host's: by default nothing.
    fn begin(&mut self) -> Result<(), ClientError> {
        Ok(())
    }
    /// Ends an operation's transaction that succeeded: by default nothing.
    fn commit(&mut self) -> Result<(), ClientError> {
        Ok(())
    }
    /// Ends an operation's transaction that failed: by default nothing,
    /// leaving its writes for the host to roll back.
    fn rollback(&mut self) -> Result<(), ClientError> {
        Ok(())
    }
}

impl<T: HostTx + ?Sized> HostTx for &mut T {
    fn query(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        (**self).query(sql, args)
    }

    fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        (**self).execute(sql, args)
    }

    fn begin(&mut self) -> Result<(), ClientError> {
        (**self).begin()
    }

    fn commit(&mut self) -> Result<(), ClientError> {
        (**self).commit()
    }

    fn rollback(&mut self) -> Result<(), ClientError> {
        (**self).rollback()
    }
}

/// A host's synchronous transaction, borrowed, as the fixed layout's
/// [`Client`]. Bind it with [`crate::fixed::FixedLayout::bind`] for a
/// storage that lives as long as the borrow, and run each operation over
/// that with [`crate::Engine::run_on`].
///
/// It numbers nothing and awaits nothing: it rewrites each of the layout's
/// statements from its numbered placeholders (`?1`, which may repeat) to
/// `?` in order, with the arguments to match, and runs it on the host's
/// transaction at once, so every future it returns is ready when first
/// polled. Its `exec` runs a statement with no arguments in the host's
/// transaction too.
///
/// The host's transaction need be neither `Send` nor `Sync`, as a
/// connection's transaction often is not, though [`Client`] is both: the
/// client refuses to reach it from any thread but the one that made the
/// client, with an error, and never reaches it twice at once.
pub struct HostClient<'h, H: ?Sized> {
    owner: ThreadId,
    host: RefCell<&'h mut H>,
}

// SAFETY: the host's transaction, and the RefCell around it, are reached
// only through `HostClient::host`, which refuses every thread but `owner`,
// the one that made the client, before it touches either. So they are used
// on one thread alone, as a value that is neither Send nor Sync may be,
// however the client or a reference to it moves; a ThreadId is never
// reused. Moving or dropping the client elsewhere touches neither: it holds
// a reference, which has no destructor.
unsafe impl<H: ?Sized> Send for HostClient<'_, H> {}
// SAFETY: as for Send.
unsafe impl<H: ?Sized> Sync for HostClient<'_, H> {}

impl<'h, H: HostTx + ?Sized> HostClient<'h, H> {
    /// The host's transaction as a client, on the calling thread.
    pub fn new(host: &'h mut H) -> Self {
        HostClient {
            owner: thread::current().id(),
            host: RefCell::new(host),
        }
    }

    /// The host's transaction, on the thread that made the client and while
    /// no statement of the client holds it.
    fn host(&self) -> Result<RefMut<'_, &'h mut H>, ClientError> {
        if thread::current().id() != self.owner {
            return Err(ClientError::new(
                "a host's transaction is reached only on the thread that bound it",
            ));
        }
        self.host.try_borrow_mut().map_err(|_| {
            ClientError::new("a host's transaction is already running a statement of the client")
        })
    }
}

impl<H: ?Sized> std::fmt::Debug for HostClient<'_, H> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("HostClient")
            .field("owner", &self.owner)
            .finish_non_exhaustive()
    }
}

#[async_trait]
impl<H: HostTx + ?Sized> Client for HostClient<'_, H> {
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
        self.host()?.begin()?;
        Ok(Box::new(HostConn {
            client: self,
            ended: false,
        }))
    }

    async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        let (sql, args) = positional(sql, &[])?;
        self.host()?.query(&sql, &args)
    }
}

/// One operation's transaction inside the host's.
struct HostConn<'c, 'h, H: HostTx + ?Sized> {
    client: &'c HostClient<'h, H>,
    ended: bool,
}

/// An operation dropped before it ended, as a future dropped unfinished is,
/// rolls its transaction back, on the thread that bound the host.
impl<H: HostTx + ?Sized> Drop for HostConn<'_, '_, H> {
    fn drop(&mut self) {
        if !self.ended {
            if let Ok(mut host) = self.client.host() {
                let _ = host.rollback();
            }
        }
    }
}

#[async_trait]
impl<H: HostTx + ?Sized> Conn for HostConn<'_, '_, H> {
    async fn query(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        let (sql, args) = positional(sql, args)?;
        self.client.host()?.query(&sql, &args)
    }

    async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        let (sql, args) = positional(sql, args)?;
        self.client.host()?.execute(&sql, &args)
    }

    async fn commit(mut self: Box<Self>) -> Result<(), ClientError> {
        self.ended = true;
        self.client.host()?.commit()
    }

    async fn rollback(mut self: Box<Self>) -> Result<(), ClientError> {
        self.ended = true;
        self.client.host()?.rollback()
    }
}

/// A statement with numbered placeholders (`?1`, `?2`, ..., each possibly
/// more than once) as one with `?` placeholders taking `args` in the order
/// they appear, and those arguments. It reads past quoted text and quoted
/// names, and refuses a bare `?` and a number past the arguments.
pub(crate) fn positional(
    sql: &str,
    args: &[SqlValue],
) -> Result<(String, Vec<SqlValue>), ClientError> {
    let mut out = String::with_capacity(sql.len());
    let mut ordered = Vec::with_capacity(args.len());
    let mut chars = sql.char_indices().peekable();
    while let Some((_, c)) = chars.next() {
        match c {
            '\'' | '"' => {
                // A quoted text or name runs to its closing quote; a doubled
                // quote inside it is one quote.
                out.push(c);
                loop {
                    match chars.next() {
                        Some((_, q)) if q == c => {
                            out.push(q);
                            if chars.peek().is_some_and(|&(_, next)| next == c) {
                                chars.next();
                                out.push(c);
                            } else {
                                break;
                            }
                        }
                        Some((_, other)) => out.push(other),
                        None => {
                            return Err(ClientError::new(format!(
                                "a statement ends inside a quote: {sql}"
                            )))
                        }
                    }
                }
            }
            '?' => {
                let mut number = String::new();
                while let Some(&(_, d)) = chars.peek() {
                    if !d.is_ascii_digit() {
                        break;
                    }
                    number.push(d);
                    chars.next();
                }
                let n: usize = number.parse().map_err(|_| {
                    ClientError::new(format!(
                        "a placeholder of the statement is not numbered: {sql}"
                    ))
                })?;
                let arg = n.checked_sub(1).and_then(|i| args.get(i)).ok_or_else(|| {
                    ClientError::new(format!(
                        "placeholder ?{n} of the statement has no argument ({} given): {sql}",
                        args.len()
                    ))
                })?;
                out.push('?');
                ordered.push(arg.clone());
            }
            other => out.push(other),
        }
    }
    Ok((out, ordered))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text(s: &str) -> SqlValue {
        SqlValue::Text(s.to_owned())
    }

    /// Numbered placeholders become `?` in order, with an argument for
    /// each, repeated where the number repeats; quoted text and names keep
    /// what they hold, a `?` included.
    #[test]
    fn numbered_placeholders_become_positional_ones() {
        let args = [text("a"), SqlValue::Int(2), SqlValue::Null];
        let (sql, ordered) = positional(
            "INSERT INTO \"t?\" (x, 'y''?') VALUES (?1, ?3, ?2, ?1) RETURNING '?1', \"a\"\"?2\"",
            &args,
        )
        .expect("positional");
        assert_eq!(
            sql,
            "INSERT INTO \"t?\" (x, 'y''?') VALUES (?, ?, ?, ?) RETURNING '?1', \"a\"\"?2\""
        );
        assert_eq!(
            ordered,
            [text("a"), SqlValue::Null, SqlValue::Int(2), text("a")]
        );
        let (sql, ordered) = positional(
            "SELECT ?10, ?1",
            &(1..=10).map(SqlValue::Int).collect::<Vec<_>>(),
        )
        .expect("two digits");
        assert_eq!(sql, "SELECT ?, ?");
        assert_eq!(ordered, [SqlValue::Int(10), SqlValue::Int(1)]);
        for (statement, refused) in [
            ("SELECT ?", "not numbered"),
            ("SELECT ?0", "has no argument"),
            ("SELECT ?4", "has no argument"),
            ("SELECT 'open", "inside a quote"),
            ("SELECT \"open", "inside a quote"),
        ] {
            let error = positional(statement, &args).expect_err(statement);
            assert!(error.to_string().contains(refused), "{statement}: {error}");
        }
    }

    /// A host that records what it was asked to run.
    #[derive(Default)]
    struct Recorder {
        ran: Vec<String>,
    }

    impl HostTx for Recorder {
        fn query(
            &mut self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            self.ran.push(format!("query {sql} {args:?}"));
            Ok(vec![vec![SqlValue::Int(1)]])
        }

        fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            self.ran.push(format!("execute {sql} {args:?}"));
            Ok(1)
        }

        fn rollback(&mut self) -> Result<(), ClientError> {
            self.ran.push("rollback".to_owned());
            Ok(())
        }
    }

    /// The client runs each statement on the host's transaction at once,
    /// with positional placeholders, and every future it returns is ready
    /// at its first poll; it refuses another thread, and a dropped
    /// transaction rolls back.
    #[test]
    fn the_client_runs_each_statement_at_once_on_its_thread_alone() {
        let mut recorder = Recorder::default();
        {
            let client = HostClient::new(&mut recorder);
            let mut conn = crate::ready(client.begin()).expect("ready").expect("begin");
            let rows = crate::ready(conn.query("SELECT ?2, ?1", &[text("a"), text("b")]))
                .expect("ready")
                .expect("query");
            assert_eq!(rows, [[SqlValue::Int(1)]]);
            crate::ready(conn.execute("DELETE FROM t WHERE x = ?1", &[SqlValue::Int(7)]))
                .expect("ready")
                .expect("execute");
            crate::ready(conn.commit()).expect("ready").expect("commit");
            thread::scope(|scope| {
                let elsewhere = scope
                    .spawn(|| crate::ready(client.exec("SELECT 1")).expect("ready"))
                    .join()
                    .expect("the thread");
                let error = elsewhere.expect_err("another thread");
                assert!(
                    error
                        .to_string()
                        .contains("only on the thread that bound it"),
                    "{error}"
                );
            });
            let dropped = crate::ready(client.begin()).expect("ready").expect("begin");
            drop(dropped);
        }
        assert_eq!(
            recorder.ran,
            [
                "query SELECT ?, ? [Text(\"b\"), Text(\"a\")]",
                "execute DELETE FROM t WHERE x = ? [Int(7)]",
                "rollback",
            ]
        );
    }
}
