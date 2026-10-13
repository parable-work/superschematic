//! The behaviors the TypeScript engine's tests define, ported to Rust with
//! the same declarations and semantics (D58), so the replayer composes them
//! where a scenario names them (`testBehaviors`) and the ported
//! `behavior-*.test.ts` cases run against them:
//!
//! - `test/behavior-fixtures.ts`: [`Counter`], [`Flag`], [`Tally`] and
//!   [`Hold`];
//! - `test/list-filters.test.ts`: [`Tag`];
//! - `test/normalize.test.ts`: [`Mailer`];
//! - `test/unique.test.ts`: [`Slugger`];
//! - `test/reach-fixtures.ts`: [`Reader`] and [`Holder`];
//! - `test/mcp.test.ts`: [`Broken`];
//! - `test/runner-fixtures.ts`: [`Ledger`], whose reactions and schedule do
//!   what a test puts in its [`Probe`], and [`Pacer`], whose interval its
//!   config or its [`PacerProbe`] gives.
//!
//! A test file may define its own behavior under a name another file uses
//! (`test.Hold`), so the replayer looks a behavior up by file and name
//! ([`test_behavior`]).

use std::sync::{Arc, Mutex, PoisonError};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, Every, Hears, IndexSpec, ReactionCx, Reactions,
    Reference, Schedule, ScheduleCx, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, EngineEvent, Error, GuardRequest, Hooks, InstanceChange,
    InstanceCx, InstanceView, OperationCx, Result, SchemaCx,
};

/// The core meta-schema's `noParams`: an object with no members.
const NO_PARAMS: &str = r#"{"type": "object", "additionalProperties": false}"#;

fn integer(value: &Value, key: &str) -> Option<i64> {
    value.get(key).and_then(Value::as_i64)
}

fn text<'v>(value: &'v Value, key: &str) -> Option<&'v str> {
    value.get(key).and_then(Value::as_str)
}

// The behavior's rows of one instance, as its SQL names them.
fn instance_key<C>(cx: &InstanceView<'_, C>) -> [SqlValue; 3] {
    [
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::from(cx.id()),
    ]
}

/// The config of [`Counter`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CounterConfig {
    pub start: i64,
    pub limit: Option<i64>,
}

/// test.Counter: counts up from a start, to an optional limit, and lists
/// what each increment added. `list-filters.test.ts` registers it with a
/// filter on its count's column, which has no index of its own
/// ([`Counter::filtered`]).
#[derive(Clone, Copy, Debug, Default)]
pub struct Counter {
    /// Whether a list may filter on `test.Counter.count`.
    pub count_filter: bool,
}

/// test.Counter's declaration.
pub fn counter_declaration() -> String {
    json!({
        "name": "test.Counter",
        "description": "Counts up from a start, to an optional limit.",
        "configSchema": {
            "type": "object",
            "additionalProperties": false,
            "properties": {"start": {"type": "integer", "minimum": 0}, "limit": {"type": "integer", "minimum": 1}}
        },
        "fields": [{"name": "count", "description": "The count."}],
        "operations": [
            {
                "name": "increment",
                "description": "Adds to the count.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"by": {"type": "integer", "minimum": 1}}},
                "resultSchema": {"type": "object", "required": ["count"], "additionalProperties": false, "properties": {"count": {"type": "integer"}}},
                "writes": true
            },
            {
                "name": "history",
                "description": "Lists what each increment added.",
                "paramsSchema": serde_json::from_str::<Value>(NO_PARAMS).unwrap_or_default(),
                "resultSchema": {"type": "array", "items": {"type": "integer"}}
            }
        ]
    })
    .to_string()
}

static COUNTER_DECLARATION: std::sync::LazyLock<String> =
    std::sync::LazyLock::new(counter_declaration);

impl Counter {
    /// test.Counter with a filter on its count (`{ ...counter, filters:
    /// { count: { column: 'count', type: 'integer' } } }`).
    pub fn filtered() -> Self {
        Counter { count_filter: true }
    }

    fn history(cx: &InstanceView<'_, CounterConfig>) -> Result<String> {
        cx.sql().table("history")
    }
}

impl Behavior for Counter {
    type Config = CounterConfig;

    fn declaration(&self) -> &str {
        &COUNTER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<CounterConfig> {
        let start = integer(raw, "start").unwrap_or(0);
        let limit = integer(raw, "limit");
        if let Some(limit) = limit {
            if start > limit {
                return Err(Error::config(format!(
                    "start {start} is past limit {limit}"
                )));
            }
        }
        Ok(CounterConfig { start, limit })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            filters: if self.count_filter {
                vec![("count", BehaviorFilter::new("count", "integer"))]
            } else {
                Vec::new()
            },
            config_change: Some(|_, before, after, _| {
                let Some(before) = before else {
                    // Instances that exist count from 0.
                    return Ok(None);
                };
                let Some(after) = after else {
                    return Ok(Some("removing it loses every count".to_owned()));
                };
                if before.start != after.start {
                    return Ok(Some("start cannot change".to_owned()));
                }
                if let Some(limit) = after.limit {
                    if before.limit.is_none_or(|earlier| limit < earlier) {
                        return Ok(Some("a limit can only rise or go".to_owned()));
                    }
                }
                Ok(None)
            }),
            migrations: vec![
                BehaviorMigration::new(1, "count")
                    .column("count", ColumnSpec::integer().not_null().default(0)),
                BehaviorMigration::new(2, "history").table(
                    TableSpec::new("history")
                        .row_id("position")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("delta", ColumnSpec::integer().not_null())
                        .column("at", ColumnSpec::integer().not_null())
                        .index(IndexSpec::new(
                            "history_by_instance",
                            &["namespace", "schema", "id"],
                        )),
                ),
            ],
            initialize: Some(|_, cx, _| {
                cx.columns()
                    .set(&[("count", SqlValue::Integer(cx.config().start))])
            }),
            guard: Some(|_, cx, request| {
                let GuardRequest::Operation {
                    operation: "increment",
                    params,
                    ..
                } = request
                else {
                    return Ok(None);
                };
                let Some(limit) = cx.config().limit else {
                    return Ok(None);
                };
                let by = integer(params, "by").unwrap_or(1);
                if cx.columns().get()?.i64("count")? + by > limit {
                    return Ok(Some(Veto::from(
                        format!("the count would pass its limit, {limit}").as_str(),
                    )));
                }
                Ok(None)
            }),
            operations: vec![
                ("increment", |_, cx, params| {
                    let by = integer(params, "by").unwrap_or(1);
                    let count = cx.columns().get()?.i64("count")? + by;
                    cx.columns().set(&[("count", SqlValue::Integer(count))])?;
                    let table = Counter::history(cx)?;
                    let [namespace, schema, id] = instance_key(cx);
                    cx.sql().run(
                        format!(
                            "INSERT INTO {table} (namespace, schema, id, delta, at) VALUES (?, ?, ?, ?, ?)"
                        ),
                        &[namespace, schema, id, SqlValue::Integer(by), SqlValue::Integer(cx.now())],
                    )?;
                    Ok(json!({ "count": count }))
                }),
                ("history", |_, cx, _| {
                    let table = Counter::history(cx)?;
                    let rows = cx.sql().all(
                        format!(
                            "SELECT delta FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position"
                        ),
                        &instance_key(cx),
                    )?;
                    rows.iter()
                        .map(|row| Ok(Value::from(row.i64("delta")?)))
                        .collect::<Result<Vec<Value>>>()
                        .map(Value::Array)
                }),
            ],
            fields: vec![("count", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("count")?)))
            })],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Delete) {
                    let table = Counter::history(cx)?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &instance_key(cx),
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

/// test.Flag: holds an instance still while it is flagged; it conflicts
/// with test.Tally.
pub struct Flag;

static FLAG_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Flag",
        "description": "Holds an instance still while it is flagged.",
        "conflicts": ["test.Tally"],
        "fields": [
            {"name": "flagged", "description": "Whether the instance is flagged."},
            {"name": "flagReason", "description": "Why; absent when it is not flagged."}
        ],
        "operations": [
            {
                "name": "flag",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["reason"], "properties": {"reason": {"type": "string", "minLength": 1}}},
                "resultSchema": {"type": "boolean"},
                "writes": true,
                "invocationPolicy": "ask"
            },
            {"name": "unflag", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {"type": "boolean"}, "writes": true}
        ]
    })
    .to_string()
});

