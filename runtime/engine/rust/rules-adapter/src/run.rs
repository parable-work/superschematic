//! The host loop (D103), written once over a store: the core's README's
//! "The host loop" and "The adapter", step for step as the HTTP runtime's
//! `src/rules.ts` runs them, so a refusal, a statement and a message are
//! the same in every language. The Postgres and SQLite modules each give
//! it a [`Store`] over the caller's transaction.
//!
//! The store's methods are `async`: Postgres's await the client, and
//! SQLite's do their work at once, which the SQLite module drives to its
//! end with one poll.

use std::collections::{BTreeMap, HashSet};

use serde::Serialize;
use serde_json::{Map, Value};
use superschematic_rules::{
    Action, Answer, Answered, Answers, Caller, ChangeSet, Code, Command, Core, DeadlineChange,
    KeyedRow, LinkedRows, Outcome, ReadRequest, Refusal, Row, RowChange, RowImage, RowWrite, Step,
    SubRow, SubRowChange, SubRowWrite, TypeSpec, ValidationIssue, WriteKind,
};

use crate::convert::{base62, from_cell, internal, key_value, to_column, Bind, Cell, UUID_TYPES};
use crate::host::Host;
use crate::layout::{Changes, Column, Deadlines, Layout, SubTable, Table};
use crate::{busy_refusal, CreateOptions, Error, ExecuteOptions, Executed, Owned};
use crate::{ChangeRun, Claim, Deadline, DeadlineRun, TableChange};
use crate::{MAX_ATTEMPTS, MAX_ROUNDS, VETOED_RETRY_SECONDS};

/// The SQL a store's statements are written in. A build with one store
/// writes its dialect alone.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[cfg_attr(not(all(feature = "postgres", feature = "sqlite")), allow(dead_code))]
pub(crate) enum Dialect {
    /// `$n` placeholders; a column read as `to_jsonb`'s JSON.
    Postgres,
    /// `?n` placeholders; a column read as it is stored.
    Sqlite,
}

/// One statement of a change set's apply, its placeholders the store's
/// dialect's.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct Statement {
    /// A write, or with `check` a condition: an SQL boolean expression,
    /// `EXISTS (...)` or `NOT EXISTS (...)`.
    pub text: String,
    pub values: Vec<Bind>,
    /// The rows the write must change: the unit is moved, and nothing of
    /// it applied, when it changes another number. `None` for any.
    pub expect: Option<u64>,
    /// `text` is a condition the unit holds to where it stands: the unit is
    /// moved, and nothing of it applied, when it is false.
    pub check: bool,
}

impl Statement {
    fn write(text: String, values: Vec<Bind>) -> Self {
        Statement {
            text,
            values,
            expect: Some(1),
            check: false,
        }
    }

    /// A write that may change any number of rows.
    fn any(text: String, values: Vec<Bind>) -> Self {
        Statement {
            text,
            values,
            expect: None,
            check: false,
        }
    }

    fn check(text: String, values: Vec<Bind>) -> Self {
        Statement {
            text,
            values,
            expect: None,
            check: true,
        }
    }
}

/// What the host loop reads and writes through: the caller's transaction.
pub(crate) trait Store {
    fn dialect(&self) -> Dialect;

    /// Whether the store's locks are fenced: it takes none, since its reads
    /// may come before the unit that writes, so the loop puts first in that
    /// unit a check of each read made under a lock (SQLite). Otherwise a
    /// lock is one the transaction holds until it ends (Postgres).
    fn fenced(&self) -> bool;

    /// Runs a read in the transaction: each row's cells in select order.
    async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error>;

    /// Runs statements as one unit, all of them or none: true when each
    /// changed the rows it expects and each check held; false, with none
    /// applied, when one did not; an error, with none applied, on a failure
    /// of the database. An empty unit applies with no statement at all.
    async fn apply(&self, statements: &[Statement]) -> Result<bool, Error>;

    /// Takes a lock the transaction holds until it ends, or none on a store
    /// whose locks are fenced.
    async fn lock(&self, key: &str) -> Result<(), Error>;

    /// Whether an error of the store's is another's lock keeping it from
    /// the database (SQLite's `SQLITE_BUSY`): the command is then refused
    /// as `busy`, nothing of it written, for the caller to roll back and
    /// try again.
    fn busy(&self, _error: &Error) -> bool {
        false
    }
}

/// A store's error as a command returns it: the `busy` refusal, after the
/// runs made so far, when another's lock kept the store from the database;
/// the error itself otherwise.
fn busy_or<S: Store>(
    store: &S,
    error: Error,
    type_name: &str,
    command: &Command,
    attempts: usize,
) -> Error {
    if store.busy(&error) {
        return busy_refusal(type_name, command, attempts).into();
    }
    error
}

/// Runs one command on a type of the layout to its outcome, in the store's
/// transaction: answers its reads, takes its locks, applies its change set
/// whole with each row fenced on its version, and runs it again from fresh
/// state when a row moved, up to [`MAX_ATTEMPTS`] times. A deadline command
/// runs only while its deadline is armed at its `at`, read before each run
/// and spent in the unit that applies its change set (D103).
pub(crate) async fn execute<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    command: Command,
    options: &ExecuteOptions,
) -> Result<Executed, Error> {
    match attempts(host, store, type_name, command, options, Spent::Nothing).await? {
        DeadlineRun::Ran(executed) => Ok(*executed),
        // A command with no claim that spends nothing on a refusal comes
        // to neither.
        DeadlineRun::ClaimLost | DeadlineRun::Gone(_) | DeadlineRun::Vetoed(_) => Err(internal(
            format!("a command on {type_name} ran nothing and was refused nothing"),
        )
        .into()),
    }
}

/// Runs a due deadline of a row (D103) at the later of the host's time and
/// the time it was armed to fall due at, so a host whose clock lags the
/// database's never runs it before it falls due in the core.
///
/// With a claim of its message, the unit that applies the command's change
/// set spends the claim (its message deleted by its id and token, right
/// after the command's own row write and before the change set's deadline
/// changes), so a deadline armed or cleared since the claim, which deleted
/// the claimed message, never runs on its old arming: the claim is read
/// when the unit does not apply, and one that is gone ran nothing
/// ([`DeadlineRun::ClaimLost`]). Without one, the deadline is read armed at
/// its `at` before each run, and its message spent in the unit by its row,
/// use, name and `at`.
///
/// A row or a use that is gone (the core's `not_found` while the table
/// holds no live row of the key, or the layout no such use) spends the
/// arming in a unit of its own: [`DeadlineRun::Gone`] with the refusal, or
/// [`DeadlineRun::ClaimLost`] when the claim was lost first. With a claim,
/// a fire a guard vetoes (the core's `vetoed`) releases the claimed
/// message in a unit of its own, due again in
/// [`crate::VETOED_RETRY_SECONDS`] and counting no attempt:
/// [`DeadlineRun::Vetoed`] with the veto, or [`DeadlineRun::ClaimLost`]
/// when the claim was lost first. Any other refusal comes back as
/// [`Error::Refused`], nothing applied, a `not_found` of something else a
/// rule reached among them, and so does a veto without a claim.
pub(crate) async fn run_deadline<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    deadline: &Deadline,
    caller: Caller,
    claim: Option<&Claim>,
) -> Result<DeadlineRun, Error> {
    let action = Action::Deadline {
        key: deadline.key.clone(),
        use_id: deadline.use_id.clone(),
        name: deadline.name.clone(),
        at: deadline.at,
        payload: deadline.payload.clone(),
    };
    let now = host.now().max(deadline.at);
    let command = Command::new(caller, now, action).with_request(host.request());
    attempts(
        host,
        store,
        type_name,
        command,
        &ExecuteOptions::default(),
        Spent::Deadline(claim),
    )
    .await
}

