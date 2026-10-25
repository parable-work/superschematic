//! The engine's calls by name, with JSON arguments and a JSON result, as
//! the TypeScript engine's methods take and return them (D58): what a
//! binding (the Node-API addon, the C ABI's `eng_call`) and the scenario
//! replayer call. `instances.create` with `["Order", {"title": "Desk"},
//! {"id": "o1"}]` is `engine.instances.create(principal, 'Order', { title:
//! 'Desk' }, { id: 'o1' })`.
//!
//! The typed API takes Rust's types, so what JSON can say and they cannot
//! is checked here, in the TypeScript engine's words: a page size, a
//! sequence or a cursor that is no integer (`1.5`, `"tail"`), a filter
//! that is no list. A value is named as JavaScript's `String()` writes it.
//! A call returns `None` where the TypeScript method returns `undefined`.
//!
//! The behaviors' calls are here (`instances.invoke`, `operate`,
//! `invokeSchema`, and `behaviors.has`, `names` and `declaration`), the
//! runner's, the tools', `search`, and `close` (the TypeScript engine's,
//! [`Engine::shutdown`]); `behaviors.register` takes an implementation,
//! which JSON cannot carry, so a host registers through
//! [`crate::Behaviors::register`].
//!
//! A server's calls are here too: `deploy`, which defines and publishes a
//! server's schema documents as the engine's own principal, which no
//! request can be, leaving each one whose live version has its hash as it
//! is, and `health`, what a server's readiness reads. Each takes no
//! principal: a host makes them, never a request.
//!
//! The HTTP API is here too, for a binding that passes JSON across its
//! boundary: `api.routes`, and `api.handle`, which takes the request as
//! JSON (its body as text) and returns `{status, headers, body, stream}`;
//! an event stream it opens, or one `stream.open` opens from a read of the
//! log, is held by the engine and named by an id, which `stream.next`
//! pulls (`{data}`, `{pending: true}` or `{end: true}`) and `stream.close`
//! ends.

use std::borrow::Cow;

use serde_json::{Map, Value};

use crate::api::{
    self, ApiOptions, Body, Caller, McpOptions, Refusal, Request, ServiceCaller, StreamOptions,
};
use crate::engine::Engine;
use crate::errors::{EngineError, Error, Result};
use crate::events::{check_filters, cursor_refusal, EventsAfter, ReadEvents};
use crate::instances::{
    expected_seq_refusal, CreateOptions, GetOptions, ListOptions, WriteOptions,
};
use crate::json;
use crate::namespaces::name_refusal;
use crate::paging::page_size_refusal;
use crate::principal::{check_principal, Principal};
use crate::registry::{DefineOptions, SchemaInput, SchemaTarget, ValidateOptions};
use crate::storage::Dialect;

/// The calls [`call`] answers, by name.
pub const CALLS: [&str; 57] = [
    "schemas.define",
    "schemas.publish",
    "schemas.live",
    "schemas.draft",
    "schemas.version",
    "schemas.list",
    "schemas.capabilities",
    "schemas.validate",
    "schemas.behaviors",
    "instances.create",
    "instances.get",
    "instances.list",
    "instances.lookup",
    "instances.update",
    "instances.delete",
    "instances.invoke",
    "instances.operate",
    "instances.invokeSchema",
    "events.read",
    "events.head",
    "events.floor",
    "namespaces.create",
    "namespaces.archive",
    "namespaces.unarchive",
    "namespaces.get",
    "namespaces.list",
    "namespaces.names",
    "namespaces.resolve",
    "values.get",
    "values.sweep",
    "values.thresholdBytes",
    "values.maxBytes",
    "tools.listBehaviors",
    "tools.describeBehavior",
    "tools.describe",
    "tools.manifest",
    "tools.call",
    "search",
    "runner.start",
    "runner.stop",
    "runner.running",
    "runner.runDue",
    "runner.prune",
    "runner.status",
    "runner.nextWake",
    "runner.resume",
    "behaviors.has",
    "behaviors.names",
    "behaviors.declaration",
    "api.routes",
    "api.handle",
    "stream.open",
    "stream.next",
    "stream.close",
    "close",
    "deploy",
    "health",
];

/// Calls a method of the engine by name, as `principal` for a method that
/// takes one, with its arguments after the principal. An unknown method is
/// refused as an argument the engine cannot take.
pub fn call(
    engine: &Engine,
    method: &str,
    principal: Option<&Principal>,
    args: &[Value],
) -> Result<Option<Value>> {
    call_with(engine, method, principal, args, None)
}

