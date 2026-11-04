//! The recorded scenarios are the specification (D103): the transcripts
//! `superschematic-rules-testkit` writes from `core-workflow.json`,
//! `core-timers.json`, `core-reactions.json`,
//! `core-reactions-changes.json`, `core-constants.json`,
//! `core-staterules.json` and `core-approvals.json`, and from the scenarios
//! written by hand in their format where the engine recorded none,
//! `core-workflow-principals.json` (D104: a transition's `by`,
//! `overridePermission`, `repeat` and `from`) and
//! `core-freezes-and-sign-offs.json` (D104: Constants' `oneWay`,
//! StateRules' `by`, and Approvals' `resetOn` and `pinned`), whose
//! transcripts hold each command's row version too
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, whose format its
//! `transcript` module states) replayed through this adapter on rusqlite
//! and on Postgres, as the HTTP runtime's `src/rules-transcript.test.ts`
//! replays them through TypeScript's. Each scenario gets a table per type
//! and database, its own fields' columns (a link's `<link>Id` among them)
//! beside the owned columns the layout rule gives each use of any version
//! and `_version` with sqlgen's trigger, a sub-table per table a use's
//! behavior declares, and a deadline queue when a use's behavior arms
//! deadlines; each check runs on the core alone, answering whether the
//! type has rows from its table; each command runs through `execute` in a
//! transaction of its own over a layout of every type of its database, the
//! command's type at the version live at it and each other at the version
//! the scenario last made live (a deadline the runner ran among them, run
//! only while it is armed at its time and its message spent with it, and a
//! write a type's change trigger queued, run as written), its refusal
//! compared whole and the row it names read back after it and compared,
//! numbers by value,
//! with its version when the transcript holds one (so a repeat answered
//! unchanged is held to writing nothing), that row's sub-table rows and its
//! armed deadlines and the other rows its change set wrote; and each raw
//! step writes owned columns with
//! SQL past the adapter. The Postgres replay is skipped without
//! `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
//!
//! The tables here follow the layout rule as the core's README states it,
//! restated by this test (the corpus's uses are type-anchored: Workflow's
//! `workflow_status` and `workflow_entered_at`, Timers' sub-table
//! `<table>_timers_deadlines`). The layout the generator writes is held by
//! the generated packages' own end-to-end tests.

#![cfg(any(feature = "sqlite", feature = "postgres"))]

mod common;

use std::collections::{BTreeMap, HashMap, HashSet};

use serde_json::{json, Map, Value};
use superschematic_rules::{Answer, Answers, Core, ReadRequest, Refusal, Step};
use superschematic_rules_adapter::{
    Action, Column, Command, Convert, Deadlines, Error, ExecuteOptions, Executed, Host, Layout,
    Operation, OwnedField, Row, SubRow, SubTable, Table, TypeSpec, Use,
};
use superschematic_rules_testkit::{
    transcript_path, Transcript, TranscriptScenario, TranscriptStep, TranscriptType,
};

use common::{camel, column_types, p, snake, storage_key, Param};

/// The transcripts every dialect replays, each with the steps it holds,
/// the testkit's floor (`tests/transcript.rs`): each dialect replays every
/// one, so a regression cannot pass by skipping.
const TRANSCRIPTS: [(&str, usize); 9] = [
    ("core-workflow", 96),
    ("core-timers", TIMERS_FLOOR),
    ("core-reactions", REACTIONS_FLOOR),
    ("core-reactions-changes", REACTIONS_CHANGES_FLOOR),
    ("core-workflow-principals", WORKFLOW_PRINCIPALS_FLOOR),
    ("core-constants", CONSTANTS_FLOOR),
    ("core-staterules", STATERULES_FLOOR),
    ("core-approvals", APPROVALS_FLOOR),
    ("core-freezes-and-sign-offs", FREEZES_AND_SIGN_OFFS_FLOOR),
];

/// core-timers' floor, as the core's milestone states it: 139 steps, 85
/// commands (12 of them deadlines and 29 changes the runner ran) and 54
/// checks (D103), the scenario whose fire StateRules vetoes among them
/// since StateRules runs on the core (D104).
const TIMERS_FLOOR: usize = 139;

