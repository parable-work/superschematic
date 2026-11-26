//! The recorded scenarios are the specification (D103), and development
//! mode is a layout the core's host reads (D117): every transcript the
//! testkit writes from the recordings of the behaviors ported onto the core
//! (`runtime/engine/testdata/rules/<name>.transcript.json`), and from the
//! scenarios written by hand in their format, replayed through the
//! handler's bridge over the development layout, each row's own fields one
//! JSON object and its owned fields another, each behavior's sub-table rows
//! in its shared sub-table: on SQLite in memory and in a file, and over
//! memory. Each scenario's database is a store of its own; each command
//! runs over every type of it, the command's at the version live at it and
//! each other at the version the scenario last made live, its refusal or
//! its result compared whole, and the row it names read back after it and
//! compared, with its version when the transcript holds one, its sub-table
//! rows, its armed deadlines, the other rows its change set wrote, and the
//! events it wrote, read from the event queue in the order written. A check
//! runs on the core alone, its type's rows read from the store, and a
//! publish's drops the shared tables' rows of each use it no longer lists,
//! as development mode's publish does (D103's step 4); a raw step writes
//! owned fields past the bridge, moving the row's version as a table's
//! trigger does.

use std::collections::{BTreeMap, HashMap};

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Answer, Answers, Command, Core, ReadRequest, Refusal, Step, TypeSpec,
};
use superschematic_rules_dev::SqliteStore;
use superschematic_rules_handler::{
    set_owned, Bridge, Changed, Declarations, MemoryStore, Ran, Store, StoreAnswer, StoreAnswers,
    StoreRead, StoreWrite, StoredRow, Unit,
};
use superschematic_rules_testkit::{
    transcript_path, Transcript, TranscriptExpect, TranscriptScenario, TranscriptStep,
};

/// The transcripts and the floors `superschematic-rules-adapter`'s replay
/// holds them to: each store replays every one.
const TRANSCRIPTS: [(&str, usize); 36] = [
    ("core-workflow", 96),
    ("core-timers", 139),
    ("core-reactions", 618),
    ("core-reactions-changes", 62),
    ("core-workflow-principals", 101),
    ("core-constants", 49),
    ("core-staterules", 88),
    ("core-approvals", 146),
    ("core-freezes-and-sign-offs", 115),
    ("core-lease", 474),
    ("core-presence", 141),
    ("core-budget", 495),
    ("core-retries", 250),
    ("core-retries-waits", 158),
    ("core-periods-and-presence", 77),
    ("core-seats", 88),
    ("core-reconcile", 58),
    ("core-schedules", 161),
    ("core-pool", 82),
    ("core-rollups", 180),
    ("core-dependencies", 266),
    ("core-numbering", 104),
    ("core-lease-numbering", 14),
    ("core-revisions", 112),
    ("core-comments", 361),
    ("core-comments-beside", 25),
    ("core-derived-provenance", 75),
    ("core-holds", 71),
    ("core-holds-timers", 14),
    ("core-commerce", 66),
    ("core-bookings", 83),
    ("core-calendar", 47),
    ("core-refunds", 52),
    ("core-payments", 73),
    ("core-blueprint", 151),
    ("core-aliased", 46),
];

fn transcript(name: &str) -> Transcript {
    let path = transcript_path(name);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    serde_json::from_str(&text).expect("the transcript reads")
}

/// What a store does for the replay beyond the handler's contract: a row's
/// armed deadlines.
trait Inspect: Store {
    fn armed_of(&mut self, type_name: &str, id: &str) -> Vec<Value>;
}

impl Inspect for MemoryStore {
    fn armed_of(&mut self, type_name: &str, id: &str) -> Vec<Value> {
        self.armed(type_name, id)
            .into_iter()
            .map(|(use_id, name, at, payload)| json!({"useId": use_id, "name": name, "at": at, "payload": payload}))
            .collect()
    }
}

impl Inspect for SqliteStore {
    fn armed_of(&mut self, type_name: &str, id: &str) -> Vec<Value> {
        self.armed(type_name, id)
            .expect("the deadlines read")
            .into_iter()
            .map(|(use_id, name, at, payload)| json!({"useId": use_id, "name": name, "at": at, "payload": payload}))
            .collect()
    }
}

