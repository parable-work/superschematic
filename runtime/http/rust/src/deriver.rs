//! The loop of a stack's Rust deriver over tables (D146, section 8.6 of
//! docs/stack-model.md): the process that fills the Derived fields of
//! tables that implement Derived (D73, D103). The behaviors' core calls no
//! model. A deriver, holding the permission each Derived config names,
//! pulls the stale fields of its tables (`staleDerivations`), hands each to
//! the project's answer, which calls a model, and settles the answers
//! (`settleDerivations`) with the hash it pulled. It is the loop the
//! TypeScript runtime's `runDeriver` runs, and the engine's
//! `derive::run_deriver` runs over an engine's types (D74), over a table's
//! repository.
//!
//! The deriver's generated `main.rs` gives [`run_deriver`] its tables by
//! name and `call`, which runs one of a table's Derived operations through
//! the database's repositories, opened as the deriver in a transaction of
//! its pool. The module knows no table: the loop is the same for every
//! one.
//!
//! It pages each table until nothing is stale, answering a page's items
//! `concurrency` at a time, and settles each page in one call. An answer
//! that panics settles as a failure, so Derived counts the attempt, and so
//! does an answer of no shape a settle takes (a confidence outside 0 to 1):
//! the loop holds each answer to the settle's parameters before it sends
//! the page, so one bad answer costs its item an attempt and never refuses
//! the page. When a pass settles and fails nothing it waits `idle` and
//! pulls again: a table has no commit to wake it, and an item the core
//! skips without counting an attempt (another behavior's guard holds it)
//! would otherwise be pulled and answered back to back.
//!
//! A pull or a settle that fails for a reason time cures (the database is
//! down, a table its migration has not made yet, a command the core refuses
//! as busy) is logged and its table waited out, a second, doubling to
//! thirty, so a deriver may start before its database does. The other
//! tables go on meanwhile. A failure time does not cure, a refusal of the
//! core's other than busy (a caller without the config's permission, among
//! them) or a result of no shape the loop reads, ends the loop with the
//! table's name ([`DeriverFailure`]). The loop returns once `shutdown`
//! resolves, dropping the answers it was waiting for, and settles nothing
//! after that.
//!
//! ```ignore
//! let call = move |table: String, operation: &'static str, params: Value| {
//!     let pool = pool.clone();
//!     async move { derived(&pool, &table, operation, &params).await }
//! };
//! deriver::run_deriver(vec!["Ticket".into()], call, move |_, item| answer(item),
//!     DeriverOptions::default(), shutdown).await?;
//! ```

use std::collections::HashMap;
use std::future::Future;
use std::pin::pin;
use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use serde_json::{json, Map, Value};
use tokio::sync::Semaphore;
use tokio::task::JoinSet;
use tokio::time::Instant;

use crate::orm;

/// The longest message a failure records, the longest class, and the
/// longest model id a usage names, as `settleDerivations` takes them.
const MAX_FAILURE: usize = 2000;
const MAX_FAILURE_CLASS: usize = 64;
const MAX_MODEL_ID: usize = 200;

/// The most stale fields a pull may ask for, and a settle take.
pub const MAX_BATCH_SIZE: usize = 100;

/// One stale derived field, as `staleDerivations` lists it.
#[derive(Clone, Debug, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct DerivationItem {
    /// The row's key.
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
    #[serde(default)]
    pub inputs: Map<String, Value>,
    /// The derived field's JSON Schema: what the value must be.
    #[serde(default)]
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

    /// The settle's item for the answer to `of`, held to what
    /// `settleDerivations` takes: a failure keeps its message and class,
    /// cut to their lengths; a value keeps a confidence from 0 to 1 and
    /// what of its usage is well formed; and a confidence outside 0 to 1 is
    /// the item's failure.
    fn settlement(&self, of: &DerivationItem) -> Value {
        let mut out = Map::new();
        out.insert("id".into(), Value::from(of.id.as_str()));
        out.insert("field".into(), Value::from(of.field.as_str()));
        out.insert("sourceHash".into(), Value::from(of.source_hash.as_str()));
        let failure = |out: &mut Map<String, Value>, class: Option<&str>, message: &str| {
            let mut failure = Map::new();
            if let Some(class) = class {
                failure.insert("class".into(), Value::from(cut(class, MAX_FAILURE_CLASS)));
            }
            failure.insert("message".into(), Value::from(cut(message, MAX_FAILURE)));
            out.insert("failure".into(), Value::Object(failure));
        };
        match self {
            DerivationAnswer::Value {
                value,
                confidence,
                usage,
            } => {
                if let Some(confidence) = confidence {
                    if !(0.0..=1.0).contains(confidence) {
                        let message = format!(
                            "the answer's confidence, {confidence}, is no number from 0 to 1"
                        );
                        failure(&mut out, Some("invalid_answer"), &message);
                        return Value::Object(out);
                    }
                    out.insert("confidence".into(), json!(confidence));
                }
                out.insert("value".into(), value.clone());
                if let Some(usage) = usage.as_ref().and_then(kept_usage) {
                    out.insert("usage".into(), usage);
                }
            }
            DerivationAnswer::Failure { class, message } => {
                failure(&mut out, class.as_deref(), message);
            }
        }
        Value::Object(out)
    }
}

