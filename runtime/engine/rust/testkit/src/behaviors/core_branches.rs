//! The behavior `test/core-branches-sweep.test.ts` defines: test.Keep,
//! whose guard vetoes a discard of the ref the test keeps, whoever asks, so
//! Branches' sweep, which discards each idle draft through its instance's
//! discard, leaves that one. The TypeScript test sets the ref through a
//! recorder hook (`testHook.keep.ref`) the recording does not carry; with
//! none set, as a replay has it, the guard vetoes nothing.

use std::sync::{Arc, Mutex, PoisonError};

use serde_json::Value;

use superschematic_engine::errors::Veto;
use superschematic_engine::{Behavior, ConfigTarget, GuardRequest, Hooks, Result};

/// test.Keep (`test/core-branches-sweep.test.ts`).
#[derive(Clone, Debug, Default)]
pub struct Keep {
    kept: Arc<Mutex<String>>,
}

impl Keep {
    /// Keeps a ref, by id: a discard of it is vetoed from then on.
    pub fn keep(&self, id: &str) {
        *self.kept.lock().unwrap_or_else(PoisonError::into_inner) = id.to_owned();
    }

    fn kept(&self) -> String {
        self.kept
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
    }
}

const DECLARATION: &str = r#"{
    "name": "test.Keep",
    "vetoes": [{"code": "kept", "description": "A discard of the ref the test keeps."}]
}"#;

impl Behavior for Keep {
    type Config = Value;

    fn declaration(&self) -> &str {
        DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|keep, _view, request| {
                let GuardRequest::Operation {
                    behavior: "Branches",
                    operation: "discard",
                    params,
                    ..
                } = request
                else {
                    return Ok(None);
                };
                let kept = keep.kept();
                Ok(
                    (params.get("ref").and_then(Value::as_str) == Some(kept.as_str())).then(|| {
                        Veto {
                            reason: "the test keeps it".to_owned(),
                            code: Some("kept".to_owned()),
                            details: None,
                        }
                    }),
                )
            }),
            ..Hooks::default()
        }
    }
}