impl Behavior for Flag {
    type Config = Value;

    fn declaration(&self) -> &str {
        &FLAG_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "flag")
                .column("flagged", ColumnSpec::integer().not_null().default(0))
                .column("reason", ColumnSpec::text())],
            // While flagged, only the flag's own operations run.
            guard: Some(|_, cx, request| {
                let columns = cx.columns().get()?;
                let flagged = columns.opt_i64("flagged")?;
                if flagged != Some(1)
                    || matches!(
                        request,
                        GuardRequest::Operation {
                            behavior: "test.Flag",
                            ..
                        }
                    )
                {
                    return Ok(None);
                }
                let reason = columns.opt_text("reason")?.unwrap_or("null").to_owned();
                Ok(Some(Veto::from(
                    format!("it is flagged: {reason}").as_str(),
                )))
            }),
            operations: vec![
                ("flag", |_, cx, params| {
                    cx.columns().set(&[
                        ("flagged", SqlValue::Integer(1)),
                        (
                            "reason",
                            SqlValue::from(text(params, "reason").unwrap_or_default()),
                        ),
                    ])?;
                    Ok(Value::Bool(true))
                }),
                ("unflag", |_, cx, _| {
                    cx.columns().set(&[
                        ("flagged", SqlValue::Integer(0)),
                        ("reason", SqlValue::Null),
                    ])?;
                    Ok(Value::Bool(false))
                }),
            ],
            fields: vec![
                ("flagged", |_, cx| {
                    Ok(Some(Value::Bool(
                        cx.columns().get()?.opt_i64("flagged")? == Some(1),
                    )))
                }),
                ("flagReason", |_, cx| {
                    Ok(cx.columns().get()?.opt_text("reason")?.map(Value::from))
                }),
            ],
            ..Hooks::default()
        }
    }
}

/// test.Tally: counts the updates of an instance and bumps its counter
/// through the counter's operation; it requires test.Counter.
pub struct Tally;

static TALLY_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Tally",
        "description": "Counts the updates of an instance and bumps its counter.",
        "requires": ["test.Counter"],
        "fields": [{"name": "edits", "description": "How many updates the instance has had."}],
        "operations": [
            {
                "name": "bump",
                "description": "Increments the counter, once or more, through its operation.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"times": {"type": "integer", "minimum": 1}}},
                "resultSchema": {"type": "object", "required": ["count"], "properties": {"count": {"type": "integer"}}},
                "writes": true
            },
            {
                "name": "tryBump",
                "description": "Increments the counter unless a guard vetoes it, and counts the attempt.",
                "paramsSchema": {"type": "object", "additionalProperties": false},
                "resultSchema": {"type": "object", "required": ["vetoed"], "properties": {"vetoed": {"type": "boolean"}}},
                "writes": true
            }
        ]
    })
    .to_string()
});

impl Behavior for Tally {
    type Config = Value;

    fn declaration(&self) -> &str {
        &TALLY_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "edits")
                .column("edits", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("bump", |_, cx, params| {
                    let mut result = Value::Null;
                    for _ in 0..integer(params, "times").unwrap_or(1) {
                        result = cx.call("test.Counter", "increment", &json!({ "by": 1 }))?;
                    }
                    Ok(result)
                }),
                ("tryBump", |_, cx, _| {
                    let edits = cx.columns().get()?.i64("edits")?;
                    cx.columns()
                        .set(&[("edits", SqlValue::Integer(edits + 100))])?;
                    match cx.call("test.Counter", "increment", &json!({ "by": 1 })) {
                        Ok(_) => Ok(json!({ "vetoed": false })),
                        Err(error) if error.is_veto() => Ok(json!({ "vetoed": true })),
                        Err(error) => Err(error),
                    }
                }),
            ],
            fields: vec![("edits", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("edits")?)))
            })],
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Update { .. }) {
                    let edits = cx.columns().get()?.i64("edits")?;
                    cx.columns()
                        .set(&[("edits", SqlValue::Integer(edits + 1))])?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

/// The config of [`Hold`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HoldConfig {
    pub require: bool,
}

/// test.Hold (`test/behavior-fixtures.ts`): numbers the instance by a
/// generation, which a write presents as its precondition to fence itself,
/// and vetoes with codes. Every request its guard is asked but a create's
/// is recorded in `requests`, in order.
#[derive(Clone, Default)]
pub struct Hold {
    pub requests: Arc<Mutex<Vec<Value>>>,
}

impl Hold {
    /// The requests its guard was asked, as JSON, emptied.
    pub fn take(&self) -> Vec<Value> {
        std::mem::take(&mut self.requests.lock().unwrap_or_else(PoisonError::into_inner))
    }
}