/// [`call`], with the tool filter the tools calls (`tools.describe`,
/// `tools.manifest`, `tools.call`) narrow by: a mount's, which a binding
/// asks in the host's language (JSON carries no function).
pub fn call_with(
    engine: &Engine,
    method: &str,
    principal: Option<&Principal>,
    args: &[Value],
    filter: Option<&dyn crate::tools::ToolFilter>,
) -> Result<Option<Value>> {
    let who = || {
        principal.ok_or_else(|| {
            Error::from(EngineError::invalid_argument(
                "an engine call needs a principal with a subject",
            ))
        })
    };
    let arg = |index: usize| args.get(index).unwrap_or(&Value::Null);
    let options = |index: usize| options_of(args.get(index));
    // A shut-down engine refuses every call but the pulls that end its
    // streams.
    if engine.is_closed() && !matches!(method, "stream.next" | "stream.close" | "close" | "health")
    {
        return Err(Error::internal("the engine is closed"));
    }
    let result = match method {
        "schemas.define" => {
            let options = options(1);
            let namespace = text_member(options, "namespace");
            let source = text_member(options, "source");
            let input = match arg(0) {
                Value::String(text) => SchemaInput::Text(Cow::Borrowed(text)),
                document => SchemaInput::Document(Cow::Borrowed(document)),
            };
            to_json(engine.schemas().define(
                who()?,
                input,
                DefineOptions {
                    namespace: namespace.as_deref(),
                    source: source.as_deref(),
                },
            )?)
        }
        "schemas.publish" => {
            let namespace = text_member(options(1), "namespace");
            to_json(engine.schemas().publish(
                who()?,
                &text(arg(0)),
                SchemaTarget {
                    namespace: namespace.as_deref(),
                },
            )?)
        }
        "schemas.live" | "schemas.draft" => {
            let namespace = text_member(options(1), "namespace");
            let target = SchemaTarget {
                namespace: namespace.as_deref(),
            };
            let schemas = engine.schemas();
            let found = if method == "schemas.live" {
                schemas.live(who()?, &text(arg(0)), target)?
            } else {
                schemas.draft(who()?, &text(arg(0)), target)?
            };
            return found.map(to_json).transpose();
        }
        "schemas.version" => {
            let principal = who()?;
            let version = integer(arg(1)).ok_or_else(|| {
                EngineError::invalid_argument(format!(
                    "a schema version is a positive integer, got {}",
                    js_string(arg(1))
                ))
            })?;
            let namespace = text_member(options(2), "namespace");
            return engine
                .schemas()
                .version(
                    principal,
                    &text(arg(0)),
                    version,
                    SchemaTarget {
                        namespace: namespace.as_deref(),
                    },
                )?
                .map(to_json)
                .transpose();
        }
        "schemas.list" | "schemas.capabilities" => {
            let namespace = text_member(options(0), "namespace");
            let target = SchemaTarget {
                namespace: namespace.as_deref(),
            };
            if method == "schemas.list" {
                to_json(engine.schemas().list(who()?, target)?)
            } else {
                to_json(engine.schemas().capabilities(who()?, target)?)
            }
        }
        "schemas.validate" | "schemas.behaviors" => {
            let at = if method == "schemas.validate" { 2 } else { 1 };
            let principal = who()?;
            let options = options(at);
            let namespace = text_member(options, "namespace");
            let version = match options.and_then(|options| options.get("version")) {
                None => None,
                Some(given) => Some(integer(given).ok_or_else(|| {
                    EngineError::invalid_argument(format!(
                        "a schema version is a positive integer, got {}",
                        js_string(given)
                    ))
                })?),
            };
            let target = ValidateOptions {
                namespace: namespace.as_deref(),
                version,
            };
            if method == "schemas.validate" {
                to_json(
                    engine
                        .schemas()
                        .validate(principal, &text(arg(0)), arg(1), target)?,
                )
            } else {
                to_json(
                    engine
                        .schemas()
                        .behaviors(principal, &text(arg(0)), target)?,
                )
            }
        }
        "instances.create" => {
            let options = options(2);
            let namespace = text_member(options, "namespace");
            let id = options.and_then(|options| options.get("id")).map(js_string);
            let behaviors = options.and_then(|options| options.get("behaviors"));
            to_json(engine.instances().create(
                who()?,
                &text(arg(0)),
                arg(1),
                CreateOptions {
                    namespace: namespace.as_deref(),
                    id: id.as_deref(),
                    behaviors,
                },
            )?)
        }
        "instances.get" | "instances.lookup" => {
            let options = options(2);
            let namespace = text_member(options, "namespace");
            let get = GetOptions {
                namespace: namespace.as_deref(),
                value_refs: flag_member(options, "valueRefs"),
            };
            let found = if method == "instances.get" {
                engine
                    .instances()
                    .get(who()?, &text(arg(0)), &text(arg(1)), get)?
            } else {
                engine
                    .instances()
                    .lookup(who()?, &text(arg(0)), arg(1), get)?
            };
            return found.map(to_json).transpose();
        }
        "instances.list" => {
            let principal = who()?;
            let options = options(1);
            let namespace = text_member(options, "namespace");
            let limit = match options.and_then(|options| options.get("limit")) {
                None => None,
                Some(given) => {
                    Some(integer(given).ok_or_else(|| page_size_refusal(&js_string(given)))?)
                }
            };
            let cursor = options
                .and_then(|options| options.get("cursor"))
                .map(js_string);
            to_json(engine.instances().list(
                principal,
                &text(arg(0)),
                ListOptions {
                    namespace: namespace.as_deref(),
                    limit,
                    cursor: cursor.as_deref(),
                    value_refs: flag_member(options, "valueRefs"),
                    filter: options.and_then(|options| options.get("where")),
                },
            )?)
        }
        "instances.update" | "instances.delete" => {
            let principal = who()?;
            let at = if method == "instances.update" { 3 } else { 2 };
            let options = options(at);
            let namespace = text_member(options, "namespace");
            let expected_seq = match options.and_then(|options| options.get("expectedSeq")) {
                None => None,
                Some(given) => Some(
                    integer(given)
                        .filter(|seq| *seq >= 0)
                        .ok_or_else(|| expected_seq_refusal(&js_string(given)))?,
                ),
            };
            let write = WriteOptions {
                namespace: namespace.as_deref(),
                expected_seq,
                preconditions: options.and_then(|options| options.get("preconditions")),
            };
            if method == "instances.update" {
                to_json(engine.instances().update(
                    principal,
                    &text(arg(0)),
                    &text(arg(1)),
                    arg(2),
                    write,
                )?)
            } else {
                Ok(Value::Bool(engine.instances().delete(
                    principal,
                    &text(arg(0)),
                    &text(arg(1)),
                    write,
                )?))
            }
        }
        "instances.invoke" | "instances.operate" => {
            let principal = who()?;
            let options = options(4);
            let namespace = text_member(options, "namespace");
            let expected_seq = match options.and_then(|options| options.get("expectedSeq")) {
                None => None,
                Some(given) => Some(
                    integer(given)
                        .filter(|seq| *seq >= 0)
                        .ok_or_else(|| expected_seq_refusal(&js_string(given)))?,
                ),
            };
            let write = WriteOptions {
                namespace: namespace.as_deref(),
                expected_seq,
                preconditions: options.and_then(|options| options.get("preconditions")),
            };
            let empty = Value::Object(Map::new());
            let outcome = engine.instances().operate(
                principal,
                &text(arg(0)),
                &text(arg(1)),
                &text(arg(2)),
                args.get(3).unwrap_or(&empty),
                write,
            )?;
            if method == "instances.invoke" {
                Ok(outcome.result)
            } else {
                to_json(outcome)
            }
        }
        "instances.invokeSchema" => {
            let principal = who()?;
            let namespace = text_member(options(3), "namespace");
            let empty = Value::Object(Map::new());
            engine.instances().invoke_schema(
                principal,
                &text(arg(0)),
                &text(arg(1)),
                args.get(2).unwrap_or(&empty),
                namespace.as_deref(),
            )
        }
        "behaviors.has" => Ok(Value::Bool(engine.behaviors().has(&text(arg(0))))),
        "behaviors.names" => to_json(engine.behaviors().names()),
        "behaviors.declaration" => return Ok(engine.behaviors().declaration(&text(arg(0)))),
        "events.read" => {
            let principal = who()?;
            let read = ReadArgs::of(options(0))?;
            to_json(engine.events().read(principal, &read.view())?)
        }
        "events.head" => Ok(Value::from(engine.events().head()?)),
        "events.floor" => {
            let namespace = args.first().filter(|given| !given.is_null()).map(js_string);
            Ok(Value::from(engine.events().floor(namespace.as_deref())?))
        }
        "namespaces.create" => {
            let principal = who()?;
            let Value::String(name) = arg(0) else {
                check_principal(principal)?;
                return Err(name_refusal(&json::stringify(arg(0))).into());
            };
            to_json(engine.namespaces().create(principal, name)?)
        }
        "namespaces.archive" => to_json(engine.namespaces().archive(who()?, &text(arg(0)))?),
        "namespaces.unarchive" => to_json(engine.namespaces().unarchive(who()?, &text(arg(0)))?),
        "namespaces.get" => to_json(engine.namespaces().get(who()?, &text(arg(0)))?),
        "namespaces.list" => to_json(engine.namespaces().list(who()?)?),
        "namespaces.names" => to_json(engine.namespaces().names()?),
        "namespaces.resolve" => {
            let namespace = args.first().filter(|given| !given.is_null()).map(js_string);
            Ok(Value::from(
                engine.namespaces().resolve(namespace.as_deref())?,
            ))
        }
        "values.get" => {
            let namespace = text_member(options(1), "namespace");
            to_json(
                engine
                    .values()
                    .get(who()?, &text(arg(0)), namespace.as_deref())?,
            )
        }
        "values.sweep" => to_json(engine.values().sweep()?),
        "values.thresholdBytes" => Ok(Value::from(engine.values().threshold_bytes())),
        "values.maxBytes" => Ok(Value::from(engine.values().max_bytes())),
        "tools.listBehaviors" => engine.tools().list_behaviors(who()?),
        "tools.describeBehavior" => engine.tools().describe_behavior(who()?, &text(arg(0))),
        "tools.describe" => {
            let namespace = text_member(options(1), "namespace");
            engine.tools().describe(
                who()?,
                &text(arg(0)),
                crate::tools::ToolTarget {
                    namespace: namespace.as_deref(),
                    filter,
                },
            )
        }
        "tools.manifest" => {
            let namespace = text_member(options(0), "namespace");
            engine.tools().manifest(
                who()?,
                crate::tools::ToolTarget {
                    namespace: namespace.as_deref(),
                    filter,
                },
            )
        }
        "tools.call" => {
            let namespace = text_member(options(2), "namespace");
            engine.tools().call(
                who()?,
                &text(arg(0)),
                arg(1),
                crate::tools::ToolTarget {
                    namespace: namespace.as_deref(),
                    filter,
                },
            )
        }
        "search" => {
            let namespace = text_member(options(1), "namespace");
            engine.search(who()?, arg(0), namespace.as_deref())
        }
        "runner.start" => {
            engine.runner().start()?;
            return Ok(None);
        }
        "runner.stop" => {
            engine.runner().stop();
            return Ok(None);
        }
        "runner.running" => Ok(Value::Bool(engine.runner().running())),
        "runner.runDue" => to_json(engine.runner().run_due()?),
        "runner.prune" => to_json(engine.runner().prune()?),
        "runner.status" => to_json(engine.runner().status()?),
        "runner.nextWake" => Ok(Value::from(engine.runner().next_wake())),
        "runner.resume" => {
            let key = arg(0);
            let member = |name: &str| text_member(key.as_object(), name).unwrap_or_default();
            let skip = flag_member(options(1), "skip");
            to_json(engine.runner().resume(
                &crate::runner::SubscriptionKey {
                    behavior: member("behavior"),
                    namespace: member("namespace"),
                    schema: member("schema"),
                },
                skip,
            )?)
        }
        "api.routes" => to_json(api::routes()),
        "api.handle" => {
            let request = request_of(arg(0))?;
            let options = api_options_of(options(1))?;
            let response = api::handle(engine, &options, &request);
            Ok(response_json(engine, response))
        }
        "stream.open" => {
            let principal = who()?;
            let read = ReadArgs::of(options(0))?;
            let stream = api::start_stream(
                engine,
                &stream_options_of(options(1))?,
                principal,
                &read.view(),
            )?;
            Ok(Value::from(engine.streams.hold(Box::new(stream))))
        }
        "stream.next" => {
            let id = stream_id(arg(0))?;
            let timeout = match arg(1) {
                Value::Null => 0,
                given => integer(given)
                    .and_then(|ms| u64::try_from(ms).ok())
                    .ok_or_else(|| {
                        EngineError::invalid_argument(format!(
                            "a stream's pull waits a whole number of milliseconds, got {}",
                            js_string(given)
                        ))
                    })?,
            };
            let pulled = engine
                .streams
                .next(id, std::time::Duration::from_millis(timeout))
                .ok_or_else(|| unknown_stream(id))?;
            Ok(match pulled {
                api::Pull::Data(bytes) => {
                    serde_json::json!({"data": String::from_utf8_lossy(&bytes)})
                }
                api::Pull::Pending => serde_json::json!({"pending": true}),
                api::Pull::End => serde_json::json!({"end": true}),
            })
        }
        "stream.close" => {
            let id = stream_id(arg(0))?;
            if !engine.streams.close(id) {
                return Err(unknown_stream(id).into());
            }
            return Ok(None);
        }
        "close" => {
            engine.shutdown();
            return Ok(None);
        }
        "deploy" => deploy(engine, args),
        "health" => Ok(health(engine)),
        other => {
            return Err(Error::usage(format!(
                "the engine has no call {}",
                json::quote(other)
            )))
        }
    };
    Ok(Some(result?))
}

