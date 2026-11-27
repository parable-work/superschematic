//! The pure core of the behaviors (D103) for a host that reaches it through
//! a C ABI: `superschematic-rules` with the behaviors
//! `superschematic-rules-behaviors` ports, every value of the ABI JSON
//! ([`ffi`]). `@superschematic/rules` (`runtime/rules/typescript`)
//! loads the `wasm32-unknown-unknown` build, as
//! `@superschematic/versiongraph` loads the version graph's
//! (`runtime/versiongraph/rust/src/ffi.rs` is the precedent), and the HTTP
//! runtime's adapter (`runtime/http/typescript/src/rules.ts`) runs its host
//! loop over it. The Go binding (`runtime/rules/go`) links the native
//! build's static archive through cgo, which `scripts/rules-archive.sh`
//! stages as `libsuperschematic_rules.a`, and its adapter runs the same
//! loop.
//!
//! Three operations, each a JSON document in and one out:
//!
//! - `run`: `{"spec": TypeSpec, "command": Command, "answers": Answers}`, the
//!   answers optional and none when absent, to the [`Step`] the core
//!   returns: `{"read": [ReadRequest]}`, `{"lock": [LockKey]}`, or
//!   `{"done": {"refused": Refusal}}` or `{"done": {"changed": ChangeSet}}`.
//!   A refusal is an outcome, not an error. A spec lists its own fields
//!   (`fields`: each one's name, IR type and `dateTime`), which a config
//!   that names a field is held to; a read asks for a row, whether a type
//!   has rows, a type's live version, or one sub-table's rows of a row
//!   (`{"kind": "subRows", "type", "key", "useId", "table"}`, answered
//!   `{"subRows": [{"key", "values"}]}` in key order), or one page of the
//!   rows of a type whose own field holds a key (`{"kind": "linked", "type",
//!   "field", "key", "after"?, "limit"}`, answered `{"linked": {"rows":
//!   [{"key", "row"}], "more"}}` in key order), or one page of the rows of a
//!   type whose use's owned field holds a value (`{"kind": "holding",
//!   "type", "useId", "field", "value", "after"?, "limit"}`, answered as a
//!   linked read is), or one page of a type's live rows whose own fields
//!   hold the values `equal` names, each with the sub-table rows `tables`
//!   names (`{"kind": "rows", "type", "equal"?, "tables"?: [{"useId",
//!   "table"}], "after"?, "limit"}`, answered as a linked read is, each row
//!   with `"tables": {<use>: {<table>: [{"key", "values"}]}}`, D104); a
//!   command may be an operation of the table as a whole, which names no
//!   row (`{"kind": "tableOperation", "useId", "operation", "params"}`,
//!   D104); a command may carry the caller's preconditions by use
//!   id (`"preconditions": {"Lease": {"token": 3}}`, D104), which the core
//!   holds to the uses before it reads the row; a deadline command (`{"kind":
//!   "deadline", ...}`) runs only while the host holds the deadline armed at
//!   its `at`, which the host checks, never the core (D103); and a change
//!   command (`{"kind": "change", "key", "change": {"write", "at",
//!   "before"?, "after"?}}`) runs a write of a row the table's change queue
//!   holds, whose change set may write the row `{"kind": "gone"}`.
//! - `declarations`: no input (an empty document, or any), to the
//!   declaration file of each behavior the core registers, in its order
//!   (`superschematic_rules_behaviors::DECLARATIONS`): a JSON array of the
//!   documents the compiler registers, each field's column type, the value
//!   field, the sub-tables (`tables`: each one's name, key and typed
//!   fields), whether it arms deadlines (`armsDeadlines`) and whether it
//!   hears its row's writes (`hearsChanges`) among them, which a host's
//!   tables, deadline queue and change queue are made from.
//! - `check`: `{"specs": [TypeSpec], "caller"?: Caller, "now"?: number}`, to
//!   the define check of each spec, in order: a JSON array whose entry is
//!   null for a spec the core takes and the [`Refusal`] for one it refuses.
//!   A host checks the specs its layout holds when it first opens the core,
//!   so a core that lacks a behavior or refuses a config fails then.
//! - `declare`: `{"declarations": [declaration file]}`, the declaration
//!   files of declarative behaviors (D103's step 5), each whole, to
//!   `{"declared": [name]}`, every declarative behavior the core runs since:
//!   `run` and `check` run them after the core's own, and `declarations`
//!   lists their files after the core's. A file the vocabulary does not
//!   read, one that names a behavior the core implements, and a second file
//!   of a declared name that differs from the first are the error document
//!   `invalid_input` with the reason; a file declared again is one behavior.
//!
//! - `expand`: a recurrence (D152), `{"anchor", "timeZone"?, "rule"?,
//!   "start"?, "every"?, "lastDone"?, "date"?, "offset"?, "adjust"?,
//!   "weekendDays"?, "holidays"?, "opensBefore"?, "remindBefore"?,
//!   "overdueAfter"?, "from"?, "to"?, "limit"?}`, to its dated occurrences,
//!   `{"occurrences": [{"key", "due", "dueAt", "opensAt"?, "remindAt"?,
//!   "overdueAt", "shifted"?, "projected"?}], "more"}`
//!   ([`superschematic_rules_behaviors::recurrence::expand`] says how each
//!   anchor dates them). A recurrence it refuses (a rule that is no RRULE,
//!   a zone the bundled database lacks) is the error document
//!   `invalid_input` with the reason, so every language dates a rule alike
//!   and refuses one alike.
//!
//! An input the operation cannot read is an error document,
//! `{"error": {"code", "message"}}` with the code `invalid_json` or
//! `invalid_input`, and a panic `internal`; [`run_json`] says which by its
//! second value.
//!
//! The core is built once per process (on wasm, once per instance), the
//! first time an operation runs, and kept: a behavior reads its declaration
//! file when it is registered, which a command should not pay for. A
//! `declare` that adds a behavior builds it again with every one declared.
//! Every thread shares it, since [`Core`] is `Send` and `Sync`, so the Go
//! binding's goroutines, on whatever threads cgo runs them, run one core.
//!
//! A project whose tables compose a behavior a class declares and a Rust
//! crate implements (D103's step 5) builds a core of its own: the core's
//! behaviors and its crates', which the project's rules crate (the build's
//! rulescore generator) assembles and exports behind this ABI with
//! [`export_rules_abi`], depending on this crate without its default
//! `abi` feature, which exports the stock core's. Its archive and module
//! stand in the stock ones' place: the Go binding links whichever archive
//! named `libsuperschematic_rules.a` its `CGO_LDFLAGS` find first, and
//! `@superschematic/rules` loads the module a host names. A `declare` adds
//! declarative behaviors to a project's core as to the stock one, and
//! refuses one its crates register.
//!
//! [`Step`]: superschematic_rules::Step
//! [`Refusal`]: superschematic_rules::Refusal