/// What of a usage is kept for reports: each count a whole number of zero
/// or more, and the model id, cut to its length; none when nothing is.
fn kept_usage(usage: &Value) -> Option<Value> {
    let usage = usage.as_object()?;
    let mut kept = Map::new();
    for member in ["inputTokens", "outputTokens", "costMicros"] {
        if let Some(count) = usage.get(member).and_then(Value::as_f64) {
            if count.is_finite() && count >= 0.0 {
                // A count is a whole number of tokens or micros: rounding
                // to an integer is the point.
                #[allow(clippy::cast_possible_truncation, clippy::cast_sign_loss)]
                let count = count.round() as u64;
                kept.insert(member.into(), Value::from(count));
            }
        }
    }
    if let Some(model) = usage.get("modelId").and_then(Value::as_str) {
        kept.insert("modelId".into(), Value::from(cut(model, MAX_MODEL_ID)));
    }
    (!kept.is_empty()).then_some(Value::Object(kept))
}

/// `text` at most `n` characters long.
fn cut(text: &str, n: usize) -> String {
    text.chars().take(n).collect()
}

/// How [`run_deriver`] runs. A zero in any of them is its default.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeriverOptions {
    /// How many items a pull asks for: at most 100, 50 by default.
    pub batch_size: usize,
    /// How many answers run at a time: 4 by default.
    pub concurrency: usize,
    /// How long a pass that settled and failed nothing waits before it
    /// pulls again: five seconds by default.
    pub idle: Duration,
    /// The first wait of a table whose pull or settle failed, which
    /// doubles to `retry_max`: a second, and thirty, by default.
    pub retry_min: Duration,
    pub retry_max: Duration,
}

impl Default for DeriverOptions {
    fn default() -> Self {
        DeriverOptions {
            batch_size: 50,
            concurrency: 4,
            idle: Duration::from_secs(5),
            retry_min: Duration::from_secs(1),
            retry_max: Duration::from_secs(30),
        }
    }
}

impl DeriverOptions {
    /// The options with each zero its default.
    fn normalized(self) -> Self {
        let defaults = DeriverOptions::default();
        let or = |value: Duration, default: Duration| {
            if value.is_zero() {
                default
            } else {
                value
            }
        };
        let retry_min = or(self.retry_min, defaults.retry_min);
        DeriverOptions {
            batch_size: match self.batch_size {
                0 => defaults.batch_size,
                size => size.min(MAX_BATCH_SIZE),
            },
            concurrency: match self.concurrency {
                0 => defaults.concurrency,
                count => count,
            },
            idle: or(self.idle, defaults.idle),
            retry_min,
            retry_max: or(self.retry_max, defaults.retry_max).max(retry_min),
        }
    }
}

/// A failure of a pull or a settle that time does not cure, which ended
/// [`run_deriver`], with the table it failed on.
#[derive(Debug)]
pub struct DeriverFailure {
    pub table: String,
    pub error: orm::Error,
}

impl std::fmt::Display for DeriverFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "deriver: table {}: {}", self.table, self.error)
    }
}

impl std::error::Error for DeriverFailure {}

/// Whether a failure of a pull or a settle is one time cures: the
/// database's, a refusal of the core's as busy, a write fenced on a
/// version that moved, or a row the database refused while another write
/// holds what it needs. Any other refusal of the core's, and a result or
/// a parameter of no shape, is not.
fn retryable(error: &orm::Error) -> bool {
    match error {
        orm::Error::Refused(refusal) => refusal.code == "busy",
        orm::Error::Busy(_)
        | orm::Error::Database(_)
        | orm::Error::VersionConflict(_)
        | orm::Error::Constraint(_) => true,
        _ => false,
    }
}

