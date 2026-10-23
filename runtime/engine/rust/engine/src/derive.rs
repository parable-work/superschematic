//! A deriver: the loop that fills Derived's fields (D73, D74), the twin of
//! the TypeScript engine's `runDeriver` (the `background` feature). The
//! engine calls no model. A deriver, as a principal holding the permission
//! each Derived config names, pulls the stale fields of its schemas
//! (`staleDerivations`), hands each to the project's answer, which calls a
//! model, and settles the answers (`settleDerivations`) with the hash it
//! pulled.
//!
//! It waits for its [`Background`] engine to open, then pages each schema
//! until nothing is stale, answering a page's items `concurrency` at a time
//! on the host's runtime and calling the engine on the blocking pool, since
//! the engine is synchronous. A page settles in one call. When a pass finds
//! nothing it waits on the storage's notifier, which a commit in any
//! process wakes on Postgres, for at most `idle`. An answer that panics
//! settles as a failure, so Derived counts the attempt. A pass that meets
//! a refusal time cures (a schema not yet published, a busy or unavailable
//! engine) waits a second, doubling to thirty, and tries again, so a
//! deriver may start before its server deploys the document. The loop
//! returns once `shutdown` resolves, dropping the answers it was waiting
//! for, or with any other error of a pull or a settle.
//!
//! ```ignore
//! let principal = Principal::new("shop-cart-derive-tickets", ["tickets.derive"]);
//! derive::run_deriver(deps.engine.clone(), principal, vec!["Ticket".into()],
//!     move |_, item| answer(item), DeriverOptions::default(), shutdown).await?;
//! ```

use std::future::Future;
use std::pin::pin;
use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use serde_json::{json, Map, Value};
use tokio::sync::Semaphore;
use tokio::task::JoinSet;

use crate::background::Background;
use crate::{Engine, EngineErrorCode, Error, Principal, Result};

/// One stale derived field, as `staleDerivations` lists it.
#[derive(Clone, Debug, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct DerivationItem {
    pub id: String,
    pub field: String,
    /// The hash a settle presents.
    pub source_hash: String,
    /// Which attempt this is, from 1.
    pub attempt: i64,
    pub instruction: String,
    /// The config's model label, which the project maps to a provider's
    /// model.
    pub model: String,
    /// The inputs that hold a value, by field name.
    pub inputs: Map<String, Value>,
    /// The derived field's JSON Schema: what the value must be.
    pub output: Value,
}

/// What the project's answer gives for an item.
#[derive(Clone, Debug, PartialEq)]
pub enum DerivationAnswer {
    /// A value, with how sure the model is (0 to 1) and what the call
    /// cost, which the settle's event keeps.
    Value {
        value: Value,
        confidence: Option<f64>,
        usage: Option<Value>,
    },
    /// Why there is no value; Derived counts it as an attempt.
    Failure {
        class: Option<String>,
        message: String,
    },
}

impl DerivationAnswer {
    /// A value with no confidence or usage.
    pub fn value(value: impl Into<Value>) -> Self {
        DerivationAnswer::Value {
            value: value.into(),
            confidence: None,
            usage: None,
        }
    }

    /// A failure of a class.
    pub fn failure(class: impl Into<String>, message: impl Into<String>) -> Self {
        DerivationAnswer::Failure {
            class: Some(class.into()),
            message: message.into(),
        }
    }

    // item is the settle's item for the answer.
    fn item(&self, of: &DerivationItem) -> Value {
        let mut out = Map::new();
        out.insert("id".into(), Value::from(of.id.as_str()));
        out.insert("field".into(), Value::from(of.field.as_str()));
        out.insert("sourceHash".into(), Value::from(of.source_hash.as_str()));
        match self {
            DerivationAnswer::Value {
                value,
                confidence,
                usage,
            } => {
                out.insert("value".into(), value.clone());
                if let Some(confidence) = confidence {
                    out.insert("confidence".into(), json!(confidence));
                }
                if let Some(usage) = usage {
                    out.insert("usage".into(), usage.clone());
                }
            }
            DerivationAnswer::Failure { class, message } => {
                let mut failure = Map::new();
                if let Some(class) = class {
                    failure.insert("class".into(), Value::from(class.as_str()));
                }
                let message: String = message.chars().take(2000).collect();
                failure.insert("message".into(), Value::from(message));
                out.insert("failure".into(), Value::Object(failure));
            }
        }
        Value::Object(out)
    }
}