/// Runs a write of a row of a type that the table's change trigger wrote
/// to its change queue (D103), as the core's change command, at the host's
/// time: the uses that hear the row's writes, on the row as it is now, and
/// the listeners of other types its relations reach.
///
/// With a claim of its message, the unit that applies the command's change
/// set spends the claim (its message deleted by its id and token, right
/// after the change set's row writes and before its sub-table rows and
/// deadlines), and one whose claim no longer holds its message runs nothing
/// ([`ChangeRun::ClaimLost`]); a row that moved runs it again from fresh
/// state. Without one, nothing is spent. A refusal comes back as
/// [`Error::Refused`], nothing applied.
pub(crate) async fn run_change<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    change: &TableChange,
    caller: Caller,
    claim: Option<&Claim>,
) -> Result<ChangeRun, Error> {
    let table = table_of(host.layout, type_name)?;
    let key = change_key(table, &change.key)?;
    let action = Action::Change {
        change: row_change(table, store.dialect(), &key, change)?,
        key,
    };
    let command = command_of(host, caller, action);
    let spent = match claim {
        Some(claim) => Spent::Change(claim),
        None => Spent::Nothing,
    };
    let options = ExecuteOptions::default();
    match attempts(host, store, type_name, command, &options, spent).await? {
        DeadlineRun::Ran(executed) => Ok(ChangeRun::Ran(executed)),
        DeadlineRun::ClaimLost => Ok(ChangeRun::ClaimLost),
        DeadlineRun::Gone(_) | DeadlineRun::Vetoed(_) => Err(internal(format!(
            "a change of {type_name} ran nothing and was refused nothing"
        ))
        .into()),
    }
}

/// The key of a change's row as the core takes it: the message's key as a
/// row read converts the key column (a UUID's base62 from either form).
fn change_key(table: &Table, key: &str) -> Result<String, Error> {
    match from_cell(Cell::Text(key.to_owned()), &table.key, table)? {
        Some(Value::String(key)) => Ok(key),
        Some(other) => Ok(other.to_string()),
        None => Err(internal(format!("{}'s change names no row's key", table.type_name)).into()),
    }
}

/// A change's message of the row `key` as the core's [`RowChange`]: its
/// write, its time, and its images before and after it, each mapped from
/// its columns to the core's names by the layout and converted as a row
/// read in `dialect` converts each column.
fn row_change(
    table: &Table,
    dialect: Dialect,
    key: &str,
    change: &TableChange,
) -> Result<RowChange, Error> {
    let write = match change.write.as_str() {
        "insert" => WriteKind::Insert,
        "update" => WriteKind::Update,
        "delete" => WriteKind::Delete,
        other => {
            return Err(internal(format!(
                "{}'s change of {key} is a write of {other}, which is no insert, update or delete",
                table.type_name
            ))
            .into())
        }
    };
    Ok(RowChange {
        write,
        at: change.at,
        before: row_image(table, dialect, &change.before)?,
        after: row_image(table, dialect, &change.after)?,
    })
}

/// One image of a change, `None` for none (null, or JSON text of null): a
/// JSON object by column, or SQLite's text of one, each member a layout own
/// column mapped to its own field and each use's owned column to that use's
/// declared field, a member the layout does not map left out, and a value
/// with none absent.
fn row_image(table: &Table, dialect: Dialect, image: &Value) -> Result<Option<RowImage>, Error> {
    let parsed;
    let image = match image {
        Value::String(text) => {
            parsed = serde_json::from_str::<Value>(text).map_err(|error| {
                internal(format!(
                    "{}'s change holds an image that is no JSON: {error}",
                    table.type_name
                ))
            })?;
            &parsed
        }
        other => other,
    };
    let columns = match image {
        Value::Null => return Ok(None),
        Value::Object(columns) => columns,
        other => {
            return Err(internal(format!(
                "{}'s change holds an image that is no object: {other}",
                table.type_name
            ))
            .into())
        }
    };
    let value = |column: &Column| -> Result<Option<Value>, Error> {
        let Some(value) = columns.get(&column.column) else {
            return Ok(None);
        };
        Ok(from_cell(image_cell(dialect, value), column, table)?)
    };
    let mut row = RowImage::default();
    for column in &table.own {
        if let Some(value) = value(column)? {
            row.own.insert(column.field.clone(), value);
        }
    }
    for used in &table.uses {
        let mut values = Map::new();
        for field in &used.fields {
            if let Some(value) = value(&field.column)? {
                values.insert(field.name.clone(), value);
            }
        }
        if !values.is_empty() {
            row.owned.insert(used.id.clone(), values);
        }
    }
    Ok(Some(row))
}

/// A value of an image as the cell a row read in `dialect` reads of its
/// column: on Postgres the `to_jsonb` JSON `jsonb_build_object` wrote; on
/// SQLite the storage class `json_object` wrote it from, a string as TEXT,
/// an integer as INTEGER, any other number as REAL and a boolean as 1 or 0.
fn image_cell(dialect: Dialect, value: &Value) -> Cell {
    match (dialect, value) {
        (_, Value::Null) => Cell::Null,
        (Dialect::Postgres, value) => Cell::Json(value.clone()),
        (Dialect::Sqlite, Value::String(text)) => Cell::Text(text.clone()),
        (Dialect::Sqlite, Value::Bool(flag)) => Cell::Int(i64::from(*flag)),
        (Dialect::Sqlite, Value::Number(number)) => match number.as_i64() {
            Some(n) => Cell::Int(n),
            None => number.as_f64().map_or(Cell::Null, Cell::Real),
        },
        (Dialect::Sqlite, value) => Cell::Json(value.clone()),
    }
}

/// What the unit that applies a run's change set spends beside it.
#[derive(Clone, Copy)]
enum Spent<'a> {
    /// Nothing but what the command itself holds: a caller's command, or
    /// a deadline command run as a transcript runs it, which spends the
    /// arming it ran for by its `at`.
    Nothing,
    /// A due deadline's arming, by a worker's claim of its message or,
    /// without one, by its `at`: a row or a use the core finds gone spends
    /// it in a unit of its own, and a fire a guard vetoes with a claim
    /// releases its message.
    Deadline(Option<&'a Claim>),
    /// A change's message of the table's change queue, by a worker's claim
    /// of it (D103).
    Change(&'a Claim),
}

impl<'a> Spent<'a> {
    /// The worker's claim the unit spends, when it spends one.
    fn claim(self) -> Option<&'a Claim> {
        match self {
            Spent::Nothing => None,
            Spent::Deadline(claim) => claim,
            Spent::Change(claim) => Some(claim),
        }
    }
}

/// The runs of one command: each settles the core on fresh reads and
/// applies its change set as one unit, until a unit applies, a claim is
/// lost or the attempts run out. A deadline run ([`Spent::Deadline`])
/// spends a deadline whose row or use the core finds gone, and releases
/// one whose fire a guard vetoed with a claim, each in a unit of its own.
async fn attempts<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    command: Command,
    options: &ExecuteOptions,
    spent: Spent<'_>,
) -> Result<DeadlineRun, Error> {
    let table = table_of(host.layout, type_name)?;
    refuse_checked(host, type_name)?;
    let run = prepared(host, table, command)?;
    refuse_owned_writes(table, &run.action)?;
    let claim = spent.claim();
    let deadline = matches!(spent, Spent::Deadline(_));
    let due = match &run.action {
        Action::Deadline {
            key,
            use_id,
            name,
            at,
            ..
        } => Some(Due {
            table,
            queue: queue_of(table, use_id, name)?,
            key,
            use_id,
            name,
            at: *at,
        }),
        _ => None,
    };
    // The queue whose claimed message the unit spends.
    let claimed = match (spent, &due) {
        (Spent::Change(_), _) => Some(changes_of(table)?.table.as_str()),
        (Spent::Deadline(Some(_)), Some(due)) => Some(due.queue.table.as_str()),
        _ => None,
    };
    for attempt in 1..=MAX_ATTEMPTS {
        let busy = |error| busy_or(store, error, type_name, &run, attempt);
        let spend = match (&due, spent) {
            (_, Spent::Change(claim)) => Some(spend_claim(store.dialect(), table, claim)?),
            (Some(due), _) => {
                if claim.is_none() {
                    due.armed(store, run.now).await.map_err(busy)?;
                }
                Some(due.spend(store.dialect(), claim)?)
            }
            (None, _) => None,
        };
        let (changes, answers) = match settle(host, store, &table.spec, &run).await {
            Ok(settled) => settled,
            Err(Error::Refused(refusal)) if deadline && refusal.code == Code::NotFound => {
                let (Some(due), Some(spend)) = (&due, spend) else {
                    return Err(Error::Refused(refusal));
                };
                if !due.gone(store).await.map_err(busy)? {
                    return Err(Error::Refused(refusal));
                }
                // The row or the use is gone, and so is the arming once
                // this unit spends it.
                if store.apply(&[spend]).await.map_err(busy)? {
                    return Ok(DeadlineRun::Gone(refusal));
                }
                if claim.is_some() {
                    return Ok(DeadlineRun::ClaimLost);
                }
                continue;
            }
            Err(Error::Refused(refusal)) if deadline && refusal.code == Code::Vetoed => {
                let (Some(due), Some(claim)) = (&due, claim) else {
                    return Err(Error::Refused(refusal));
                };
                // A guard holds the fire: the message falls due again in a
                // minute, as the engine's sweep tried it at every run, and
                // its sub-table row stays pending (D103).
                if store
                    .apply(&[due.release(store.dialect(), claim)])
                    .await
                    .map_err(busy)?
                {
                    return Ok(DeadlineRun::Vetoed(refusal));
                }
                return Ok(DeadlineRun::ClaimLost);
            }
            Err(error) => return Err(busy(error)),
        };
        let mut statements = fences_of(host.layout, store, &answers)?;
        statements.extend(
            statements_of(
                host.layout,
                store,
                type_name,
                &run,
                &changes,
                options,
                spend,
            )
            .await
            .map_err(busy)?,
        );
        if store.apply(&statements).await.map_err(busy)? {
            let result = changes.result.clone();
            return Ok(DeadlineRun::Ran(Box::new(Executed {
                changes,
                result,
                key: run.action.key().map(str::to_owned),
                attempts: attempt,
            })));
        }
        // A row moved, a check failed, or the claim was spent before this
        // unit could spend it: a claim armed or cleared since, or claimed
        // again after its lease, runs nothing.
        if let (Some(queue), Some(claim)) = (claimed, claim) {
            if !claim_held(store, queue, claim).await.map_err(busy)? {
                return Ok(DeadlineRun::ClaimLost);
            }
        }
    }
    Err(busy_refusal(type_name, &run, MAX_ATTEMPTS).into())
}