/// [`Engine::deploy`] by its JSON arguments, `[documents, { namespace? }]`:
/// each schema document, or its text, defined and published as the
/// engine's own principal, which no request can be, one whose live version
/// has its hash left as it is. It returns, per document, its `name`, the
/// `namespace` and live `version` after the deploy, whether it was
/// `defined` and whether a version was `published`. Arguments of another
/// shape are a usage error (`TypeError`), as a host's misuse is, and a
/// document the engine refuses its refusal.
pub fn deploy(engine: &Engine, args: &[Value]) -> Result<Value> {
    let Some(Value::Array(documents)) = args.first() else {
        return Err(Error::usage(
            "deploy takes [documents, { namespace? }]: a list of schema documents",
        ));
    };
    let namespace = args
        .get(1)
        .and_then(Value::as_object)
        .and_then(|options| options.get("namespace"))
        .filter(|namespace| !namespace.is_null())
        .map(js_string);
    let deployed = engine.deploy(documents, namespace.as_deref())?;
    Ok(Value::Array(
        deployed.iter().map(crate::Deployed::to_json).collect(),
    ))
}

/// What a server's readiness reads of the engine: `ready`, false once the
/// engine closed (`closed`) or panicked inside a transaction (`poisoned`),
/// or while its database does not answer a read (`error`, why); its
/// `dialect`; and whether it is `listening` for other processes' commits
/// on Postgres. It answers on a closed engine too.
pub fn health(engine: &Engine) -> Value {
    let closed = engine.is_closed();
    let poisoned = engine.poisoned();
    let error = if closed || poisoned {
        None
    } else {
        engine.events().head().err().map(|error| error.to_string())
    };
    let mut out = Map::new();
    out.insert(
        "ready".into(),
        Value::Bool(!closed && !poisoned && error.is_none()),
    );
    out.insert("closed".into(), Value::Bool(closed));
    out.insert("poisoned".into(), Value::Bool(poisoned));
    out.insert(
        "dialect".into(),
        Value::from(match engine.dialect() {
            Dialect::Postgres => "postgres",
            _ => "sqlite",
        }),
    );
    out.insert(
        "listening".into(),
        Value::Bool(engine.db().notifier().listening()),
    );
    if let Some(error) = error {
        out.insert("error".into(), Value::from(error));
    }
    Value::Object(out)
}