/// How [`run_deriver`] runs. A zero in any of them is its default.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeriverOptions {
    /// The namespace its schemas are in; the engine's default when `None`.
    pub namespace: Option<String>,
    /// How many items a pull asks for: at most 100, 50 by default.
    pub batch_size: usize,
    /// How many answers run at a time: 4 by default.
    pub concurrency: usize,
    /// How long a pass that found nothing waits for a commit: five seconds
    /// by default.
    pub idle: Duration,
}

impl Default for DeriverOptions {
    fn default() -> Self {
        DeriverOptions {
            namespace: None,
            batch_size: 50,
            concurrency: 4,
            idle: Duration::from_secs(5),
        }
    }
}

/// Runs a deriver over a background engine until `shutdown` resolves.
pub async fn run_deriver<A, F>(
    engine: Background,
    principal: Principal,
    schemas: Vec<String>,
    answer: A,
    options: DeriverOptions,
    shutdown: impl Future<Output = ()>,
) -> Result<()>
where
    A: Fn(String, DerivationItem) -> F + Send + Sync + 'static,
    F: Future<Output = DerivationAnswer> + Send + 'static,
{
    let defaults = DeriverOptions::default();
    let options = DeriverOptions {
        batch_size: match options.batch_size {
            0 => defaults.batch_size,
            size => size.min(100),
        },
        concurrency: match options.concurrency {
            0 => defaults.concurrency,
            count => count,
        },
        idle: if options.idle.is_zero() {
            defaults.idle
        } else {
            options.idle
        },
        namespace: options.namespace,
    };
    let answer = Arc::new(answer);
    let principal = Arc::new(principal);
    let mut shutdown = pin!(shutdown);
    let engine = tokio::select! {
        opened = engine.wait() => opened.map_err(|not_open| Error::Internal(not_open.to_string()))?,
        () = &mut shutdown => return Ok(()),
    };
    let mut backoff = RETRY_MIN;
    loop {
        let epoch = engine.events().epoch();
        let pass = derive_pass(&engine, &principal, &schemas, &answer, &options);
        let found = tokio::select! {
            found = pass => found,
            () = &mut shutdown => return Ok(()),
        };
        let found = match found {
            Ok(found) => {
                backoff = RETRY_MIN;
                found
            }
            // A refusal time cures: the schema not published yet, a busy or
            // unavailable engine. Wait, then try again.
            Err(error) if retryable(&error) => {
                tokio::select! {
                    () = tokio::time::sleep(backoff) => {}
                    () = &mut shutdown => return Ok(()),
                }
                backoff = (backoff * 2).min(RETRY_MAX);
                continue;
            }
            Err(error) => return Err(error),
        };
        if found {
            // Stop between passes when asked, without waiting.
            tokio::select! {
                biased;
                () = &mut shutdown => return Ok(()),
                () = std::future::ready(()) => {}
            }
            continue;
        }
        let waiting = Arc::clone(&engine);
        let idle = options.idle;
        let wait = tokio::task::spawn_blocking(move || waiting.events().wait(epoch, idle));
        tokio::select! {
            _ = wait => {}
            () = &mut shutdown => return Ok(()),
        }
    }
}

// The waits after a pass a refusal time cures: a second, doubling to
// thirty.
const RETRY_MIN: Duration = Duration::from_secs(1);
const RETRY_MAX: Duration = Duration::from_secs(30);