/// Runs a deriver over `tables` until `shutdown` resolves. `call` runs one
/// of a table's Derived operations, `staleDerivations` or
/// `settleDerivations`, with its parameters, as the deriver, and `answer`
/// is the project's model call for one item of a table.
///
/// # Errors
///
/// A [`DeriverFailure`] for a failure of a pull or a settle that time does
/// not cure.
pub async fn run_deriver<C, CF, A, AF>(
    tables: Vec<String>,
    call: C,
    answer: A,
    options: DeriverOptions,
    shutdown: impl Future<Output = ()>,
) -> Result<(), DeriverFailure>
where
    C: Fn(String, &'static str, Value) -> CF + Send + Sync + 'static,
    CF: Future<Output = Result<Value, orm::Error>> + Send + 'static,
    A: Fn(String, DerivationItem) -> AF + Send + Sync + 'static,
    AF: Future<Output = DerivationAnswer> + Send + 'static,
{
    let options = options.normalized();
    let answer = Arc::new(answer);
    let mut shutdown = pin!(shutdown);
    // Each table that failed, with its wait and when it is tried again.
    let mut waiting: HashMap<String, (Duration, Instant)> = HashMap::new();
    loop {
        let mut progressed = false;
        for table in &tables {
            let held = waiting.get(table).copied();
            if held.is_some_and(|(_, at)| at > Instant::now()) {
                continue;
            }
            let pass = derive(table, &call, &answer, &options);
            let (did, failed) = tokio::select! {
                derived = pass => derived,
                () = &mut shutdown => return Ok(()),
            };
            progressed |= did;
            let Some(error) = failed else {
                waiting.remove(table);
                continue;
            };
            if !retryable(&error) {
                return Err(DeriverFailure {
                    table: table.clone(),
                    error,
                });
            }
            let backoff = held.map_or(options.retry_min, |(backoff, _)| {
                (backoff * 2).min(options.retry_max)
            });
            waiting.insert(table.clone(), (backoff, Instant::now() + backoff));
            let wait_ms = u64::try_from(backoff.as_millis()).unwrap_or(u64::MAX);
            tracing::warn!(table = %table, error = %error, wait_ms, "deriver waits");
        }
        if progressed {
            // Stop between passes when asked, without waiting.
            tokio::select! {
                biased;
                () = &mut shutdown => return Ok(()),
                () = std::future::ready(()) => {}
            }
            continue;
        }
        // Until the idle wait is over, or a table that failed is due again.
        let now = Instant::now();
        let wait = waiting
            .values()
            .map(|(_, at)| at.saturating_duration_since(now))
            .fold(options.idle, Duration::min);
        tokio::select! {
            () = tokio::time::sleep(wait) => {}
            () = &mut shutdown => return Ok(()),
        }
    }
}

/// Pages one table's stale fields, answers and settles each page, and says
/// whether a settle wrote a value or counted an attempt, with the failure
/// that stopped it: a pass that only met items the core skips made no
/// progress.
async fn derive<C, CF, A, AF>(
    table: &str,
    call: &C,
    answer: &Arc<A>,
    options: &DeriverOptions,
) -> (bool, Option<orm::Error>)
where
    C: Fn(String, &'static str, Value) -> CF + Send + Sync + 'static,
    CF: Future<Output = Result<Value, orm::Error>> + Send + 'static,
    A: Fn(String, DerivationItem) -> AF + Send + Sync + 'static,
    AF: Future<Output = DerivationAnswer> + Send + 'static,
{
    let mut cursor: Option<String> = None;
    let mut progressed = false;
    loop {
        let mut params = json!({ "limit": options.batch_size });
        if let Some(cursor) = &cursor {
            params["cursor"] = Value::from(cursor.as_str());
        }
        let page = match call(table.to_owned(), "staleDerivations", params).await {
            Ok(page) => page,
            Err(error) => return (progressed, Some(error)),
        };
        let items: Vec<DerivationItem> = match serde_json::from_value(page["items"].clone()) {
            Ok(items) => items,
            Err(error) => {
                let error =
                    orm::Error::Definition(format!("the stale derivations of {table}: {error}"));
                return (progressed, Some(error));
            }
        };
        cursor = page["next"].as_str().map(str::to_owned);
        if !items.is_empty() {
            let answers = answer_all(table, &items, answer, options.concurrency).await;
            let settlements: Vec<Value> = items
                .iter()
                .zip(&answers)
                .map(|(item, answer)| answer.settlement(item))
                .collect();
            let params = json!({ "items": settlements });
            let result = match call(table.to_owned(), "settleDerivations", params).await {
                Ok(result) => result,
                Err(error) => return (progressed, Some(error)),
            };
            let settled = result["settled"].as_i64().unwrap_or(0);
            let failed = result["failed"].as_i64().unwrap_or(0);
            let skipped = result["skipped"].as_array().map_or(0, Vec::len);
            progressed = progressed || settled > 0 || failed > 0;
            if settled > 0 || failed > 0 || skipped > 0 {
                tracing::info!(table, settled, failed, skipped, "derivations settled");
            }
        }
        if cursor.is_none() {
            return (progressed, None);
        }
    }
}

/// Answers items, `concurrency` at a time, in their order; a panic is the
/// item's failure.
async fn answer_all<A, AF>(
    table: &str,
    items: &[DerivationItem],
    answer: &Arc<A>,
    concurrency: usize,
) -> Vec<DerivationAnswer>
where
    A: Fn(String, DerivationItem) -> AF + Send + Sync + 'static,
    AF: Future<Output = DerivationAnswer> + Send + 'static,
{
    let slots = Arc::new(Semaphore::new(concurrency));
    let mut running = JoinSet::new();
    for (index, item) in items.iter().cloned().enumerate() {
        let (slots, answer, table) = (Arc::clone(&slots), Arc::clone(answer), table.to_owned());
        running.spawn(async move {
            let _slot = slots.acquire_owned().await;
            (index, answer(table, item).await)
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

#[cfg(test)]
mod tests;
