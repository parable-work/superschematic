//! What the guidance tests share (`tests/guidance_<behavior>.rs`, one per
//! behavior, each holding a behavior's guidance to the TypeScript
//! package's text for the same configs, as `test/guidance.test.ts` reads
//! it from `dist/guidance/*.js`): the type a config is described on, and
//! guidance written as the TypeScript object holds it, a member left out
//! where it has none.

#![allow(dead_code)]

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, DescribedTypeOperation, OperationScope,
};

/// A Job type with the behaviors and configs given, and no operations.
pub fn target(behaviors: &[&str], configs: Value) -> DescribeTarget {
    DescribeTarget {
        schema: "Job".to_owned(),
        type_name: "Job".to_owned(),
        behaviors: behaviors.iter().map(|name| (*name).to_owned()).collect(),
        configs: match configs {
            Value::Object(configs) => configs,
            _ => Map::new(),
        },
        operations: Vec::new(),
    }
}

/// An operation of the type, as describe lists it.
pub fn operation(name: &str, behavior: Option<&str>, writes: bool) -> DescribedTypeOperation {
    DescribedTypeOperation {
        name: name.to_owned(),
        behavior: behavior.map(str::to_owned),
        writes,
        scope: OperationScope::Instance,
    }
}

/// Guidance as the TypeScript object holds it.
pub fn as_json(guidance: &BehaviorGuidance) -> Value {
    let mut operations = Map::new();
    for (name, said) in &guidance.operations {
        let mut out = Map::new();
        for (key, text) in [
            ("useWhen", &said.use_when),
            ("doNotUseWhen", &said.do_not_use_when),
            ("success", &said.success),
        ] {
            if let Some(text) = text {
                out.insert(key.into(), Value::from(text.as_str()));
            }
        }
        if !said.errors.is_empty() {
            let errors = said
                .errors
                .iter()
                .map(|error| {
                    let mut entry = Map::new();
                    entry.insert("code".into(), Value::from(error.code.as_str()));
                    if let Some(description) = &error.description {
                        entry.insert("description".into(), Value::from(description.as_str()));
                    }
                    entry.insert(
                        "commonCorrection".into(),
                        Value::from(error.common_correction.as_str()),
                    );
                    Value::Object(entry)
                })
                .collect();
            out.insert("errors".into(), Value::Array(errors));
        }
        operations.insert(name.clone(), Value::Object(out));
    }
    json!({ "summary": guidance.summary, "operations": operations })
}
