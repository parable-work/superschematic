//! The test kit's engine archive (D58): the engine's C ABI
//! (`superschematic-engine-abi`) over a project that opens an engine as the
//! scenario replayer opens one, so a binding's replay of the corpora
//! (`runtime/engine/testdata`) can run the cases that need what only a
//! recording says: the test kit's behaviors, a policy that answers from the
//! recording's answer table, and the open meta-schema. The Go binding's
//! replay links it in place of the core's archive; it is never shipped.
//!
//! An engine's config takes these in its `project` member, each optional:
//!
//! ```json
//! {
//!   "file": "http",
//!   "behaviors": ["test.Counter", "test.Flag"],
//!   "answers": [[{"principal": "alice", "action": "read", "...": "..."}, true]],
//!   "principals": {"alice": {"subject": "alice", "permissions": []}},
//!   "openMetaSchema": "/path/to/the/repository"
//! }
//! ```
//!
//! `behaviors` are registered beside the core's, each the test kit's
//! implementation for the corpus file `file` names
//! ([`superschematic_engine_testkit::behaviors::test_behavior`]); `answers`
//! is the policy, its questions naming principals by their keys in
//! `principals` ([`AnswerPolicy`]), and a question the table lacks is
//! refused and written to standard error; `openMetaSchema` is the
//! repository whose core meta-schema, behaviors let through, documents
//! load against ([`superschematic_engine_testkit::open_meta_schema`]).

use std::path::Path;
use std::sync::{Arc, Mutex, PoisonError};

use serde_json::{Map, Value};

use superschematic_engine::{AccessPolicy, AccessRequest, EngineOptions, MetaSchema, Policy};
use superschematic_engine_testkit::{behaviors, open_meta_schema, AnswerPolicy};

const SHAPE: &str =
    "the test kit's project is { file?, behaviors?, answers?, principals?, openMetaSchema? }";

fn configure(options: &mut EngineOptions, project: Option<&Value>) -> Result<(), String> {
    options.behaviors = superschematic_engine_behaviors::core();
    let Some(project) = project else {
        return Ok(());
    };
    let Value::Object(members) = project else {
        return Err(SHAPE.to_owned());
    };
    if let Some(unknown) = members.keys().find(|key| {
        ![
            "file",
            "behaviors",
            "answers",
            "principals",
            "openMetaSchema",
        ]
        .contains(&key.as_str())
    }) {
        return Err(format!("{SHAPE}: {unknown} is not one of its members"));
    }
    let file = members
        .get("file")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if let Some(names) = members.get("behaviors") {
        let names = names
            .as_array()
            .ok_or_else(|| format!("{SHAPE}: behaviors are names"))?;
        for name in names {
            let name = name
                .as_str()
                .ok_or_else(|| format!("{SHAPE}: behaviors are names"))?;
            let set = behaviors::test_behavior(file, name)
                .or_else(|| behaviors::core_behavior(name))
                .ok_or_else(|| format!("the test kit has no behavior {name}"))?;
            options.behaviors = std::mem::take(&mut options.behaviors).extend(set);
        }
    }
    if let Some(answers) = members.get("answers") {
        let answers = answers
            .as_array()
            .ok_or_else(|| format!("{SHAPE}: answers are [question, answer] pairs"))?
            .iter()
            .filter_map(|pair| Some((pair.get(0)?.clone(), pair.get(1)?.clone())))
            .collect();
        let principals = members
            .get("principals")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_else(Map::new);
        let unknown = Arc::new(Mutex::new(Vec::new()));
        options.policy = Some(Policy::custom(Recorded {
            answers: AnswerPolicy::new(answers, principals, Arc::clone(&unknown)),
            unknown,
        }));
    }
    if let Some(repository) = members.get("openMetaSchema") {
        let repository = repository
            .as_str()
            .ok_or_else(|| format!("{SHAPE}: openMetaSchema is the repository's path"))?;
        options.meta_schema = Some(MetaSchema::Document(open_meta_schema(Path::new(
            repository,
        ))?));
    }
    Ok(())
}

// Recorded answers from the recording's table, and writes each question
// it lacks to standard error, where a replay's failing step is explained.
struct Recorded {
    answers: AnswerPolicy,
    unknown: Arc<Mutex<Vec<String>>>,
}

impl AccessPolicy for Recorded {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let allowed = self.answers.allows(request);
        let asked: Vec<String> =
            std::mem::take(&mut *self.unknown.lock().unwrap_or_else(PoisonError::into_inner));
        for question in asked {
            eprintln!("engine replay: the recording never asked the policy {question}");
        }
        allowed
    }
}

superschematic_engine_abi::export_engine_c_abi!(configure = configure);