/// core-reactions' floor, as the core's milestone states it: 586 steps,
/// 476 commands (252 of them changes the runner ran) and 110 checks.
const REACTIONS_FLOOR: usize = 586;

/// core-reactions-changes' floor, as the core's milestone states it: 60
/// steps, 48 commands (24 of them changes) and 12 checks.
const REACTIONS_CHANGES_FLOOR: usize = 60;

/// core-workflow-principals' floor, as the core's milestone states it: 92
/// steps of the scenarios written by hand (D104), 58 commands, each holding
/// its row's version, and 34 checks. What the transcript leaves out, with
/// its reasons, is the scenario whose gate is a test behavior of the
/// reference host's, and the opens and reads.
const WORKFLOW_PRINCIPALS_FLOOR: usize = 92;

/// core-constants' floor, as the core's milestone states it: 39 steps, 20
/// commands (5 creates and 15 updates, its object, list and string fields
/// among their values) and 19 checks (D104).
const CONSTANTS_FLOOR: usize = 39;

/// core-staterules' floor, as the core's milestone states it: 76 steps, 43
/// commands (13 of them transitions) and 33 checks (D104).
const STATERULES_FLOOR: usize = 76;

/// core-approvals' floor, as the core's milestone states it: 122 steps, 93
/// commands (71 of them operations, the sign-offs kept in a sub-table, and
/// 5 changes) and 29 checks (D104).
const APPROVALS_FLOOR: usize = 122;

/// core-freezes-and-sign-offs' floor, as the core's milestone states it:
/// 115 steps of the scenarios written by hand (D104), 75 commands, each
/// holding its row's version, among them updates of booleans, numbers and
/// date-times, and 40 checks.
const FREEZES_AND_SIGN_OFFS_FLOOR: usize = 115;

/// How many runs a check makes before the test calls it unsettled.
const CHECK_ROUNDS: usize = 16;

fn transcript(name: &str) -> Transcript {
    let path = transcript_path(name);
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
    /// Each sub-table it keeps per row: its name, its key field, and its
    /// other fields with their types.
    tables: Vec<DeclaredTable>,
    /// Whether it arms deadlines, which its table's queue holds.
    arms_deadlines: bool,
}

/// A sub-table a declaration declares.
struct DeclaredTable {
    name: String,
    key: String,
    fields: Vec<(String, String)>,
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
            let typed = |fields: &[Value]| -> Vec<(String, String)> {
                fields
                    .iter()
                    .filter_map(|field| {
                        Some((
                            field["name"].as_str()?.to_owned(),
                            field.get("type")?.as_str()?.to_owned(),
                        ))
                    })
                    .collect()
            };
            Declared {
                name: file["name"].as_str().unwrap_or_default().to_owned(),
                value: file.get("value").and_then(Value::as_str).map(str::to_owned),
                fields: typed(&list("fields")),
                operations: list("operations")
                    .iter()
                    .map(|operation| Operation {
                        name: operation["name"].as_str().unwrap_or_default().to_owned(),
                        writes: operation.get("writes") == Some(&Value::Bool(true)),
                    })
                    .collect(),
                tables: list("tables")
                    .iter()
                    .map(|table| {
                        let key = table["key"].as_str().unwrap_or_default().to_owned();
                        let fields = table["fields"].as_array().cloned().unwrap_or_default();
                        DeclaredTable {
                            name: table["name"].as_str().unwrap_or_default().to_owned(),
                            fields: typed(&fields)
                                .into_iter()
                                .filter(|(name, _)| *name != key)
                                .collect(),
                            key,
                        }
                    })
                    .collect(),
                arms_deadlines: file.get("armsDeadlines") == Some(&Value::Bool(true)),
            }
        })
        .collect()
}

/// A declared field's column by its declared name and IR type, a
/// date-time held as epoch milliseconds.
fn column_of(name: &str, column: String, type_name: &str) -> Column {
    let column = Column::new(name, column, type_name);
    if type_name == "Temporal.DateTime" {
        return column.converted(Convert::EpochMillis);
    }
    column
}