/// The change queue of a table a claimed change runs on, or an internal
/// refusal for a table the layout gives none.
fn changes_of(table: &Table) -> Result<&Changes, Error> {
    table.changes.as_ref().ok_or_else(|| {
        internal(format!(
            "a change of {type_name} ran with a claim of its message, and the behaviors' layout gives {type_name} no change queue",
            type_name = table.type_name
        ))
        .into()
    })
}

/// The statement that spends a claimed change, expecting one row: its
/// message of the table's change queue, by its id and its claim's token.
fn spend_claim(dialect: Dialect, table: &Table, claim: &Claim) -> Result<Statement, Error> {
    Ok(Statement::write(
        format!(
            "DELETE FROM {} WHERE {}",
            quote(&changes_of(table)?.table),
            claimed_where(dialect)
        ),
        vec![
            Bind::Text(claim.id.clone()),
            Bind::Text(claim.token.clone()),
        ],
    ))
}

/// The deadline a deadline command runs: its row's table and its queue, the
/// row's key, the use, the name and the time it was armed to fall due at.
struct Due<'a> {
    table: &'a Table,
    queue: &'a Deadlines,
    key: &'a str,
    use_id: &'a str,
    name: &'a str,
    at: i64,
}

impl Due<'_> {
    /// Whether the deadline's row or use is gone: the layout lists no such
    /// use of the table, or the table holds no live row of the key. A
    /// `not_found` the core gave while both are there is of something a
    /// rule reached, which the run refuses rather than spends.
    async fn gone<S: Store>(&self, store: &S) -> Result<bool, Error> {
        if self.table.uses.iter().all(|used| used.id != self.use_id) {
            return Ok(true);
        }
        Ok(read_row_of(store, self.table, self.key).await?.is_none())
    }

    /// Refuses the command unless the deadline is armed at its `at` and has
    /// fallen due by `now`, in the reference host's words.
    async fn armed<S: Store>(&self, store: &S, now: i64) -> Result<(), Error> {
        let dialect = store.dialect();
        let q = self.queue;
        let text = format!(
            "SELECT {}, {} FROM {} WHERE {} = {} AND {} = {} AND {} = {} AND \"state\" IN ('ready', 'claimed')",
            quote(&q.at),
            quote(&q.payload),
            quote(&q.table),
            quote(&q.row_key),
            placeholder(dialect, 1),
            quote(&q.use_id),
            placeholder(dialect, 2),
            quote(&q.name),
            placeholder(dialect, 3)
        );
        let values = [
            key_value(self.table, self.key)?,
            Bind::Text(self.use_id.to_owned()),
            Bind::Text(self.name.to_owned()),
        ];
        let rows = store.query(&text, &values).await?;
        let armed = rows
            .iter()
            .any(|cells| cells.first().and_then(integer_of) == Some(self.at));
        if !armed {
            return Err(Refusal::not_found(format!(
                "{} {} has no deadline {}.{} armed at {}",
                self.table.type_name, self.key, self.use_id, self.name, self.at
            ))
            .into());
        }
        if self.at > now {
            return Err(Refusal::invalid_argument(format!(
                "deadline {}.{} of {} {} falls due at {}, after {now}",
                self.use_id, self.name, self.table.type_name, self.key, self.at
            ))
            .into());
        }
        Ok(())
    }

    /// The statement that spends the arming, expecting one row: with a
    /// claim, the claimed message by its id and token; without one, the
    /// live message armed at `at`.
    fn spend(&self, dialect: Dialect, claim: Option<&Claim>) -> Result<Statement, Error> {
        let q = self.queue;
        Ok(match claim {
            Some(claim) => Statement::write(
                format!("DELETE FROM {} WHERE {}", quote(&q.table), claimed_where(dialect)),
                vec![Bind::Text(claim.id.clone()), Bind::Text(claim.token.clone())],
            ),
            None => Statement::write(
                format!(
                    "DELETE FROM {} WHERE {} = {} AND {} = {} AND {} = {} AND {} = {} AND \"state\" IN ('ready', 'claimed')",
                    quote(&q.table),
                    quote(&q.row_key),
                    placeholder(dialect, 1),
                    quote(&q.use_id),
                    placeholder(dialect, 2),
                    quote(&q.name),
                    placeholder(dialect, 3),
                    quote(&q.at),
                    placeholder(dialect, 4)
                ),
                vec![
                    key_value(self.table, self.key)?,
                    Bind::Text(self.use_id.to_owned()),
                    Bind::Text(self.name.to_owned()),
                    Bind::Int(self.at),
                ],
            ),
        })
    }

    /// The statement that releases the claimed message of a vetoed fire,
    /// expecting one row: ready again, due in
    /// [`crate::VETOED_RETRY_SECONDS`], its claim's attempt given back, as
    /// D80's release counts none.
    fn release(&self, dialect: Dialect, claim: &Claim) -> Statement {
        let (attempts, due_at, now) = match dialect {
            Dialect::Postgres => (
                "GREATEST(\"attempts\" - 1, 0)",
                "now() + make_interval(secs => $3)",
                "now()",
            ),
            Dialect::Sqlite => (
                "MAX(\"attempts\" - 1, 0)",
                "strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+' || (?3) || ' seconds')",
                "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')",
            ),
        };
        Statement::write(
            format!(
                "UPDATE {} SET \"state\" = 'ready', \"attempts\" = {attempts}, \"claim_token\" = NULL, \"claim_expires_at\" = NULL, \"due_at\" = {due_at}, \"updated_at\" = {now} WHERE {}",
                quote(&self.queue.table),
                claimed_where(dialect)
            ),
            vec![
                Bind::Text(claim.id.clone()),
                Bind::Text(claim.token.clone()),
                Bind::Int(VETOED_RETRY_SECONDS),
            ],
        )
    }
}

/// Whether a claim still holds its message of a queue's table: the
/// message's id and the claim's token, claimed.
async fn claim_held<S: Store>(store: &S, table: &str, claim: &Claim) -> Result<bool, Error> {
    let text = format!(
        "SELECT 1 FROM {} WHERE {}",
        quote(table),
        claimed_where(store.dialect())
    );
    let values = [
        Bind::Text(claim.id.clone()),
        Bind::Text(claim.token.clone()),
    ];
    Ok(!store.query(&text, &values).await?.is_empty())
}

/// The condition of a claimed message by its id and its claim's token.
fn claimed_where(dialect: Dialect) -> &'static str {
    match dialect {
        Dialect::Postgres => {
            "\"id\" = $1::uuid AND \"claim_token\" = $2::uuid AND \"state\" = 'claimed'"
        }
        Dialect::Sqlite => "\"id\" = ?1 AND \"claim_token\" = ?2 AND \"state\" = 'claimed'",
    }
}