static HOLD_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Hold",
        "description": "Numbers the instance by a generation, which a write presents as its precondition to fence itself.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {"require": {"type": "boolean"}}},
        "fields": [{"name": "generation", "description": "The generation a write presents."}],
        "operations": [
            {
                "name": "advance",
                "description": "Moves the instance to its next generation.",
                "paramsSchema": {"type": "object", "additionalProperties": false},
                "resultSchema": {"type": "object", "required": ["generation"], "properties": {"generation": {"type": "integer"}}},
                "writes": true
            },
            {
                "name": "forward",
                "description": "Advances another instance, presenting the generation given as its precondition.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["id"], "properties": {"id": {"type": "string"}, "generation": {"type": "integer"}}},
                "resultSchema": {"type": "object", "required": ["generation"], "properties": {"generation": {"type": "integer"}}},
                "writes": true
            },
            {
                "name": "refuse",
                "description": "Refuses with a veto that carries the code and details given.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"code": {"type": "string"}, "details": {}}},
                "resultSchema": {"type": "null"},
                "writes": true
            },
            {
                "name": "peek",
                "description": "Reads the generation.",
                "paramsSchema": {"type": "object", "additionalProperties": false},
                "resultSchema": {"type": "object", "required": ["generation"], "properties": {"generation": {"type": "integer"}}}
            }
        ],
        "preconditionSchema": {
            "type": "object",
            "additionalProperties": false,
            "required": ["generation"],
            "properties": {"generation": {"type": "integer", "minimum": 0}}
        },
        "vetoes": [
            {"code": "stale", "description": "The generation presented is not the instance's."},
            {"code": "required", "description": "With require, a write presents no generation."},
            {"code": "refused"}
        ]
    })
    .to_string()
});

impl Behavior for Hold {
    type Config = HoldConfig;

    fn declaration(&self) -> &str {
        &HOLD_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<HoldConfig> {
        Ok(HoldConfig {
            require: raw.get("require") == Some(&Value::Bool(true)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "generation")
                .column("generation", ColumnSpec::integer().not_null().default(0))],
            // A write that presents a generation other than the instance's
            // is refused; with require, so is one that presents none. A
            // behavior's own requests never present one, and a create is let
            // through unrecorded.
            guard: Some(|hold, cx, request| {
                if matches!(request, GuardRequest::Create { .. }) {
                    return Ok(None);
                }
                hold.requests
                    .lock()
                    .unwrap_or_else(PoisonError::into_inner)
                    .push(request.to_json());
                if matches!(request, GuardRequest::Operation { writes: false, .. }) {
                    return Ok(None);
                }
                let current = cx.columns().get()?.i64("generation")?;
                let presented = request
                    .precondition()
                    .and_then(|precondition| integer(precondition, "generation"));
                if let Some(presented) = presented {
                    if presented != current {
                        let mut details = Map::new();
                        details.insert("generation".into(), Value::from(presented));
                        details.insert("current".into(), Value::from(current));
                        return Ok(Some(Veto {
                            reason: format!(
                                "generation {presented} is stale: the instance is at {current}"
                            ),
                            code: Some("stale".to_owned()),
                            details: Some(details),
                        }));
                    }
                }
                if cx.config().require
                    && presented.is_none()
                    && (matches!(request, GuardRequest::Delete { .. })
                        || request.caller().is_none())
                {
                    return Ok(Some(Veto {
                        reason: "a write presents the generation".to_owned(),
                        code: Some("required".to_owned()),
                        details: None,
                    }));
                }
                Ok(None)
            }),
            operations: vec![
                ("advance", |_, cx, _| {
                    let generation = cx.columns().get()?.i64("generation")? + 1;
                    cx.columns()
                        .set(&[("generation", SqlValue::Integer(generation))])?;
                    Ok(json!({ "generation": generation }))
                }),
                ("forward", |_, cx, params| {
                    let preconditions = integer(params, "generation")
                        .map(|generation| json!({"test.Hold": {"generation": generation}}));
                    cx.instances().invoke(
                        cx.schema(),
                        text(params, "id").unwrap_or_default(),
                        "advance",
                        &json!({}),
                        preconditions.as_ref(),
                    )
                }),
                ("refuse", |_, cx, params| {
                    let details = match params.get("details") {
                        None => None,
                        Some(Value::Object(details)) => Some(details.clone()),
                        Some(_) => {
                            return Err(Error::behavior(
                                "test.Hold",
                                "a veto's details are a JSON object",
                            ))
                        }
                    };
                    Err(Error::veto(
                        "test.Hold",
                        "refuse",
                        cx.schema(),
                        cx.id(),
                        Veto {
                            reason: "refused as asked".to_owned(),
                            code: text(params, "code").map(str::to_owned),
                            details,
                        },
                    ))
                }),
                ("peek", |_, cx, _| {
                    Ok(json!({ "generation": cx.columns().get()?.i64("generation")? }))
                }),
            ],
            fields: vec![("generation", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("generation")?)))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Tag (`test/list-filters.test.ts`): gives an instance an owner, or
/// none, in a column its tag field's owner member reads, which a list
/// filters on as `test.Tag.tag.owner` through an index on the column.
pub struct Tag;

static TAG_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Tag",
        "description": "Tags an instance with an owner, or none.",
        "fields": [{"name": "tag", "description": "The tag, { owner }."}],
        "operations": [
            {
                "name": "own",
                "description": "Sets the owner, or clears it with null.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["owner"], "properties": {"owner": {"type": ["string", "null"]}}},
                "resultSchema": {"type": "object", "additionalProperties": false},
                "writes": true
            }
        ]
    })
    .to_string()
});

impl Behavior for Tag {
    type Config = Value;

    fn declaration(&self) -> &str {
        &TAG_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "tag")
                .column("owner", ColumnSpec::text())
                .index("owner", &["owner"])],
            filters: vec![(
                "tag.owner",
                BehaviorFilter::new("owner", "string").described("Who owns it; null for no one."),
            )],
            operations: vec![("own", |_, cx, params| {
                cx.columns().set(&[(
                    "owner",
                    match params.get("owner") {
                        Some(Value::String(owner)) => SqlValue::from(owner.as_str()),
                        _ => SqlValue::Null,
                    },
                )])?;
                Ok(json!({}))
            })],
            fields: vec![("tag", |_, cx| {
                let owner = cx
                    .columns()
                    .get()?
                    .opt_text("owner")?
                    .map_or(Value::Null, Value::from);
                Ok(Some(json!({ "owner": owner })))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Mailer (`test/normalize.test.ts`): changes the email through
/// update(), and creates a contact as a behavior does.
pub struct Mailer;

const EMAIL_PARAMS: &str = r#"{"type": "object", "additionalProperties": false, "required": ["email"], "properties": {"email": {"type": "string"}}}"#;

static MAILER_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let params: Value = serde_json::from_str(EMAIL_PARAMS).unwrap_or_default();
    json!({
        "name": "test.Mailer",
        "operations": [
            {"name": "setEmail", "paramsSchema": params, "resultSchema": true, "writes": true},
            {"name": "invite", "scope": "schema", "paramsSchema": params, "resultSchema": true, "writes": true}
        ]
    })
    .to_string()
});

impl Behavior for Mailer {
    type Config = Value;

    fn declaration(&self) -> &str {
        &MAILER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("setEmail", |_, cx: &OperationCx<'_, Value>, params| {
                let fields = cx.update(&json!({ "email": params.get("email") }))?;
                Ok(Value::Object((*fields).clone()))
            })],
            schema_operations: vec![("invite", |_, cx: &SchemaCx<'_, Value>, params| {
                let created = cx.instances().create(
                    "Contact",
                    &json!({"name": "Invited", "email": params.get("email")}),
                    Some("invited"),
                    None,
                )?;
                Ok(Value::Object(created.data))
            })],
            ..Hooks::default()
        }
    }
}