/// The uses of a type across its versions, each with the owned columns the
/// layout rule gives it, its declared operations and its sub-tables, each
/// `<table>_<member>_<declared name>` in snake case with a column of the
/// row's key, `<type>_id`.
fn uses_of(declared: &[Declared], type_def: &TranscriptType, table: &str) -> Vec<Use> {
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
                    OwnedField {
                        name: name.clone(),
                        column: column_of(&camel(&column), column, type_name),
                        value,
                    }
                })
                .collect();
            let member = match anchor {
                Some(anchor) => anchor.to_owned(),
                None => camel(&storage_key(&entry.behavior)),
            };
            let tables = declaration
                .tables
                .iter()
                .map(|declared| SubTable {
                    name: declared.name.clone(),
                    table: format!("{table}_{}_{}", snake(&member), snake(&declared.name)),
                    row: format!("{}_id", snake(&type_def.name)),
                    key: OwnedField {
                        name: declared.key.clone(),
                        column: Column::new(&declared.key, snake(&declared.key), "string"),
                        value: false,
                    },
                    fields: declared
                        .fields
                        .iter()
                        .map(|(name, type_name)| OwnedField {
                            name: name.clone(),
                            column: column_of(name, snake(name), type_name),
                            value: false,
                        })
                        .collect(),
                })
                .collect();
            uses.push(Use {
                id: id.to_owned(),
                behavior: entry.behavior.clone(),
                member,
                anchor: anchor.map(str::to_owned),
                fields,
                operations: declaration.operations.clone(),
                tables,
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

/// Whether a declaration of a behavior any of the uses run arms deadlines.
fn arms_deadlines(declared: &[Declared], uses: &[Use]) -> bool {
    uses.iter().any(|used| {
        declared
            .iter()
            .any(|declaration| declaration.name == used.behavior && declaration.arms_deadlines)
    })
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

    /// Reads a row's rows of a use's sub-table back in a fresh transaction.
    async fn read_sub_rows(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        use_id: &str,
        table: &str,
    ) -> Vec<SubRow> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                superschematic_rules_adapter::sqlite::read_sub_rows(
                    host, &tx, type_name, key, use_id, table,
                )
                .expect("the sub-table rows read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                superschematic_rules_adapter::postgres::read_sub_rows(
                    host, &tx, type_name, key, use_id, table,
                )
                .await
                .expect("the sub-table rows read")
            }
        }
    }

    /// A row's armed deadlines, its live messages in the queue, by use id
    /// then name: each its use id, name, time and payload.
    async fn armed(&mut self, queue: &str, key: &str) -> Vec<Value> {
        let order = if self.postgres() {
            " COLLATE \"C\""
        } else {
            ""
        };
        let text = format!(
            "SELECT use_id, name, at, CAST(payload AS TEXT) FROM \"{queue}\" WHERE row_key = {} AND state IN ('ready', 'claimed') ORDER BY use_id{order}, name{order}",
            p(self.postgres(), 1)
        );
        let rows: Vec<(String, String, i64, Option<String>)> = match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let rows = statement
                    .query_map([key], |row| {
                        Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?))
                    })
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query(&text, &[&key])
                .await
                .expect("the read runs")
                .iter()
                .map(|row| (row.get(0), row.get(1), row.get(2), row.get(3)))
                .collect(),
        };
        rows.into_iter()
            .map(|(use_id, name, at, payload)| {
                let payload = payload
                    .map(|text| serde_json::from_str(&text).expect("a payload of JSON"))
                    .unwrap_or(Value::Null);
                json!({"useId": use_id, "name": name, "at": at, "payload": payload})
            })
            .collect()
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

/// One scenario's replay, its tables named `<prefix>_s<index>_d<database>_..`.
struct Scenario<'s> {
    prefix: &'s str,
    index: usize,
    scenario: &'s TranscriptScenario,
    databases: Vec<&'s str>,
    declared: &'s [Declared],
    created: HashSet<String>,
    /// The index of each type's version the scenario last made live.
    live: HashMap<String, usize>,
    mismatches: Vec<String>,
}