/// A cell's integer: a deadline's `at` as a store reads it.
fn integer_of(cell: &Cell) -> Option<i64> {
    match cell {
        Cell::Int(n) => Some(*n),
        Cell::Real(x) if x.is_finite() && x.fract() == 0.0 => Some(*x as i64),
        Cell::Text(text) => text.trim().parse().ok(),
        Cell::Json(Value::Number(number)) => number.as_i64(),
        _ => None,
    }
}

/// The deadline queue of a table a deadline command runs on, or an internal
/// refusal for a table the layout gives none.
fn queue_of<'t>(table: &'t Table, use_id: &str, name: &str) -> Result<&'t Deadlines, Error> {
    table.deadlines.as_ref().ok_or_else(|| {
        internal(format!(
            "a deadline {use_id}.{name} of {type_name} ran, and the behaviors' layout gives {type_name} no deadline queue",
            type_name = table.type_name
        ))
        .into()
    })
}

/// Runs an operation of a use on a row and returns its result.
#[allow(clippy::too_many_arguments)]
pub(crate) async fn run_operation<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    key: &str,
    use_id: &str,
    operation: &str,
    params: Value,
    caller: Caller,
) -> Result<Value, Error> {
    let params = if params.is_null() {
        Value::Object(Map::new())
    } else {
        params
    };
    let action = Action::Operation {
        key: key.to_owned(),
        use_id: use_id.to_owned(),
        operation: operation.to_owned(),
        params,
    };
    let command = command_of(host, caller, action);
    let executed = execute(host, store, type_name, command, &ExecuteOptions::default()).await?;
    Ok(executed.result)
}

/// Creates a row through the core and returns its key.
pub(crate) async fn run_create<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<String, Error> {
    let action = Action::Create {
        id: options.id.clone(),
        own,
        params: options.params.clone(),
    };
    let command = command_of(host, caller, action);
    let executed = execute(host, store, type_name, command, &options.execute).await?;
    executed.key.ok_or_else(|| {
        internal(format!(
            "a create of {type_name} reached the adapter without a key"
        ))
        .into()
    })
}

/// Updates a row's own fields through the core with a merge patch.
pub(crate) async fn run_update<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    key: &str,
    patch: Map<String, Value>,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    let action = Action::Update {
        key: key.to_owned(),
        patch,
    };
    execute(
        host,
        store,
        type_name,
        command_of(host, caller, action),
        options,
    )
    .await?;
    Ok(())
}

/// Deletes a row through the core.
pub(crate) async fn run_delete<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    key: &str,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    let action = Action::Delete {
        key: key.to_owned(),
    };
    execute(
        host,
        store,
        type_name,
        command_of(host, caller, action),
        options,
    )
    .await?;
    Ok(())
}

/// Runs a create of a row through the core to its change set and applies
/// nothing, returning the owned fields the change set gives the row. A
/// change set that writes anything but the one row, or that reads under a
/// lock a fenced store could check only in a unit of its own, refuses the
/// create as internal.
pub(crate) async fn prepare_create<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<Owned, Error> {
    let table = table_of(host.layout, type_name)?;
    refuse_checked(host, type_name)?;
    let action = Action::Create {
        id: options.id.clone().filter(|id| !id.is_empty()),
        own,
        params: options.params.clone(),
    };
    let command = command_of(host, caller, action);
    refuse_owned_writes(table, &command.action)?;
    let (changes, answers) = settle(host, store, &table.spec, &command)
        .await
        .map_err(|error| busy_or(store, error, type_name, &command, 1))?;
    let alone =
        changes.sub_rows.is_empty() && changes.events.is_empty() && changes.deadlines.is_empty();
    let owned = match changes.rows.as_slice() {
        [RowWrite::Create {
            type_name: created,
            owned,
            ..
        }] if alone && created == type_name => owned,
        _ => {
            return Err(internal(format!(
                "an upsert of {type_name} inserts the row a create of the core gives it, and that create writes more than the row; create the row with a create of its own"
            ))
            .into())
        }
    };
    if !fences_of(host.layout, store, &answers)?.is_empty() {
        return Err(internal(format!(
            "an upsert of {type_name} binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own"
        ))
        .into());
    }
    Ok(owned.clone())
}

/// Reads a row of a type by its key as the core reads it; `None` when the
/// table holds no such row, or none that is live.
pub(crate) async fn read_row<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    key: &str,
) -> Result<Option<Row>, Error> {
    read_row_of(store, table_of(host.layout, type_name)?, key).await
}

/// Reads the rows of one sub-table of a use on a row of a type, as the core
/// reads them; none for a type the layout lacks.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub(crate) async fn read_sub_rows_of<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    key: &str,
    use_id: &str,
    table: &str,
) -> Result<Vec<SubRow>, Error> {
    let Some(of) = host.layout.table(type_name) else {
        return Ok(Vec::new());
    };
    read_sub_rows(store, of, sub_table_of(of, use_id, table)?, key).await
}

/// The command a wrapper builds: the caller, the host's time and a request
/// id of its own.
fn command_of(host: &Host<'_>, caller: Caller, action: Action) -> Command {
    Command::new(caller, host.now(), action).with_request(host.request())
}

/// The table of a type, or an internal refusal for a type the layout lacks.
fn table_of<'l>(layout: &'l Layout, type_name: &str) -> Result<&'l Table, Error> {
    layout.table(type_name).ok_or_else(|| {
        let known: Vec<&str> = layout.types.keys().map(String::as_str).collect();
        let held = if known.is_empty() {
            "none".to_owned()
        } else {
            known.join(", ")
        };
        internal(format!(
            "the behaviors' layout has no type {type_name}; it holds {held}"
        ))
        .into()
    })
}

/// Refuses a command on a type whose spec the layout's define check
/// refused, made once per layout and core.
fn refuse_checked(host: &Host<'_>, type_name: &str) -> Result<(), Error> {
    let refusals = host
        .layout
        .checked(host.core, |types| define_check(host.core, types));
    match refusals.get(type_name) {
        Some(refusal) => Err(refusal.clone().into()),
        None => Ok(()),
    }
}

/// The define check of each type's spec, as a binding's `check` runs it:
/// by a caller of no subject and no permissions, at time 0.
fn define_check(core: &Core, types: &BTreeMap<String, Table>) -> BTreeMap<String, Refusal> {
    let define = Command::new(Caller::default(), 0, Action::Define);
    types
        .iter()
        .filter_map(
            |(name, table)| match core.run(&table.spec, &define, &Answers::new()) {
                Step::Done(Outcome::Refused(refusal)) => Some((name.clone(), refusal)),
                _ => None,
            },
        )
        .collect()
}

/// The command as every run of it runs: a request id, and a create's key.
fn prepared(host: &Host<'_>, table: &Table, mut command: Command) -> Result<Command, Error> {
    if command.request.is_empty() {
        command.request = host.request();
    }
    if let Action::Create { id, .. } = &mut command.action {
        if id.as_deref().is_none_or(str::is_empty) {
            *id = Some(new_key(table)?);
        }
    }
    Ok(command)
}

/// A key for a create that names none: a UUID's wire form, or a UUID's
/// text for a string key.
fn new_key(table: &Table) -> Result<String, Refusal> {
    let key_type = table.key.type_name.as_str();
    if UUID_TYPES.contains(&key_type) {
        return Ok(base62(uuid::Uuid::new_v4().as_u128()));
    }
    if key_type == "string" {
        return Ok(uuid::Uuid::new_v4().to_string());
    }
    Err(Refusal::invalid_argument(format!(
        "a create of {} names no key, and the adapter makes none for a key of type {key_type}: pass the id",
        table.type_name
    )))
}

/// Refuses a create or an update whose own fields name a field a behavior
/// owns under its field's IR name (`statusEnteredAt`), as the core refuses
/// one that names a field anchor's own field (rule `owned`): only the
/// behavior's operations change it. The anchor field itself is left to the
/// core, which refuses it in its own words.
fn refuse_owned_writes(table: &Table, action: &Action) -> Result<(), Error> {
    let own = match action {
        Action::Create { own, .. } => own,
        Action::Update { patch, .. } => patch,
        _ => return Ok(()),
    };
    let own_fields: HashSet<&str> = table
        .own
        .iter()
        .map(|column| column.field.as_str())
        .collect();
    let issues: Vec<ValidationIssue> = table
        .uses
        .iter()
        .flat_map(|used| used.fields.iter().map(move |field| (used, field)))
        .filter(|(used, field)| {
            let name = field.column.field.as_str();
            !(field.value && used.anchor().is_some())
                && !own_fields.contains(name)
                && own.contains_key(name)
        })
        .map(|(used, field)| {
            let name = &field.column.field;
            ValidationIssue::new(
                name,
                "owned",
                format!(
                    "{name} is behavior {}'s field: only its operations change it",
                    used.behavior
                ),
            )
        })
        .collect();
    if issues.is_empty() {
        return Ok(());
    }
    Err(Refusal::invalid_instance(&table.type_name, issues).into())
}