/// test.Slugger (`test/unique.test.ts`): changes an instance's slug through
/// update(), and creates two instances with one slug in one call.
pub struct Slugger;

static SLUGGER_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let params: Value = serde_json::from_value(json!({
        "type": "object", "additionalProperties": false, "required": ["slug"], "properties": {"slug": {"type": "string"}}
    }))
    .unwrap_or_default();
    json!({
        "name": "test.Slugger",
        "operations": [
            {"name": "setSlug", "paramsSchema": params, "resultSchema": true, "writes": true},
            {"name": "twins", "scope": "schema", "paramsSchema": params, "resultSchema": true, "writes": true},
            {"name": "twinsCaught", "scope": "schema", "paramsSchema": params, "resultSchema": true, "writes": true}
        ]
    })
    .to_string()
});

impl Behavior for Slugger {
    type Config = Value;

    fn declaration(&self) -> &str {
        &SLUGGER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("setSlug", |_, cx, params| {
                let fields = cx.update(&json!({ "slug": params.get("slug") }))?;
                Ok(Value::Object((*fields).clone()))
            })],
            schema_operations: vec![
                ("twins", |_, cx, params| {
                    let data = json!({ "slug": params.get("slug") });
                    cx.instances()
                        .create("Model", &data, Some("twin-1"), None)?;
                    cx.instances()
                        .create("Model", &data, Some("twin-2"), None)?;
                    Ok(Value::Null)
                }),
                ("twinsCaught", |_, cx, params| {
                    let data = json!({ "slug": params.get("slug") });
                    cx.instances()
                        .create("Model", &data, Some("twin-1"), None)?;
                    match cx.instances().create("Model", &data, Some("twin-2"), None) {
                        Ok(_) => Ok(Value::from("created")),
                        Err(Error::Engine(refusal)) => Ok(Value::from(refusal.code().as_str())),
                        Err(_) => Ok(Value::Null),
                    }
                }),
            ],
            ..Hooks::default()
        }
    }
}

/// What a test makes [`Ledger`]'s reactions and schedule do; nothing when
/// unset.
pub type ReactProbe =
    Arc<dyn Fn(&ReactionCx<'_, LedgerConfig>, &EngineEvent) -> Result<()> + Send + Sync>;
/// What a test makes [`Ledger`]'s schedule do.
pub type SweepProbe = Arc<dyn Fn(&ScheduleCx<'_, LedgerConfig>) -> Result<()> + Send + Sync>;
/// What a test makes [`Ledger`]'s `watches` answer, in place of its config's.
pub type WatchesProbe = Arc<dyn Fn(&LedgerConfig) -> Result<Option<Vec<String>>> + Send + Sync>;

/// A test's hand in [`Ledger`]'s reactions and schedule.
#[derive(Clone, Default)]
pub struct Probe {
    pub react: Arc<Mutex<Option<ReactProbe>>>,
    pub sweep: Arc<Mutex<Option<SweepProbe>>>,
    pub watches: Arc<Mutex<Option<WatchesProbe>>>,
}

impl Probe {
    /// Has the reactions do `react`.
    pub fn on_react(
        &self,
        react: impl Fn(&ReactionCx<'_, LedgerConfig>, &EngineEvent) -> Result<()>
            + Send
            + Sync
            + 'static,
    ) {
        *self.react.lock().unwrap_or_else(PoisonError::into_inner) = Some(Arc::new(react));
    }

    /// Has the schedule do `sweep`.
    pub fn on_sweep(
        &self,
        sweep: impl Fn(&ScheduleCx<'_, LedgerConfig>) -> Result<()> + Send + Sync + 'static,
    ) {
        *self.sweep.lock().unwrap_or_else(PoisonError::into_inner) = Some(Arc::new(sweep));
    }

    /// Has `watches` answer `watches`; `None` puts the config's back.
    pub fn on_watches(&self, watches: Option<WatchesProbe>) {
        *self.watches.lock().unwrap_or_else(PoisonError::into_inner) = watches;
    }
}

/// The config of [`Ledger`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LedgerConfig {
    /// The schemas besides its own whose events its reactions hear.
    pub watch: Vec<String>,
    /// Whether its watches turns its reactions off on the schema.
    pub off: bool,
}

/// test.Ledger (`test/runner-fixtures.ts`): keeps the notes its mark
/// operation writes on an instance; its reactions and schedule do what the
/// test puts in its probe.
#[derive(Clone, Default)]
pub struct Ledger {
    pub probe: Probe,
}

static LEDGER_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Ledger",
        "description": "Keeps the notes the runner writes on an instance.",
        "configSchema": {
            "type": "object",
            "additionalProperties": false,
            "properties": {"watch": {"type": "array", "items": {"type": "string"}}, "off": {"type": "boolean"}}
        },
        "fields": [{"name": "notes", "description": "The notes, in the order they were written."}],
        "operations": [
            {
                "name": "mark",
                "description": "Writes a note.",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["note"], "properties": {"note": {"type": "string"}}},
                "resultSchema": {"type": "object", "required": ["count"], "properties": {"count": {"type": "integer"}}},
                "writes": true
            },
            {
                "name": "count",
                "description": "Counts the notes of every instance of the schema.",
                "scope": "schema",
                "paramsSchema": {"type": "object", "additionalProperties": false},
                "resultSchema": {"type": "integer"}
            }
        ]
    })
    .to_string()
});

impl Behavior for Ledger {
    type Config = LedgerConfig;