pub mod ffi;

use std::sync::{Arc, LazyLock, RwLock};

use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::{json, Value};

use superschematic_rules::{Action, Answers, Caller, Command, Core, Outcome, Step, TypeSpec};

/// What every operation of the ABI runs on: a core and the declarative
/// behaviors (D103's step 5) declared on it since. The core is built when
/// the value is, which [`export_rules_abi`]'s static and [`run_json`]'s
/// stock one do the first time an operation runs, and shared by every
/// thread after: Core is Send and Sync, so the Go binding's goroutines, on
/// whatever threads cgo runs them, run one core. A wasm32-unknown-unknown
/// module runs on one thread. A `declare` that adds a behavior builds the
/// core again, the built core's behaviors and every one declared.
pub struct Rules {
    build: fn() -> Core,
    declarations: Vec<&'static str>,
    // The names of the behaviors the built core registers, which a
    // declaration may not take.
    names: Vec<String>,
    held: RwLock<Declared>,
}

// The core and the declaration files of the declarative behaviors it runs
// beside the built ones.
struct Declared {
    core: Arc<Core>,
    files: Arc<Vec<Value>>,
}

impl Rules {
    /// The core `build` makes, whose behaviors' declaration files are
    /// `declarations`, in its order, with no behavior declared yet: the
    /// stock core's ([`superschematic_rules_behaviors::core`] and its
    /// `DECLARATIONS`) or a project's (D103's step 5).
    pub fn new(build: fn() -> Core, declarations: Vec<&'static str>) -> Rules {
        let names = declarations
            .iter()
            .filter_map(|text| {
                let file: Value = serde_json::from_str(text).ok()?;
                file.get("name").and_then(Value::as_str).map(str::to_owned)
            })
            .collect();
        Rules {
            build,
            declarations,
            names,
            held: RwLock::new(Declared {
                core: Arc::new(build()),
                files: Arc::new(Vec::new()),
            }),
        }
    }