/// A principal a call passes as JSON, checked as the TypeScript engine's
/// `checkPrincipal` checks it: an object naming its subject, and a service
/// that is `{ deployable, serves, subject, standsIn }` and, standing in for
/// an end user, holds no permissions (D37).
pub fn principal_from_json(given: &Value) -> Result<Principal, EngineError> {
    let no_subject =
        || EngineError::invalid_argument("an engine call needs a principal with a subject");
    let Value::Object(members) = given else {
        return Err(no_subject());
    };
    match members.get("subject") {
        Some(Value::String(subject)) if !subject.is_empty() => {}
        _ => return Err(no_subject()),
    }
    if let Some(service) = members.get("service") {
        let empty = Map::new();
        let fields = service.as_object().unwrap_or(&empty);
        let deployable = fields
            .get("deployable")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let serves = fields
            .get("serves")
            .and_then(Value::as_array)
            .is_some_and(|serves| serves.iter().all(Value::is_string));
        let well_formed = !deployable.is_empty()
            && serves
            && fields.get("subject").is_some_and(Value::is_string)
            && fields.get("standsIn").is_some_and(Value::is_boolean);
        if !well_formed {
            return Err(EngineError::invalid_argument(
                "a principal's service is { deployable, serves, subject, standsIn }",
            ));
        }
        let holds = members
            .get("permissions")
            .is_none_or(|permissions| permissions.as_array().is_none_or(|list| !list.is_empty()));
        if fields.get("standsIn") == Some(&Value::Bool(true)) && holds {
            return Err(EngineError::invalid_argument(format!(
                "service {deployable} stands in for an end user and holds no permissions (D37)"
            )));
        }
    }
    serde_json::from_value(given.clone()).map_err(|error| {
        EngineError::invalid_argument(format!(
            "a principal is {{ subject, permissions, claims?, service? }}: {error}"
        ))
    })
}