/// Runs the core until its outcome: answers each read through the store,
/// recording with it how many locks were held when it was made, and takes
/// each lock asked for. Returns the change set and the answers the core
/// decided on, or the core's refusal.
async fn settle<S: Store>(
    host: &Host<'_>,
    store: &S,
    spec: &TypeSpec,
    command: &Command,
) -> Result<(ChangeSet, Answers), Error> {
    let mut answers = Answers::new();
    for _ in 0..MAX_ROUNDS {
        match host.core.run(spec, command, &answers) {
            Step::Done(Outcome::Refused(refusal)) => return Err(refusal.into()),
            Step::Done(Outcome::Changed(changes)) => return Ok((*changes, answers)),
            Step::Read(requests) => {
                for request in requests {
                    // A store whose locks are fenced checks each read made
                    // under one in the unit that writes, and has no check
                    // of sub-table rows or of the rows that link to a row
                    // yet (D103, left for later).
                    if store.fenced() && !answers.locks.is_empty() {
                        match request {
                            ReadRequest::SubRows { .. } => return Err(internal(
                                "a read of sub-table rows under a lock has no check on SQLite or D1 yet (D103)",
                            )
                            .into()),
                            ReadRequest::Linked { .. } => return Err(internal(
                                "a read of the rows that link to a row under a lock has no check on SQLite or D1 yet (D103)",
                            )
                            .into()),
                            _ => {}
                        }
                    }
                    let answer = answer_of(host.layout, store, &request).await?;
                    answers.answer(request, answer);
                }
            }
            Step::Lock(keys) => {
                for key in keys {
                    if !answers.holds(&key) {
                        store.lock(key.as_str()).await?;
                        answers.hold(key);
                    }
                }
            }
        }
    }
    Err(internal(format!(
        "{} of {}: the run still asked for reads or locks after {MAX_ROUNDS} rounds",
        command.action.name(),
        spec.name
    ))
    .into())
}

/// The answer to one of the core's reads, through the store; a kind of
/// read the adapter does not take refuses the command as internal.
async fn answer_of<S: Store>(
    layout: &Layout,
    store: &S,
    request: &ReadRequest,
) -> Result<Answer, Error> {
    match request {
        ReadRequest::Row { type_name, key } => Ok(Answer::Row(match layout.table(type_name) {
            Some(table) => read_row_of(store, table, key).await?,
            None => None,
        })),
        ReadRequest::HasRows { type_name } => {
            let Some(table) = layout.table(type_name) else {
                return Ok(Answer::HasRows(false));
            };
            let text = format!(
                "SELECT 1 AS one FROM {}{} LIMIT 1",
                quote(&table.table),
                live_where(table)
            );
            Ok(Answer::HasRows(!store.query(&text, &[]).await?.is_empty()))
        }
        ReadRequest::Spec { type_name } => Ok(Answer::Spec(
            layout.table(type_name).map(|table| table.spec.clone()),
        )),
        ReadRequest::SubRows {
            type_name,
            key,
            use_id,
            table: name,
        } => {
            let Some(table) = layout.table(type_name) else {
                return Ok(Answer::SubRows(Vec::new()));
            };
            let sub = sub_table_of(table, use_id, name)?;
            Ok(Answer::SubRows(
                read_sub_rows(store, table, sub, key).await?,
            ))
        }
        ReadRequest::Linked {
            type_name,
            field,
            key,
            after,
            limit,
        } => {
            let Some(table) = layout.table(type_name) else {
                return Ok(Answer::Linked(LinkedRows::default()));
            };
            Ok(Answer::Linked(
                read_linked(store, table, field, key, after.as_deref(), *limit).await?,
            ))
        }
        other => Err(internal(format!(
            "the adapter answers no read of this kind: {}",
            json_of(other)
        ))
        .into()),
    }
}

/// A row by its key: its own columns as the core's own fields and each
/// use's owned columns as that use's owned fields, each converted and a
/// NULL column absent, a use with no value absent, and its version.
async fn read_row_of<S: Store>(store: &S, table: &Table, key: &str) -> Result<Option<Row>, Error> {
    let dialect = store.dialect();
    let text = format!(
        "SELECT {} FROM {} WHERE {} = {}{}",
        row_list(dialect, table).join(", "),
        quote(&table.table),
        quote(&table.key.column),
        placeholder(dialect, 1),
        live_and(table)
    );
    let rows = store.query(&text, &[key_value(table, key)?]).await?;
    let Some(cells) = rows.into_iter().next() else {
        return Ok(None);
    };
    Ok(Some(row_of(cells.into_iter(), table, key)?))
}

/// The select list of a row read: each own column, then each use's owned
/// columns, each under an alias of its place, so no two columns' names
/// meet in the row, and the version last.
fn row_list(dialect: Dialect, table: &Table) -> Vec<String> {
    let owned = table
        .uses
        .iter()
        .flat_map(|used| used.fields.iter().map(|field| &field.column));
    let mut list: Vec<String> = table
        .own
        .iter()
        .chain(owned)
        .enumerate()
        .map(|(i, column)| format!("{} AS c{i}", selected(dialect, &column.column)))
        .collect();
    list.push(format!("{} AS v", quote(&table.version)));
    list
}

/// A row from the cells of [`row_list`], in its order.
fn row_of(mut cells: impl Iterator<Item = Cell>, table: &Table, key: &str) -> Result<Row, Error> {
    let mut next = |column: &Column| from_cell(cells.next().unwrap_or(Cell::Null), column, table);
    let mut row = Row::default();
    for column in &table.own {
        if let Some(value) = next(column)? {
            row.own.insert(column.field.clone(), value);
        }
    }
    for used in &table.uses {
        let mut values = Map::new();
        for field in &used.fields {
            if let Some(value) = next(&field.column)? {
                values.insert(field.name.clone(), value);
            }
        }
        if !values.is_empty() {
            row.owned.insert(used.id.clone(), values);
        }
    }
    row.version = version_of(cells.next(), table, key)?;
    Ok(row)
}

/// One page of the rows of a table whose own field `field` holds `key`
/// (D103): live rows alone on a soft-deletable table, in the key column's
/// order, after the key `after` when given, at most `limit`, each by its
/// key as the key column reads (a UUID's base62) and read as a row read
/// reads it, and whether more follow. A field the table has no own column
/// for is internal.
async fn read_linked<S: Store>(
    store: &S,
    table: &Table,
    field: &str,
    key: &str,
    after: Option<&str>,
    limit: usize,
) -> Result<LinkedRows, Error> {
    let Some(column) = table.own.iter().find(|column| column.field == field) else {
        return Err(internal(format!(
            "{} has no own field {field} in the behaviors' layout",
            table.type_name
        ))
        .into());
    };
    let dialect = store.dialect();
    let mut values = vec![to_column(&Value::String(key.to_owned()), column, table)?];
    let mut text = format!(
        "SELECT {} AS k, {} FROM {} WHERE {} = {}{}",
        selected(dialect, &table.key.column),
        row_list(dialect, table).join(", "),
        quote(&table.table),
        quote(&column.column),
        placeholder(dialect, 1),
        live_and(table)
    );
    if let Some(after) = after {
        values.push(key_value(table, after)?);
        text.push_str(&format!(
            " AND {} > {}",
            quote(&table.key.column),
            placeholder(dialect, 2)
        ));
    }
    text.push_str(&format!(
        " ORDER BY {} LIMIT {}",
        quote(&table.key.column),
        limit.saturating_add(1)
    ));
    let mut page = LinkedRows::default();
    for cells in store.query(&text, &values).await? {
        if page.rows.len() == limit {
            page.more = true;
            break;
        }
        let mut cells = cells.into_iter();
        let at = match from_cell(cells.next().unwrap_or(Cell::Null), &table.key, table)? {
            Some(Value::String(at)) => at,
            Some(other) => other.to_string(),
            None => continue,
        };
        let row = row_of(cells, table, &at)?;
        page.rows.push(KeyedRow { key: at, row });
    }
    Ok(page)
}

