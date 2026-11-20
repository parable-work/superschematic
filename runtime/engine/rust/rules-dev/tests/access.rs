//! The access steps the transcripts leave out, replayed (D58's access
//! policy as data, D117): the recorded scenarios whose engine was opened
//! with a policy of the test's own, a table of the questions it was asked
//! and what it answered (`{"answers": [[question, answer], ...]}`). The
//! testkit's replayer skips them, since the reference host applies no
//! policy; development mode's handler asks one, so each table is read as
//! the policy it is: a rule per question, allowing it when the test
//! answered `true` and refusing it otherwise, and a question no rule names
//! refused.
//!
//! Each step a request maps onto is run through the host as its principal
//! (`schemas.define`, `schemas.publish`, `schemas.live` as describe,
//! `instances.create`, `get`, `list`, `update`, `delete` and `invoke`) and
//! compared by what it came to: a refusal `forbidden` with the engine's
//! message, another refusal by its code, or none. The rest are skipped,
//! and so is every step after one that cannot be replayed:
//!
//! - a step in a namespace other than `default`, which development mode
//!   keeps alone;
//! - a refusal of the engine's for a read of another type than the step's
//!   own, which held a behavior's reads to the policy as its caller's
//!   (Dependencies' blockers, Rollups' linked rows); a table's API
//!   authorizes the request alone (D103), and development mode answers as
//!   it does;
//! - a step whose answer is not access's: one the host refuses where the
//!   engine took it, which the transcripts compare already.
//!
//! The counts are held to floors, so a regression cannot pass by skipping.

use std::collections::BTreeMap;
use std::path::Path;

use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Code, Refusal};
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::document::{self, Model};
use superschematic_rules_handler::{Policy, Request};

/// The scenario files whose engines a test's own policy answered, and how
/// many of their steps must be replayed, how many of those refused
/// `forbidden`. Rollups' one such scenario stops at its first define,
/// whose link is the engine's Links behavior rather than a relation; the
/// behaviors' other refusals are of a behavior's read of another type.
const FILES: [(&str, usize, usize); 5] = [
    ("access", 81, 14),
    ("core-rollups", 0, 0),
    ("core-dependencies", 44, 0),
    ("core-revisions", 55, 0),
    ("core-timers", 2, 0),
];

/// The calls that read and write nothing a later step reads, which a
/// scenario goes on past when they map onto no request.
const READS: [&str; 13] = [
    "schemas.draft",
    "schemas.version",
    "schemas.list",
    "schemas.validate",
    "schemas.validator",
    "schemas.behaviors",
    "events.read",
    "instances.lookup",
    "runner.status",
    "storage.get",
    "storage.all",
    "tools.manifest",
    "tools.describe",
];

fn load(name: &str) -> Vec<Value> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/scenarios")
        .join(format!("{name}.json"));
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    let mut file: Value = serde_json::from_str(&text).expect("a scenario file of JSON");
    let shared = file
        .get("shared")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    let mut scenarios = file
        .get_mut("scenarios")
        .and_then(Value::as_array_mut)
        .map(std::mem::take)
        .unwrap_or_default();
    for scenario in &mut scenarios {
        resolve(scenario, &shared);
    }
    scenarios
}

fn resolve(value: &mut Value, shared: &Map<String, Value>) {
    match value {
        Value::Object(members) => {
            if let Some(Value::String(key)) = members.get("$shared") {
                *value = shared.get(key).cloned().expect("a shared value");
                return resolve(value, shared);
            }
            for member in members.values_mut() {
                resolve(member, shared);
            }
        }
        Value::Array(items) => {
            for item in items {
                resolve(item, shared);
            }
        }
        _ => {}
    }
}

/// The policy a recorded table of answers is: a rule per question, in the
/// table's order, `allow` true only for the answer `true`.
fn policy_of(answers: &[Value]) -> Policy {
    let rules: Vec<Value> = answers
        .iter()
        .filter_map(|entry| {
            let [question, answer] = entry.as_array()?.as_slice() else {
                return None;
            };
            let mut rule = Map::new();
            rule.insert("subject".into(), question.get("principal")?.clone());
            for member in ["action", "namespace", "schema", "operation"] {
                if let Some(value) = question.get(member) {
                    rule.insert(member.into(), value.clone());
                }
            }
            rule.insert("allow".into(), Value::Bool(*answer == Value::Bool(true)));
            Some(Value::Object(rule))
        })
        .collect();
    Policy::from_json(&json!({ "rules": rules })).expect("the table reads as rules")
}

#[derive(Default)]
struct Counts {
    replayed: usize,
    forbidden: usize,
    skipped: Vec<String>,
}

struct Replay {
    principals: Map<String, Value>,
    dev: Option<Dev>,
    models: BTreeMap<String, Model>,
    clock: i64,
}

/// What a step came to, as the comparison reads it.
#[derive(Debug, PartialEq)]
enum Came {
    Ok,
    Forbidden(String),
    Refused(String),
}