// ReadArgs is a read of the log a call's options give, owned: the cursor,
// the page size and the filters checked in the TypeScript engine's order
// (the cursor, the page size, then the filters).
struct ReadArgs {
    namespace: Option<String>,
    schema: Option<String>,
    instance_id: Option<String>,
    after: Option<EventsAfter>,
    limit: Option<i64>,
    kinds: Option<Vec<String>>,
    behaviors: Option<Vec<String>>,
    exclude: Option<Vec<String>>,
    beside: Option<bool>,
}

impl ReadArgs {
    fn of(options: Option<&Map<String, Value>>) -> Result<ReadArgs> {
        let member = |key: &str| options.and_then(|options| options.get(key));
        let after = match member("after") {
            None => None,
            Some(Value::String(head)) if head == "head" => Some(EventsAfter::Head),
            Some(given) => Some(EventsAfter::Cursor(
                integer(given).ok_or_else(|| cursor_refusal(&js_string(given)))?,
            )),
        };
        let limit = match member("limit") {
            None => None,
            Some(given) => {
                Some(integer(given).ok_or_else(|| page_size_refusal(&js_string(given)))?)
            }
        };
        let [kinds, behaviors, exclude] =
            check_filters([member("kinds"), member("behaviors"), member("exclude")])?;
        let beside = match member("beside") {
            None => None,
            Some(Value::Bool(beside)) => Some(*beside),
            Some(_) => return Err(EngineError::invalid_argument(
                "beside is a boolean: false drops beside operations' events, true keeps only them",
            )
            .into()),
        };
        Ok(ReadArgs {
            namespace: text_member(options, "namespace"),
            schema: text_member(options, "schema"),
            instance_id: text_member(options, "instanceId"),
            after,
            limit,
            kinds,
            behaviors,
            exclude,
            beside,
        })
    }

