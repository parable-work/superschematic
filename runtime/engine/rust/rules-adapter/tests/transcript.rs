//! The recorded scenarios are the specification (D103): the transcript
//! `superschematic-rules-testkit` writes from `core-workflow.json`
//! (`runtime/engine/testdata/rules/core-workflow.transcript.json`, whose
//! format its `transcript` module states) replayed through this adapter on
//! rusqlite and on Postgres, as the HTTP runtime's
//! `src/rules-transcript.test.ts` replays it through TypeScript's. Each
//! scenario gets a table per type and database, its own fields' columns
//! beside the owned columns the layout rule gives each use of any version
//! and `_version` with sqlgen's trigger; each check runs on the core alone,
//! answering whether the type has rows from its table; each command runs
//! through `execute` in a transaction of its own with the version live at
//! it, its refusal compared whole and the row it names read back after it
//! and compared; and each raw step writes owned columns with SQL past the
//! adapter. The Postgres replay is skipped without
//! `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
//!
//! The tables here follow the layout rule as the core's README states it,
//! restated by this test (the corpus's uses are type-anchored: Workflow's
//! `workflow_status` and `workflow_entered_at`). The layout the generator
//! writes is held by the generated packages' own end-to-end tests.

#![cfg(any(feature = "sqlite", feature = "postgres"))]

mod common;

use std::collections::HashSet;

use serde_json::{json, Value};
use superschematic_rules::{Answer, Answers, Core, ReadRequest, Refusal, Step};
use superschematic_rules_adapter::{
    Action, Column, Command, Convert, Error, ExecuteOptions, Executed, Host, Layout, Operation,
    OwnedField, Row, Table, TypeSpec, Use,
};
use superschematic_rules_testkit::{
    transcript_path, Transcript, TranscriptScenario, TranscriptStep, TranscriptType,
};

use common::{camel, column_types, p, snake, storage_key, Param};

/// The steps the transcript holds, the testkit's floor
/// (`tests/transcript.rs`): each dialect replays every one, so a
/// regression cannot pass by skipping.
const FLOOR: usize = 96;

/// How many runs a check makes before the test calls it unsettled.
const CHECK_ROUNDS: usize = 16;

fn transcript() -> Transcript {
    let path = transcript_path("core-workflow");
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    serde_json::from_str(&text).expect("the transcript reads")
}

/// A behavior's declaration file, as much of it as the layout rule reads.
struct Declared {
    name: String,
    value: Option<String>,
    /// Each declared field with a column type: its name and type.
    fields: Vec<(String, String)>,
    operations: Vec<Operation>,
}

/// The declaration files of the core's behaviors.
fn declarations() -> Vec<Declared> {
    superschematic_rules_behaviors::DECLARATIONS
        .iter()
        .map(|text| {
            let file: Value = serde_json::from_str(text).expect("a declaration file reads");
            let list = |name: &str| {
                file.get(name)
                    .and_then(Value::as_array)
                    .cloned()
                    .unwrap_or_default()
            };
            Declared {
                name: file["name"].as_str().unwrap_or_default().to_owned(),
                value: file.get("value").and_then(Value::as_str).map(str::to_owned),
                fields: list("fields")
                    .iter()
                    .filter_map(|field| {
                        Some((
                            field["name"].as_str()?.to_owned(),
                            field.get("type")?.as_str()?.to_owned(),
                        ))
                    })
                    .collect(),
                operations: list("operations")
                    .iter()
                    .map(|operation| Operation {
                        name: operation["name"].as_str().unwrap_or_default().to_owned(),
                        writes: operation.get("writes") == Some(&Value::Bool(true)),
                    })
                    .collect(),
            }
        })
        .collect()
}

/// The uses of a type across its versions, each with the owned columns the
/// layout rule gives it and its declared operations.
fn uses_of(declared: &[Declared], type_def: &TranscriptType) -> Vec<Use> {
    let mut uses: Vec<Use> = Vec::new();
    for version in &type_def.versions {
        for entry in &version.uses {
            let anchor = entry.field();
            let id = entry.id();
            if uses.iter().any(|known| known.id == id) {
                continue;
            }
            let declaration = declared
                .iter()
                .find(|declaration| declaration.name == entry.behavior)
                .unwrap_or_else(|| panic!("the core declares no behavior {}", entry.behavior));
            let fields = declaration
                .fields
                .iter()
                .map(|(name, type_name)| {
                    let value = anchor.is_some() && declaration.value.as_deref() == Some(name);
                    let column = match anchor {
                        None => format!("{}_{}", storage_key(&entry.behavior), snake(name)),
                        Some(anchor) if value => snake(anchor),
                        Some(anchor) => format!("{}_{}", snake(anchor), snake(name)),
                    };
                    let mut column = Column::new(camel(&column), column, type_name.clone());
                    if type_name == "Temporal.DateTime" {
                        column = column.converted(Convert::EpochMillis);
                    }
                    OwnedField {
                        name: name.clone(),
                        column,
                        value,
                    }
                })
                .collect();
            uses.push(Use {
                id: id.to_owned(),
                behavior: entry.behavior.clone(),
                anchor: anchor.map(str::to_owned),
                fields,
                operations: declaration.operations.clone(),
            });
        }
    }
    uses
}