/// The rows of a use's sub-table on one row, in key order (by code point,
/// as the core's reference host orders them): each by its key, its values
/// by the declared field's name, converted as an owned field's are, a NULL
/// column absent.
async fn read_sub_rows<S: Store>(
    store: &S,
    table: &Table,
    sub: &SubTable,
    key: &str,
) -> Result<Vec<SubRow>, Error> {
    let dialect = store.dialect();
    let list: Vec<String> = std::iter::once(&sub.key)
        .chain(&sub.fields)
        .enumerate()
        .map(|(i, field)| format!("{} AS c{i}", selected(dialect, &field.column.column)))
        .collect();
    let order = match dialect {
        Dialect::Postgres => " COLLATE \"C\"",
        Dialect::Sqlite => "",
    };
    let text = format!(
        "SELECT {} FROM {} WHERE {} = {} ORDER BY {}{order}",
        list.join(", "),
        quote(&sub.table),
        quote(&sub.row),
        placeholder(dialect, 1),
        quote(&sub.key.column.column)
    );
    let mut rows = Vec::new();
    for cells in store.query(&text, &[key_value(table, key)?]).await? {
        let mut cells = cells.into_iter();
        let mut next =
            |column: &Column| from_cell(cells.next().unwrap_or(Cell::Null), column, table);
        let at = match next(&sub.key.column)? {
            Some(Value::String(at)) => at,
            Some(other) => other.to_string(),
            None => continue,
        };
        let mut values = Map::new();
        for field in &sub.fields {
            if let Some(value) = next(&field.column)? {
                values.insert(field.name.clone(), value);
            }
        }
        rows.push(SubRow { key: at, values });
    }
    Ok(rows)
}

/// A row's version as its column holds it.
fn version_of(cell: Option<Cell>, table: &Table, key: &str) -> Result<i64, Error> {
    let version = match &cell {
        Some(Cell::Int(n)) => Some(*n),
        Some(Cell::Real(x)) if x.is_finite() => Some(*x as i64),
        Some(Cell::Text(text)) => text.trim().parse().ok(),
        Some(Cell::Json(Value::Number(number))) => number.as_i64(),
        _ => None,
    };
    version.ok_or_else(|| {
        internal(format!(
            "{} {key}'s version {} is no number",
            table.type_name,
            quote(&table.version)
        ))
        .into()
    })
}

/// The checks that hold a store whose locks are fenced to the reads a
/// command made under a lock, each a condition the unit checks before its
/// writes: a row read is still at the version read, a row read absent is
/// still absent, and a table read to have rows still has them, or still
/// none, each of a soft-deletable table's live rows. A read of a type the
/// layout lacks, or of a spec, answers from the layout and needs none; one
/// of sub-table rows under a lock was refused as it was asked for.
fn fences_of<S: Store>(
    layout: &Layout,
    store: &S,
    answers: &Answers,
) -> Result<Vec<Statement>, Error> {
    if !store.fenced() {
        return Ok(Vec::new());
    }
    let dialect = store.dialect();
    let mut fences = Vec::new();
    for Answered {
        request,
        answer,
        held,
    } in &answers.reads
    {
        if *held == 0 {
            continue;
        }
        match (request, answer) {
            (ReadRequest::Row { type_name, key }, Answer::Row(row)) => {
                let Some(table) = layout.table(type_name) else {
                    continue;
                };
                let mut values = vec![key_value(table, key)?];
                let at = match row {
                    Some(row) => {
                        values.push(Bind::Int(row.version));
                        format!(
                            " AND {} = {}",
                            quote(&table.version),
                            placeholder(dialect, 2)
                        )
                    }
                    None => String::new(),
                };
                let found = format!(
                    "SELECT 1 FROM {} WHERE {} = {}{at}{}",
                    quote(&table.table),
                    quote(&table.key.column),
                    placeholder(dialect, 1),
                    live_and(table)
                );
                let not = if row.is_none() { "NOT " } else { "" };
                fences.push(Statement::check(format!("{not}EXISTS ({found})"), values));
            }
            (ReadRequest::HasRows { type_name }, Answer::HasRows(has)) => {
                let Some(table) = layout.table(type_name) else {
                    continue;
                };
                let not = if *has { "" } else { "NOT " };
                let any = format!("SELECT 1 FROM {}{}", quote(&table.table), live_where(table));
                fences.push(Statement::check(format!("{not}EXISTS ({any})"), Vec::new()));
            }
            _ => {}
        }
    }
    Ok(fences)
}

/// The statements of a change set, in the order of a unit (D103): every row
/// write, each fenced to change one row (a create of a key the table holds
/// is refused as conflict), and a row a change command found gone, its
/// deadlines and sub-table rows deleted unless a row of its key came back;
/// a deadline command's spend of its arming, or a change command's of its
/// message; the sub-table row writes; the deadlines armed and cleared;
/// then, for each row the change set deletes, its deadlines' messages and,
/// deleted for good, its sub-table rows, whose writes and deadlines the
/// unit skips. Outbox events, and a kind of write the adapter does not
/// know, refuse the command as internal.
async fn statements_of<S: Store>(
    layout: &Layout,
    store: &S,
    type_name: &str,
    command: &Command,
    changes: &ChangeSet,
    options: &ExecuteOptions,
    spend: Option<Statement>,
) -> Result<Vec<Statement>, Error> {
    if !changes.events.is_empty() {
        return Err(internal(format!(
            "{} of {type_name} writes outbox events of a behavior's own, which the adapter does not take yet: a table's change queue holds its writes, and the event stream comes later (D103)",
            command.action.name()
        ))
        .into());
    }
    let dialect = store.dialect();
    let command_key = command.action.key();
    let mut statements = Vec::new();
    // Each row the change set deletes: its table, its key and whether it
    // is deleted for good.
    let mut deleted: Vec<(&Table, &str, bool)> = Vec::new();
    for write in &changes.rows {
        let table = table_of(layout, write.type_name())?;
        // The command's own row binds the columns its caller encoded.
        let mine = write.type_name() == type_name && write.key() == command_key;
        let statement = match write {
            RowWrite::Create {
                key, own, owned, ..
            } => {
                let encoded = options.own_columns.as_ref().filter(|_| mine);
                create_of(store, table, key.as_deref(), own, owned, encoded).await?
            }
            RowWrite::Update {
                key,
                expected_version,
                own,
                owned,
                ..
            } => {
                let encoded = options.own_columns.as_ref().filter(|_| mine);
                let mut binder = Binder::new(dialect);
                let mut sets: Vec<String> = own_columns(table, own, encoded)?
                    .into_iter()
                    .chain(owned_columns(table, owned)?)
                    .map(|(column, value)| format!("{} = {}", quote(&column), binder.bind(value)))
                    .collect();
                // The version moves on with every write, the core's own:
                // on Postgres @optimistic's trigger sets it to the same value.
                sets.push(next_version(table));
                held_write(
                    &mut binder,
                    table,
                    key,
                    *expected_version,
                    &format!("UPDATE {} SET {}", quote(&table.table), sets.join(", ")),
                )?
            }
            RowWrite::Delete {
                key,
                expected_version,
                ..
            } => match options.soft_delete.as_ref().filter(|_| mine) {
                Some(soft) => {
                    deleted.push((table, key, false));
                    let mut binder = Binder::new(dialect);
                    let mut sets: Vec<String> = soft
                        .iter()
                        .map(|(column, value)| {
                            format!("{} = {}", quote(column), binder.bind(Bind::of_json(value)))
                        })
                        .collect();
                    sets.push(next_version(table));
                    held_write(
                        &mut binder,
                        table,
                        key,
                        *expected_version,
                        &format!("UPDATE {} SET {}", quote(&table.table), sets.join(", ")),
                    )?
                }
                None => {
                    deleted.push((table, key, true));
                    let mut binder = Binder::new(dialect);
                    held_write(
                        &mut binder,
                        table,
                        key,
                        *expected_version,
                        &format!("DELETE FROM {}", quote(&table.table)),
                    )?
                }
            },
            RowWrite::Gone { key, .. } => {
                statements.extend(gone_of(dialect, table, key)?);
                continue;
            }
            other => {
                return Err(internal(format!(
                    "the adapter takes no row write of this kind: {}",
                    json_of(other)
                ))
                .into())
            }
        };
        statements.push(statement);
    }
    // The arming a deadline command spends, or the message a change
    // command does, after the rows it holds to their versions, so its lock
    // comes after theirs as every command's does, and before the deadlines
    // the command arms again.
    statements.extend(spend);
    let gone = |type_name: &str, key: Option<&str>| {
        deleted
            .iter()
            .any(|(table, deleted, _)| table.type_name == type_name && key == Some(*deleted))
    };
    for write in &changes.sub_rows {
        if gone(&write.row.type_name, write.row.key.as_deref()) {
            continue;
        }
        statements.extend(sub_row_of(layout, dialect, write)?);
    }
    for change in &changes.deadlines {
        let row = change.row();
        if gone(&row.type_name, row.key.as_deref()) {
            continue;
        }
        statements.extend(deadline_of(layout, dialect, change)?);
    }
    for (table, key, hard) in deleted {
        let at_row = || key_value(table, key);
        if let Some(queue) = &table.deadlines {
            statements.push(Statement::any(
                format!(
                    "DELETE FROM {} WHERE {} = {}",
                    quote(&queue.table),
                    quote(&queue.row_key),
                    placeholder(dialect, 1)
                ),
                vec![at_row()?],
            ));
        }
        // A soft-deleted row keeps its sub-table rows, its state for a
        // restore; SQLite deletes them with the row only when its
        // connection enforces foreign keys, so the unit deletes them.
        if hard {
            for sub in table.uses.iter().flat_map(|used| &used.tables) {
                statements.push(Statement::any(
                    format!(
                        "DELETE FROM {} WHERE {} = {}",
                        quote(&sub.table),
                        quote(&sub.row),
                        placeholder(dialect, 1)
                    ),
                    vec![at_row()?],
                ));
            }
        }
    }
    Ok(statements)
}