    fn view(&self) -> ReadEvents<'_> {
        ReadEvents {
            namespace: self.namespace.as_deref(),
            schema: self.schema.as_deref(),
            instance_id: self.instance_id.as_deref(),
            after: self.after,
            limit: self.limit,
            kinds: self.kinds.clone(),
            behaviors: self.behaviors.clone(),
            exclude: self.exclude.clone(),
            beside: self.beside,
        }
    }
}

/// What a binding hands back for one HTTP request ([`http`]): the
/// response's status and headers (lowercase names), its body's bytes, and
/// the id of the event stream it holds open instead, which
/// [`stream_pull`] pulls and `stream.close` ends.
#[derive(Debug)]
pub struct HttpReply {
    pub status: u16,
    pub headers: Vec<(String, String)>,
    pub body: Option<Vec<u8>>,
    pub stream: Option<u64>,
}

/// Serves one HTTP request whose head is JSON, as `api.handle` takes it
/// but for its body, which comes as bytes, and the mount's options
/// (`options`: `stream`, `mcp`) and tool filter: what a binding's HTTP
/// adapter calls, with a typed body either way.
pub fn http(
    engine: &Engine,
    head: &Value,
    body: Option<Vec<u8>>,
    tools: Option<std::sync::Arc<dyn crate::tools::ToolFilter>>,
) -> Result<HttpReply> {
    let mut request = request_of(head)?;
    if body.is_some() {
        request.body = body;
    }
    let mut options = api_options_of(head.get("options").and_then(Value::as_object))?;
    options.tools = tools;
    let response = api::handle(engine, &options, &request);
    let (body, stream) = match response.body {
        Body::Empty => (None, None),
        Body::Bytes(bytes) => (Some(bytes), None),
        Body::Stream(stream) => (None, Some(engine.streams.hold(stream))),
    };
    Ok(HttpReply {
        status: response.status,
        headers: response.headers,
        body,
        stream,
    })
}

/// Pulls an event stream [`http`] or `stream.open` holds open, waiting up
/// to `timeout` for its next bytes: what `stream.next` pulls, as bytes.
pub fn stream_pull(engine: &Engine, id: u64, timeout: std::time::Duration) -> Result<api::Pull> {
    engine
        .streams
        .next(id, timeout)
        .ok_or_else(|| unknown_stream(id).into())
}