fn came(outcome: &Result<Value, Refusal>) -> Came {
    match outcome {
        Ok(_) => Came::Ok,
        Err(refusal) if refusal.code == Code::Forbidden => Came::Forbidden(refusal.message.clone()),
        Err(refusal) => Came::Refused(refusal.code.as_str().to_owned()),
    }
}

fn recorded(step: &Value) -> Came {
    match step.get("error") {
        None | Some(Value::Null) => Came::Ok,
        Some(error) => {
            let code = error.get("code").and_then(Value::as_str).unwrap_or("");
            let message = error.get("message").and_then(Value::as_str).unwrap_or("");
            if code == "forbidden" {
                Came::Forbidden(message.to_owned())
            } else {
                Came::Refused(code.to_owned())
            }
        }
    }
}

/// The caller a step's principal is, from the scenario's principals by
/// name; `Err` for one acting for another (D77), which no caller here
/// does. A principal the step gives as a value with no subject is a caller
/// with none.
fn caller_of(step: &Value, principals: &Map<String, Value>) -> Result<Caller, String> {
    let Some(name) = step.get("principal").and_then(Value::as_str) else {
        return Ok(Caller::new("", Vec::<String>::new()));
    };
    let Some(principal) = principals.get(name) else {
        return Ok(Caller::new(name, Vec::<String>::new()));
    };
    if principal.get("actingFor").is_some() {
        return Err(format!("{name} acts for another (D77)"));
    }
    let permissions: Vec<String> = principal
        .get("permissions")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    let mut caller = Caller::new(
        principal
            .get("subject")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        permissions,
    );
    if let Some(service) = principal.get("service") {
        let formed = service
            .get("deployable")
            .and_then(Value::as_str)
            .is_some_and(|deployable| !deployable.is_empty())
            && service.get("serves").is_some_and(Value::is_array);
        if !formed {
            return Err(format!(
                "{name}'s service is malformed, which the engine refused"
            ));
        }
    }
    caller.stands_in = principal
        .get("service")
        .and_then(|service| service.get("standsIn"))
        == Some(&Value::Bool(true));
    Ok(caller)
}