/// The statements of a row a change command found gone (D103), neither
/// fenced nor held to a count, each harmless when the row came back: its
/// deadlines' messages, unless a live row of its key is there, and its
/// sub-table rows, unless any row of its key is there (a soft-deleted row
/// keeps them, as a soft delete through the core does).
fn gone_of(dialect: Dialect, table: &Table, key: &str) -> Result<Vec<Statement>, Error> {
    let p1 = placeholder(dialect, 1);
    let row = |live: &str| {
        format!(
            "NOT EXISTS (SELECT 1 FROM {} WHERE {} = {p1}{live})",
            quote(&table.table),
            quote(&table.key.column)
        )
    };
    let mut statements = Vec::new();
    if let Some(queue) = &table.deadlines {
        statements.push(Statement::any(
            format!(
                "DELETE FROM {} WHERE {} = {p1} AND {}",
                quote(&queue.table),
                quote(&queue.row_key),
                row(&live_and(table))
            ),
            vec![key_value(table, key)?],
        ));
    }
    for sub in table.uses.iter().flat_map(|used| &used.tables) {
        statements.push(Statement::any(
            format!(
                "DELETE FROM {} WHERE {} = {p1} AND {}",
                quote(&sub.table),
                quote(&sub.row),
                row("")
            ),
            vec![key_value(table, key)?],
        ));
    }
    Ok(statements)
}

/// The statement of one sub-table row write, each expecting one row: an
/// insert that a row of its key the table holds already leaves as it is,
/// an update of the values it sets, none for an update that sets none, or
/// a delete. The row and the key bind first, then each value the write
/// names, in the layout's field order.
fn sub_row_of(
    layout: &Layout,
    dialect: Dialect,
    write: &SubRowWrite,
) -> Result<Option<Statement>, Error> {
    let table = table_of(layout, &write.row.type_name)?;
    let Some(key) = write.row.key.as_deref() else {
        return Err(internal(format!(
            "a write of {}'s {}.{} reached the adapter without its row's key",
            table.type_name, write.use_id, write.table
        ))
        .into());
    };
    let sub = sub_table_of(table, &write.use_id, &write.table)?;
    let mut values = vec![
        key_value(table, key)?,
        to_column(&Value::String(write.key.clone()), &sub.key.column, table)?,
    ];
    let at_row = format!(
        "{} = {} AND {} = {}",
        quote(&sub.row),
        placeholder(dialect, 1),
        quote(&sub.key.column.column),
        placeholder(dialect, 2)
    );
    let statement = match &write.change {
        SubRowChange::Insert { values: set } => {
            let mut columns = vec![quote(&sub.row), quote(&sub.key.column.column)];
            for (column, value) in sub_values(table, sub, set)? {
                columns.push(quote(column));
                values.push(value);
            }
            let placeholders: Vec<String> = (1..=values.len())
                .map(|n| placeholder(dialect, n))
                .collect();
            Statement::write(
                format!(
                    "INSERT INTO {} ({}) VALUES ({}) ON CONFLICT ({}, {}) DO NOTHING",
                    quote(&sub.table),
                    columns.join(", "),
                    placeholders.join(", "),
                    quote(&sub.row),
                    quote(&sub.key.column.column)
                ),
                values,
            )
        }
        SubRowChange::Update { values: set } => {
            let mut sets = Vec::new();
            for (column, value) in sub_values(table, sub, set)? {
                values.push(value);
                sets.push(format!(
                    "{} = {}",
                    quote(column),
                    placeholder(dialect, values.len())
                ));
            }
            if sets.is_empty() {
                return Ok(None);
            }
            Statement::write(
                format!(
                    "UPDATE {} SET {} WHERE {at_row}",
                    quote(&sub.table),
                    sets.join(", ")
                ),
                values,
            )
        }
        SubRowChange::Delete => Statement::write(
            format!("DELETE FROM {} WHERE {at_row}", quote(&sub.table)),
            values,
        ),
        other => {
            return Err(internal(format!(
                "the adapter takes no sub-table row write of this kind: {}",
                json_of(other)
            ))
            .into())
        }
    };
    Ok(Some(statement))
}

/// The columns and values a sub-table row write sets, in the layout's
/// field order; a value of a field the table does not hold, the key's
/// among them, is internal.
fn sub_values<'s>(
    table: &Table,
    sub: &'s SubTable,
    set: &Map<String, Value>,
) -> Result<Vec<(&'s str, Bind)>, Error> {
    if let Some(name) = set.keys().find(|name| sub.field(name).is_none()) {
        return Err(internal(format!("sub-table {} has no field {name}", sub.table)).into());
    }
    sub.fields
        .iter()
        .filter_map(|field| set.get(&field.name).map(|value| (field, value)))
        .map(|(field, value)| {
            Ok((
                field.column.column.as_str(),
                to_column(value, &field.column, table)?,
            ))
        })
        .collect()
}

/// A use's sub-table on a table by the use's id and the table's declared
/// name, or an internal refusal for one the layout lacks.
fn sub_table_of<'t>(table: &'t Table, use_id: &str, name: &str) -> Result<&'t SubTable, Error> {
    let Some(used) = table.uses.iter().find(|used| used.id == use_id) else {
        return Err(internal(format!(
            "{} has no use {use_id} in the behaviors' layout",
            table.type_name
        ))
        .into());
    };
    used.table(name).ok_or_else(|| {
        internal(format!(
            "{}'s use {use_id} ({}) has no sub-table {name} in the behaviors' layout",
            table.type_name, used.behavior
        ))
        .into()
    })
}