// request_of is the request `api.handle` takes as JSON: `method`, `path`,
// and optionally `prefix`, `query`, `headers` (an object, or pairs),
// `body` (its text), `caller` (`principal`, `service`, `refused`) and
// `requestId`.
fn request_of(given: &Value) -> Result<Request> {
    let refuse = |what: &str| -> Error {
        EngineError::invalid_argument(format!("api.handle takes a request whose {what}")).into()
    };
    let Value::Object(members) = given else {
        return Err(refuse("method and path name a route, as a JSON object"));
    };
    let string = |key: &str| members.get(key).and_then(Value::as_str).map(str::to_owned);
    let headers = match members.get("headers") {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Object(headers)) => headers
            .iter()
            .map(|(name, value)| (name.clone(), js_string(value)))
            .collect(),
        Some(Value::Array(pairs)) => pairs
            .iter()
            .map(|pair| match pair.as_array().map(Vec::as_slice) {
                Some([Value::String(name), Value::String(value)]) => {
                    Ok((name.clone(), value.clone()))
                }
                _ => Err(refuse("headers are an object or [name, value] pairs")),
            })
            .collect::<Result<Vec<_>>>()?,
        Some(_) => return Err(refuse("headers are an object or [name, value] pairs")),
    };
    let body = match members.get("body") {
        None | Some(Value::Null) => None,
        Some(Value::String(text)) => Some(text.clone().into_bytes()),
        Some(_) => return Err(refuse("body is its text")),
    };
    let empty = Map::new();
    let caller = members
        .get("caller")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let principal = match caller.get("principal") {
        None | Some(Value::Null) => None,
        Some(given) => Some(
            serde_json::from_value::<Principal>(given.clone())
                .map_err(|_| refuse("caller's principal is { subject, permissions, claims? }"))?,
        ),
    };
    let service = match caller.get("service") {
        None | Some(Value::Null) => None,
        Some(Value::Object(service)) => {
            let text = |key: &str| service.get(key).and_then(Value::as_str).map(str::to_owned);
            Some(ServiceCaller {
                deployable: text("deployable")
                    .ok_or_else(|| refuse("caller's service is { deployable, serves, subject }"))?,
                serves: service
                    .get("serves")
                    .and_then(Value::as_array)
                    .map(|serves| {
                        serves
                            .iter()
                            .filter_map(Value::as_str)
                            .map(str::to_owned)
                            .collect()
                    })
                    .unwrap_or_default(),
                subject: text("subject").unwrap_or_default(),
            })
        }
        Some(_) => {
            return Err(refuse(
                "caller's service is { deployable, serves, subject }",
            ))
        }
    };
    let refused = match caller.get("refused") {
        None | Some(Value::Null) => None,
        Some(Value::Object(refusal)) => Some(Refusal {
            status: refusal
                .get("status")
                .and_then(Value::as_u64)
                .and_then(|status| u16::try_from(status).ok())
                .ok_or_else(|| refuse("caller's refusal is { status, code, detail? }"))?,
            code: refusal
                .get("code")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            detail: refusal
                .get("detail")
                .and_then(Value::as_str)
                .map(str::to_owned),
        }),
        Some(_) => return Err(refuse("caller's refusal is { status, code, detail? }")),
    };
    Ok(Request {
        method: string("method").ok_or_else(|| refuse("method is a string"))?,
        prefix: string("prefix").unwrap_or_default(),
        path: string("path").ok_or_else(|| refuse("path is a string"))?,
        query: string("query").unwrap_or_default(),
        headers,
        body,
        caller: Caller {
            principal,
            service,
            refused,
        },
        request_id: string("requestId").unwrap_or_default(),
    })
}

// api_options_of is how `api.handle` serves: `stream` ({pageSize,
// heartbeatMs}) and `mcp` ({serverInfo: {name, version}, instructions}).
// A tool filter is a function, which JSON cannot carry.
fn api_options_of(options: Option<&Map<String, Value>>) -> Result<ApiOptions> {
    let member = |key: &str| {
        options
            .and_then(|options| options.get(key))
            .and_then(Value::as_object)
    };
    let mcp = member("mcp");
    let server = mcp
        .and_then(|mcp| mcp.get("serverInfo"))
        .and_then(Value::as_object);
    Ok(ApiOptions {
        stream: stream_options_of(member("stream"))?,
        mcp: McpOptions {
            server_name: text_member(server, "name"),
            server_version: text_member(server, "version"),
            instructions: text_member(mcp, "instructions"),
        },
        tools: None,
    })
}

fn stream_options_of(options: Option<&Map<String, Value>>) -> Result<StreamOptions> {
    let member = |key: &str| {
        options
            .and_then(|options| options.get(key))
            .filter(|value| !value.is_null())
    };
    let page_size = member("pageSize").map(|given| {
        integer(given).ok_or_else(|| {
            EngineError::invalid_argument(format!(
                "stream.pageSize must be an integer, got {}",
                js_string(given)
            ))
        })
    });
    let heartbeat_ms = member("heartbeatMs").map(|given| {
        integer(given)
            .and_then(|ms| u64::try_from(ms).ok())
            .ok_or_else(|| {
                EngineError::invalid_argument(format!(
                    "stream.heartbeatMs must be a positive integer, got {}",
                    js_string(given)
                ))
            })
    });
    let options = StreamOptions {
        page_size: page_size.transpose()?,
        heartbeat_ms: heartbeat_ms.transpose()?,
    };
    options.check().map_err(EngineError::invalid_argument)?;
    Ok(options)
}