    fn declaration(&self) -> &str {
        &LEDGER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<LedgerConfig> {
        Ok(LedgerConfig {
            watch: raw
                .get("watch")
                .and_then(Value::as_array)
                .map(|watch| {
                    watch
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            off: raw.get("off") == Some(&Value::Bool(true)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "notes").table(
                TableSpec::new("notes")
                    .row_id("position")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("note", ColumnSpec::text().not_null()),
            )],
            operations: vec![("mark", |_, cx, params| {
                let table = cx.sql().table("notes")?;
                let [namespace, schema, id] = instance_key(cx);
                cx.sql().run(
                    format!(
                        "INSERT INTO {table} (namespace, schema, id, note) VALUES (?, ?, ?, ?)"
                    ),
                    &[
                        namespace,
                        schema,
                        id,
                        SqlValue::from(text(params, "note").unwrap_or_default()),
                    ],
                )?;
                let row = cx.sql().get(
                    format!("SELECT COUNT(*) AS count FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
                    &instance_key(cx),
                )?;
                Ok(json!({ "count": row.map(|row| row.i64("count")).transpose()?.unwrap_or(0) }))
            })],
            schema_operations: vec![("count", |_, cx, _| {
                let table = cx.sql().table("notes")?;
                let row = cx.sql().get(
                    format!(
                        "SELECT COUNT(*) AS count FROM {table} WHERE namespace = ? AND schema = ?"
                    ),
                    &[SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())],
                )?;
                Ok(Value::from(
                    row.map(|row| row.i64("count")).transpose()?.unwrap_or(0),
                ))
            })],
            fields: vec![("notes", |_, cx| {
                let table = cx.sql().table("notes")?;
                let rows = cx.sql().all(
                    format!("SELECT note FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position"),
                    &instance_key(cx),
                )?;
                if rows.is_empty() {
                    return Ok(None);
                }
                Ok(Some(Value::Array(
                    rows.iter()
                        .map(|row| Ok(Value::from(row.text("note")?)))
                        .collect::<Result<Vec<Value>>>()?,
                )))
            })],
            // It keeps nothing an instance needs, so a version may add,
            // remove or change it on a schema with instances.
            config_change: Some(|_, _, _, _| Ok(None)),
            after_change: Some(|_, cx: &InstanceCx<'_, LedgerConfig>, change| {
                if matches!(change, InstanceChange::Delete) {
                    let table = cx.sql().table("notes")?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &instance_key(cx),
                    )?;
                }
                Ok(())
            }),
            reactions: Some(Reactions {
                watches: Some(|ledger, config, _| {
                    let probe = ledger
                        .probe
                        .watches
                        .lock()
                        .unwrap_or_else(PoisonError::into_inner)
                        .clone();
                    if let Some(probe) = probe {
                        return probe(config);
                    }
                    Ok(if config.off {
                        None
                    } else {
                        Some(config.watch.clone())
                    })
                }),
                react: |ledger, cx, event| {
                    let probe = ledger
                        .probe
                        .react
                        .lock()
                        .unwrap_or_else(PoisonError::into_inner)
                        .clone();
                    match probe {
                        Some(probe) => probe(cx, event),
                        None => Ok(()),
                    }
                },
            }),
            schedules: vec![(
                "sweep",
                Schedule {
                    every: Every::Ms(60_000),
                    run: |ledger, cx| {
                        let probe = ledger
                            .probe
                            .sweep
                            .lock()
                            .unwrap_or_else(PoisonError::into_inner)
                            .clone();
                        match probe {
                            Some(probe) => probe(cx),
                            None => Ok(()),
                        }
                    },
                },
            )],
            ..Hooks::default()
        }
    }
}

// param is a string member of an operation's parameters, '' when absent.
fn param<'v>(params: &'v Value, key: &str) -> &'v str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

/// A reference as the TypeScript engine's object holds it.
pub fn reference_json(reference: &Reference) -> Value {
    let mut out = json!({"schema": reference.schema, "id": reference.id, "key": reference.key});
    match &reference.hears {
        None => {}
        Some(Hears::Delete) => out["hears"] = json!("delete"),
        Some(Hears::Path { path, crosses }) => {
            let mut hears = json!({ "path": path });
            if let Some(crosses) = crosses {
                hears["crosses"] = if crosses.fract() == 0.0 && crosses.abs() < 9e15 {
                    json!(*crosses as i64)
                } else {
                    json!(crosses)
                };
            }
            out["hears"] = hears;
        }
    }
    out
}

/// The config of [`Holder`].
#[derive(Clone, Debug, Default)]
pub struct HolderConfig {
    /// The changes of a held instance its guard vetoes.
    pub veto: Vec<String>,
    /// Leave the reference behind when a held instance is deleted.
    pub leave: bool,
}

/// test.Holder (`test/reach-fixtures.ts`): holds references to other
/// instances and notes what happens to them.
#[derive(Clone, Copy, Debug, Default)]
pub struct Holder;

static HOLDER: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let target = json!({"schema": {"type": "string", "minLength": 1}, "id": {"type": "string", "minLength": 1}});
    let object = |required: Value, properties: Value| json!({"type": "object", "additionalProperties": false, "required": required, "properties": properties});
    let with = |more: Value| {
        let mut out = target.clone();
        for (key, value) in more.as_object().unwrap() {
            out[key] = value.clone();
        }
        out
    };
    let no_params = json!({"type": "object", "additionalProperties": false});
    json!({
        "name": "test.Holder",
        "description": "Holds references to other instances and notes what happens to them.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {
            "veto": {"type": "array", "items": {"enum": ["update", "delete", "operation"]}},
            "leave": {"type": "boolean"}
        }},
        "fields": [{"name": "held", "description": "What the instance holds: schema/id/key."}],
        "operations": [
            {"name": "hold", "description": "Holds an instance, under a key, hearing what hears says of it: every change when absent.", "paramsSchema": object(json!(["schema", "id"]), with(json!({"key": {"type": "string"}, "hears": {}}))), "resultSchema": true, "writes": true},
            {"name": "release", "paramsSchema": object(json!(["schema", "id"]), with(json!({"key": {"type": "string"}}))), "resultSchema": {"type": "boolean"}, "writes": true},
            {"name": "note", "description": "Notes what happened to a held instance.", "paramsSchema": object(json!(["schema", "id", "kind"]), with(json!({"kind": {"type": "string"}}))), "resultSchema": true, "writes": true},
            {"name": "notes", "paramsSchema": no_params, "resultSchema": {"type": "array", "items": {"type": "string"}}},
            {"name": "holding", "description": "The references the engine holds for the instance.", "paramsSchema": no_params, "resultSchema": {"type": "array"}},
            {"name": "holders", "description": "The instances of the schema that hold one.", "scope": "schema", "paramsSchema": object(json!(["schema", "id"]), target.clone()), "resultSchema": {"type": "array", "items": {"type": "string"}}},
            {"name": "releaseAll", "description": "Releases one held instance from every holder, through release.", "scope": "schema", "paramsSchema": object(json!(["schema", "id"]), target.clone()), "resultSchema": {"type": "integer"}, "writes": true},
            {"name": "scribble", "description": "Tries to write its table in a read.", "scope": "schema", "paramsSchema": no_params, "resultSchema": true},
            {"name": "tidy", "description": "Clears every note of the schema's instances in its own table.", "scope": "schema", "paramsSchema": no_params, "resultSchema": {"type": "integer"}, "writes": true}
        ]
    })
    .to_string()
});

/// What a hold's hears says. JSON has no number past the finite ones, so
/// the string "Infinity" stands for one.
pub fn hears_of(hears: Option<&Value>) -> Option<Hears> {
    match hears? {
        Value::String(word) if word == "delete" => Some(Hears::Delete),
        given => Some(Hears::Path {
            path: param(given, "path").to_owned(),
            crosses: match given.get("crosses") {
                Some(Value::String(word)) if word == "Infinity" => Some(f64::INFINITY),
                other => other.and_then(Value::as_f64),
            },
        }),
    }
}

