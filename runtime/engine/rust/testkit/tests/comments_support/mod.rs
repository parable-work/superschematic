//! What the tests of Comments' batches 4 and 5 share: an engine whose
//! Review (r1, r2) and Memo (m1) compose Comments, the principals who
//! call, and readers of the behavior's rows.

#![allow(dead_code)]

use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    AccessRequest, Clock, CreateOptions, DefineOptions, Engine, EngineEvent, NamespaceCall, Policy,
    Principal, ReadEvents, Result, RunnerOptions, SchemaTarget, WriteOptions,
};

use crate::support::{alice, issues_of, named, refusal, schema_document, veto, Target};

pub fn bob() -> Principal {
    named("bob")
}

pub fn carol() -> Principal {
    named("carol")
}

pub fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// The reviewer agent, acting for alice.
pub fn agent() -> Principal {
    Principal::new("agent:reviewer", Vec::<String>::new()).acting_for("alice")
}

/// The reviewer agent, acting for bob.
pub fn agent_for_bob() -> Principal {
    Principal::new("agent:reviewer", Vec::<String>::new()).acting_for("bob")
}

/// The reviewer agent, acting for nobody.
pub fn agent_alone() -> Principal {
    Principal::new("agent:reviewer", Vec::<String>::new())
}

/// Holds Review's moderatePermission.
pub fn moderator() -> Principal {
    Principal::new("mod", ["comments.moderate"])
}

/// Claims Review's deliveries.
pub fn notifier() -> Principal {
    Principal::new("notifier", ["comments.notify"])
}

pub fn schema_of(name: &str, config: &Value) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "Comments", "config": config}]);
    document
}

pub fn publish(engine: &Engine, name: &str, config: &Value) {
    engine
        .schemas()
        .define(&alice(), schema_of(name, config), DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), name, SchemaTarget::default())
        .expect("publish");
}

pub fn create(engine: &Engine, schema: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

/// Review's config unless a test gives its own.
pub fn review_config() -> Value {
    json!({"deliverPermission": "comments.notify", "moderatePermission": "comments.moderate"})
}

/// An engine whose Review (r1, r2) and Memo (m1) compose Comments, with a
/// manual clock at 1000, the runner acting as `runner`, and a policy that
/// lets `rita` read no Memo, `outsider` read no schema, and everyone else
/// do anything.
pub struct World {
    pub engine: Arc<Engine>,
}

pub fn world(target: &Target, review: Value) -> World {
    world_with(target, review, json!({}))
}

pub fn world_with(target: &Target, review: Value, memo: Value) -> World {
    let policy =
        crate::support::RecordingPolicy::new(|request: &AccessRequest<'_>| {
            match request.principal().subject.as_str() {
                "rita" => request.schema() != Some("Memo"),
                "outsider" => request.schema().is_none(),
                _ => true,
            }
        });
    let engine = target
        .open_core_with(|options| {
            options.clock = Clock::Manual(1000);
            options.runner = Some(RunnerOptions::new(runner()));
            options.policy = Some(Policy::custom(policy));
        })
        .shared();
    publish(&engine, "Review", &review);
    publish(&engine, "Memo", &memo);
    create(&engine, "Review", "r1");
    create(&engine, "Review", "r2");
    create(&engine, "Memo", "m1");
    World { engine }
}

impl World {
    pub fn on(
        &self,
        who: &Principal,
        schema: &str,
        id: &str,
        operation: &str,
        params: Value,
    ) -> Result<Value> {
        self.engine
            .instances()
            .invoke(who, schema, id, operation, &params, WriteOptions::default())
    }

    pub fn call(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.on(who, "Review", "r1", operation, params)
    }

    pub fn ok(&self, who: &Principal, operation: &str, params: Value) -> Value {
        self.call(who, operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    pub fn post(&self, who: &Principal, params: Value) -> i64 {
        self.ok(who, "post", params)["id"].as_i64().expect("an id")
    }

    pub fn say(&self, who: &Principal, body: &str) -> i64 {
        self.post(who, json!({ "body": body }))
    }

    pub fn reply(&self, who: &Principal, thread: i64, body: &str) -> i64 {
        self.post(who, json!({ "body": body, "thread": thread }))
    }

    pub fn message(&self, id: i64) -> Value {
        self.ok(&alice(), "getMessage", json!({ "message": id }))
    }

    pub fn fields(&self, who: &Principal) -> Value {
        self.engine
            .instances()
            .get(who, "Review", "r1", Default::default())
            .expect("get")
            .expect("r1")
            .behaviors["Comments"]
            .clone()
    }

    pub fn ns(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.engine.behaviors().invoke(
            who,
            &NamespaceCall {
                namespace: None,
                behavior: "Comments",
                operation,
                params: Some(&params),
            },
        )
    }

    pub fn ns_ok(&self, who: &Principal, operation: &str, params: Value) -> Value {
        self.ns(who, operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    pub fn set_clock(&self, now: i64) {
        self.engine.clock().set(now).expect("set the clock");
    }

    pub fn events(&self) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Review"),
                    instance_id: Some("r1"),
                    beside: Some(true),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
    }

    /// The deliveries, each `[schema, id, message, subject, mention,
    /// reply, state]`, sorted.
    pub fn deliveries(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, message, subject, mention, reply, state FROM bhv_comments__comment_deliveries",
        )
    }

    /// The follows, each `[schema, id, thread, subject, followed,
    /// read_up_to]`, sorted.
    pub fn follows(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, thread, subject, followed, read_up_to FROM bhv_comments__comment_follows",
        )
    }

    /// The conversation markers, each `[schema, id, subject, up_to]`,
    /// sorted.
    pub fn reads(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, subject, up_to FROM bhv_comments__comment_reads",
        )
    }
}

/// Every row a statement returns, each an array of its values, sorted by
/// their JSON.
pub fn rows(engine: &Engine, sql: &str) -> Vec<Value> {
    let mut out: Vec<Value> = engine
        .db()
        .read(|tx| {
            Ok::<_, superschematic_engine::storage::Error>(
                tx.all(sql, &[])?
                    .iter()
                    .map(|row| {
                        Value::Array(
                            row.values()
                                .iter()
                                .map(|value| match value {
                                    SqlValue::Null => Value::Null,
                                    SqlValue::Integer(number) => Value::from(*number),
                                    SqlValue::Text(text) => Value::from(text.as_str()),
                                    other => Value::from(other.to_string()),
                                })
                                .collect(),
                        )
                    })
                    .collect(),
            )
        })
        .expect("read");
    out.sort_by_key(Value::to_string);
    out
}

/// `expected`, sorted as [`rows`] sorts.
pub fn sorted(mut expected: Vec<Value>) -> Vec<Value> {
    expected.sort_by_key(Value::to_string);
    expected
}

/// A veto's code and details.
pub fn vetoed<T: std::fmt::Debug>(result: Result<T>) -> (String, Value) {
    let (behavior, _, _, code, details) = veto(result);
    assert_eq!(behavior, "Comments");
    (code.expect("a code"), details.unwrap_or(Value::Null))
}

/// An OperationParamsError's issues.
pub fn invalid<T: std::fmt::Debug>(result: Result<T>) -> Vec<(String, String)> {
    let refused = refusal(result);
    assert_eq!(refused.name(), "OperationParamsError", "{refused:?}");
    issues_of(&refused)
}