/// Whether two JSON values are one value: numbers by value, objects by
/// member whatever their order, arrays in order.
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

#[derive(Default)]
struct Counts {
    commands: usize,
    checks: usize,
    raws: usize,
}

struct Scenario<'s, S> {
    scenario: &'s TranscriptScenario,
    core: &'s Core,
    declarations: &'s Declarations,
    stores: BTreeMap<String, S>,
    make: &'s dyn Fn() -> S,
    live: HashMap<String, usize>,
    events: BTreeMap<String, i64>,
    mismatches: Vec<String>,
}

fn read_row<S: Store>(store: &mut S, type_name: &str, id: &str) -> Option<StoredRow> {
    match store
        .read(&StoreRead::Row {
            type_name: type_name.to_owned(),
            id: id.to_owned(),
        })
        .expect("the row reads")
    {
        StoreAnswer::Row(row) => row,
        other => panic!("a row read answered {other:?}"),
    }
}

impl<'s, S: Inspect> Scenario<'s, S> {
    fn store(&mut self, database: &str) -> &mut S {
        let make = self.make;
        self.stores.entry(database.to_owned()).or_insert_with(make)
    }

    fn compare(&mut self, step: usize, call: &str, what: &str, expected: Value, actual: Value) {
        if !same(&expected, &actual) {
            self.mismatches.push(format!(
                "step {step} ({call}) {what}:\n    expected {expected}\n    actual   {actual}"
            ));
        }
    }

    /// Every type of the scenario at the version it last made live, the
    /// command's own at `own`.
    fn specs(&self, own: &TypeSpec) -> BTreeMap<String, TypeSpec> {
        let mut specs = BTreeMap::new();
        for type_def in &self.scenario.types {
            let at = self.live.get(&type_def.name).copied().unwrap_or(0);
            if let Some(spec) = type_def.versions.get(at) {
                specs.insert(type_def.name.clone(), spec.clone());
            }
        }
        specs.insert(own.name.clone(), own.clone());
        specs
    }

    fn replay(&mut self, counts: &mut Counts) {
        for step in &self.scenario.steps {
            match step {
                TranscriptStep::Check { .. } => {
                    counts.checks += 1;
                    self.check(step);
                }
                TranscriptStep::Raw { .. } => {
                    counts.raws += 1;
                    self.raw(step);
                }
                TranscriptStep::Command { .. } => {
                    counts.commands += 1;
                    self.command(step);
                }
            }
        }
    }