impl Behavior for Holder {
    type Config = HolderConfig;

    fn declaration(&self) -> &str {
        &HOLDER
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<HolderConfig> {
        Ok(HolderConfig {
            veto: raw
                .get("veto")
                .and_then(Value::as_array)
                .map(|kinds| {
                    kinds
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            leave: raw.get("leave") == Some(&Value::Bool(true)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "holds")
                .table(
                    TableSpec::new("holds")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("target_schema", ColumnSpec::text().not_null())
                        .column("target_id", ColumnSpec::text().not_null())
                        .column("key", ColumnSpec::text().not_null())
                        .primary_key(&[
                            "namespace",
                            "schema",
                            "id",
                            "target_schema",
                            "target_id",
                            "key",
                        ]),
                )
                .table(
                    TableSpec::new("notes")
                        .row_id("position")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("note", ColumnSpec::text().not_null()),
                )],
            operations: vec![
                ("hold", |_, cx, params| {
                    let key = params.get("key").and_then(Value::as_str).unwrap_or("");
                    cx.references().add(
                        param(params, "schema"),
                        param(params, "id"),
                        key,
                        hears_of(params.get("hears")),
                    )?;
                    let table = cx.sql().table("holds")?;
                    let [namespace, schema, id] = instance_key(cx);
                    cx.sql().run(
                        format!("INSERT INTO {table} (namespace, schema, id, target_schema, target_id, key) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING"),
                        &[namespace, schema, id, param(params, "schema").into(), param(params, "id").into(), key.into()],
                    )?;
                    Ok(Value::Null)
                }),
                ("release", |_, cx, params| {
                    let key = params.get("key").and_then(Value::as_str).unwrap_or("");
                    let table = cx.sql().table("holds")?;
                    let [namespace, schema, id] = instance_key(cx);
                    cx.sql().run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND target_schema = ? AND target_id = ? AND key = ?"),
                        &[namespace, schema, id, param(params, "schema").into(), param(params, "id").into(), key.into()],
                    )?;
                    Ok(Value::Bool(cx.references().remove(
                        param(params, "schema"),
                        param(params, "id"),
                        key,
                    )?))
                }),
                ("note", |_, cx, params| {
                    let table = cx.sql().table("notes")?;
                    let [namespace, schema, id] = instance_key(cx);
                    let note = format!(
                        "{} {} {}",
                        param(params, "kind"),
                        param(params, "schema"),
                        param(params, "id")
                    );
                    cx.sql().run(
                        format!(
                            "INSERT INTO {table} (namespace, schema, id, note) VALUES (?, ?, ?, ?)"
                        ),
                        &[namespace, schema, id, note.into()],
                    )?;
                    Ok(Value::Null)
                }),
                ("notes", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    let rows = cx.sql().all(
                        format!("SELECT note FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position"),
                        &instance_key(cx),
                    )?;
                    rows.iter()
                        .map(|row| Ok(Value::from(row.text("note")?)))
                        .collect::<Result<Vec<_>>>()
                        .map(Value::Array)
                }),
                ("holding", |_, cx, _| {
                    Ok(Value::Array(
                        cx.references().list()?.iter().map(reference_json).collect(),
                    ))
                }),
            ],
            schema_operations: vec![
                ("holders", |_, cx, params| {
                    let table = cx.sql().table("holds")?;
                    let rows = cx.sql().all(
                        format!("SELECT id FROM {table} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id"),
                        &[cx.namespace().into(), cx.schema().into(), param(params, "schema").into(), param(params, "id").into()],
                    )?;
                    rows.iter()
                        .map(|row| Ok(Value::from(row.text("id")?)))
                        .collect::<Result<Vec<_>>>()
                        .map(Value::Array)
                }),
                ("releaseAll", |_, cx, params| {
                    let table = cx.sql().table("holds")?;
                    let rows = cx.sql().all(
                        format!("SELECT id, key FROM {table} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id"),
                        &[cx.namespace().into(), cx.schema().into(), param(params, "schema").into(), param(params, "id").into()],
                    )?;
                    for row in &rows {
                        cx.instances().invoke(
                            cx.schema(),
                            row.text("id")?,
                            "release",
                            &json!({"schema": param(params, "schema"), "id": param(params, "id"), "key": row.text("key")?}),
                            None,
                        )?;
                    }
                    Ok(Value::from(rows.len()))
                }),
                ("scribble", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    cx.sql().run(format!("DELETE FROM {table}"), &[])?;
                    Ok(Value::Null)
                }),
                ("tidy", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    let ran = cx.sql().run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
                        &[cx.namespace().into(), cx.schema().into()],
                    )?;
                    Ok(Value::from(ran.changes))
                }),
            ],
            fields: vec![("held", |_, view| {
                let held: Vec<Value> = view
                    .references()
                    .list()?
                    .iter()
                    .map(|reference| {
                        Value::from(format!(
                            "{}/{}/{}",
                            reference.schema, reference.id, reference.key
                        ))
                    })
                    .collect();
                Ok((!held.is_empty()).then_some(Value::Array(held)))
            })],
            guard_reference: Some(|_, view, reference, request| {
                let kind = match request {
                    GuardRequest::Create { .. } => "create",
                    GuardRequest::Update { .. } => "update",
                    GuardRequest::Delete { .. } => "delete",
                    GuardRequest::Operation { .. } => "operation",
                };
                if !view.config().veto.iter().any(|vetoed| vetoed == kind) {
                    return Ok(None);
                }
                let key = if reference.key.is_empty() {
                    "no key"
                } else {
                    reference.key.as_str()
                };
                Ok(Some(Veto::from(
                    format!("{} {} holds it ({key})", view.schema(), view.id()).as_str(),
                )))
            }),
            after_reference_change: Some(|_, cx, reference, change| {
                // Its own write changed what it holds: noting it now would
                // be a cycle.
                if cx.writing() {
                    return Ok(());
                }
                cx.instances().invoke(
                    cx.schema(),
                    cx.id(),
                    "note",
                    &json!({"schema": reference.schema, "id": reference.id, "kind": change.kind()}),
                    None,
                )?;
                if matches!(change, InstanceChange::Delete) && !cx.config().leave {
                    cx.instances().invoke(
                        cx.schema(),
                        cx.id(),
                        "release",
                        &json!({"schema": reference.schema, "id": reference.id, "key": reference.key}),
                        None,
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// passed is the params a call passes on, `{}` when it gives none.
fn passed(params: &Value) -> Value {
    params.get("params").cloned().unwrap_or_else(|| json!({}))
}

// record_json is a read's `{ data, behaviors }`.
fn record_json(record: &superschematic_engine::InstanceRecord) -> Value {
    json!({ "data": record.data, "behaviors": record.behaviors })
}

// error_name is the name a TypeScript caller's catch reads.
fn error_name(error: &Error) -> &'static str {
    match error {
        Error::Engine(error) => error.name(),
        Error::Behavior(_) => "BehaviorError",
        Error::Config(_) => "BehaviorConfigError",
        Error::Usage(_) => "TypeError",
        _ => "Error",
    }
}

/// The config of [`Reader`].
#[derive(Clone, Debug, Default)]
pub struct ReaderConfig {
    /// The schema an instance's partner is of; its own when absent.
    pub partner_schema: Option<String>,
}

/// test.Reader (`test/reach-fixtures.ts`): reads other instances and
/// invokes their operations.
#[derive(Clone, Copy, Debug, Default)]
pub struct Reader;

static READER: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let target = json!({"schema": {"type": "string", "minLength": 1}, "id": {"type": "string", "minLength": 1}});
    let mut call = target.clone();
    call["operation"] = json!({"type": "string", "minLength": 1});
    call["params"] = json!({"type": "object"});
    let schema_call = json!({"schema": target["schema"], "operation": {"type": "string", "minLength": 1}, "params": {"type": "object"}});
    let with = |base: &Value, more: Value| {
        let mut out = base.clone();
        for (key, value) in more.as_object().unwrap() {
            out[key] = value.clone();
        }
        out
    };
    let object = |required: Value, properties: Value| json!({"type": "object", "additionalProperties": false, "required": required, "properties": properties});
    let no_params = json!({"type": "object", "additionalProperties": false});
    let fields = json!({"type": "array", "items": {"type": "string"}});
    json!({
        "name": "test.Reader",
        "description": "Reads other instances and invokes their operations.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {"partnerSchema": {"type": "string"}}},
        "fields": [
            {"name": "pokes", "description": "How many times poke ran on the instance."},
            {"name": "echo", "description": "The partner's title, read when the instance names one."}
        ],
        "operations": [
            {"name": "peek", "paramsSchema": object(json!(["schema", "id"]), with(&target, json!({"fields": fields}))), "resultSchema": true},
            {"name": "peekMany", "paramsSchema": object(json!(["schema", "ids"]), json!({"schema": target["schema"], "ids": {"type": "array", "items": {"type": "string"}}, "fields": fields})), "resultSchema": {"type": "object"}},
            {"name": "configOf", "paramsSchema": object(json!(["schema", "behavior"]), json!({"schema": target["schema"], "behavior": {"type": "string"}})), "resultSchema": true},
            {"name": "canRead", "paramsSchema": object(json!(["schema"]), json!({"schema": target["schema"]})), "resultSchema": {"type": "boolean"}},
            {"name": "poke", "description": "Invokes an operation of another instance, then fails when asked to.", "paramsSchema": object(json!(["schema", "id", "operation"]), with(&call, json!({"fail": {"type": "boolean"}}))), "resultSchema": true, "writes": true},
            {"name": "pokeCatch", "description": "Invokes an operation of another instance and returns the name of what it threw.", "paramsSchema": object(json!(["schema", "id", "operation"]), call.clone()), "resultSchema": true, "writes": true},
            {"name": "pokeRead", "description": "Invokes an operation of another instance from a read-only operation.", "paramsSchema": object(json!(["schema", "id", "operation"]), call.clone()), "resultSchema": true},
            {"name": "peekSchema", "description": "Invokes a schema-level operation from a read-only operation.", "paramsSchema": object(json!(["schema", "operation"]), schema_call.clone()), "resultSchema": true},
            {"name": "pokeSchema", "description": "Counts a poke, then invokes a schema-level operation.", "paramsSchema": object(json!(["schema", "operation"]), schema_call), "resultSchema": true, "writes": true},
            {"name": "guarded", "description": "Its guard invokes an operation of another instance before it runs.", "paramsSchema": object(json!(["schema", "id", "operation"]), call), "resultSchema": true, "writes": true},
            {"name": "failAfterWrite", "description": "Counts a poke, then throws.", "paramsSchema": no_params, "resultSchema": true, "writes": true}
        ]
    })
    .to_string()
});

// poked counts a poke on the instance.
fn poked(cx: &OperationCx<'_, ReaderConfig>) -> Result<()> {
    let pokes = cx.columns().get()?.i64("pokes")?;
    cx.columns().set(&[("pokes", SqlValue::Integer(pokes + 1))])
}

// read_fields is a read's fields, when it names them.
fn read_fields(params: &Value) -> Option<Vec<&str>> {
    params
        .get("fields")
        .and_then(Value::as_array)
        .map(|fields| fields.iter().filter_map(Value::as_str).collect())
}

impl Behavior for Reader {
    type Config = ReaderConfig;

    fn declaration(&self) -> &str {
        &READER
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<ReaderConfig> {
        Ok(ReaderConfig {
            partner_schema: raw
                .get("partnerSchema")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "pokes")
                .column("pokes", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|_, view, request| {
                if let GuardRequest::Operation {
                    operation: "guarded",
                    params,
                    ..
                } = request
                {
                    view.instances().invoke(
                        param(params, "schema"),
                        param(params, "id"),
                        param(params, "operation"),
                        &passed(params),
                        None,
                    )?;
                }
                Ok(None)
            }),
            operations: vec![
                ("peek", |_, cx, params| {
                    let fields = read_fields(params);
                    let found = cx.instances().get(
                        param(params, "schema"),
                        param(params, "id"),
                        fields.as_deref(),
                    )?;
                    Ok(found.as_ref().map_or(Value::Null, record_json))
                }),
                ("peekMany", |_, cx, params| {
                    let fields = read_fields(params);
                    let ids: Vec<&str> = params["ids"]
                        .as_array()
                        .map(|ids| ids.iter().filter_map(Value::as_str).collect())
                        .unwrap_or_default();
                    let found = cx.instances().get_many(
                        param(params, "schema"),
                        &ids,
                        fields.as_deref(),
                    )?;
                    Ok(Value::Object(
                        found
                            .iter()
                            .map(|(id, record)| (id.clone(), record_json(record)))
                            .collect(),
                    ))
                }),
                ("configOf", |_, cx, params| {
                    Ok(cx
                        .schemas()
                        .config(param(params, "schema"), param(params, "behavior"))?
                        .unwrap_or(Value::Null))
                }),
                ("canRead", |_, cx, params| {
                    Ok(Value::Bool(cx.schemas().readable(param(params, "schema"))?))
                }),
                ("poke", |_, cx, params| {
                    poked(cx)?;
                    let result = cx.instances().invoke(
                        param(params, "schema"),
                        param(params, "id"),
                        param(params, "operation"),
                        &passed(params),
                        None,
                    )?;
                    if params.get("fail") == Some(&Value::Bool(true)) {
                        return Err(Error::Engine(
                            superschematic_engine::EngineError::invalid_argument(
                                "poke failed after its invoke, as asked",
                            ),
                        ));
                    }
                    Ok(result)
                }),
                ("pokeCatch", |_, cx, params| {
                    poked(cx)?;
                    Ok(
                        match cx.instances().invoke(
                            param(params, "schema"),
                            param(params, "id"),
                            param(params, "operation"),
                            &passed(params),
                            None,
                        ) {
                            Ok(result) => json!({ "result": result }),
                            Err(error) => json!({ "threw": error_name(&error) }),
                        },
                    )
                }),
                ("pokeRead", |_, cx, params| {
                    cx.instances().invoke(
                        param(params, "schema"),
                        param(params, "id"),
                        param(params, "operation"),
                        &passed(params),
                        None,
                    )
                }),
                ("peekSchema", |_, cx, params| {
                    cx.instances().invoke_schema(
                        param(params, "schema"),
                        param(params, "operation"),
                        &passed(params),
                    )
                }),
                ("pokeSchema", |_, cx, params| {
                    poked(cx)?;
                    cx.instances().invoke_schema(
                        param(params, "schema"),
                        param(params, "operation"),
                        &passed(params),
                    )
                }),
                ("guarded", |_, _, _| Ok(json!("ran"))),
                ("failAfterWrite", |_, cx, _| {
                    poked(cx)?;
                    Err(Error::Engine(
                        superschematic_engine::EngineError::invalid_argument(
                            "failAfterWrite fails after its write",
                        ),
                    ))
                }),
            ],
            fields: vec![
                ("pokes", |_, view| {
                    Ok(Some(Value::from(view.columns().get()?.i64("pokes")?)))
                }),
                ("echo", |_, view| {
                    let data = view.data()?;
                    let Some(partner) = data.get("partner").and_then(Value::as_str) else {
                        return Ok(None);
                    };
                    let schema = view
                        .config()
                        .partner_schema
                        .as_deref()
                        .unwrap_or(view.schema());
                    let found =
                        view.instances()
                            .get(schema, partner, Some(&["test.Reader.echo"]))?;
                    Ok(Some(
                        found
                            .and_then(|record| record.data.get("title").cloned())
                            .unwrap_or(Value::Null),
                    ))
                }),
            ],
            ..Hooks::default()
        }
    }
}

/// test.Broken (`test/mcp.test.ts`): an operation that fails in its own
/// code, a defect, not a refusal.
#[derive(Clone, Copy, Debug, Default)]
pub struct Broken;

impl Behavior for Broken {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Broken", "operations": [{"name": "explode", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}]}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("explode", |_, _, _| {
                Err(Error::behavior(
                    "test.Broken",
                    "the handler failed with a secret: 42",
                ))
            })],
            ..Hooks::default()
        }
    }
}