/// The IR type of an own field of a JSON type.
fn ir_type(json_type: &str) -> &'static str {
    match json_type {
        "string" => "string",
        "number" | "integer" => "number",
        "boolean" => "boolean",
        _ => "Generic.JSON",
    }
}

/// The SQL type of a column in a dialect.
fn sql_type(postgres: bool, column: &Column) -> &'static str {
    let t = column_types(postgres);
    if column.convert == Some(Convert::EpochMillis) {
        return t.timestamp;
    }
    match column.type_name.as_str() {
        "string" => t.text,
        "number" => t.number,
        "boolean" => t.boolean,
        _ => t.json,
    }
}

/// A database of a dialect the replay runs on.
enum Db {
    #[cfg(feature = "sqlite")]
    Sqlite(rusqlite::Connection),
    #[cfg(feature = "postgres")]
    Postgres(tokio_postgres::Client),
}

impl Db {
    fn postgres(&self) -> bool {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(_) => false,
            #[cfg(feature = "postgres")]
            Db::Postgres(_) => true,
        }
    }

    /// Runs DDL or a write of the test's own, and returns the rows it
    /// changed (SQLite's count leaves out a trigger's writes).
    async fn exec(&mut self, text: &str, values: &[Param]) -> u64 {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                conn.execute(
                    text,
                    rusqlite::params_from_iter(common::sqlite_params(values)),
                )
                .unwrap_or_else(|error| panic!("{text}: {error}")) as u64
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let texts = common::pg::texts(values);
                client
                    .execute(text, &common::pg::refs(&texts))
                    .await
                    .unwrap_or_else(|error| panic!("{text}: {error}"))
            }
        }
    }

    async fn has_rows(&mut self, table: &str) -> bool {
        let text = format!("SELECT 1 FROM \"{table}\" LIMIT 1");
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                statement.exists([]).expect("the read runs")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => !client
                .query(&text, &[])
                .await
                .expect("the read runs")
                .is_empty(),
        }
    }

    /// Runs a command through the adapter in a transaction of its own,
    /// committed when it applies and rolled back when it is refused.
    async fn execute(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        command: Command,
    ) -> Result<Executed, Error> {
        let options = ExecuteOptions::default();
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                let outcome = superschematic_rules_adapter::sqlite::execute(
                    host, &tx, type_name, command, &options,
                );
                if outcome.is_ok() {
                    tx.commit().expect("the transaction commits");
                }
                outcome
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                let outcome = superschematic_rules_adapter::postgres::execute(
                    host, &tx, type_name, command, &options,
                )
                .await;
                if outcome.is_ok() {
                    tx.commit().await.expect("the transaction commits");
                } else {
                    tx.rollback().await.expect("the transaction rolls back");
                }
                outcome
            }
        }
    }

    /// Reads a row back in a fresh transaction.
    async fn read_row(&mut self, host: &Host<'_>, type_name: &str, key: &str) -> Option<Row> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                superschematic_rules_adapter::sqlite::read_row(host, &tx, type_name, key)
                    .expect("the row reads")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                superschematic_rules_adapter::postgres::read_row(host, &tx, type_name, key)
                    .await
                    .expect("the row reads")
            }
        }
    }
}

/// How many steps of each kind a replay ran, and what the transcript left
/// out.
#[derive(Default)]
struct Counts {
    commands: usize,
    checks: usize,
    raws: usize,
    skipped: usize,
}

/// One scenario's replay.
struct Scenario<'s> {
    index: usize,
    scenario: &'s TranscriptScenario,
    databases: Vec<&'s str>,
    declared: &'s [Declared],
    created: HashSet<String>,
    mismatches: Vec<String>,
}

impl<'s> Scenario<'s> {
    fn new(index: usize, scenario: &'s TranscriptScenario, declared: &'s [Declared]) -> Self {
        let mut databases: Vec<&str> = Vec::new();
        for step in &scenario.steps {
            let database = database_of(step);
            if !databases.contains(&database) {
                databases.push(database);
            }
        }
        Scenario {
            index,
            scenario,
            databases,
            declared,
            created: HashSet::new(),
            mismatches: Vec::new(),
        }
    }