impl<'s> Scenario<'s> {
    fn new(
        prefix: &'s str,
        index: usize,
        scenario: &'s TranscriptScenario,
        declared: &'s [Declared],
    ) -> Self {
        let mut databases: Vec<&str> = Vec::new();
        for step in &scenario.steps {
            let database = database_of(step);
            if !databases.contains(&database) {
                databases.push(database);
            }
        }
        Scenario {
            prefix,
            index,
            scenario,
            databases,
            declared,
            created: HashSet::new(),
            live: HashMap::new(),
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
        let table = format!("{}_s{}_d{at}_{}", self.prefix, self.index, snake(type_name));
        let uses = type_def
            .map(|known| uses_of(self.declared, known, &table))
            .unwrap_or_default();
        let deadlines = arms_deadlines(self.declared, &uses).then(|| Deadlines {
            queue: format!("{type_name}Deadline"),
            table: format!("{table}_deadline_queue"),
            row_key: "row_key".into(),
            use_id: "use_id".into(),
            name: "name".into(),
            at: "at".into(),
            payload: "payload".into(),
        });
        Table {
            type_name: type_name.to_owned(),
            table,
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
            audit: Vec::new(),
            uses,
            deadlines,
            changes: None,
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
        // Each sub-table, its rows deleted with the row's, and keyed by the
        // row and their key.
        for sub in table.uses.iter().flat_map(|used| &used.tables) {
            let mut columns = vec![
                format!(
                    "\"{}\" TEXT NOT NULL REFERENCES \"{}\" (id) ON DELETE CASCADE",
                    sub.row, table.table
                ),
                format!("\"{}\" TEXT NOT NULL", sub.key.column.column),
            ];
            for field in &sub.fields {
                columns.push(format!(
                    "\"{}\" {}",
                    field.column.column,
                    sql_type(postgres, &field.column)
                ));
            }
            columns.push(format!(
                "PRIMARY KEY (\"{}\", \"{}\")",
                sub.row, sub.key.column.column
            ));
            db.exec(
                &format!("CREATE TABLE \"{}\" ({})", sub.table, columns.join(", ")),
                &[],
            )
            .await;
        }
        if let Some(queue) = &table.deadlines {
            for ddl in common::deadline_queue_ddl(postgres, &queue.table, "TEXT") {
                db.exec(&ddl, &[]).await;
            }
        }
    }

    fn compare(&mut self, step: usize, call: &str, what: &str, expected: Value, actual: Value) {
        if !same(&expected, &actual) {
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
        // A check that passes makes its spec the type's live version, as
        // the define or publish the recording made after it.
        if expect.refused.is_none() {
            if let Some(index) = self
                .scenario
                .types
                .iter()
                .find(|known| known.name == spec.name)
                .and_then(|known| known.versions.iter().position(|version| version == spec))
            {
                self.live.insert(spec.name.clone(), index);
            }
        }
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
        self.live.insert(type_name.clone(), *version);
        let table = self.table_of(database, type_name, spec);
        self.ensure(db, &table).await;
        // Every other type of the database at the version the scenario
        // last made live: what a listener or a rule's move reads of it.
        let mut tables = vec![table];
        for other in &self.scenario.types {
            if other.name == *type_name {
                continue;
            }
            let spec = self
                .live
                .get(&other.name)
                .and_then(|at| other.versions.get(*at));
            let table = self.table_of(database, &other.name, spec);
            self.ensure(db, &table).await;
            tables.push(table);
        }
        let layout = Layout::new(tables);
        let host = Host::new(core, &layout);
        let (refused, result, key, written) =
            match db.execute(&host, type_name, command.clone()).await {
                Ok(executed) => (None, executed.result, executed.key, executed.changes.rows),
                Err(Error::Refused(refusal)) => (Some(*refusal), Value::Null, None, Vec::new()),
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
            let version = row.as_ref().map(|row| row.version);
            let actual = row.map(|row| json!({"own": row.own, "owned": row.owned}));
            self.compare(*at, call, "row", json!(expect.row), json!(actual));
            // The version a hand-written step holds (D104), which a repeat
            // that writes nothing leaves where the step before it left it.
            if let Some(held) = expect.version {
                self.compare(*at, call, "version", json!(held), json!(version));
            }
            // The row's sub-table rows and its armed deadlines, each absent
            // when it has none.
            let expected = serde_json::to_value(expect).expect("an expectation of JSON");
            let table = layout.table(type_name).expect("the step's table");
            let mut sub_rows = Map::new();
            for used in &table.uses {
                let mut tables = BTreeMap::new();
                for sub in &used.tables {
                    let rows = db
                        .read_sub_rows(&host, type_name, &row_key, &used.id, &sub.name)
                        .await;
                    if !rows.is_empty() {
                        tables.insert(sub.name.clone(), rows);
                    }
                }
                if !tables.is_empty() {
                    sub_rows.insert(used.id.clone(), json!(tables));
                }
            }
            let actual = (!sub_rows.is_empty()).then_some(Value::Object(sub_rows));
            self.compare(
                *at,
                call,
                "subRows",
                json!(expected.get("subRows")),
                json!(actual),
            );
            let armed = match &table.deadlines {
                Some(queue) => db.armed(&queue.table, &row_key).await,
                None => Vec::new(),
            };
            let actual = (!armed.is_empty()).then_some(Value::Array(armed));
            self.compare(
                *at,
                call,
                "deadlines",
                json!(expected.get("deadlines")),
                json!(actual),
            );
        }
        // The other rows the change set wrote, each once, in its order,
        // read back after the step: a row a rule moved through a relation.
        let own_key = match &command.action {
            Action::Create { id, .. } => id.clone().or(key.clone()),
            action => action.key().map(str::to_owned),
        };
        let mut others: Vec<(String, String)> = Vec::new();
        for write in &written {
            let (Some(write_key), write_type) = (write.key(), write.type_name()) else {
                continue;
            };
            let other = (write_type.to_owned(), write_key.to_owned());
            let mine = write_type == type_name && own_key.as_deref() == Some(write_key);
            if !mine && !others.contains(&other) {
                others.push(other);
            }
        }
        let mut actual = Vec::new();
        for (other_type, other_key) in others {
            let mut entry = json!({"type": other_type, "key": other_key});
            if let Some(row) = db.read_row(&host, &other_type, &other_key).await {
                entry["row"] = json!({"own": row.own, "owned": row.owned});
            }
            actual.push(entry);
        }
        let expected = serde_json::to_value(expect).expect("an expectation of JSON");
        let actual = (!actual.is_empty()).then_some(Value::Array(actual));
        self.compare(
            *at,
            call,
            "others",
            json!(expected.get("others")),
            json!(actual),
        );
    }
}

/// Whether two JSON values are one value, as the core compares them (D104):
/// numbers by value, so a `5` the transcript holds is the `5.0` a column
/// may give back; objects by member, whatever their order; arrays in
/// order; the rest as they are. A refusal's message is text, compared as
/// it is.
fn same(expected: &Value, actual: &Value) -> bool {
    match (expected, actual) {
        (Value::Number(a), Value::Number(b)) => {
            a == b
                || match (a.as_i64(), b.as_i64()) {
                    (Some(a), Some(b)) => a == b,
                    _ => a.as_f64().is_some() && a.as_f64() == b.as_f64(),
                }
        }
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(a, b)| same(a, b))
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(name, a)| b.get(name).is_some_and(|b| same(a, b)))
        }
        _ => expected == actual,
    }
}