/// The statements of a deadline armed or cleared (D103), neither held to a
/// count: a clear deletes the deadline's messages, ready, claimed or dead,
/// so a claim on its old arming finds its message gone and a dead arming
/// the new one supersedes goes, and an operator's retry of the queue's dead
/// messages never meets a live one of the same deadline; an arm deletes
/// them and inserts its message, due at the time it falls due, with its
/// payload's JSON.
fn deadline_of(
    layout: &Layout,
    dialect: Dialect,
    change: &DeadlineChange,
) -> Result<Vec<Statement>, Error> {
    let row = change.row();
    let table = table_of(layout, &row.type_name)?;
    let (use_id, name) = (change.use_id(), change.name());
    let Some(queue) = &table.deadlines else {
        return Err(internal(format!(
            "{type_name}'s use {use_id} arms the deadline {name}, and the behaviors' layout gives {type_name} no deadline queue",
            type_name = table.type_name
        ))
        .into());
    };
    let Some(key) = row.key.as_deref() else {
        return Err(internal(format!(
            "a deadline {use_id}.{name} of {} reached the adapter without its row's key",
            table.type_name
        ))
        .into());
    };
    let p = |n| placeholder(dialect, n);
    let mut values = vec![
        key_value(table, key)?,
        Bind::Text(use_id.to_owned()),
        Bind::Text(name.to_owned()),
    ];
    let clear = Statement::any(
        format!(
            "DELETE FROM {} WHERE {} = {} AND {} = {} AND {} = {}",
            quote(&queue.table),
            quote(&queue.row_key),
            p(1),
            quote(&queue.use_id),
            p(2),
            quote(&queue.name),
            p(3)
        ),
        values.clone(),
    );
    match change {
        DeadlineChange::Clear { .. } => Ok(vec![clear]),
        DeadlineChange::Arm { at, payload, .. } => {
            values.push(Bind::Int(*at));
            values.push(match payload {
                Value::Null => Bind::Null,
                payload => Bind::Text(payload.to_string()),
            });
            let (payload_at, due_at) = match dialect {
                Dialect::Postgres => (
                    format!("{}::jsonb", p(5)),
                    format!("to_timestamp({}::bigint / 1000.0)", p(4)),
                ),
                Dialect::Sqlite => (
                    p(5),
                    format!(
                        "strftime('%Y-%m-%dT%H:%M:%fZ', {} / 1000.0, 'unixepoch')",
                        p(4)
                    ),
                ),
            };
            let insert = Statement::any(
                format!(
                    "INSERT INTO {} ({}, {}, {}, {}, {}, \"due_at\") VALUES ({}, {}, {}, {}, {payload_at}, {due_at})",
                    quote(&queue.table),
                    quote(&queue.row_key),
                    quote(&queue.use_id),
                    quote(&queue.name),
                    quote(&queue.at),
                    quote(&queue.payload),
                    p(1),
                    p(2),
                    p(3),
                    p(4)
                ),
                values,
            );
            Ok(vec![clear, insert])
        }
        other => Err(internal(format!(
            "the adapter takes no deadline change of this kind: {}",
            json_of(other)
        ))
        .into()),
    }
}

/// A create's insert of its key, its own columns and its owned columns,
/// after a probe of the key: a row the table holds under it, live or not,
/// refuses the create as conflict, as the reference host refuses it.
async fn create_of<S: Store>(
    store: &S,
    table: &Table,
    key: Option<&str>,
    own: &Map<String, Value>,
    owned: &BTreeMap<String, Map<String, Value>>,
    encoded: Option<&Map<String, Value>>,
) -> Result<Statement, Error> {
    let Some(key) = key else {
        return Err(internal(format!(
            "a create of {} reached the adapter without a key",
            table.type_name
        ))
        .into());
    };
    let dialect = store.dialect();
    let key_bind = key_value(table, key)?;
    let probe = format!(
        "SELECT 1 AS one FROM {} WHERE {} = {}",
        quote(&table.table),
        quote(&table.key.column),
        placeholder(dialect, 1)
    );
    if !store
        .query(&probe, std::slice::from_ref(&key_bind))
        .await?
        .is_empty()
    {
        return Err(Refusal::new(
            Code::Conflict,
            format!("{} {key} already exists", table.type_name),
        )
        .into());
    }
    let mut assigned = vec![(table.key.column.clone(), key_bind)];
    assigned.extend(own_columns(table, own, encoded)?);
    assigned.extend(owned_columns(table, owned)?);
    let columns: Vec<String> = assigned.iter().map(|(column, _)| quote(column)).collect();
    let mut binder = Binder::new(dialect);
    let placeholders: Vec<String> = assigned
        .into_iter()
        .map(|(_, value)| binder.bind(value))
        .collect();
    Ok(Statement::write(
        format!(
            "INSERT INTO {} ({}) VALUES ({})",
            quote(&table.table),
            columns.join(", "),
            placeholders.join(", ")
        ),
        binder.values,
    ))
}

/// A write of one row held to the version expected, and live: `head` and
/// `WHERE key AND version[ AND live]`.
fn held_write(
    binder: &mut Binder,
    table: &Table,
    key: &str,
    expected: i64,
    head: &str,
) -> Result<Statement, Error> {
    let at_key = binder.bind(key_value(table, key)?);
    let at_version = binder.bind(Bind::Int(expected));
    let text = format!(
        "{head} WHERE {} = {at_key} AND {} = {at_version}{}",
        quote(&table.key.column),
        quote(&table.version),
        live_and(table)
    );
    Ok(Statement::write(text, std::mem::take(&mut binder.values)))
}

fn next_version(table: &Table) -> String {
    let version = quote(&table.version);
    format!("{version} = {version} + 1")
}

/// The own columns a write sets: the caller's encoded columns for the
/// command's own row, else each own field of the change set by the
/// layout's column. A null for a field the table has no column for clears
/// nothing, as a merge patch's null for a member the document lacks; a
/// value for one refuses the command as internal.
fn own_columns(
    table: &Table,
    own: &Map<String, Value>,
    encoded: Option<&Map<String, Value>>,
) -> Result<Vec<(String, Bind)>, Error> {
    if let Some(encoded) = encoded {
        return Ok(encoded
            .iter()
            .map(|(column, value)| (column.clone(), Bind::of_json(value)))
            .collect());
    }
    let mut out = Vec::new();
    for (field, value) in own {
        let Some(column) = table.own.iter().find(|column| column.field == *field) else {
            if value.is_null() {
                continue;
            }
            return Err(internal(format!(
                "{} has no own field {field} in the behaviors' layout",
                table.type_name
            ))
            .into());
        };
        out.push((column.column.clone(), to_column(value, column, table)?));
    }
    Ok(out)
}

/// The owned columns a write sets, each use's declared field by its layout
/// column; null clears one.
fn owned_columns(
    table: &Table,
    owned: &BTreeMap<String, Map<String, Value>>,
) -> Result<Vec<(String, Bind)>, Error> {
    let mut out = Vec::new();
    for (use_id, fields) in owned {
        let Some(used) = table.uses.iter().find(|used| used.id == *use_id) else {
            return Err(internal(format!(
                "{} has no use {use_id} in the behaviors' layout",
                table.type_name
            ))
            .into());
        };
        for (name, value) in fields {
            let Some(field) = used.fields.iter().find(|field| field.name == *name) else {
                return Err(internal(format!(
                    "{}'s use {use_id} ({}) has no field {name} in the behaviors' layout",
                    table.type_name, used.behavior
                ))
                .into());
            };
            out.push((
                field.column.column.clone(),
                to_column(value, &field.column, table)?,
            ));
        }
    }
    Ok(out)
}

/// The values of one statement, each bound at the next placeholder.
struct Binder {
    dialect: Dialect,
    values: Vec<Bind>,
}

impl Binder {
    fn new(dialect: Dialect) -> Self {
        Binder {
            dialect,
            values: Vec::new(),
        }
    }

    fn bind(&mut self, value: Bind) -> String {
        self.values.push(value);
        placeholder(self.dialect, self.values.len())
    }
}

/// An identifier quoted as Postgres and SQLite both read one.
pub(crate) fn quote(identifier: &str) -> String {
    format!("\"{}\"", identifier.replace('"', "\"\""))
}

/// The nth placeholder in a dialect: `$n` on Postgres, `?n` on SQLite.
fn placeholder(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Postgres => format!("${n}"),
        Dialect::Sqlite => format!("?{n}"),
    }
}

/// A column as a row read selects it: Postgres's `to_jsonb` of it, its
/// value as JSON, which keeps a number's text and reads every type alike;
/// SQLite's as it is stored.
fn selected(dialect: Dialect, column: &str) -> String {
    match dialect {
        Dialect::Postgres => format!("to_jsonb({})", quote(column)),
        Dialect::Sqlite => quote(column),
    }
}

/// The condition a statement adds for a soft-deletable table: the row is
/// live.
fn live_and(table: &Table) -> String {
    table
        .live
        .as_ref()
        .map(|live| format!(" AND {} IS NULL", quote(&live.column)))
        .unwrap_or_default()
}

/// A soft-deletable table's live rows, as a whole statement's condition.
fn live_where(table: &Table) -> String {
    table
        .live
        .as_ref()
        .map(|live| format!(" WHERE {} IS NULL", quote(&live.column)))
        .unwrap_or_default()
}

fn json_of(value: &impl Serialize) -> String {
    serde_json::to_string(value).unwrap_or_default()
}
