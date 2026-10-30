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
    Action, Answer, Answered, Answers, Caller, ChangeSet, Code, Command, Core, Outcome,
    ReadRequest, Refusal, Row, RowWrite, Step, TypeSpec, ValidationIssue,
};

use crate::convert::{base62, from_cell, internal, key_value, to_column, Bind, Cell, UUID_TYPES};
use crate::host::Host;
use crate::layout::{Column, Layout, Table};
use crate::{busy_refusal, CreateOptions, Error, ExecuteOptions, Executed, Owned};
use crate::{MAX_ATTEMPTS, MAX_ROUNDS};

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
/// state when a row moved, up to [`MAX_ATTEMPTS`] times.
pub(crate) async fn execute<S: Store>(
    host: &Host<'_>,
    store: &S,
    type_name: &str,
    command: Command,
    options: &ExecuteOptions,
) -> Result<Executed, Error> {
    let table = table_of(host.layout, type_name)?;
    refuse_checked(host, type_name)?;
    let run = prepared(host, table, command)?;
    refuse_owned_writes(table, &run.action)?;
    for attempt in 1..=MAX_ATTEMPTS {
        let busy = |error| busy_or(store, error, type_name, &run, attempt);
        let (changes, answers) = settle(host, store, &table.spec, &run).await.map_err(busy)?;
        let mut statements = fences_of(host.layout, store, &answers)?;
        statements.extend(
            statements_of(host.layout, store, type_name, &run, &changes, options)
                .await
                .map_err(busy)?,
        );
        if store.apply(&statements).await.map_err(busy)? {
            let result = changes.result.clone();
            return Ok(Executed {
                changes,
                result,
                key: run.action.key().map(str::to_owned),
                attempts: attempt,
            });
        }
    }
    Err(busy_refusal(type_name, &run, MAX_ATTEMPTS).into())
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
    let owned = table
        .uses
        .iter()
        .flat_map(|used| used.fields.iter().map(|field| &field.column));
    // Each column under an alias of its place, so no two columns' names
    // meet in the row.
    let mut list: Vec<String> = table
        .own
        .iter()
        .chain(owned)
        .enumerate()
        .map(|(i, column)| format!("{} AS c{i}", selected(dialect, &column.column)))
        .collect();
    list.push(format!("{} AS v", quote(&table.version)));
    let text = format!(
        "SELECT {} FROM {} WHERE {} = {}{}",
        list.join(", "),
        quote(&table.table),
        quote(&table.key.column),
        placeholder(dialect, 1),
        live_and(table)
    );
    let rows = store.query(&text, &[key_value(table, key)?]).await?;
    let Some(cells) = rows.into_iter().next() else {
        return Ok(None);
    };
    let mut cells = cells.into_iter();
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
    Ok(Some(row))
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
/// layout lacks, or of a spec, answers from the layout and needs none.
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

/// The statements of a change set, in its order, each fenced to change one
/// row. A create of a key the table holds is refused as conflict;
/// sub-table rows, outbox events and deadlines, and a kind of write the
/// adapter does not know, refuse the command as internal.
async fn statements_of<S: Store>(
    layout: &Layout,
    store: &S,
    type_name: &str,
    command: &Command,
    changes: &ChangeSet,
    options: &ExecuteOptions,
) -> Result<Vec<Statement>, Error> {
    let not_yet = |what: &str| -> Error {
        internal(format!(
            "{} of {type_name} writes {what}, which the adapter takes once each has a table of its own (D103's order of the work, step 3)",
            command.action.name()
        ))
        .into()
    };
    if !changes.sub_rows.is_empty() {
        return Err(not_yet("sub-table rows"));
    }
    if !changes.events.is_empty() {
        return Err(not_yet("outbox events"));
    }
    if !changes.deadlines.is_empty() {
        return Err(not_yet("deadlines"));
    }
    let dialect = store.dialect();
    let command_key = command.action.key();
    let mut statements = Vec::new();
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
    Ok(statements)
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
