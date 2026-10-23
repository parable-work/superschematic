//! The bridge to behaviors written in TypeScript (D58, milestone 7b): the
//! test addon's, built with the `js-behaviors` feature, which no shipped
//! addon has. A `defineBehavior` object the facade describes
//! (`src/native/bridge.ts`) becomes a [`JsBehavior`], a dynamic behavior
//! the engine runs as it runs any other; each hook it has calls the
//! facade's host function on the JavaScript thread with the hook's
//! arguments as JSON and the generation of its context.
//!
//! - **Contexts.** While a hook runs, its Rust context is on this thread's
//!   stack of live contexts under a new generation. The JavaScript context
//!   object calls [`bridge_call`] with that generation and a method, which
//!   finds the Rust context and calls it: `sql.run`, `instances.get`,
//!   `call`, `update`, ... A nested call (an `instances.invoke` that runs
//!   another hook) runs in a savepoint, as every nested call of the engine
//!   does, on the transaction the call holds. Once the hook returns, its
//!   generation is gone and a method of its context throws
//!   `context_expired`.
//! - **The JavaScript thread alone.** A hook called off it (an
//!   asynchronous call, the runner's thread) is a `BehaviorError`; an
//!   engine with a behavior in TypeScript refuses the asynchronous call
//!   and the runner's thread outright, and the facade runs the runner on
//!   the event loop. A behavior's hook that calls the engine itself, not
//!   through its context, is refused (a `TypeError`): its call would open
//!   a second transaction inside the one the hook runs in, where the
//!   TypeScript engine would have nested it.
//! - **Errors.** What a hook throws comes back as data: a veto, a config's
//!   refusal, a refusal with a code or a `BehaviorError` as the engine's
//!   own; an error the engine threw into the hook as itself, by the token
//!   the addon parked it under; anything else as [`Error::Host`], the
//!   value the facade parked, which it throws again where the call
//!   returns.

use std::cell::RefCell;
use std::collections::HashMap;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, PoisonError};

use napi::bindgen_prelude::*;
use serde_json::{Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorGuidance, ConfigChange, ConfigTarget, DescribeTarget, DynamicBehavior,
    DynamicHooks, GuidanceError, Hears, InstanceChange, InstanceSchemaForm, OperationGuidance,
    Reference, SqlReader, SqlWriter, ValidationRequest, ValueReader, Values,
};
use superschematic_engine::behaviors::{
    PublishCx, ReactionCx, ReferenceCx, ScheduleCx, SchemaCx, ValidationCx,
};
use superschematic_engine::storage::behavior::{BehaviorMigration, BehaviorSql};
use superschematic_engine::storage::{ColumnSpec, ColumnType, Row, RunResult, SqlValue};
use superschematic_engine::{
    json, EngineError, EngineErrorCode, EngineEvent, Error as Failed, GuardRequest, HostError,
    InstanceCx, InstanceView, OperationCx, Result as EngineResult, ValidationIssue, Veto,
};

use crate::sql::{row_json, run_json, sql_params, sql_value};
use crate::{Failure, Host, OnJsThread, SYNC_ENV};

/// Config tokens the engine let go, which the facade forgets with the next
/// hook call.
pub(crate) type Released = Arc<Mutex<Vec<u64>>>;

// Generations of live contexts and tokens of parked errors, from 1.
static NEXT: AtomicU64 = AtomicU64::new(1);

fn next() -> u64 {
    NEXT.fetch_add(1, Ordering::Relaxed)
}

/// A config a behavior's parseConfig returned, which the facade keeps.
pub struct JsConfig {
    token: u64,
    released: Released,
}

impl Drop for JsConfig {
    fn drop(&mut self) {
        self.released
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .push(self.token);
    }
}