fn database_of(step: &TranscriptStep) -> &str {
    match step {
        TranscriptStep::Command { database, .. }
        | TranscriptStep::Check { database, .. }
        | TranscriptStep::Raw { database, .. } => database,
    }
}

/// Replays every transcript on db, each in a namespace of tables of its
/// own.
async fn replay(mut db: Db, dialect: &str) {
    for (index, (name, floor)) in TRANSCRIPTS.into_iter().enumerate() {
        replay_one(&mut db, dialect, &format!("t{index}"), name, floor).await;
    }
}

/// Replays every scenario of a transcript on db, failing on any mismatch,
/// listed in full by scenario, or on fewer steps replayed than its floor.
async fn replay_one(db: &mut Db, dialect: &str, prefix: &str, name: &str, floor: usize) {
    let transcript = transcript(name);
    let declared = declarations();
    let core = superschematic_rules_behaviors::core();
    let mut counts = Counts::default();
    let mut failed = Vec::new();
    for (index, scenario) in transcript.scenarios.iter().enumerate() {
        counts.skipped += scenario.skipped.len();
        let mut replaying = Scenario::new(prefix, index, scenario, &declared);
        replaying.replay(db, &core, &mut counts).await;
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
        replayed >= floor,
        "{replayed} steps of {name} replayed, under the floor of {floor}"
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
