//! What the work queue's tests share (the ports of
//! `runtime/engine-workqueue/typescript/test/helpers.ts`): the Job schema
//! and its flow, the token a call presents as Lease's precondition, and a
//! published schema's instances read and written as one principal or
//! another, on an engine clock the test moves.

#![allow(dead_code)]

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Engine, EngineEvent, Error, GetOptions, InstanceRecord, Principal, ReadEvents, RunnerPass,
    WriteOptions,
};

use crate::support::alice;

/// When every work-queue test's clock starts, as `new Clock()` does.
pub const T0: i64 = 1_000_000;

/// A principal with the permissions given.
pub fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().copied())
}

/// An instant as the refusals write it (`new Date(ms).toISOString()`).
pub fn iso(ms: i64) -> String {
    superschematic_engine_testkit::iso_of(ms)
}

/// A job is queued, runs, and ends done or failed; a running job can go
/// back to the queue (`jobFlow`).
pub fn job_flow() -> Value {
    json!({
        "states": ["queued", "running", "done", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"},
            {"from": "running", "to": "failed"},
            {"from": "queued", "to": "failed"}
        ]
    })
}

/// A General schema whose instance type has a title, a priority, a time
/// limit, a topic and an urgent flag, and composes the behaviors given
/// (`jobsDocument`).
pub fn jobs_document_named(behaviors: Value, name: &str) -> Value {
    json!({
        "kind": "General",
        "name": name,
        "types": {
            name: {
                "name": name,
                "role": "EmbeddedStruct",
                "behaviors": behaviors,
                "fields": [
                    {"name": "title", "typeRef": {"name": "string"}, "required": true},
                    {"name": "priority", "typeRef": {"name": "Generic.Int64"}},
                    {"name": "timeLimitMs", "typeRef": {"name": "Generic.Int64"}},
                    {"name": "topic", "typeRef": {"name": "string"}},
                    {"name": "urgent", "typeRef": {"name": "boolean"}}
                ]
            }
        }
    })
}

/// The Job schema composing the behaviors given.
pub fn jobs_document(behaviors: Value) -> Value {
    jobs_document_named(behaviors, "Job")
}

/// The preconditions of a call that presents a lease's token (`fenced`).
pub fn fenced(token: i64) -> Value {
    json!({ "Lease": { "token": token } })
}

/// Defines and publishes a document as alice.
pub fn publish(engine: &Engine, document: &Value) {
    crate::support::publish(engine, document, None);
}

/// The definition a document is refused with, as its message says it.
pub fn define_refusal(engine: &Engine, document: &Value) -> String {
    let refused = engine
        .schemas()
        .define(
            &alice(),
            document,
            superschematic_engine::DefineOptions::default(),
        )
        .expect_err("a refused definition");
    refused.to_string()
}

/// A published schema's instances, as the tests read and write them.
pub struct Instances<'e> {
    pub engine: &'e Engine,
    pub schema: &'e str,
}

impl Instances<'_> {
    pub fn create(&self, id: &str, data: Value) -> InstanceRecord {
        self.create_as(&alice(), id, data).expect("create")
    }

    pub fn create_as(
        &self,
        who: &Principal,
        id: &str,
        data: Value,
    ) -> Result<InstanceRecord, Error> {
        self.engine.instances().create(
            who,
            self.schema,
            &data,
            superschematic_engine::CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
    }

    /// Invokes an operation, presenting the token as Lease's precondition
    /// when one is given.
    pub fn invoke_on(
        &self,
        who: &Principal,
        id: &str,
        operation: &str,
        params: Value,
        token: Option<i64>,
    ) -> Result<Value, Error> {
        let preconditions = token.map(fenced);
        self.engine.instances().invoke(
            who,
            self.schema,
            id,
            operation,
            &params,
            WriteOptions {
                preconditions: preconditions.as_ref(),
                ..Default::default()
            },
        )
    }

    pub fn invoke_schema(
        &self,
        who: &Principal,
        operation: &str,
        params: Value,
    ) -> Result<Value, Error> {
        self.engine
            .instances()
            .invoke_schema(who, self.schema, operation, &params, None)
    }

    pub fn update(
        &self,
        who: &Principal,
        id: &str,
        patch: Value,
        token: Option<i64>,
    ) -> Result<InstanceRecord, Error> {
        let preconditions = token.map(fenced);
        self.engine.instances().update(
            who,
            self.schema,
            id,
            &patch,
            WriteOptions {
                preconditions: preconditions.as_ref(),
                ..Default::default()
            },
        )
    }

    pub fn delete(&self, who: &Principal, id: &str, token: Option<i64>) -> Result<bool, Error> {
        let preconditions = token.map(fenced);
        self.engine.instances().delete(
            who,
            self.schema,
            id,
            WriteOptions {
                preconditions: preconditions.as_ref(),
                ..Default::default()
            },
        )
    }

    pub fn read(&self, id: &str) -> InstanceRecord {
        self.engine
            .instances()
            .get(&alice(), self.schema, id, GetOptions::default())
            .expect("get")
            .expect("the instance")
    }

    /// A behavior's fields of an instance, `null` when it has none.
    pub fn behavior(&self, id: &str, name: &str) -> Value {
        self.read(id)
            .behaviors
            .get(name)
            .cloned()
            .unwrap_or(Value::Null)
    }

    pub fn status(&self, id: &str) -> Value {
        self.behavior(id, "Workflow")["status"].clone()
    }

    pub fn seq(&self, id: &str) -> i64 {
        self.read(id).seq
    }

    /// The instance's events, oldest first.
    pub fn events(&self, id: &str) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some(self.schema),
                    instance_id: Some(id),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
    }

    /// Every event of the schema's instances, oldest first.
    pub fn schema_events(&self) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some(self.schema),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
            .into_iter()
            .filter(|event| event.instance_id.is_some())
            .collect()
    }

    pub fn last_event(&self, id: &str) -> EngineEvent {
        self.events(id).pop().expect("an event")
    }
}

/// Moves the engine's clock on.
pub fn advance(engine: &Engine, ms: i64) -> i64 {
    engine.clock().advance(ms).expect("advance the clock")
}

/// Runs what the runner has due.
pub fn run_due(engine: &Engine) -> RunnerPass {
    engine.runner().run_due().expect("the runner's pass")
}

/// The values of the one row a statement returns, on the engine's storage.
pub fn row_values(engine: &Engine, sql: &str) -> Vec<SqlValue> {
    engine
        .db()
        .read(|tx| {
            Ok::<_, superschematic_engine::storage::Error>(
                tx.get(sql, &[])?.expect("a row").into_values(),
            )
        })
        .expect("read")
}

/// A event's cause as JSON.
pub fn cause_of(event: &EngineEvent) -> Value {
    serde_json::to_value(&event.cause).expect("JSON")
}