    /// The table of a type in one of the scenario's databases, with a
    /// version's spec.
    fn table_of(&self, database: &str, type_name: &str, spec: Option<&TypeSpec>) -> Table {
        let type_def = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == type_name);
        let at = self
            .databases
            .iter()
            .position(|known| *known == database)
            .unwrap_or(0);
        Table {
            type_name: type_name.to_owned(),
            table: format!("s{}_d{at}_{}", self.index, snake(type_name)),
            key: Column::new("id", "id", "string"),
            version: "_version".into(),
            live: None,
            own: type_def
                .map(|known| {
                    known
                        .fields
                        .iter()
                        .map(|field| {
                            Column::new(&field.name, &field.name, ir_type(&field.json_type))
                        })
                        .collect()
                })
                .unwrap_or_default(),
            uses: type_def
                .map(|known| uses_of(self.declared, known))
                .unwrap_or_default(),
            spec: spec
                .cloned()
                .or_else(|| type_def.and_then(|known| known.versions.first().cloned()))
                .unwrap_or_else(|| TypeSpec::new(type_name)),
        }
    }

    /// The table, created the first time a step needs it.
    async fn ensure(&mut self, db: &mut Db, table: &Table) {
        if !self.created.insert(table.table.clone()) {
            return;
        }
        let postgres = db.postgres();
        let t = column_types(postgres);
        let mut columns = vec![format!("id {}", t.key)];
        for column in table.own.iter().chain(
            table
                .uses
                .iter()
                .flat_map(|used| used.fields.iter().map(|field| &field.column)),
        ) {
            columns.push(format!(
                "\"{}\" {}",
                column.column,
                sql_type(postgres, column)
            ));
        }
        columns.push(format!("_version {}", t.version));
        db.exec(
            &format!("CREATE TABLE \"{}\" ({})", table.table, columns.join(", ")),
            &[],
        )
        .await;
        if postgres {
            for ddl in common::postgres_bump(&table.table) {
                db.exec(&ddl, &[]).await;
            }
        } else {
            db.exec(&common::sqlite_bump(&table.table), &[]).await;
        }
    }

    fn compare(&mut self, step: usize, call: &str, what: &str, expected: Value, actual: Value) {
        if expected != actual {
            self.mismatches.push(format!(
                "step {step} ({call}) {what}:\n    expected {expected}\n    actual   {actual}"
            ));
        }
    }

    async fn replay(&mut self, db: &mut Db, core: &Core, counts: &mut Counts) {
        for step in &self.scenario.steps {
            match step {
                TranscriptStep::Check { .. } => {
                    counts.checks += 1;
                    self.check(db, core, step).await;
                }
                TranscriptStep::Raw { .. } => {
                    counts.raws += 1;
                    self.raw(db, step).await;
                }
                TranscriptStep::Command { .. } => {
                    counts.commands += 1;
                    self.command(db, core, step).await;
                }
            }
        }
    }

    /// A check, on the core alone: a publish asks whether the type has
    /// rows, which its table in the step's database answers.
    async fn check(&mut self, db: &mut Db, core: &Core, step: &TranscriptStep) {
        let TranscriptStep::Check {
            step: at,
            call,
            database,
            spec,
            command,
            expect,
        } = step
        else {
            return;
        };
        let table = self.table_of(database, &spec.name, None);
        self.ensure(db, &table).await;
        let mut answers = Answers::new();
        let mut refused: Option<Refusal> = None;
        for round in 0.. {
            assert!(round < CHECK_ROUNDS, "step {at}: the check never settled");
            match core.run(spec, command, &answers) {
                Step::Done(outcome) => {
                    refused = outcome.refusal().cloned();
                    break;
                }
                Step::Read(requests) => {
                    for request in requests {
                        let ReadRequest::HasRows { type_name } = &request else {
                            panic!("step {at}: a check asked for {request:?}");
                        };
                        let asked = self.table_of(database, type_name, None);
                        self.ensure(db, &asked).await;
                        let has = db.has_rows(&asked.table).await;
                        answers.answer(request, Answer::HasRows(has));
                    }
                }
                other => panic!("step {at}: a check asked for {other:?}"),
            }
        }
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
    }

    /// A raw write past the adapter, as an operator's repair or the test's
    /// own SQL: the escape hatch D103 keeps open.
    async fn raw(&mut self, db: &mut Db, step: &TranscriptStep) {
        let TranscriptStep::Raw {
            step: at,
            call,
            database,
            type_name,
            key,
            owned,
        } = step
        else {
            return;
        };
        let table = self.table_of(database, type_name, None);
        self.ensure(db, &table).await;
        let postgres = db.postgres();
        let mut sets = Vec::new();
        let mut values = Vec::new();
        for (use_id, fields) in owned {
            let used = table
                .uses
                .iter()
                .find(|used| used.id == *use_id)
                .expect("a use the layout has");
            for (name, value) in fields {
                let field = used
                    .fields
                    .iter()
                    .find(|field| field.name == *name)
                    .expect("a field the use has");
                values.push(match value {
                    Value::Null => Param::Null,
                    Value::Number(millis) if field.column.convert == Some(Convert::EpochMillis) => {
                        Param::text(common::iso(millis.as_i64().expect("epoch milliseconds")))
                    }
                    Value::String(text) => Param::text(text.clone()),
                    other => Param::text(other.to_string()),
                });
                sets.push(format!(
                    "\"{}\" = {}",
                    field.column.column,
                    p(postgres, values.len())
                ));
            }
        }
        values.push(Param::text(key.clone()));
        let text = format!(
            "UPDATE \"{}\" SET {} WHERE id = {}",
            table.table,
            sets.join(", "),
            p(postgres, values.len())
        );
        let changed = db.exec(&text, &values).await;
        self.compare(*at, call, "raw.changes", json!(1), json!(changed));
    }

    /// A command through the adapter on the version live at it, and the
    /// row it names read back after it.
    async fn command(&mut self, db: &mut Db, core: &Core, step: &TranscriptStep) {
        let TranscriptStep::Command {
            step: at,
            call,
            database,
            type_name,
            version,
            command,
            expect,
        } = step
        else {
            return;
        };
        let type_def = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == *type_name);
        let spec = type_def.and_then(|known| known.versions.get(*version));
        let table = self.table_of(database, type_name, spec);
        self.ensure(db, &table).await;
        let layout = Layout::new([table]);
        let host = Host::new(core, &layout);
        let (refused, result, key) = match db.execute(&host, type_name, command.clone()).await {
            Ok(executed) => (None, executed.result, executed.key),
            Err(Error::Refused(refusal)) => (Some(*refusal), Value::Null, None),
            Err(error) => panic!("step {at} ({call}): {error}"),
        };
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
        self.compare(*at, call, "result", expect.result.clone(), result);
        if let Some(expected) = &expect.key {
            self.compare(*at, call, "key", json!(expected), json!(key));
        }
        let row_key = match &command.action {
            Action::Create { id, .. } => id.clone(),
            action => action.key().map(str::to_owned),
        };
        if let Some(row_key) = row_key.filter(|key| !key.is_empty()) {
            let row = db.read_row(&host, type_name, &row_key).await;
            let actual = row.map(|row| json!({"own": row.own, "owned": row.owned}));
            self.compare(*at, call, "row", json!(expect.row), json!(actual));
        }
    }
}