/// The config of [`Pacer`].
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PacerConfig {
    /// The interval of its schedule on the schema.
    pub every_ms: Option<i64>,
}

/// What a test makes [`Pacer`]'s interval, in place of its config's.
pub type EveryProbe = Arc<dyn Fn(&PacerConfig) -> Result<Option<i64>> + Send + Sync>;
/// What a test makes [`Pacer`]'s schedule do.
pub type TickProbe = Arc<dyn Fn(&ScheduleCx<'_, PacerConfig>) -> Result<()> + Send + Sync>;

/// A test's hand in [`Pacer`]'s interval and schedule.
#[derive(Clone, Default)]
pub struct PacerProbe {
    pub every: Arc<Mutex<Option<EveryProbe>>>,
    pub tick: Arc<Mutex<Option<TickProbe>>>,
}

impl PacerProbe {
    /// Has the interval be what `every` answers for a config.
    pub fn on_every(
        &self,
        every: impl Fn(&PacerConfig) -> Result<Option<i64>> + Send + Sync + 'static,
    ) {
        *self.every.lock().unwrap_or_else(PoisonError::into_inner) = Some(Arc::new(every));
    }

    /// Has the schedule do `tick`.
    pub fn on_tick(
        &self,
        tick: impl Fn(&ScheduleCx<'_, PacerConfig>) -> Result<()> + Send + Sync + 'static,
    ) {
        *self.tick.lock().unwrap_or_else(PoisonError::into_inner) = Some(Arc::new(tick));
    }
}

/// test.Pacer (`test/runner-fixtures.ts`): one schedule, whose interval
/// follows the config of each schema that composes it.
#[derive(Clone, Default)]
pub struct Pacer {
    pub probe: PacerProbe,
}

static PACER_DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Pacer",
        "description": "Ticks on each schema at the interval its config gives.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {"everyMs": {"type": "integer"}}}
    })
    .to_string()
});