    // The core as declare last left it, and the files declared on it; one
    // a panic poisoned answers as it was.
    fn current(&self) -> (Arc<Core>, Arc<Vec<Value>>) {
        let held = self
            .held
            .read()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        (held.core.clone(), held.files.clone())
    }

    // declare adds the declarative behaviors of declaration files to the
    // core, building it again when one is new.
    fn declare(&self, input: &[u8]) -> Result<Value, (&'static str, String)> {
        let input: DeclareInput = parse(Op::Declare, input)?;
        let mut held = self
            .held
            .write()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let mut files = held.files.as_ref().clone();
        for file in input.declarations {
            if !files.contains(&file) {
                files.push(file);
            }
        }
        let refused = |reason: String| ("invalid_input", format!("declare: {reason}"));
        let declared = superschematic_rules_behaviors::declaratives(&files).map_err(refused)?;
        // A project's core registers its crates' behaviors beside the
        // stock ones, which a declaration may not replace either.
        if let Some(behavior) = declared
            .iter()
            .find(|behavior| self.names.iter().any(|name| name == behavior.name()))
        {
            return Err(refused(format!(
                "behavior {}: the core implements it, and a declaration cannot replace it",
                behavior.name()
            )));
        }
        let names: Vec<Value> = declared
            .iter()
            .map(|behavior| Value::from(behavior.name()))
            .collect();
        // Two files of one behavior are refused above; a file given again is
        // already there.
        let files: Vec<Value> = declared
            .iter()
            .map(|behavior| behavior.document().clone())
            .collect();
        if files != *held.files {
            let mut core = (self.build)();
            for behavior in declared {
                core.push(behavior);
            }
            held.core = Arc::new(core);
            held.files = Arc::new(files);
        }
        Ok(json!({ "declared": names }))
    }
}

// The stock core, which run_json runs on: every behavior ported so far.
static STOCK: LazyLock<Rules> = LazyLock::new(|| {
    Rules::new(
        superschematic_rules_behaviors::core,
        superschematic_rules_behaviors::DECLARATIONS.to_vec(),
    )
});

// The core is shared across threads, which only a Send and Sync core may
// be; a behavior that broke it would fail to compile here.
const _: () = {
    const fn shared<T: Send + Sync>() {}
    shared::<Core>();
};

/// The stock core's behaviors' declaration files, in its order.
#[cfg(feature = "abi")]
fn stock_declarations() -> Vec<&'static str> {
    superschematic_rules_behaviors::DECLARATIONS.to_vec()
}

// The stock core's ABI: every behavior ported so far, which a project with
// no crate of its own runs.
#[cfg(feature = "abi")]
export_rules_abi!(
    core = superschematic_rules_behaviors::core,
    declarations = stock_declarations
);

/// An operation of the ABI.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Op {
    Run,
    Declarations,
    Check,
    Expand,
    Declare,
}

impl Op {
    /// The operation's name, as an error names it.
    pub fn name(self) -> &'static str {
        match self {
            Op::Run => "run",
            Op::Declarations => "declarations",
            Op::Check => "check",
            Op::Expand => "expand",
            Op::Declare => "declare",
        }
    }
}

