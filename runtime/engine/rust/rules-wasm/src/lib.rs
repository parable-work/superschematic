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
//!   A refusal is an outcome, not an error.
//! - `declarations`: no input (an empty document, or any), to the
//!   declaration file of each behavior the core registers, in its order
//!   (`superschematic_rules_behaviors::DECLARATIONS`): a JSON array of the
//!   documents the compiler registers, each field's column type and the
//!   value field among them.
//! - `check`: `{"specs": [TypeSpec], "caller"?: Caller, "now"?: number}`, to
//!   the define check of each spec, in order: a JSON array whose entry is
//!   null for a spec the core takes and the [`Refusal`] for one it refuses.
//!   A host checks the specs its layout holds when it first opens the core,
//!   so a core that lacks a behavior or refuses a config fails then.
//!
//! An input the operation cannot read is an error document,
//! `{"error": {"code", "message"}}` with the code `invalid_json` or
//! `invalid_input`, and a panic `internal`; [`run_json`] says which by its
//! second value.
//!
//! The core is built once per process (on wasm, once per instance), the
//! first time an operation runs, and kept: a behavior reads its declaration
//! file when it is registered, which a command should not pay for. Every
//! thread shares it, since [`Core`] is `Send` and `Sync`, so the Go
//! binding's goroutines, on whatever threads cgo runs them, run one core.
//!
//! [`Step`]: superschematic_rules::Step
//! [`Refusal`]: superschematic_rules::Refusal

pub mod ffi;

use std::sync::LazyLock;

use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::{json, Value};

use superschematic_rules::{Action, Answers, Caller, Command, Core, Outcome, Step, TypeSpec};

// The core every operation runs on: each behavior ported so far
// (superschematic_rules_behaviors::core()), built the first time an
// operation runs and shared by every thread after: Core is Send and Sync,
// so the Go binding's goroutines, on whatever threads cgo runs them, run
// one core. A wasm32-unknown-unknown module runs on one thread.
static CORE: LazyLock<Core> = LazyLock::new(superschematic_rules_behaviors::core);

// The core is shared across threads, which only a Send and Sync core may
// be; a behavior that broke it would fail to compile here.
const _: () = {
    const fn shared<T: Send + Sync>() {}
    shared::<Core>();
};

/// An operation of the ABI.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Op {
    Run,
    Declarations,
    Check,
}

impl Op {
    /// The operation's name, as an error names it.
    pub fn name(self) -> &'static str {
        match self {
            Op::Run => "run",
            Op::Declarations => "declarations",
            Op::Check => "check",
        }
    }
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

/// Runs an operation on its JSON input and returns its output document,
/// with true for the operation's output and false for an error document.
pub fn run_json(op: Op, input: &[u8]) -> (String, bool) {
    match output(op, input) {
        Ok(value) => (value.to_string(), true),
        Err((code, message)) => (error_document(code, &message), false),
    }
}

/// The error document of a refused input: `{"error": {"code", "message"}}`.
pub fn error_document(code: &str, message: &str) -> String {
    json!({ "error": { "code": code, "message": message } }).to_string()
}

fn output(op: Op, input: &[u8]) -> Result<Value, (&'static str, String)> {
    output_on(&CORE, op, input)
}

fn output_on(core: &Core, op: Op, input: &[u8]) -> Result<Value, (&'static str, String)> {
    match op {
        Op::Declarations => Ok(Value::Array(
            superschematic_rules_behaviors::DECLARATIONS
                .iter()
                .map(|text| serde_json::from_str(text).unwrap_or(Value::Null))
                .collect(),
        )),
        Op::Run => {
            let input: RunInput = parse(op, input)?;
            let step: Step = core.run(&input.spec, &input.command, &input.answers);
            serde_json::to_value(step).map_err(|error| ("internal", error.to_string()))
        }
        Op::Check => {
            let input: CheckInput = parse(op, input)?;
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