impl Behavior for Pacer {
    type Config = PacerConfig;

    fn declaration(&self) -> &str {
        &PACER_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<PacerConfig> {
        Ok(PacerConfig {
            every_ms: raw.get("everyMs").and_then(Value::as_i64),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            schedules: vec![(
                "tick",
                Schedule {
                    every: Every::Config(|pacer, config| {
                        let probe = pacer
                            .probe
                            .every
                            .lock()
                            .unwrap_or_else(PoisonError::into_inner)
                            .clone();
                        match probe {
                            Some(probe) => probe(config),
                            None => Ok(Some(config.every_ms.unwrap_or(60_000))),
                        }
                    }),
                    run: |pacer, cx| {
                        let probe = pacer
                            .probe
                            .tick
                            .lock()
                            .unwrap_or_else(PoisonError::into_inner)
                            .clone();
                        match probe {
                            Some(probe) => probe(cx),
                            None => Ok(()),
                        }
                    },
                },
            )],
            ..Hooks::default()
        }
    }
}

/// The test behavior a scenario file names, by the file's name and the
/// behavior's: each file's own where it defines one, else the shared
/// fixtures'. `None` for one the test kit has not ported.
pub fn test_behavior(file: &str, name: &str) -> Option<BehaviorSet> {
    let set = BehaviorSet::new();
    Some(match (file, name) {
        ("list-filters", "test.Counter") => set.with(Counter::filtered()),
        (_, "test.Counter") => set.with(Counter::default()),
        (_, "test.Flag") => set.with(Flag),
        (_, "test.Tally") => set.with(Tally),
        (_, "test.Tag") => set.with(Tag),
        (_, "test.Mailer") => set.with(Mailer),
        (_, "test.Slugger") => set.with(Slugger),
        (_, "test.Ledger") => set.with(Ledger::default()),
        (_, "test.Pacer") => set.with(Pacer::default()),
        (_, "test.Holder") => set.with(Holder),
        (_, "test.Reader") => set.with(Reader),
        (_, "test.Broken") => set.with(Broken),
        (file, "test.Hold") if !file.starts_with("core-") => set.with(Hold::default()),
        _ => return None,
    })
}