// A live context: what a hook's generation names while the hook runs. Its
// lifetimes are erased; an entry lives only while the hook that pushed it
// is on this thread's stack (Entered), so a pointer is never read after
// the context it points at is gone.
#[derive(Clone, Copy)]
enum Live {
    View(*const InstanceView<'static, JsConfig>),
    Reference(*const ReferenceCx<'static, JsConfig>),
    Instance(*const InstanceCx<'static, JsConfig>),
    Operation(*const OperationCx<'static, JsConfig>),
    Validation(*const ValidationCx<'static, JsConfig>),
    Schema(*const SchemaCx<'static, JsConfig>),
    Reaction(*const ReactionCx<'static, JsConfig>, *const EngineEvent),
    Schedule(*const ScheduleCx<'static, JsConfig>),
    Publish(*const PublishCx<'static, JsConfig>),
    Migration(*const BehaviorSql<'static, 'static>),
    Target(*const ConfigTarget<'static>),
    Change(*const ConfigChange<'static>),
    Render(*const (dyn Fn(&str, bool) -> Value + 'static)),
}

thread_local! {
    static LIVE: RefCell<Vec<(u64, Live)>> = const { RefCell::new(Vec::new()) };
    // The engine's errors a context method threw into a hook, by token,
    // until the outermost call returns: a hook that rethrows one hands
    // back the error itself.
    static PARKED: RefCell<HashMap<u64, Failed>> = RefCell::new(HashMap::new());
    // How deep the addon's entries nest on this thread: PARKED empties
    // when the outermost returns.
    static ENTRIES: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
}

/// Holds an entry of the addon on this thread; the outermost's drop
/// forgets the errors parked during it.
pub(crate) struct Entry;

impl Entry {
    pub(crate) fn enter() -> Self {
        ENTRIES.set(ENTRIES.get() + 1);
        Entry
    }
}

impl Drop for Entry {
    fn drop(&mut self) {
        let depth = ENTRIES.get().saturating_sub(1);
        ENTRIES.set(depth);
        if depth == 0 {
            let parked = PARKED.with(|parked| std::mem::take(&mut *parked.borrow_mut()));
            drop(parked);
        }
    }
}

// Entered keeps a context live for as long as it lives.
struct Entered(u64);

impl Entered {
    fn push(live: Live) -> Self {
        let generation = next();
        LIVE.with(|stack| stack.borrow_mut().push((generation, live)));
        Entered(generation)
    }
}

impl Drop for Entered {
    fn drop(&mut self) {
        LIVE.with(|stack| {
            stack
                .borrow_mut()
                .retain(|(generation, _)| *generation != self.0)
        });
    }
}

fn live(generation: u64) -> Option<Live> {
    LIVE.with(|stack| {
        stack
            .borrow()
            .iter()
            .find(|(each, _)| *each == generation)
            .map(|(_, live)| *live)
    })
}

/// Parks an engine error a context method throws into a hook; the token
/// goes on the JavaScript error.
pub(crate) fn park(error: Failed) -> u64 {
    let token = next();
    PARKED.with(|parked| parked.borrow_mut().insert(token, error));
    token
}

fn unpark(token: u64) -> Option<Failed> {
    PARKED.with(|parked| parked.borrow_mut().remove(&token))
}

/// A behavior written in TypeScript, as the facade described it.
pub(crate) struct JsBehavior {
    id: u64,
    name: String,
    declaration: String,
    hooks: DynamicHooks,
    host: Host,
    released: Released,
}

impl JsBehavior {
    /// The behavior a descriptor names: `{ id, declaration, hooks, problems }`.
    pub(crate) fn of(
        descriptor: &Value,
        host: &Host,
        released: &Released,
    ) -> std::result::Result<Self, Failure> {
        let usage = |what: &str| Failure::usage(format!("a behavior descriptor's {what}"));
        let id = descriptor
            .get("id")
            .and_then(Value::as_u64)
            .ok_or_else(|| usage("id is its index"))?;
        let declaration = descriptor
            .get("declaration")
            .and_then(Value::as_str)
            .ok_or_else(|| usage("declaration is JSON text"))?
            .to_owned();
        let name = serde_json::from_str::<Value>(&declaration)
            .ok()
            .and_then(|value| value.get("name").and_then(Value::as_str).map(str::to_owned))
            .unwrap_or_else(|| "implementation".to_owned());
        let given = descriptor.get("hooks").cloned().unwrap_or(Value::Null);
        let flag = |key: &str| given.get(key).and_then(Value::as_bool).unwrap_or(false);
        let names = |key: &str| -> Vec<String> {
            given
                .get(key)
                .and_then(Value::as_array)
                .map(|list| {
                    list.iter()
                        .filter_map(|each| each.as_str().map(str::to_owned))
                        .collect()
                })
                .unwrap_or_default()
        };
        let mut problems: Vec<String> = descriptor
            .get("problems")
            .and_then(Value::as_array)
            .map(|list| {
                list.iter()
                    .filter_map(|each| each.as_str().map(str::to_owned))
                    .collect()
            })
            .unwrap_or_default();
        let filters = given
            .get("filters")
            .and_then(Value::as_array)
            .map(|list| {
                list.iter()
                    .filter_map(|pair| {
                        let name = pair.get(0)?.as_str()?;
                        let filter = pair.get(1)?;
                        let text =
                            |key: &str| filter.get(key).and_then(Value::as_str).unwrap_or("");
                        let mut out = BehaviorFilter::new(text("column"), text("type"));
                        if let Some(description) = filter.get("description").and_then(Value::as_str)
                        {
                            out.description = Some(description.to_owned());
                        }
                        Some((name.to_owned(), out))
                    })
                    .collect()
            })
            .unwrap_or_default();
        let schedules = given
            .get("schedules")
            .and_then(Value::as_array)
            .map(|list| {
                list.iter()
                    .filter_map(|pair| {
                        Some((pair.get(0)?.as_str()?.to_owned(), pair.get(1)?.as_i64()))
                    })
                    .collect()
            })
            .unwrap_or_default();
        let migrations = migrations_of(
            given.get("migrations"),
            id,
            &name,
            host,
            released,
            &mut problems,
        );
        let hooks = DynamicHooks {
            config_change: flag("configChange"),
            after_config_change: flag("afterConfigChange"),
            initialize: flag("initialize"),
            guard: flag("guard"),
            validate: flag("validate"),
            checked_types: flag("checkedTypes"),
            instance_schema: flag("instanceSchema"),
            guidance: flag("guidance"),
            create_params_schema: flag("createParamsSchema"),
            after_change: flag("afterChange"),
            guard_reference: flag("guardReference"),
            after_reference_change: flag("afterReferenceChange"),
            reactions: flag("reactions"),
            watches: flag("watches"),
            operations: names("operations"),
            schema_operations: names("schemaOperations"),
            fields: names("fields"),
            filters,
            schedules,
            migrations,
            problems,
        };
        Ok(JsBehavior {
            id,
            name,
            declaration,
            hooks,
            host: host.clone(),
            released: Arc::clone(released),
        })
    }

    // ask runs one hook in the facade: its answer's value, None for
    // undefined, or what it threw.
    fn ask(
        &self,
        hook: &str,
        name: Option<&str>,
        live: Option<Live>,
        cx: Option<Value>,
        config: Option<&JsConfig>,
        args: Vec<Value>,
    ) -> EngineResult<Option<Value>> {
        ask(
            &self.host,
            &self.released,
            &self.name,
            self.id,
            hook,
            name,
            live,
            cx,
            config.map(|config| config.token),
            args,
        )
    }
}

#[allow(clippy::too_many_arguments)]
fn ask(
    host: &Host,
    released: &Released,
    behavior: &str,
    id: u64,
    hook: &str,
    name: Option<&str>,
    live: Option<Live>,
    cx: Option<Value>,
    config: Option<u64>,
    args: Vec<Value>,
) -> EngineResult<Option<Value>> {
    if SYNC_ENV.get().is_null() {
        return Err(Failed::behavior(
            behavior,
            format!(
                "{hook} is written in TypeScript, which runs on the JavaScript thread in a synchronous call alone: this call ran off it (an asynchronous call, or the runner's thread)"
            ),
        ));
    }
    let entered = live.map(Entered::push);
    let mut request = Map::new();
    request.insert("b".into(), Value::from(id));
    request.insert("hook".into(), Value::from(hook));
    if let Some(name) = name {
        request.insert("name".into(), Value::from(name));
    }
    request.insert(
        "gen".into(),
        Value::from(entered.as_ref().map_or(0, |entered| entered.0)),
    );
    if let Some(cx) = cx {
        request.insert("cx".into(), cx);
    }
    if let Some(token) = config {
        request.insert("config".into(), Value::from(token));
    }
    request.insert("args".into(), Value::Array(args));
    let release: Vec<u64> =
        std::mem::take(&mut *released.lock().unwrap_or_else(PoisonError::into_inner));
    if !release.is_empty() {
        request.insert("release".into(), Value::from(release));
    }
    let answer = host.ask("behavior", json::stringify(&Value::Object(request)));
    drop(entered);
    let Some(answer) = answer else {
        return Err(Failed::behavior(
            behavior,
            format!("{hook}: the facade's bridge did not answer"),
        ));
    };
    let answer: Value = serde_json::from_str(&answer).map_err(|error| {
        Failed::behavior(
            behavior,
            format!("{hook}: the bridge's answer is not JSON: {error}"),
        )
    })?;
    if let Some(threw) = answer.get("threw") {
        return Err(thrown_of(threw, behavior));
    }
    Ok(answer.get("value").cloned())
}

// thrown_of is what a hook threw, as the engine's error.
fn thrown_of(threw: &Value, behavior: &str) -> Failed {
    let text = |value: Option<&Value>| value.and_then(Value::as_str).unwrap_or("").to_owned();
    if let Some(token) = threw.get("rust").and_then(Value::as_u64) {
        return unpark(token).unwrap_or_else(|| {
            Failed::behavior(
                behavior,
                "it threw an engine error from an earlier call, which the engine no longer holds",
            )
        });
    }
    if let Some(veto) = threw.get("veto") {
        return Failed::veto(
            &text(veto.get("behavior")),
            &text(veto.get("action")),
            &text(veto.get("schema")),
            &text(veto.get("id")),
            Veto {
                reason: text(veto.get("reason")),
                code: veto.get("code").and_then(Value::as_str).map(str::to_owned),
                details: veto.get("details").and_then(Value::as_object).cloned(),
            },
        );
    }
    if let Some(message) = threw.get("config") {
        return Failed::config(text(Some(message)));
    }
    if let Some(refusal) = threw.get("engine") {
        let code = text(refusal.get("code"));
        let message = text(refusal.get("message"));
        return match EngineErrorCode::ALL
            .iter()
            .find(|each| each.as_str() == code)
        {
            Some(code) => Failed::Engine(EngineError::new(*code, message)),
            None => Failed::Host(HostError {
                token: 0,
                message,
                described: format!("{code}: {}", text(refusal.get("message"))),
            }),
        };
    }
    if let Some(defect) = threw.get("behavior") {
        return Failed::behavior(&text(defect.get("behavior")), text(defect.get("message")));
    }
    let host = threw.get("host").cloned().unwrap_or(Value::Null);
    Failed::Host(HostError {
        token: host.get("token").and_then(Value::as_u64).unwrap_or(0),
        message: text(host.get("message")),
        described: text(host.get("described")),
    })
}

fn migrations_of(
    given: Option<&Value>,
    id: u64,
    behavior: &str,
    host: &Host,
    released: &Released,
    problems: &mut Vec<String>,
) -> Vec<BehaviorMigration> {
    let Some(list) = given.and_then(Value::as_array) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for migration in list {
        let version = migration
            .get("version")
            .and_then(Value::as_u64)
            .and_then(|version| u32::try_from(version).ok())
            .unwrap_or(0);
        let name = migration
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_owned();
        let mut step = BehaviorMigration::new(version, name);
        for pair in migration
            .get("columns")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
        {
            let (Some(column), Some(spec)) = (pair.get(0).and_then(Value::as_str), pair.get(1))
            else {
                continue;
            };
            let ty = match spec.get("type").and_then(Value::as_str) {
                Some("integer") => ColumnType::Integer,
                Some("real") => ColumnType::Real,
                Some("text") => ColumnType::Text,
                Some("blob") => ColumnType::Blob,
                _ => ColumnType::Any,
            };
            let mut column_spec = ColumnSpec::new(ty);
            column_spec.not_null = spec
                .get("notNull")
                .and_then(Value::as_bool)
                .unwrap_or(false);
            column_spec.default = match spec.get("default") {
                None | Some(Value::Null) => None,
                Some(value) => match default_of(value) {
                    Some(value) => Some(value),
                    None => {
                        problems.push(format!(
                            "migration {version}: column {column}: a default of {} does not fit type {}",
                            json::stringify(value),
                            ty.name()
                        ));
                        None
                    }
                },
            };
            step.columns.push((column.to_owned(), column_spec));
        }
        for pair in migration
            .get("indexes")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
        {
            let (Some(index), Some(columns)) = (
                pair.get(0).and_then(Value::as_str),
                pair.get(1).and_then(Value::as_array),
            ) else {
                continue;
            };
            step.indexes.push((
                index.to_owned(),
                columns
                    .iter()
                    .filter_map(|column| column.as_str().map(str::to_owned))
                    .collect(),
            ));
        }
        if migration.get("up").and_then(Value::as_bool) == Some(true) {
            let host = host.clone();
            let released = Arc::clone(released);
            let behavior = behavior.to_owned();
            let label = version.to_string();
            step.up = Some(Arc::new(move |sql: &BehaviorSql<'_, '_>| {
                // SAFETY: the entry is popped before ask returns, while
                // `sql` is still borrowed.
                let pointer = std::ptr::from_ref(sql).cast::<BehaviorSql<'static, 'static>>();
                ask(
                    &host,
                    &released,
                    &behavior,
                    id,
                    "migration",
                    Some(&label),
                    Some(Live::Migration(pointer)),
                    None,
                    None,
                    Vec::new(),
                )
                .map(|_| ())
                .map_err(superschematic_engine::storage::Error::from)
            }));
        }
        out.push(step);
    }
    out
}

// default_of is a column's default as SQL: a number, a string.
fn default_of(value: &Value) -> Option<SqlValue> {
    match value {
        Value::String(text) => Some(SqlValue::from(text.as_str())),
        Value::Number(number) => Some(match number.as_i64() {
            Some(integer) if !json::stringify(value).contains(['.', 'e', 'E']) => {
                SqlValue::Integer(integer)
            }
            _ => SqlValue::Real(json::number_value(number)),
        }),
        _ => None,
    }
}

// The context's fixed members, as the facade builds its context from.

fn scope_info(
    behavior: &str,
    namespace: &str,
    schema: &str,
    version: i64,
    principal: &superschematic_engine::Principal,
    now: i64,
) -> Map<String, Value> {
    let mut info = Map::new();
    info.insert("behavior".into(), Value::from(behavior));
    info.insert("namespace".into(), Value::from(namespace));
    info.insert("schema".into(), Value::from(schema));
    info.insert("version".into(), Value::from(version));
    info.insert(
        "principal".into(),
        serde_json::to_value(principal).unwrap_or(Value::Null),
    );
    info.insert("now".into(), Value::from(now));
    info
}

fn view_info(view: &InstanceView<'_, JsConfig>) -> Map<String, Value> {
    let mut info = scope_info(
        view.behavior(),
        view.namespace(),
        view.schema(),
        view.version(),
        view.principal(),
        view.now(),
    );
    info.insert("id".into(), Value::from(view.id()));
    info
}

impl DynamicBehavior for JsBehavior {
    type Config = JsConfig;

    fn declaration(&self) -> &str {
        &self.declaration
    }

    fn hooks(&self) -> DynamicHooks {
        self.hooks.clone()
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> EngineResult<JsConfig> {
        let mut behavior_fields = Map::new();
        for name in target.behaviors() {
            if let Some(fields) = target.behavior_fields(name) {
                behavior_fields.insert(
                    name.clone(),
                    fields
                        .iter()
                        .map(|field| {
                            serde_json::json!({ "name": field.name, "volatile": field.volatile })
                        })
                        .collect(),
                );
            }
        }
        let info = serde_json::json!({
            "schema": target.schema(),
            "type": target.type_name(),
            "fields": target.fields(),
            "fieldSchemas": target.field_schemas(),
            "typeNames": target.types().names(),
            "behaviors": target.behaviors(),
            "behaviorFields": behavior_fields,
            "configs": target.configs(),
            "hasSchemas": target.schemas().is_some(),
        });
        let pointer = std::ptr::from_ref(target).cast::<ConfigTarget<'static>>();
        let token = self
            .ask(
                "parseConfig",
                None,
                Some(Live::Target(pointer)),
                None,
                None,
                vec![raw.clone(), info],
            )?
            .and_then(|value| value.as_u64())
            .ok_or_else(|| {
                Failed::behavior(&self.name, "parseConfig: the bridge kept no config")
            })?;
        Ok(JsConfig {
            token,
            released: Arc::clone(&self.released),
        })
    }

    fn config_change(
        &self,
        before: Option<&JsConfig>,
        after: Option<&JsConfig>,
        change: &ConfigChange<'_>,
    ) -> EngineResult<Option<String>> {
        let pointer = std::ptr::from_ref(change).cast::<ConfigChange<'static>>();
        let token = |config: Option<&JsConfig>| {
            config.map_or(Value::Null, |config| Value::from(config.token))
        };
        Ok(self
            .ask(
                "configChange",
                None,
                Some(Live::Change(pointer)),
                None,
                None,
                vec![token(before), token(after)],
            )?
            .and_then(|value| value.as_str().map(str::to_owned)))
    }

    fn checked_types(&self, config: &JsConfig) -> EngineResult<Vec<String>> {
        Ok(strings(
            self.ask("checkedTypes", None, None, None, Some(config), Vec::new())?
                .as_ref(),
        ))
    }

    fn after_config_change(&self, cx: &PublishCx<'_, JsConfig>) -> EngineResult<()> {
        let mut info = Map::new();
        info.insert("behavior".into(), Value::from(cx.behavior()));
        info.insert("namespace".into(), Value::from(cx.namespace()));
        info.insert("schema".into(), Value::from(cx.schema()));
        info.insert("version".into(), Value::from(cx.version()));
        info.insert("now".into(), Value::from(cx.now()));
        info.insert(
            "config".into(),
            cx.config()
                .map_or(Value::Null, |config| Value::from(config.token)),
        );
        info.insert(
            "before".into(),
            cx.before()
                .map_or(Value::Null, |config| Value::from(config.token)),
        );
        let pointer = std::ptr::from_ref(cx).cast::<PublishCx<'static, JsConfig>>();
        self.ask(
            "afterConfigChange",
            None,
            Some(Live::Publish(pointer)),
            Some(Value::Object(info)),
            None,
            Vec::new(),
        )
        .map(|_| ())
    }

    fn initialize(&self, cx: &InstanceCx<'_, JsConfig>, params: &Value) -> EngineResult<()> {
        let pointer = std::ptr::from_ref(cx).cast::<InstanceCx<'static, JsConfig>>();
        self.ask(
            "initialize",
            None,
            Some(Live::Instance(pointer)),
            Some(Value::Object(view_info(cx))),
            Some(cx.config()),
            vec![params.clone()],
        )
        .map(|_| ())
    }

    fn guard(
        &self,
        view: &InstanceView<'_, JsConfig>,
        request: &GuardRequest<'_>,
    ) -> EngineResult<Option<Veto>> {
        let pointer = std::ptr::from_ref(view).cast::<InstanceView<'static, JsConfig>>();
        let answer = self.ask(
            "guard",
            None,
            Some(Live::View(pointer)),
            Some(Value::Object(view_info(view))),
            Some(view.config()),
            vec![request.to_json()],
        )?;
        Ok(veto_of(answer.as_ref()))
    }

    fn validate(
        &self,
        cx: &ValidationCx<'_, JsConfig>,
        request: &ValidationRequest<'_>,
    ) -> EngineResult<Vec<ValidationIssue>> {
        let mut info = scope_info(
            cx.behavior(),
            cx.namespace(),
            cx.schema(),
            cx.version(),
            cx.principal(),
            cx.now(),
        );
        info.insert("id".into(), Value::from(cx.id()));
        let request = match request {
            ValidationRequest::Create { data } => {
                serde_json::json!({ "kind": "create", "data": data })
            }
            ValidationRequest::Update {
                before,
                after,
                caller,
            } => {
                let mut out =
                    serde_json::json!({ "kind": "update", "before": before, "after": after });
                if let Some(caller) = caller {
                    out["caller"] = Value::from(*caller);
                }
                out
            }
        };
        let pointer = std::ptr::from_ref(cx).cast::<ValidationCx<'static, JsConfig>>();
        let answer = self.ask(
            "validate",
            None,
            Some(Live::Validation(pointer)),
            Some(Value::Object(info)),
            Some(cx.config()),
            vec![request],
        )?;
        Ok(issues_of(answer.as_ref()))
    }

    fn instance_schema(
        &self,
        config: &JsConfig,
        form: InstanceSchemaForm,
        render: &dyn Fn(&str, bool) -> Value,
    ) -> EngineResult<Vec<Value>> {
        // SAFETY: the entry is popped before ask returns, while `render`
        // is still borrowed.
        let pointer: *const (dyn Fn(&str, bool) -> Value + 'static) = unsafe {
            std::mem::transmute::<
                *const (dyn Fn(&str, bool) -> Value + '_),
                *const (dyn Fn(&str, bool) -> Value + 'static),
            >(render as *const (dyn Fn(&str, bool) -> Value + '_))
        };
        let form = match form {
            InstanceSchemaForm::Instance => "instance",
            InstanceSchemaForm::Patch => "patch",
        };
        let answer = self.ask(
            "instanceSchema",
            None,
            Some(Live::Render(pointer)),
            None,
            Some(config),
            vec![Value::from(form)],
        )?;
        Ok(match answer {
            Some(Value::Array(schemas)) => schemas,
            _ => Vec::new(),
        })
    }

    fn guidance(
        &self,
        config: &JsConfig,
        target: &DescribeTarget,
    ) -> EngineResult<BehaviorGuidance> {
        let answer = self.ask(
            "guidance",
            None,
            None,
            None,
            Some(config),
            vec![describe_target(target)],
        )?;
        Ok(guidance_of(answer.as_ref()))
    }

    fn create_params_schema(
        &self,
        config: &JsConfig,
        target: &DescribeTarget,
    ) -> EngineResult<Value> {
        Ok(self
            .ask(
                "createParamsSchema",
                None,
                None,
                None,
                Some(config),
                vec![describe_target(target)],
            )?
            .unwrap_or(Value::Null))
    }

    fn operation(
        &self,
        name: &str,
        cx: &OperationCx<'_, JsConfig>,
        params: &Value,
    ) -> EngineResult<Value> {
        let pointer = std::ptr::from_ref(cx).cast::<OperationCx<'static, JsConfig>>();
        Ok(self
            .ask(
                "operation",
                Some(name),
                Some(Live::Operation(pointer)),
                Some(Value::Object(view_info(cx))),
                Some(cx.config()),
                vec![params.clone()],
            )?
            .unwrap_or(Value::Null))
    }

    fn schema_operation(
        &self,
        name: &str,
        cx: &SchemaCx<'_, JsConfig>,
        params: &Value,
    ) -> EngineResult<Value> {
        let info = scope_info(
            cx.behavior(),
            cx.namespace(),
            cx.schema(),
            cx.version(),
            cx.principal(),
            cx.now(),
        );
        let pointer = std::ptr::from_ref(cx).cast::<SchemaCx<'static, JsConfig>>();
        Ok(self
            .ask(
                "schemaOperation",
                Some(name),
                Some(Live::Schema(pointer)),
                Some(Value::Object(info)),
                Some(cx.config()),
                vec![params.clone()],
            )?
            .unwrap_or(Value::Null))
    }

    fn field(&self, name: &str, view: &InstanceView<'_, JsConfig>) -> EngineResult<Option<Value>> {
        let pointer = std::ptr::from_ref(view).cast::<InstanceView<'static, JsConfig>>();
        Ok(self
            .ask(
                "field",
                Some(name),
                Some(Live::View(pointer)),
                Some(Value::Object(view_info(view))),
                Some(view.config()),
                Vec::new(),
            )?
            .filter(|value| !value.is_null()))
    }

    fn after_change(
        &self,
        cx: &InstanceCx<'_, JsConfig>,
        change: &InstanceChange<'_>,
    ) -> EngineResult<()> {
        let pointer = std::ptr::from_ref(cx).cast::<InstanceCx<'static, JsConfig>>();
        self.ask(
            "afterChange",
            None,
            Some(Live::Instance(pointer)),
            Some(Value::Object(view_info(cx))),
            Some(cx.config()),
            vec![change.to_json()],
        )
        .map(|_| ())
    }

    fn guard_reference(
        &self,
        view: &InstanceView<'_, JsConfig>,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> EngineResult<Option<Veto>> {
        let pointer = std::ptr::from_ref(view).cast::<InstanceView<'static, JsConfig>>();
        let answer = self.ask(
            "guardReference",
            None,
            Some(Live::View(pointer)),
            Some(Value::Object(view_info(view))),
            Some(view.config()),
            vec![reference_json(reference), request.to_json()],
        )?;
        Ok(veto_of(answer.as_ref()))
    }

    fn after_reference_change(
        &self,
        cx: &ReferenceCx<'_, JsConfig>,
        reference: &Reference,
        change: &InstanceChange<'_>,
    ) -> EngineResult<()> {
        let mut info = view_info(cx);
        info.insert("writing".into(), Value::Bool(cx.writing()));
        let pointer = std::ptr::from_ref(cx).cast::<ReferenceCx<'static, JsConfig>>();
        self.ask(
            "afterReferenceChange",
            None,
            Some(Live::Reference(pointer)),
            Some(Value::Object(info)),
            Some(cx.config()),
            vec![reference_json(reference), change.to_json()],
        )
        .map(|_| ())
    }

    fn watches(&self, config: &JsConfig, schema: &str) -> EngineResult<Option<Vec<String>>> {
        let answer = self.ask(
            "watches",
            None,
            None,
            None,
            Some(config),
            vec![Value::from(schema)],
        )?;
        Ok(match answer {
            None | Some(Value::Null) => None,
            Some(value) => Some(strings(Some(&value))),
        })
    }

    fn react(&self, cx: &ReactionCx<'_, JsConfig>, event: &EngineEvent) -> EngineResult<()> {
        let info = scope_info(
            cx.behavior(),
            cx.namespace(),
            cx.schema(),
            cx.version(),
            cx.principal(),
            cx.now(),
        );
        let pointer = std::ptr::from_ref(cx).cast::<ReactionCx<'static, JsConfig>>();
        self.ask(
            "react",
            None,
            Some(Live::Reaction(pointer, event as *const EngineEvent)),
            Some(Value::Object(info)),
            Some(cx.config()),
            vec![serde_json::to_value(event).unwrap_or(Value::Null)],
        )
        .map(|_| ())
    }

    fn every(&self, schedule: &str, config: &JsConfig) -> EngineResult<Option<i64>> {
        let answer = self.ask(
            "every",
            Some(schedule),
            None,
            None,
            Some(config),
            Vec::new(),
        )?;
        Ok(answer.and_then(|value| value.as_i64()))
    }

    fn run_schedule(&self, schedule: &str, cx: &ScheduleCx<'_, JsConfig>) -> EngineResult<()> {
        let mut info = scope_info(
            cx.behavior(),
            cx.namespace(),
            cx.schema(),
            cx.version(),
            cx.principal(),
            cx.now(),
        );
        info.insert("schedule".into(), Value::from(cx.schedule()));
        info.insert(
            "previous".into(),
            cx.previous().map_or(Value::Null, Value::from),
        );
        let pointer = std::ptr::from_ref(cx).cast::<ScheduleCx<'static, JsConfig>>();
        self.ask(
            "schedule",
            Some(schedule),
            Some(Live::Schedule(pointer)),
            Some(Value::Object(info)),
            Some(cx.config()),
            Vec::new(),
        )
        .map(|_| ())
    }
}

fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .filter_map(|each| each.as_str().map(str::to_owned))
                .collect()
        })
        .unwrap_or_default()
}

fn veto_of(answer: Option<&Value>) -> Option<Veto> {
    let answer = answer.filter(|value| !value.is_null())?;
    Some(Veto {
        reason: answer
            .get("reason")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_owned(),
        code: answer
            .get("code")
            .and_then(Value::as_str)
            .map(str::to_owned),
        details: answer.get("details").and_then(Value::as_object).cloned(),
    })
}

fn issues_of(answer: Option<&Value>) -> Vec<ValidationIssue> {
    answer
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .map(|issue| {
                    let text = |key: &str| issue.get(key).and_then(Value::as_str).unwrap_or("");
                    ValidationIssue::new(text("path"), text("rule"), text("message"))
                })
                .collect()
        })
        .unwrap_or_default()
}

fn guidance_of(answer: Option<&Value>) -> BehaviorGuidance {
    let Some(answer) = answer else {
        return BehaviorGuidance::default();
    };
    let text = |value: &Value, key: &str| value.get(key).and_then(Value::as_str).map(str::to_owned);
    let mut operations = Vec::new();
    if let Some(given) = answer.get("operations").and_then(Value::as_object) {
        for (name, raw) in given {
            let errors = raw
                .get("errors")
                .and_then(Value::as_array)
                .map(|list| {
                    list.iter()
                        .map(|error| GuidanceError {
                            code: text(error, "code").unwrap_or_default(),
                            description: text(error, "description").filter(|text| !text.is_empty()),
                            common_correction: text(error, "commonCorrection").unwrap_or_default(),
                        })
                        .collect()
                })
                .unwrap_or_default();
            operations.push((
                name.clone(),
                OperationGuidance {
                    use_when: text(raw, "useWhen"),
                    do_not_use_when: text(raw, "doNotUseWhen"),
                    success: text(raw, "success"),
                    errors,
                },
            ));
        }
    }
    BehaviorGuidance {
        summary: text(answer, "summary").unwrap_or_default(),
        operations,
    }
}

fn describe_target(target: &DescribeTarget) -> Value {
    serde_json::json!({
        "schema": target.schema,
        "type": target.type_name,
        "behaviors": target.behaviors,
        "configs": target.configs,
        "operations": target.operations.iter().map(|operation| {
            let mut out = Map::new();
            out.insert("name".into(), Value::from(operation.name.as_str()));
            if let Some(behavior) = &operation.behavior {
                out.insert("behavior".into(), Value::from(behavior.as_str()));
            }
            out.insert("writes".into(), Value::Bool(operation.writes));
            out.insert("scope".into(), Value::from(operation.scope.as_str()));
            Value::Object(out)
        }).collect::<Vec<_>>(),
    })
}

fn reference_json(reference: &Reference) -> Value {
    let mut out = Map::new();
    out.insert("schema".into(), Value::from(reference.schema.as_str()));
    out.insert("id".into(), Value::from(reference.id.as_str()));
    out.insert("key".into(), Value::from(reference.key.as_str()));
    match &reference.hears {
        None => {}
        Some(Hears::Delete) => {
            out.insert("hears".into(), Value::from("delete"));
        }
        Some(Hears::Path { path, crosses }) => {
            let mut hears = Map::new();
            hears.insert("path".into(), Value::from(path.as_str()));
            if let Some(crosses) = crosses {
                hears.insert("crosses".into(), number(*crosses));
            }
            out.insert("hears".into(), Value::Object(hears));
        }
    }
    Value::Object(out)
}

fn number(value: f64) -> Value {
    serde_json::from_str(&json::js_number(value)).unwrap_or(Value::Null)
}

// The SQL a context reaches.
enum SqlHandle<'a> {
    Reader(SqlReader<'a>),
    Writer(SqlWriter<'a>),
    Migration(&'a BehaviorSql<'a, 'a>),
}

impl SqlHandle<'_> {
    fn table(&self, name: &str) -> EngineResult<String> {
        match self {
            SqlHandle::Reader(sql) => sql.table(name),
            SqlHandle::Writer(sql) => sql.table(name),
            SqlHandle::Migration(sql) => Ok(sql.table(name)?),
        }
    }

    fn instances(&self) -> EngineResult<String> {
        match self {
            SqlHandle::Reader(sql) => sql.instances(),
            SqlHandle::Writer(sql) => sql.instances(),
            SqlHandle::Migration(sql) => Ok(sql.instances()?),
        }
    }

    fn get(&self, text: &str, params: &[SqlValue]) -> EngineResult<Option<Row>> {
        match self {
            SqlHandle::Reader(sql) => sql.get(text, params),
            SqlHandle::Writer(sql) => sql.get(text, params),
            SqlHandle::Migration(sql) => Ok(sql.get(text, params)?),
        }
    }

    fn all(&self, text: &str, params: &[SqlValue]) -> EngineResult<Vec<Row>> {
        match self {
            SqlHandle::Reader(sql) => sql.all(text, params),
            SqlHandle::Writer(sql) => sql.all(text, params),
            SqlHandle::Migration(sql) => Ok(sql.all(text, params)?),
        }
    }

    fn run(&self, behavior: &str, text: &str, params: &[SqlValue]) -> EngineResult<RunResult> {
        match self {
            SqlHandle::Reader(_) => Err(Failed::behavior(
                behavior,
                "this context's SQL only reads: it has no run()",
            )),
            SqlHandle::Writer(sql) => sql.run(text, params),
            SqlHandle::Migration(sql) => Ok(sql.run(text, params)?),
        }
    }
}

enum ValuesHandle<'a> {
    Reader(ValueReader<'a>),
    Writer(Values<'a>),
}

/// A context's method, called from JavaScript: the generation of the hook
/// whose context it is, the method, and its arguments as JSON; the result
/// as JSON, `""` for undefined. A generation whose hook has returned
/// throws `context_expired`. The test addon's `bridgeCall`
/// ([`crate::export_engine_napi!`] exports it when the library has the
/// `js-behaviors` feature).
pub fn bridge_call(env: Env, generation: f64, method: String, args: String) -> Result<String> {
    let _on_js_thread = OnJsThread::enter(&env);
    let _entry = Entry::enter();
    // A generation is a whole number below 2^53, as JavaScript holds it.
    let generation = generation as u64;
    let outcome = catch_unwind(AssertUnwindSafe(|| {
        let Some(live) = live(generation) else {
            return Err(Err(Failure::new(
                "Error",
                Some("context_expired"),
                format!("this behavior context's hook has returned, so its {method} reaches nothing: a context lives only while its hook runs"),
            )));
        };
        let args: Vec<Value> = serde_json::from_str(&args).map_err(|error| {
            Err(Failure::usage(format!(
                "a context method's arguments are JSON: {error}"
            )))
        })?;
        dispatch(live, &method, &args).map_err(Ok)
    }));
    match outcome {
        Ok(Ok(value)) => Ok(value.map_or_else(String::new, |value| json::stringify(&value))),
        Ok(Err(Ok(error))) => {
            let failure = Failure::of(&error);
            let token = park(error);
            Err(failure.with_rust_token(token).into_napi(&env))
        }
        Ok(Err(Err(failure))) => Err(failure.into_napi(&env)),
        Err(payload) => Err(Failure::panic(payload.as_ref()).into_napi(&env)),
    }
}

fn arg(args: &[Value], index: usize) -> &Value {
    args.get(index).unwrap_or(&Value::Null)
}

fn text(args: &[Value], index: usize) -> &str {
    arg(args, index).as_str().unwrap_or("")
}

fn opt_text(args: &[Value], index: usize) -> Option<&str> {
    arg(args, index).as_str()
}

fn to_json(value: serde_json::Result<Value>) -> EngineResult<Option<Value>> {
    value
        .map(Some)
        .map_err(|error| Failed::Usage(format!("the engine could not write its answer: {error}")))
}

// dispatch calls one method of a live context: one arm per method, as the
// TypeScript contexts list them.
#[allow(clippy::too_many_lines, clippy::cognitive_complexity)]
fn dispatch(live: Live, method: &str, args: &[Value]) -> EngineResult<Option<Value>> {
    // SAFETY: a live entry's pointer is valid while its hook runs, and
    // bridge_call runs only inside it, on its thread (Entered).
    unsafe {
        match (method, live) {
            ("target.types.get", Live::Target(target)) => {
                let target = &*target;
                return Ok(Some(match target.types().get(text(args, 0)) {
                    None => Value::Null,
                    Some(ty) => serde_json::json!({
                        "name": ty.name,
                        "fields": ty.fields.iter().map(|field| {
                            let mut out = Map::new();
                            out.insert("key".into(), Value::from(field.key.as_str()));
                            out.insert("type".into(), Value::from(field.type_name.as_str()));
                            out.insert("kind".into(), Value::from(field.kind.as_str()));
                            if let Some(json_type) = &field.json_type {
                                out.insert("jsonType".into(), Value::from(json_type.as_str()));
                            }
                            out.insert("depth".into(), Value::from(field.depth));
                            out.insert("optional".into(), Value::Bool(field.optional));
                            Value::Object(out)
                        }).collect::<Vec<_>>(),
                    }),
                }));
            }
            ("target.schemas.get", Live::Target(target)) => {
                let target = &*target;
                let Some(schemas) = target.schemas() else {
                    return Ok(None);
                };
                return Ok(Some(match schemas.get(text(args, 0))? {
                    None => Value::Null,
                    Some(schema) => serde_json::json!({
                        "schema": schema.schema,
                        "type": schema.type_name,
                        "fields": schema.fields,
                        "behaviors": schema.behaviors,
                        "configs": schema.configs,
                    }),
                }));
            }
            ("change.instances", Live::Change(change)) => {
                return Ok(Some(Value::Bool((*change).instances()?)));
            }
            ("typeSchema", Live::Render(render)) => {
                let nullable = arg(args, 1).as_bool().unwrap_or(false);
                return Ok(Some((*render)(text(args, 0), nullable)));
            }
            ("eachInstance", Live::Publish(cx)) => {
                let mut instances = Vec::new();
                (*cx).each_instance(|instance| {
                    instances.push(serde_json::json!({ "id": instance.id, "data": instance.data }));
                    Ok(())
                })?;
                return Ok(Some(Value::Array(instances)));
            }
            ("validate", Live::Publish(cx)) => {
                return to_json(serde_json::to_value(
                    (*cx).validate(text(args, 0), arg(args, 1))?,
                ));
            }
            ("checkType", Live::Validation(cx)) => {
                return to_json(serde_json::to_value((*cx).check_type(
                    text(args, 0),
                    arg(args, 1),
                    text(args, 2),
                )?));
            }
            ("can", Live::Validation(cx)) => {
                return Ok(Some(Value::Bool((*cx).can(text(args, 0))?)));
            }
            ("before", Live::Reaction(cx, event)) => {
                let given = arg(args, 0);
                let fields = if given.is_null() {
                    (*cx).before(&*event)?
                } else {
                    (*cx).before(&event_of(given))?
                };
                return Ok(fields.map(|fields| serde_json::to_value(fields).unwrap_or(Value::Null)));
            }
            _ => {}
        }
        // The SQL and the value store, as each context reaches them.
        let sql_handle = || -> Option<SqlHandle<'static>> {
            Some(match live {
                Live::View(view) => SqlHandle::Reader((*view).sql()),
                Live::Reference(cx) => SqlHandle::Reader((*cx).sql()),
                Live::Reaction(cx, _) => SqlHandle::Reader((*cx).sql()),
                Live::Instance(cx) => SqlHandle::Writer((*cx).sql()),
                Live::Operation(cx) => SqlHandle::Writer((*cx).sql()),
                Live::Schema(cx) => SqlHandle::Writer((*cx).sql()),
                Live::Schedule(cx) => SqlHandle::Writer((*cx).sql()),
                Live::Publish(cx) => SqlHandle::Writer((*cx).sql()),
                Live::Migration(sql) => SqlHandle::Migration(&*sql),
                _ => return None,
            })
        };
        let values = || -> Option<ValuesHandle<'static>> {
            Some(match live {
                Live::View(view) => ValuesHandle::Reader((*view).values()),
                Live::Reference(cx) => ValuesHandle::Reader((*cx).values()),
                Live::Reaction(cx, _) => ValuesHandle::Reader((*cx).values()),
                Live::Instance(cx) => ValuesHandle::Writer((*cx).values()),
                Live::Operation(cx) => ValuesHandle::Writer((*cx).values()),
                Live::Schema(cx) => ValuesHandle::Writer((*cx).values()),
                Live::Schedule(cx) => ValuesHandle::Writer((*cx).values()),
                Live::Publish(cx) => ValuesHandle::Writer((*cx).values()),
                _ => return None,
            })
        };
        let behavior = behavior_of(live);
        let refused = || Failed::behavior(&behavior, format!("this context has no {method}"));
        if let Some(sql) = method.strip_prefix("sql.") {
            let handle = sql_handle().ok_or_else(refused)?;
            return match sql {
                "table" => Ok(Some(Value::from(handle.table(text(args, 0))?))),
                "instances" => Ok(Some(Value::from(handle.instances()?))),
                "get" => Ok(Some(
                    handle
                        .get(text(args, 0), &sql_params(args.get(1))?)?
                        .map_or(Value::Null, |row| row_json(&row)),
                )),
                "all" => Ok(Some(Value::Array(
                    handle
                        .all(text(args, 0), &sql_params(args.get(1))?)?
                        .iter()
                        .map(row_json)
                        .collect(),
                ))),
                "run" => Ok(Some(run_json(&handle.run(
                    &behavior,
                    text(args, 0),
                    &sql_params(args.get(1))?,
                )?))),
                _ => Err(refused()),
            };
        }
        if let Some(value_method) = method.strip_prefix("values.") {
            let handle = values().ok_or_else(refused)?;
            return match (value_method, handle) {
                ("load", ValuesHandle::Reader(values)) => to_json(serde_json::to_value(
                    values.load(text(args, 0), opt_text(args, 1))?,
                )),
                ("load", ValuesHandle::Writer(values)) => to_json(serde_json::to_value(
                    values.load(text(args, 0), opt_text(args, 1))?,
                )),
                ("stow", ValuesHandle::Writer(values)) => {
                    let object = arg(args, 1).as_object().cloned().unwrap_or_default();
                    let stowed = values.stow(text(args, 0), &object)?;
                    Ok(Some(
                        serde_json::json!({ "json": stowed.json, "refs": stowed.refs }),
                    ))
                }
                ("release", ValuesHandle::Writer(values)) => {
                    values.release(opt_text(args, 0))?;
                    Ok(None)
                }
                _ => Err(refused()),
            };
        }
        // The rest is a scope's, an instance's or an operation's.
        let scope = scope_of(live).ok_or_else(refused)?;
        match method {
            "can" => return Ok(Some(Value::Bool(scope.can(text(args, 0))?))),
            "validate" => {
                return to_json(serde_json::to_value(
                    scope.validate(text(args, 0), arg(args, 1))?,
                ))
            }
            "instances.get" => {
                let fields = fields_of(args.get(2));
                let names: Option<Vec<&str>> = fields
                    .as_ref()
                    .map(|list| list.iter().map(String::as_str).collect());
                return Ok(Some(
                    match scope
                        .instances()
                        .get(text(args, 0), text(args, 1), names.as_deref())?
                    {
                        None => Value::Null,
                        Some(record) => serde_json::to_value(record).unwrap_or(Value::Null),
                    },
                ));
            }
            "instances.getMany" => {
                let ids: Vec<String> = strings(args.get(1));
                let id_refs: Vec<&str> = ids.iter().map(String::as_str).collect();
                let fields = fields_of(args.get(2));
                let names: Option<Vec<&str>> = fields
                    .as_ref()
                    .map(|list| list.iter().map(String::as_str).collect());
                let found =
                    scope
                        .instances()
                        .get_many(text(args, 0), &id_refs, names.as_deref())?;
                return Ok(Some(Value::Array(
                    found
                        .into_iter()
                        .map(|(id, record)| {
                            Value::Array(vec![
                                Value::from(id),
                                serde_json::to_value(record).unwrap_or(Value::Null),
                            ])
                        })
                        .collect(),
                )));
            }
            "instances.invoke" => {
                let preconditions = Some(arg(args, 4)).filter(|value| !value.is_null());
                return Ok(Some(scope.instances().invoke(
                    text(args, 0),
                    text(args, 1),
                    text(args, 2),
                    arg(args, 3),
                    preconditions,
                )?));
            }
            "instances.invokeSchema" => {
                return Ok(Some(scope.instances().invoke_schema(
                    text(args, 0),
                    text(args, 1),
                    arg(args, 2),
                )?));
            }
            "instances.create" => {
                let behaviors = Some(arg(args, 3)).filter(|value| !value.is_null());
                let record = scope.instances().create(
                    text(args, 0),
                    arg(args, 1),
                    opt_text(args, 2),
                    behaviors,
                )?;
                return to_json(serde_json::to_value(record));
            }
            "schemas.config" => {
                return scope.schemas().config(text(args, 0), text(args, 1));
            }
            "schemas.readable" => {
                return Ok(Some(Value::Bool(scope.schemas().readable(text(args, 0))?)));
            }
            _ => {}
        }
        let view = view_of(live).ok_or_else(refused)?;
        match method {
            "data" => return Ok(Some(Value::Object((*view.data()?).clone()))),
            "columns.get" => return Ok(Some(row_json(&view.columns().get()?))),
            "references.list" => {
                return Ok(Some(Value::Array(
                    view.references()
                        .list()?
                        .iter()
                        .map(reference_json)
                        .collect(),
                )));
            }
            _ => {}
        }
        let cx = instance_of(live).ok_or_else(refused)?;
        match method {
            "columns.set" => {
                let mut pairs: Vec<(String, SqlValue)> = Vec::new();
                for pair in arg(args, 0).as_array().into_iter().flatten() {
                    let column = pair.get(0).and_then(Value::as_str).unwrap_or("").to_owned();
                    pairs.push((column, sql_value(pair.get(1).unwrap_or(&Value::Null))?));
                }
                let borrowed: Vec<(&str, SqlValue)> = pairs
                    .iter()
                    .map(|(column, value)| (column.as_str(), value.clone()))
                    .collect();
                cx.columns().set(&borrowed)?;
                return Ok(None);
            }
            "references.add" => {
                cx.references().add(
                    text(args, 0),
                    text(args, 1),
                    text(args, 2),
                    hears_of(arg(args, 3)),
                )?;
                return Ok(None);
            }
            "references.remove" => {
                return Ok(Some(Value::Bool(cx.references().remove(
                    text(args, 0),
                    text(args, 1),
                    text(args, 2),
                )?)));
            }
            "call" => {
                return Ok(Some(cx.call(text(args, 0), text(args, 1), arg(args, 2))?));
            }
            _ => {}
        }
        let Live::Operation(operation) = live else {
            return Err(refused());
        };
        let operation = &*operation;
        match method {
            "update" => Ok(Some(Value::Object(
                (*match args.get(1) {
                    Some(attribution) if !attribution.is_null() => {
                        operation.update_attributed(arg(args, 0), attribution)?
                    }
                    _ => operation.update(arg(args, 0))?,
                })
                .clone(),
            ))),
            "validateUpdate" => to_json(serde_json::to_value(
                operation.validate_update(arg(args, 0))?,
            )),
            "unchanged" => {
                operation.unchanged();
                Ok(None)
            }
            _ => Err(refused()),
        }
    }
}

fn behavior_of(live: Live) -> String {
    // SAFETY: as in dispatch.
    unsafe {
        match live {
            Live::Validation(cx) => (*cx).behavior().to_owned(),
            Live::Publish(cx) => (*cx).behavior().to_owned(),
            other => scope_of(other).map_or_else(String::new, |scope| scope.behavior().to_owned()),
        }
    }
}

// SAFETY (scope_of, view_of, instance_of): as in dispatch.
unsafe fn scope_of(
    live: Live,
) -> Option<&'static superschematic_engine::behaviors::Scope<'static, JsConfig>> {
    Some(match live {
        Live::View(view) => &**view,
        Live::Reference(cx) => &***cx,
        Live::Instance(cx) => &***cx,
        Live::Operation(cx) => &****cx,
        Live::Schema(cx) => &**cx,
        Live::Reaction(cx, _) => &**cx,
        Live::Schedule(cx) => &**cx,
        _ => return None,
    })
}

unsafe fn view_of(live: Live) -> Option<&'static InstanceView<'static, JsConfig>> {
    Some(match live {
        Live::View(view) => &*view,
        Live::Reference(cx) => &**cx,
        Live::Instance(cx) => &**cx,
        Live::Operation(cx) => &***cx,
        _ => return None,
    })
}

unsafe fn instance_of(live: Live) -> Option<&'static InstanceCx<'static, JsConfig>> {
    Some(match live {
        Live::Instance(cx) => &*cx,
        Live::Operation(cx) => &**cx,
        _ => return None,
    })
}

fn fields_of(value: Option<&Value>) -> Option<Vec<String>> {
    value
        .filter(|value| !value.is_null())
        .map(|value| strings(Some(value)))
}

fn hears_of(value: &Value) -> Option<Hears> {
    match value {
        Value::String(text) if text == "delete" => Some(Hears::Delete),
        Value::Object(members) => Some(Hears::Path {
            path: members
                .get("path")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_owned(),
            crosses: members
                .get("crosses")
                .and_then(Value::as_number)
                .map(json::number_value),
        }),
        _ => None,
    }
}

// event_of is an event a reaction passes to before(), from its JSON.
fn event_of(value: &Value) -> EngineEvent {
    let text = |key: &str| {
        value
            .get(key)
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_owned()
    };
    EngineEvent {
        cursor: value.get("cursor").and_then(Value::as_i64).unwrap_or(0),
        kind: text("kind"),
        namespace: text("namespace"),
        schema: text("schema"),
        instance_id: value
            .get("instanceId")
            .and_then(Value::as_str)
            .map(str::to_owned),
        seq: value.get("seq").and_then(Value::as_i64),
        version: value.get("version").and_then(Value::as_i64),
        actor: text("actor"),
        service: value
            .get("service")
            .and_then(Value::as_str)
            .map(str::to_owned),
        at: value.get("at").and_then(Value::as_i64).unwrap_or(0),
        change: value.get("change").cloned().unwrap_or(Value::Null),
        cause: None,
        value_refs: None,
    }
}