// retryable is whether a refusal may pass once time does: a schema not yet
// published (not_found), and a busy or unavailable engine.
fn retryable(error: &Error) -> bool {
    matches!(
        error.code(),
        Some(EngineErrorCode::NotFound | EngineErrorCode::Unavailable | EngineErrorCode::Busy)
    )
}

// derive_pass pages each schema until nothing is stale, answering and
// settling each page, and says whether it found any.
async fn derive_pass<A, F>(
    engine: &Arc<Engine>,
    principal: &Arc<Principal>,
    schemas: &[String],
    answer: &Arc<A>,
    options: &DeriverOptions,
) -> Result<bool>
where
    A: Fn(String, DerivationItem) -> F + Send + Sync + 'static,
    F: Future<Output = DerivationAnswer> + Send + 'static,
{
    let mut found = false;
    for schema in schemas {
        let mut cursor: Option<String> = None;
        loop {
            let mut params = json!({ "limit": options.batch_size });
            if let Some(cursor) = &cursor {
                params["cursor"] = Value::from(cursor.as_str());
            }
            let page = call(
                engine,
                principal,
                options,
                schema,
                "staleDerivations",
                params,
            )
            .await?;
            let items: Vec<DerivationItem> = serde_json::from_value(page["items"].clone())
                .map_err(|error| {
                    Error::Internal(format!("staleDerivations of {schema}: {error}"))
                })?;
            cursor = page["next"].as_str().map(str::to_owned);
            if !items.is_empty() {
                found = true;
                let answers = answer_all(schema, &items, answer, options.concurrency).await;
                let settled: Vec<Value> = items
                    .iter()
                    .zip(&answers)
                    .map(|(item, answer)| answer.item(item))
                    .collect();
                call(
                    engine,
                    principal,
                    options,
                    schema,
                    "settleDerivations",
                    json!({ "items": settled }),
                )
                .await?;
            }
            if cursor.is_none() {
                break;
            }
        }
    }
    Ok(found)
}

// call runs a schema-level operation on the blocking pool.
async fn call(
    engine: &Arc<Engine>,
    principal: &Arc<Principal>,
    options: &DeriverOptions,
    schema: &str,
    operation: &'static str,
    params: Value,
) -> Result<Value> {
    let (engine, principal) = (Arc::clone(engine), Arc::clone(principal));
    let (schema, namespace) = (schema.to_owned(), options.namespace.clone());
    tokio::task::spawn_blocking(move || {
        engine.instances().invoke_schema(
            &principal,
            &schema,
            operation,
            &params,
            namespace.as_deref(),
        )
    })
    .await
    .map_err(|joined| Error::Internal(format!("{operation} did not finish: {joined}")))?
}

// answer_all answers items, concurrency at a time, in their order; a panic
// is the item's failure.
async fn answer_all<A, F>(
    schema: &str,
    items: &[DerivationItem],
    answer: &Arc<A>,
    concurrency: usize,
) -> Vec<DerivationAnswer>
where
    A: Fn(String, DerivationItem) -> F + Send + Sync + 'static,
    F: Future<Output = DerivationAnswer> + Send + 'static,
{
    let slots = Arc::new(Semaphore::new(concurrency));
    let mut running = JoinSet::new();
    for (index, item) in items.iter().cloned().enumerate() {
        let (slots, answer, schema) = (Arc::clone(&slots), Arc::clone(answer), schema.to_owned());
        running.spawn(async move {
            let _slot = slots.acquire_owned().await;
            (index, answer(schema, item).await)
        });
    }
    let mut out: Vec<Option<DerivationAnswer>> = vec![None; items.len()];
    while let Some(joined) = running.join_next().await {
        if let Ok((index, answer)) = joined {
            out[index] = Some(answer);
        }
    }
    out.into_iter()
        .map(|answer| {
            answer.unwrap_or_else(|| DerivationAnswer::failure("panicked", "the answer panicked"))
        })
        .collect()
}