/// `declare`'s input.
#[derive(Deserialize)]
struct DeclareInput {
    declarations: Vec<Value>,
}

/// `run`'s input.
#[derive(Deserialize)]
struct RunInput {
    spec: TypeSpec,
    command: Command,
    #[serde(default)]
    answers: Answers,
}

/// `check`'s input.
#[derive(Deserialize)]
struct CheckInput {
    specs: Vec<TypeSpec>,
    #[serde(default)]
    caller: Caller,
    #[serde(default)]
    now: i64,
}

/// Runs an operation on its JSON input on the stock core and returns its
/// output document, with true for the operation's output and false for an
/// error document.
pub fn run_json(op: Op, input: &[u8]) -> (String, bool) {
    run_json_on(&STOCK, op, input)
}

/// Runs an operation on rules, a core and the behaviors declared on it, as
/// [`run_json`] runs it on the stock core: a project's core
/// ([`export_rules_abi`]) answers through it.
pub fn run_json_on(rules: &Rules, op: Op, input: &[u8]) -> (String, bool) {
    match output_on(rules, op, input) {
        Ok(value) => (value.to_string(), true),
        Err((code, message)) => (error_document(code, &message), false),
    }
}

/// The error document of a refused input: `{"error": {"code", "message"}}`.
pub fn error_document(code: &str, message: &str) -> String {
    json!({ "error": { "code": code, "message": message } }).to_string()
}

fn output_on(rules: &Rules, op: Op, input: &[u8]) -> Result<Value, (&'static str, String)> {
    match op {
        Op::Declarations => {
            let (_, declared) = rules.current();
            Ok(Value::Array(
                rules
                    .declarations
                    .iter()
                    .map(|text| serde_json::from_str(text).unwrap_or(Value::Null))
                    .chain(declared.iter().cloned())
                    .collect(),
            ))
        }
        Op::Declare => rules.declare(input),
        Op::Run => {
            let input: RunInput = parse(op, input)?;
            let (core, _) = rules.current();
            let step: Step = core.run(&input.spec, &input.command, &input.answers);
            serde_json::to_value(step).map_err(|error| ("internal", error.to_string()))
        }
        Op::Expand => {
            let input: Value = parse(op, input)?;
            superschematic_rules_behaviors::recurrence::expand_json(&input)
                .map_err(|reason| ("invalid_input", format!("expand: {reason}")))
        }
        Op::Check => {
            let input: CheckInput = parse(op, input)?;
            let (core, _) = rules.current();
            let define = Command::new(input.caller, input.now, Action::Define);
            let refusals = input
                .specs
                .iter()
                .map(|spec| match core.run(spec, &define, &Answers::new()) {
                    Step::Done(Outcome::Refused(refusal)) => serde_json::to_value(refusal),
                    _ => Ok(Value::Null),
                })
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| ("internal", error.to_string()))?;
            Ok(Value::Array(refusals))
        }
    }
}