// response_json is a response as JSON: its status, its headers as pairs,
// its body's text, and the id of the stream it holds open, if any.
fn response_json(engine: &Engine, response: api::Response) -> Value {
    let headers: Vec<Value> = response
        .headers
        .iter()
        .map(|(name, value)| serde_json::json!([name, value]))
        .collect();
    let (body, stream) = match response.body {
        Body::Empty => (Value::Null, Value::Null),
        Body::Bytes(bytes) => (
            Value::from(String::from_utf8_lossy(&bytes).into_owned()),
            Value::Null,
        ),
        Body::Stream(stream) => (Value::Null, Value::from(engine.streams.hold(stream))),
    };
    serde_json::json!({
        "status": response.status,
        "headers": headers,
        "body": body,
        "stream": stream,
    })
}

fn stream_id(given: &Value) -> Result<u64> {
    integer(given)
        .and_then(|id| u64::try_from(id).ok())
        .ok_or_else(|| {
            EngineError::invalid_argument(format!(
                "a stream is named by the id stream.open or api.handle gave, got {}",
                js_string(given)
            ))
            .into()
        })
}

fn unknown_stream(id: u64) -> EngineError {
    EngineError::invalid_argument(format!("no event stream {id} is open"))
}

fn to_json(value: impl serde::Serialize) -> Result<Value> {
    serde_json::to_value(value)
        .map_err(|error| Error::internal(format!("a result does not write as JSON: {error}")))
}

// options_of is a call's options: an object, or none.
fn options_of(given: Option<&Value>) -> Option<&Map<String, Value>> {
    given.and_then(Value::as_object)
}

fn text_member(options: Option<&Map<String, Value>>, key: &str) -> Option<String> {
    options
        .and_then(|options| options.get(key))
        .filter(|value| !value.is_null())
        .map(js_string)
}

fn flag_member(options: Option<&Map<String, Value>>, key: &str) -> bool {
    options
        .and_then(|options| options.get(key))
        .is_some_and(|value| value == &Value::Bool(true))
}

// text is an argument the TypeScript engine reads as a string, as
// JavaScript's String() writes it.
fn text(value: &Value) -> String {
    js_string(value)
}

// integer is a JSON number that is an integer (Number.isInteger), within
// the range a JavaScript number holds exactly; None for anything else.
fn integer(value: &Value) -> Option<i64> {
    let Value::Number(number) = value else {
        return None;
    };
    let read = json::number_value(number);
    (read.is_finite() && read.fract() == 0.0 && read.abs() <= 9_007_199_254_740_991.0)
        .then_some(read as i64)
}

/// A JSON value as JavaScript's `String()` writes it: a string as itself,
/// a number as `Number#toString`, an array's items joined with commas, an
/// object as `[object Object]`, and a value a binding marked as no JSON
/// as the binding wrote it (`NaN`, [`json::NOT_JSON_STRING`]).
pub fn js_string(value: &Value) -> String {
    if let Some(text) = json::not_json_string(value) {
        return text.to_owned();
    }
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(flag) => flag.to_string(),
        Value::Number(number) => json::js_number(json::number_value(number)),
        Value::String(text) => text.clone(),
        Value::Array(items) => items
            .iter()
            .map(|item| match item {
                Value::Null => String::new(),
                other => js_string(other),
            })
            .collect::<Vec<_>>()
            .join(","),
        Value::Object(_) => "[object Object]".to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn values_are_named_as_javascript_names_them() {
        assert_eq!(js_string(&json!(1.5)), "1.5");
        assert_eq!(js_string(&json!(2.0)), "2");
        assert_eq!(js_string(&json!("tail")), "tail");
        assert_eq!(js_string(&json!(["a", 1, null])), "a,1,");
        assert_eq!(js_string(&json!({})), "[object Object]");
        assert_eq!(
            js_string(&json!({
                json::NOT_JSON: "NaN is not a JSON number",
                json::NOT_JSON_STRING: "NaN",
            })),
            "NaN"
        );
        assert_eq!(integer(&json!(3.0)), Some(3));
        assert_eq!(integer(&json!(1.5)), None);
        assert_eq!(integer(&json!("3")), None);
    }
}