    fn check(&mut self, step: &TranscriptStep) {
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
        let core = self.core;
        let store = self.store(database);
        let mut answers = Answers::new();
        let mut refused = None;
        for _ in 0..16 {
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
                        let rows = match store
                            .read(&StoreRead::Rows {
                                type_name: type_name.clone(),
                            })
                            .expect("the rows read")
                        {
                            StoreAnswer::Rows(rows) => rows,
                            other => panic!("a rows read answered {other:?}"),
                        };
                        answers.answer(request, Answer::HasRows(!rows.is_empty()));
                    }
                }
                other => panic!("step {at}: a check asked for {other:?}"),
            }
        }
        if refused.is_none() && matches!(command.action, Action::Publish { .. }) {
            self.drop_shared_of_removed_uses(database, spec);
        }
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
    }

    /// Drops the rows of the shared tables of each use of a type that a
    /// published version no longer lists, or gives another behavior, as
    /// development mode's publish does with a removed use's writes
    /// (D103's step 4), so a use added again counts from fresh.
    fn drop_shared_of_removed_uses(&mut self, database: &str, spec: &TypeSpec) {
        let mut writes = Vec::new();
        let versions = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == spec.name)
            .map(|known| known.versions.as_slice())
            .unwrap_or_default();
        for entry in versions.iter().flat_map(|version| &version.uses) {
            let kept = spec
                .uses
                .iter()
                .any(|used| used.id() == entry.id() && used.behavior == entry.behavior);
            let shares = self
                .declarations
                .get(&entry.behavior)
                .is_some_and(|declared| declared.tables.iter().any(|table| table.shared));
            let write = StoreWrite::DropUse {
                type_name: spec.name.clone(),
                behavior: entry.behavior.clone(),
                use_id: entry.id().to_owned(),
            };
            if !kept && shares && !writes.contains(&write) {
                writes.push(write);
            }
        }
        if writes.is_empty() {
            return;
        }
        let store = self.store(database);
        store.begin().expect("begun");
        let applied = store
            .apply(&Unit {
                signals: Vec::new(),
                writes,
            })
            .expect("the drop applies");
        store.commit().expect("committed");
        assert!(applied, "a drop of a removed use's shared rows moved");
    }

    fn raw(&mut self, step: &TranscriptStep) {
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
        let store = self.store(database);
        let Some(row) = read_row(store, type_name, key) else {
            self.mismatches.push(format!(
                "step {at} ({call}): no row {type_name} {key} to write"
            ));
            return;
        };
        let unit = Unit {
            signals: Vec::new(),
            writes: vec![StoreWrite::Update {
                type_name: type_name.clone(),
                id: key.clone(),
                expected: row.version,
                data: row.data.clone(),
                owned: set_owned(&row.owned, owned),
            }],
        };
        store.begin().expect("begun");
        let applied = store.apply(&unit).expect("the raw write applies");
        store.commit().expect("committed");
        self.compare(*at, call, "raw.applied", json!(true), json!(applied));
    }

    /// Runs a command through the bridge in a transaction of its own,
    /// committed when its unit applies, again from fresh state when a
    /// fence did not hold.
    fn execute(
        core: &Core,
        specs: &BTreeMap<String, TypeSpec>,
        spec: &TypeSpec,
        command: &Command,
        store: &mut S,
    ) -> Result<Changed, Refusal> {
        let hearing = Default::default();
        // The reference host's mapping, each patch merged as the core
        // wrote it: a transcript leaves out what a store's own validation
        // refuses.
        let bridge = Bridge {
            core,
            specs,
            hearing: &hearing,
            own: None,
            readable: None,
        };
        for _ in 0..8 {
            store.begin()?;
            let mut answers = StoreAnswers::new();
            loop {
                match bridge.run(spec, command, &answers) {
                    Ran::Read(reads) => {
                        for read in reads {
                            let answer = store.read(&read)?;
                            answers.answer(read, answer);
                        }
                    }
                    Ran::Refused(refusal) => {
                        store.rollback()?;
                        return Err(refusal);
                    }
                    Ran::Changed(changed) => {
                        let unit = Unit {
                            signals: Vec::new(),
                            writes: changed.writes.clone(),
                        };
                        if store.apply(&unit)? {
                            store.commit()?;
                            return Ok(*changed);
                        }
                        store.rollback()?;
                        break;
                    }
                }
            }
        }
        Err(Refusal::internal("the command kept moving"))
    }

    fn command(&mut self, step: &TranscriptStep) {
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
        let Some(spec) = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == *type_name)
            .and_then(|known| known.versions.get(*version))
            .cloned()
        else {
            self.mismatches.push(format!(
                "step {at} ({call}): no version {version} of {type_name}"
            ));
            return;
        };
        self.live.insert(type_name.clone(), *version);
        let specs = self.specs(&spec);
        let core = self.core;
        let outcome = Self::execute(core, &specs, &spec, command, self.store(database));
        let (refused, result, written) = match outcome {
            Ok(changed) => (None, changed.changes.result.clone(), changed.changes.rows),
            Err(refusal) => (Some(refusal), Value::Null, Vec::new()),
        };
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
        self.compare(*at, call, "result", expect.result.clone(), result);
        let row_key = match &command.action {
            Action::Create { id, .. } => id.clone(),
            action => action.key().map(str::to_owned),
        };
        if let Some(expected) = &expect.key {
            self.compare(*at, call, "key", json!(expected), json!(row_key));
        }
        if let Some(row_key) = row_key.filter(|key| !key.is_empty()) {
            self.compare_row(*at, call, database, &spec, type_name, &row_key, expect);
        }
        let own_key = match &command.action {
            Action::Create { id, .. } => id.clone(),
            action => action.key().map(str::to_owned),
        };
        let mut others: Vec<(String, String)> = Vec::new();
        for write in &written {
            let (Some(key), write_type) = (write.key(), write.type_name()) else {
                continue;
            };
            let other = (write_type.to_owned(), key.to_owned());
            let mine = write_type == type_name && own_key.as_deref() == Some(key);
            if !mine && !others.contains(&other) {
                others.push(other);
            }
        }
        let mut actual = Vec::new();
        for (other_type, other_key) in others {
            let mut entry = json!({"type": other_type, "key": other_key});
            if let Some(row) = read_row(self.store(database), &other_type, &other_key) {
                entry["row"] = json!({"own": row.data, "owned": row.owned});
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
        let after = self.events.get(database.as_str()).copied().unwrap_or(0);
        let events = self.store(database).events(after).expect("the events read");
        if let Some(last) = events.last() {
            self.events.insert(database.clone(), last.seq);
        }
        let actual: Vec<Value> = events
            .iter()
            .map(|event| {
                json!({"type": event.type_name, "key": event.id, "useId": event.use_id, "kind": event.kind, "at": event.at, "payload": event.payload})
            })
            .collect();
        let expected: Vec<Value> = expect
            .events
            .iter()
            .map(|event| serde_json::to_value(event).expect("an event of JSON"))
            .collect();
        self.compare(
            *at,
            call,
            "events",
            Value::Array(expected),
            Value::Array(actual),
        );
    }

    #[allow(clippy::too_many_arguments)]
    fn compare_row(
        &mut self,
        at: usize,
        call: &str,
        database: &str,
        spec: &TypeSpec,
        type_name: &str,
        row_key: &str,
        expect: &TranscriptExpect,
    ) {
        let row = read_row(self.store(database), type_name, row_key);
        let version = row.as_ref().map(|row| row.version);
        let actual = row.map(|row| json!({"own": row.data, "owned": row.owned}));
        self.compare(at, call, "row", json!(expect.row), json!(actual));
        if let Some(held) = expect.version {
            self.compare(at, call, "version", json!(held), json!(version));
        }
        // The sub-table rows of each use of any of the type's versions.
        let mut uses: Vec<(String, String)> = Vec::new();
        let type_def = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == type_name);
        for version in type_def
            .map(|known| known.versions.as_slice())
            .unwrap_or(std::slice::from_ref(spec))
        {
            for entry in &version.uses {
                let used = (entry.id().to_owned(), entry.behavior.clone());
                if !uses.contains(&used) {
                    uses.push(used);
                }
            }
        }
        let mut sub_rows = Map::new();
        for (use_id, behavior) in uses {
            let Some(declared) = self.declarations.get(&behavior) else {
                continue;
            };
            let mut tables = Map::new();
            // A shared table is the type's as a whole, no row's.
            for table in declared.tables.iter().filter(|table| !table.shared) {
                let rows = match self
                    .store(database)
                    .read(&StoreRead::SubRows {
                        type_name: type_name.to_owned(),
                        id: row_key.to_owned(),
                        use_id: use_id.clone(),
                        table: table.name.clone(),
                    })
                    .expect("the sub-table rows read")
                {
                    StoreAnswer::SubRows(rows) => rows,
                    other => panic!("a sub-rows read answered {other:?}"),
                };
                if !rows.is_empty() {
                    tables.insert(table.name.clone(), json!(rows));
                }
            }
            if !tables.is_empty() {
                sub_rows.insert(use_id, Value::Object(tables));
            }
        }
        let expected = serde_json::to_value(expect).expect("an expectation of JSON");
        let actual = (!sub_rows.is_empty()).then_some(Value::Object(sub_rows));
        self.compare(
            at,
            call,
            "subRows",
            json!(expected.get("subRows")),
            json!(actual),
        );
        let armed = self.store(database).armed_of(type_name, row_key);
        let actual = (!armed.is_empty()).then_some(Value::Array(armed));
        self.compare(
            at,
            call,
            "deadlines",
            json!(expected.get("deadlines")),
            json!(actual),
        );
    }
}