fn text(args: &[Value], index: usize) -> String {
    args.get(index)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn object(value: Option<&Value>) -> Map<String, Value> {
    value
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default()
}

impl Replay {
    // request is the request a step maps onto, or why it maps onto none.
    fn request(&self, call: &str, args: &[Value]) -> Result<Request, String> {
        let options = args.iter().rev().find(|arg| arg.is_object());
        Ok(match call {
            "schemas.define" => Request::Define {
                document: args.first().cloned().unwrap_or(Value::Null),
            },
            "schemas.publish" => Request::Publish {
                type_name: text(args, 0),
                discard: false,
            },
            "schemas.live" => Request::Describe {
                type_name: text(args, 0),
            },
            "instances.create" => {
                let mut data = object(args.get(1));
                let options = object(args.get(2));
                if let Some(id) = options.get("id") {
                    data.insert("id".into(), id.clone());
                }
                Request::Create {
                    type_name: text(args, 0),
                    data,
                    params: object(options.get("behaviors")),
                }
            }
            "instances.get" => Request::Get {
                type_name: text(args, 0),
                id: text(args, 1),
            },
            "instances.list" => Request::List {
                type_name: text(args, 0),
                filter: Value::Null,
                sort: Vec::new(),
                limit: None,
                cursor: None,
            },
            "instances.update" => Request::Update {
                type_name: text(args, 0),
                id: text(args, 1),
                patch: object(args.get(2)),
                preconditions: Map::new(),
            },
            "instances.delete" => Request::Delete {
                type_name: text(args, 0),
                id: text(args, 1),
                preconditions: Map::new(),
            },
            "instances.invoke" => {
                let type_name = text(args, 0);
                let operation = text(args, 2);
                let declarations = self.dev.as_ref().map(Dev::declarations);
                let use_id = self
                    .models
                    .get(&type_name)
                    .and_then(|model| {
                        model.uses.iter().find(|used| {
                            declarations
                                .and_then(|declarations| declarations.get(&used.spec.behavior))
                                .is_some_and(|declared| {
                                    declared.operations.iter().any(|op| op.name == operation)
                                })
                        })
                    })
                    .map(|used| used.id().to_owned())
                    .ok_or_else(|| format!("no use of {type_name} declares {operation}"))?;
                Request::Operation {
                    type_name,
                    id: text(args, 1),
                    use_id,
                    operation,
                    params: args.get(3).cloned().unwrap_or(Value::Null),
                    preconditions: Map::new(),
                }
            }
            other => return Err(format!("{other} maps onto no request")),
        })
        .and_then(|request| {
            let namespace = options
                .and_then(|options| options.get("namespace"))
                .and_then(Value::as_str);
            match namespace {
                Some(namespace) if namespace != "default" => Err(format!(
                    "it names namespace {namespace}; development mode keeps one"
                )),
                _ => Ok(request),
            }
        })
    }

    /// Replays a scenario's steps; false once it stops.
    fn step(&mut self, file: &str, name: &str, step: &Value, counts: &mut Counts) -> bool {
        let call = step.get("call").and_then(Value::as_str).unwrap_or_default();
        let args: Vec<Value> = step
            .get("args")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let skip = |counts: &mut Counts, reason: String| {
            counts
                .skipped
                .push(format!("{file}: {name}: {call}: {reason}"));
        };
        if call == "open" {
            let options = object(args.first());
            let policy = match options.get("policy") {
                Some(Value::String(name)) if name == "allowAll" => Policy::AllowAll,
                Some(Value::Object(table)) => {
                    match table.get("answers").and_then(Value::as_array) {
                        Some(answers) => policy_of(answers),
                        None => {
                            skip(counts, "a policy that is no table of answers".into());
                            return false;
                        }
                    }
                }
                _ => {
                    skip(counts, "an engine with no policy".into());
                    return false;
                }
            };
            let clock = self.clock;
            self.dev = Some(
                Dev::memory()
                    .expect("SQLite opens")
                    .with_policy(policy)
                    .with_clock(move || clock),
            );
            self.models.clear();
            return true;
        }
        let Some(dev) = self.dev.as_ref() else {
            return false;
        };
        let expected = recorded(step);
        if step.get("error").and_then(|error| error.get("class")) == Some(&json!("TypeError")) {
            skip(counts, "the test's policy function itself refused".into());
            return true;
        }
        let caller = match caller_of(step, &self.principals) {
            Ok(caller) => caller,
            Err(reason) => {
                skip(counts, reason);
                return false;
            }
        };
        let request = match self.request(call, &args) {
            Ok(request) => request,
            Err(reason) => {
                skip(counts, reason);
                // A read changes nothing a later step reads; after a write
                // the scenario stops.
                return READS.contains(&call);
            }
        };
        if let Came::Forbidden(message) = &expected {
            let own = match &request {
                Request::Define { document } => document
                    .get("name")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                other => serde_json::to_value(other).expect("a request of JSON")["type"]
                    .as_str()
                    .unwrap_or_default()
                    .to_owned(),
            };
            if !message.contains(&format!(" {own} in namespace")) {
                skip(
                    counts,
                    format!("the engine held a behavior's read of another type to the policy: {message}"),
                );
                return false;
            }
        }
        if let Request::Define { document } = &request {
            if let Ok(model) = document::read(document, dev.declarations()) {
                self.models.insert(model.name.clone(), model);
            }
        }
        let dev = self.dev.as_mut().expect("open");
        let outcome = dev.request(&caller, &request);
        let actual = came(&outcome);
        counts.replayed += 1;
        if expected == actual {
            if matches!(expected, Came::Forbidden(_)) {
                counts.forbidden += 1;
            }
            return true;
        }
        let access = matches!(expected, Came::Forbidden(_)) || matches!(actual, Came::Forbidden(_));
        assert!(
            !access,
            "{file}: {name}: {call} {args:?}: the engine answered {expected:?}, development mode {actual:?}"
        );
        counts.replayed -= 1;
        skip(
            counts,
            format!("not access's to answer: the engine answered {expected:?}, development mode {actual:?}"),
        );
        false
    }
}

#[test]
fn the_recorded_access_steps_replay_through_the_handlers_policy() {
    for (file, floor, forbidden_floor) in FILES {
        let mut counts = Counts::default();
        for scenario in load(file) {
            let name = scenario["name"].as_str().unwrap_or_default().to_owned();
            let opens_policy = scenario["steps"]
                .as_array()
                .into_iter()
                .flatten()
                .any(|step| {
                    step["call"] == "open"
                        && step["args"][0]
                            .get("policy")
                            .is_some_and(|policy| policy.get("answers").is_some())
                });
            if file != "access" && !opens_policy {
                continue;
            }
            let mut replay = Replay {
                principals: object(scenario.get("principals")),
                dev: None,
                models: BTreeMap::new(),
                clock: 1_767_225_600_000,
            };
            for step in scenario["steps"].as_array().into_iter().flatten() {
                if !replay.step(file, &name, step, &mut counts) {
                    break;
                }
            }
        }
        println!(
            "{file}: {} steps replayed, {} refused forbidden, {} skipped:\n  {}",
            counts.replayed,
            counts.forbidden,
            counts.skipped.len(),
            counts.skipped.join("\n  ")
        );
        assert!(
            counts.replayed >= floor,
            "{file}: {} steps replayed, under the floor of {floor}",
            counts.replayed
        );
        assert!(
            counts.forbidden >= forbidden_floor,
            "{file}: {} forbidden steps replayed, under the floor of {forbidden_floor}",
            counts.forbidden
        );
    }
}