fn database_of(step: &TranscriptStep) -> &str {
    match step {
        TranscriptStep::Command { database, .. }
        | TranscriptStep::Check { database, .. }
        | TranscriptStep::Raw { database, .. } => database,
    }
}

/// Replays every scenario on db, failing on any mismatch, listed in full by
/// scenario, or on fewer steps replayed than the floor.
async fn replay(mut db: Db, dialect: &str) {
    let transcript = transcript();
    let declared = declarations();
    let core = superschematic_rules_behaviors::core();
    let mut counts = Counts::default();
    let mut failed = Vec::new();
    for (index, scenario) in transcript.scenarios.iter().enumerate() {
        counts.skipped += scenario.skipped.len();
        let mut replaying = Scenario::new(index, scenario, &declared);
        replaying.replay(&mut db, &core, &mut counts).await;
        if !replaying.mismatches.is_empty() {
            failed.push(format!(
                "{}:\n  {}",
                scenario.name,
                replaying.mismatches.join("\n  ")
            ));
        }
    }
    let replayed = counts.commands + counts.checks + counts.raws;
    println!(
        "{} on {dialect}: {} scenarios, {replayed} steps replayed ({} commands, {} checks, {} raw writes), {} left out by the transcript with its reasons",
        transcript.file,
        transcript.scenarios.len(),
        counts.commands,
        counts.checks,
        counts.raws,
        counts.skipped
    );
    assert!(
        failed.is_empty(),
        "{} scenarios answered otherwise than the transcript:\n{}",
        failed.len(),
        failed.join("\n")
    );
    assert!(
        replayed >= FLOOR,
        "{replayed} steps replayed, under the floor of {FLOOR}"
    );
    assert_eq!(
        replayed,
        transcript.steps(),
        "every step the transcript holds is replayed"
    );
}

#[cfg(feature = "sqlite")]
#[tokio::test]
async fn transcript_replays_on_sqlite() {
    let conn = rusqlite::Connection::open_in_memory().expect("SQLite opens");
    replay(Db::Sqlite(conn), "SQLite").await;
}

#[cfg(feature = "postgres")]
#[tokio::test]
async fn transcript_replays_on_postgres() {
    let Some(url) = common::pg::dsn(
        "set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to replay the transcript on Postgres",
    ) else {
        return;
    };
    common::pg::on_schema(url, |schema| async move {
        let client = schema.client().await;
        replay(Db::Postgres(client), "Postgres").await;
    })
    .await;
}