/// Replays every scenario of every transcript over stores `make` makes,
/// one per scenario's database, failing on any mismatch or on fewer steps
/// than a transcript's floor.
fn replay_all<S: Inspect>(store: &str, names: &[&str], make: &dyn Fn() -> S) {
    let core = superschematic_rules_behaviors::core();
    let declarations = Declarations::ported();
    for (name, floor) in TRANSCRIPTS
        .iter()
        .filter(|(name, _)| names.is_empty() || names.contains(name))
    {
        let transcript = transcript(name);
        let mut counts = Counts::default();
        let mut failed = Vec::new();
        for scenario in &transcript.scenarios {
            let mut replaying = Scenario {
                scenario,
                core: &core,
                declarations: &declarations,
                stores: BTreeMap::new(),
                make,
                live: HashMap::new(),
                events: BTreeMap::new(),
                mismatches: Vec::new(),
            };
            replaying.replay(&mut counts);
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
            "{name} over {store}: {} scenarios, {replayed} steps ({} commands, {} checks, {} raw writes)",
            transcript.scenarios.len(),
            counts.commands,
            counts.checks,
            counts.raws
        );
        assert!(
            failed.is_empty(),
            "{name} over {store}: {} scenarios answered otherwise than the transcript:\n{}",
            failed.len(),
            failed.join("\n")
        );
        assert!(
            replayed >= *floor,
            "{replayed} steps of {name} replayed, under the floor of {floor}"
        );
        assert_eq!(
            replayed,
            transcript.steps(),
            "every step of {name} is replayed"
        );
    }
}