// parse reads an operation's input: `invalid_json` for text that is no
// JSON, `invalid_input` for JSON of another shape.
fn parse<T: DeserializeOwned>(op: Op, input: &[u8]) -> Result<T, (&'static str, String)> {
    let document: Value = serde_json::from_slice(input)
        .map_err(|error| ("invalid_json", format!("{}: {error}", op.name())))?;
    serde_json::from_value(document)
        .map_err(|error| ("invalid_input", format!("{}: {error}", op.name())))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn order(config: Value) -> Value {
        json!({"name": "Order", "uses": [{"behavior": "Workflow", "config": config, "anchor": {"field": "status"}}]})
    }

    fn flow() -> Value {
        json!({"states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]})
    }

    #[test]
    fn run_returns_the_cores_step() {
        let input = json!({
            "spec": order(flow()),
            "command": {"caller": {"subject": "sam"}, "now": 1000, "request": "r1",
                "action": {"kind": "operation", "key": "o1", "useId": "status", "operation": "transition", "params": {"to": "paid"}}}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(
            step,
            json!({"read": [{"kind": "row", "type": "Order", "key": "o1"}]})
        );
    }

    #[test]
    fn declarations_are_the_files() {
        let (out, ok) = run_json(Op::Declarations, b"");
        assert!(ok, "{out}");
        let files: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(files[0]["name"], "Workflow");
        assert_eq!(files[0]["value"], "status");
        assert_eq!(files[0]["fields"][1]["type"], "Temporal.DateTime");
        assert_eq!(files[1]["name"], "Reactions");
        assert_eq!(files[1]["hearsChanges"], true);
        assert_eq!(files[2]["name"], "Constants");
        assert_eq!(files[3]["name"], "StateRules");
        assert_eq!(files[4]["name"], "Approvals");
        assert_eq!(files[4]["tables"][0]["name"], "signOffs");
        assert_eq!(files[4]["tables"][0]["key"], "number");
        assert_eq!(
            files[4]["tables"][0]["fields"][4]["type"],
            "Temporal.DateTime"
        );
        assert_eq!(files[5]["name"], "Timers");
        assert_eq!(files[5]["armsDeadlines"], true);
        assert_eq!(files[5]["hearsChanges"], true);
        assert_eq!(files[5]["tables"][0]["name"], "deadlines");
        assert_eq!(files[5]["tables"][0]["key"], "name");
        assert_eq!(
            files[5]["tables"][0]["fields"][3]["type"],
            "Temporal.DateTime"
        );
        let named: Vec<&str> = files
            .as_array()
            .unwrap()
            .iter()
            .filter_map(|file| file["name"].as_str())
            .collect();
        assert_eq!(named, superschematic_rules_behaviors::NAMES);
        let lease = &files[named.iter().position(|name| *name == "Lease").unwrap()];
        assert_eq!(lease["armsDeadlines"], true);
        assert_eq!(lease["tables"][0]["name"], "directives");
        assert!(lease["preconditionSchema"].is_object());
    }

    /// Schedules declares the event it emits (D104), with its payloadSchema,
    /// which a binding hands its host whole; Pool its operation of the table
    /// as a whole and the contract it asks; and Numbering its shared table
    /// (D103's step 4).
    #[test]
    fn schedules_declares_the_event_it_emits() {
        let (out, ok) = run_json(Op::Declarations, b"");
        assert!(ok, "{out}");
        let files: Value = serde_json::from_str(&out).unwrap();
        let named = |name: &str| {
            files
                .as_array()
                .and_then(|files| files.iter().find(|file| file["name"] == name))
                .unwrap()
                .clone()
        };
        let pool = named("Pool");
        assert_eq!(pool["operations"][2]["scope"], "schema");
        assert_eq!(pool["contracts"][0]["role"], "asks");
        assert_eq!(pool["events"][0]["kind"], "recheck");
        let schedules = named("Schedules");
        assert_eq!(schedules["name"], "Schedules");
        assert_eq!(schedules["tables"][0]["name"], "schedules");
        assert_eq!(schedules["events"][0]["kind"], "run");
        assert_eq!(
            schedules["events"][0]["payloadSchema"]["additionalProperties"],
            false
        );
        let numbering = named("Numbering");
        assert_eq!(numbering["value"], "label");
        assert_eq!(numbering["tables"][0]["scope"], "schema");
    }

    // A job whose lease's token a command presents as its precondition.
    fn job() -> Value {
        json!({
            "name": "Job",
            "uses": [
                {"behavior": "Lease", "config": {}, "anchor": "type"},
                {"behavior": "Workflow", "config": flow(), "anchor": {"field": "status"}}
            ],
            "fields": [{"name": "title", "type": "string"}]
        })
    }

    #[test]
    fn a_commands_preconditions_are_held_to_the_uses_before_the_row_is_read() {
        let input = json!({
            "spec": job(),
            "command": {"caller": {"subject": "wren"}, "now": 1000, "request": "r1",
                "action": {"kind": "operation", "key": "j1", "useId": "Lease", "operation": "heartbeat", "params": {}},
                "preconditions": {"Lease": {"token": -1}}}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(step["done"]["refused"]["class"], "PreconditionsError");
        assert_eq!(
            step["done"]["refused"]["message"],
            "preconditions of Job: /Lease/token: must be >= 0"
        );
        // One the uses take goes on to read the row.
        let input = json!({
            "spec": job(),
            "command": {"caller": {"subject": "wren"}, "now": 1000, "request": "r1",
                "action": {"kind": "operation", "key": "j1", "useId": "Lease", "operation": "heartbeat", "params": {}},
                "preconditions": {"Lease": {"token": 1}}}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(
            step,
            json!({"read": [{"kind": "row", "type": "Job", "key": "j1"}]})
        );
    }

    // A task whose project field holds a Project's key, and a project whose
    // Reactions hears its tasks through it.
    fn task() -> Value {
        json!({
            "name": "Task",
            "uses": [{"behavior": "Workflow", "config": flow(), "anchor": {"field": "status"}}],
            "fields": [{"name": "projectId", "type": "Identity.UUID", "relation": {"name": "project", "type": "Project"}}]
        })
    }

    #[test]
    fn a_change_of_a_row_asks_for_its_listeners_and_their_linked_rows() {
        let project = json!({
            "name": "Project",
            "uses": [
                {"behavior": "Workflow", "config": flow(), "anchor": {"field": "status"}},
                {"behavior": "Reactions", "config": {"rules": [{"when": {"allTerminal": {"schema": "Task", "link": "project"}}, "then": {"transition": "paid"}}]}, "anchor": "type"}
            ]
        });
        let input = json!({
            "spec": task(),
            "command": {"caller": {"subject": "worker"}, "now": 2000, "request": "r1",
                "action": {"kind": "change", "key": "t1", "change": {"write": "update", "at": 2000,
                    "before": {"own": {"projectId": "p1"}, "owned": {"status": {"status": "draft"}}},
                    "after": {"own": {"projectId": "p1"}, "owned": {"status": {"status": "paid"}}}}}},
            "answers": {"reads": [
                {"request": {"kind": "row", "type": "Task", "key": "t1"},
                 "answer": {"row": {"own": {"projectId": "p1"}, "owned": {"status": {"status": "paid"}}, "version": 2}}},
                {"request": {"kind": "spec", "type": "Project"}, "answer": {"spec": project}},
                {"request": {"kind": "row", "type": "Project", "key": "p1"},
                 "answer": {"row": {"own": {}, "owned": {"status": {"status": "draft"}}, "version": 1}}}
            ]}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(
            step,
            json!({"read": [{"kind": "linked", "type": "Task", "field": "projectId", "key": "p1", "limit": 500}]})
        );
        // A change of a row that is gone writes it gone.
        let input = json!({
            "spec": task(),
            "command": {"caller": {"subject": "worker"}, "now": 2000, "request": "r1",
                "action": {"kind": "change", "key": "t2", "change": {"write": "delete", "at": 2000, "before": {}}}},
            "answers": {"reads": [{"request": {"kind": "row", "type": "Task", "key": "t2"}, "answer": {"row": null}}]}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(
            step["done"]["changed"]["rows"],
            json!([{"kind": "gone", "type": "Task", "key": "t2"}])
        );
    }

    // A ticket whose status is a Workflow field and whose Timers starts a
    // deadline at the create, an hour out.
    fn ticket() -> Value {
        json!({
            "name": "Ticket",
            "uses": [
                {"behavior": "Workflow", "config": flow(), "anchor": {"field": "status"}},
                {"behavior": "Timers", "config": {"deadlines": {"respond": {"start": {"create": true}, "afterMs": 3_600_000}}}, "anchor": "type"}
            ],
            "fields": [{"name": "title", "type": "string"}]
        })
    }

    #[test]
    fn a_create_writes_timers_sub_table_row_and_arms_its_deadline() {
        let input = json!({
            "spec": ticket(),
            "command": {"caller": {"subject": "sam"}, "now": 1000, "request": "r1",
                "action": {"kind": "create", "id": "t1", "own": {"title": "t1"}}}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        let changed = &step["done"]["changed"];
        assert_eq!(
            changed["subRows"],
            json!([{"row": {"type": "Ticket", "key": "t1"}, "useId": "Timers", "table": "deadlines", "key": "respond",
                "change": {"kind": "insert", "values": {"baseAt": 1000, "armedFrom": "1000", "dueAt": 3_601_000, "fired": false}}}])
        );
        assert_eq!(
            changed["deadlines"],
            json!([{"kind": "arm", "row": {"type": "Ticket", "key": "t1"}, "useId": "Timers", "name": "respond", "at": 3_601_000, "payload": null}])
        );
    }

    #[test]
    fn a_timers_operation_asks_for_the_rows_sub_table_rows() {
        let input = json!({
            "spec": ticket(),
            "command": {"caller": {"subject": "sam"}, "now": 1000, "request": "r1",
                "action": {"kind": "operation", "key": "t1", "useId": "Timers", "operation": "fireTimer", "params": {"deadline": "respond"}}},
            "answers": {"reads": [{"request": {"kind": "row", "type": "Ticket", "key": "t1"},
                "answer": {"row": {"own": {"title": "t1"}, "owned": {"status": {"status": "draft", "enteredAt": 1000}}, "version": 1}}}]}
        });
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let step: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(
            step,
            json!({"read": [{"kind": "subRows", "type": "Ticket", "key": "t1", "useId": "Timers", "table": "deadlines"}]})
        );
    }

    // An account in a pool, its rank ordering its members.
    fn account() -> Value {
        json!({
            "name": "Account",
            "uses": [{"behavior": "Pool", "config": {"order": "rank"}, "anchor": "type"}],
            "fields": [{"name": "rank", "type": "Generic.Int64"}]
        })
    }

    /// An operation of the table as a whole runs on no row (D104): it asks
    /// for no row by its key but for a read across the table, and answers
    /// from the rows it is given.
    #[test]
    fn a_table_operation_runs_with_no_row() {
        let command = json!({"caller": {"subject": "sam"}, "now": 1000, "request": "r1",
            "action": {"kind": "tableOperation", "useId": "Pool", "operation": "pick", "params": {}}});
        let input = json!({"spec": account(), "command": command});
        let (out, ok) = run_json(Op::Run, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let rows = json!({"kind": "rows", "type": "Account", "limit": 500});
        assert_eq!(
            serde_json::from_str::<Value>(&out).unwrap(),
            json!({ "read": [rows] })
        );
        let answered = json!({"spec": account(), "command": command, "answers": {"reads": [{
            "request": rows,
            "answer": {"linked": {"rows": [
                {"key": "a1", "row": {"own": {"rank": 2}, "owned": {"Pool": {"availability": "open"}}, "version": 1}},
                {"key": "a2", "row": {"own": {"rank": 1}, "owned": {}, "version": 1}}
            ], "more": false}}
        }]}});
        let (out, ok) = run_json(Op::Run, answered.to_string().as_bytes());
        assert!(ok, "{out}");
        assert_eq!(
            serde_json::from_str::<Value>(&out).unwrap(),
            json!({"done": {"changed": {"rows": [], "subRows": [], "events": [], "deadlines": [], "result":
                {"member": {"key": "a2", "availability": "open", "order": 1, "preferred": true}}}}})
        );
    }

    /// A declare adds a declarative behavior (D103's step 5) the core runs
    /// from then on and lists among its declarations; the same file again is
    /// one behavior, and a second file of its name that differs, one of a
    /// behavior the core implements and one the vocabulary does not read are
    /// refused, leaving the core as it was. A state of the test's own keeps
    /// the process's core as it is for the other tests.
    #[test]
    fn declare_adds_a_declarative_behavior_the_core_runs() {
        let votes: Value = serde_json::from_str(include_str!(
            "../../../testdata/rules/declarations/test.Votes.behavior.json"
        ))
        .unwrap();
        let rules = Rules::new(
            superschematic_rules_behaviors::core,
            superschematic_rules_behaviors::DECLARATIONS.to_vec(),
        );
        let input = json!({ "declarations": [votes.clone()] }).to_string();
        assert_eq!(
            rules.declare(input.as_bytes()),
            Ok(json!({"declared": ["test.Votes"]}))
        );
        assert_eq!(
            rules.declare(input.as_bytes()),
            Ok(json!({"declared": ["test.Votes"]}))
        );
        let (core, files) = rules.current();
        assert_eq!(files.as_slice(), std::slice::from_ref(&votes));
        assert!(core.declaration("test.Votes").is_some());
        let listed = output_on(&rules, Op::Declarations, b"").unwrap();
        assert_eq!(listed.as_array().unwrap().last(), Some(&votes));
        // A proposal whose votes the core now runs passes the define check.
        let proposal = json!({"specs": [{
            "name": "Proposal",
            "uses": [
                {"behavior": "Workflow", "config": {"states": ["open", "accepted", "closed"], "transitions": [{"from": "open", "to": "accepted"}, {"from": "open", "to": "closed"}]}, "anchor": {"field": "status"}},
                {"behavior": "test.Votes", "config": {"maxScore": 5, "quorum": 2}, "anchor": "type"}
            ],
            "fields": [{"name": "title", "type": "string"}, {"name": "owner", "type": "string"}]
        }]});
        let checked = output_on(&rules, Op::Check, proposal.to_string().as_bytes()).unwrap();
        assert_eq!(checked, json!([null]));
        let mut other = votes.clone();
        other["description"] = Value::from("another");
        for (refused, reason) in [
            (other, "two declarations of it differ"),
            (
                json!({"name": "Workflow", "rules": {}}),
                "the core implements it",
            ),
            (json!({"name": "test.Plain"}), "states no rules"),
        ] {
            let input = json!({ "declarations": [refused] }).to_string();
            let (code, message) = rules.declare(input.as_bytes()).unwrap_err();
            assert_eq!(code, "invalid_input");
            assert!(message.contains(reason), "{message}");
        }
        assert_eq!(rules.current().1.as_slice(), [votes]);
    }

    /// A project's core (D103's step 5) refuses a declaration of a behavior
    /// its crates register, as the stock core refuses one of its own: a
    /// name the core it was built with registers.
    #[test]
    fn declare_refuses_a_behavior_the_built_core_registers() {
        fn project() -> Core {
            superschematic_rules_behaviors::core()
        }
        let mut declarations = superschematic_rules_behaviors::DECLARATIONS.to_vec();
        declarations.push(r#"{"name": "test.Plain"}"#);
        let rules = Rules::new(project, declarations);
        let input = json!({ "declarations": [{"name": "test.Plain", "rules": {}}] }).to_string();
        let (code, message) = rules.declare(input.as_bytes()).unwrap_err();
        assert_eq!(code, "invalid_input");
        assert!(
            message.contains("test.Plain: the core implements it"),
            "{message}"
        );
        assert!(rules.current().1.is_empty());
    }

    #[test]
    fn check_refuses_a_config_the_core_refuses() {
        let input = json!({"specs": [order(flow()), order(json!({"states": []}))]});
        let (out, ok) = run_json(Op::Check, input.to_string().as_bytes());
        assert!(ok, "{out}");
        let refusals: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(refusals[0], Value::Null);
        assert_eq!(refusals[1]["code"], "invalid_schema");
    }

    #[test]
    fn an_input_it_cannot_read_is_an_error_document() {
        let (out, ok) = run_json(Op::Run, b"{not json");
        assert!(!ok);
        let error: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(error["error"]["code"], "invalid_json");
        let (out, ok) = run_json(Op::Check, br#"{"specs": 1}"#);
        assert!(!ok);
        assert!(out.contains("invalid_input"), "{out}");
    }
}