#[test]
fn the_transcripts_replay_over_sqlite_in_memory() {
    let declarations = Declarations::ported();
    replay_all("SQLite in memory", &[], &|| {
        SqliteStore::memory(&declarations).expect("SQLite opens")
    });
}

#[test]
fn the_transcripts_replay_over_memory() {
    replay_all("memory", &[], &MemoryStore::new);
}

#[test]
fn the_transcripts_replay_over_a_sqlite_file() {
    let declarations = Declarations::ported();
    let dir = std::path::Path::new(env!("CARGO_TARGET_TMPDIR")).join("rules-dev-transcripts");
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("the test's directory");
    let n = std::cell::Cell::new(0);
    replay_all(
        "a SQLite file",
        &[
            "core-workflow",
            "core-timers",
            "core-approvals",
            "core-schedules",
            "core-pool",
        ],
        &|| {
            n.set(n.get() + 1);
            SqliteStore::open(dir.join(format!("s{}.db", n.get())), &declarations)
                .expect("the file opens")
        },
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[cfg(feature = "postgres")]
impl Inspect for superschematic_rules_dev::PgStore {
    fn armed_of(&mut self, type_name: &str, id: &str) -> Vec<Value> {
        self.armed(type_name, id)
            .expect("the deadlines read")
            .into_iter()
            .map(|(use_id, name, at, payload)| {
                json!({"useId": use_id, "name": name, "at": at, "payload": payload})
            })
            .collect()
    }
}

/// Every transcript over a Postgres schema: each scenario's databases in
/// schemas of their own, emptied before each scenario takes one and
/// dropped after. Skipped without `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
#[cfg(feature = "postgres")]
#[test]
fn the_transcripts_replay_over_postgres() {
    let Ok(url) = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") else {
        println!(
            "set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to replay the transcripts on Postgres"
        );
        return;
    };
    let declarations = Declarations::ported();
    let prefix = format!("rules_dev_{}", std::process::id());
    let n = std::cell::Cell::new(0usize);
    replay_all("Postgres", &[], &|| {
        n.set(n.get() + 1);
        let schema = format!("{prefix}_t{}", n.get() % 8);
        let mut store = superschematic_rules_dev::PgStore::open(&url, &schema, &declarations)
            .expect("the Postgres store opens");
        store.clear().expect("the store empties");
        store
    });
    for k in 0..8 {
        superschematic_rules_dev::PgStore::open(&url, &format!("{prefix}_t{k}"), &declarations)
            .and_then(superschematic_rules_dev::PgStore::drop_schema)
            .expect("the schema drops");
    }
}
