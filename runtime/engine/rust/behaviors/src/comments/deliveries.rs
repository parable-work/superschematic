//! Comments' deliveries across a namespace (D77 step 3,
//! `comments-contract.md`, "claimDeliveries", "settleDeliveries", "The
//! schedule prune"): `claimDeliveries` claims the due deliveries of the
//! schemas whose `deliverPermission` the caller holds under one token,
//! dropping those whose message no longer stands or whose subject has read
//! it; `settleDeliveries` marks what the claimer did with them, backing a
//! failure off until `maxAttempts`; and the schedule `prune` removes those
//! past `deliveryRetentionMs`. Both operations are namespace operations
//! that write, under the namespace's writer, and append no event.

use std::collections::{BTreeSet, HashMap};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::paging::{base64url_decode, base64url_encode};
use superschematic_engine::behaviors::{NamespaceCx, ScheduleCx};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{EngineError, EngineErrorCode, Error, Result};

use super::checks::{integer, invalid};
use super::storage::{placeholders, DELIVERIES, FOLLOWS, MESSAGES, READS};
use super::{CommentsConfig, NAME};

type Ncx<'a, 'b> = &'a NamespaceCx<'b, CommentsConfig>;

/// A claim's size when `limit` is absent.
const CLAIM_LIMIT: i64 = 20;
/// How long a claim holds when `ttlMs` is absent.
const CLAIM_TTL_MS: i64 = 60_000;
/// How many due deliveries a claim reads at a time.
const CLAIM_PAGE: i64 = 100;
/// The most due deliveries one claim reads.
const CLAIM_READ: i64 = 1000;
/// The longest body a claimed delivery carries, in code points.
const BODY: usize = 4000;
/// The longest backoff, in milliseconds.
const MAX_BACKOFF_MS: i64 = 3_600_000;
/// How many deliveries `prune` reads and removes at a time.
const PRUNE: i64 = 1000;
/// The most batches of [`PRUNE`] one run of `prune` removes: 20000
/// deliveries a run, 480000 a day at the hourly schedule.
const PRUNE_BATCHES: usize = 20;

/// A veto of a namespace operation: it names the namespace in place of an
/// instance.
fn veto(cx: Ncx<'_, '_>, operation: &str, code: &str, reason: String, details: Value) -> Error {
    Error::veto(
        NAME,
        operation,
        "namespace",
        cx.namespace(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: match details {
                Value::Object(map) => Some(map),
                _ => None,
            },
        },
    )
}

/// The schemas of the call whose deliveries the caller may claim and
/// settle, each with its config: the schemas it may read whose config
/// names a `deliverPermission` it holds. `not_configured` when none it may
/// read names one, `forbidden` when it holds none of those named.
fn deliverable<'a>(
    cx: &NamespaceCx<'a, CommentsConfig>,
    operation: &str,
) -> Result<HashMap<String, &'a CommentsConfig>> {
    let readable = cx.sql().readable()?;
    let schemas: Vec<String> = cx
        .sql()
        .all(
            format!("SELECT schema FROM {readable} ORDER BY schema"),
            &[],
        )?
        .iter()
        .map(|row| Ok(row.text("schema")?.to_owned()))
        .collect::<Result<_>>()?;
    let mut named: Vec<(String, &'a CommentsConfig, &'a str)> = Vec::new();
    for schema in schemas {
        if let Some(config) = cx.config(&schema)? {
            if let Some(permission) = config.deliver_permission.as_deref() {
                named.push((schema, config, permission));
            }
        }
    }
    if named.is_empty() {
        return Err(veto(
            cx,
            operation,
            "not_configured",
            format!(
                "no schema of namespace {} that {} may read names a deliverPermission for Comments, so nobody may {operation} there",
                cx.namespace(),
                cx.principal().subject
            ),
            json!({ "permission": "deliverPermission" }),
        ));
    }
    let permissions: BTreeSet<&str> = named.iter().map(|(_, _, permission)| *permission).collect();
    let mut granted: HashMap<&str, bool> = HashMap::new();
    for permission in &permissions {
        granted.insert(permission, cx.can(permission)?);
    }
    let out: HashMap<String, &'a CommentsConfig> = named
        .into_iter()
        .filter(|(_, _, permission)| granted.get(permission).copied().unwrap_or(false))
        .map(|(schema, config, _)| (schema, config))
        .collect();
    if out.is_empty() {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {operation} in namespace {}: it needs permission {}",
                cx.principal().subject,
                cx.namespace(),
                permissions.into_iter().collect::<Vec<_>>().join(", ")
            ),
        )
        .into());
    }
    Ok(out)
}

// A delivery's key within the namespace.
#[derive(Clone, Debug, PartialEq, Eq)]
struct DeliveryKey {
    schema: String,
    instance: String,
    message: i64,
    subject: String,
}

impl DeliveryKey {
    // id is the delivery's id: its key as a compact JSON array, in
    // unpadded base64url.
    fn id(&self) -> String {
        let written = serde_json::to_string(&json!([
            self.schema,
            self.instance,
            self.message,
            self.subject
        ]))
        .unwrap_or_default();
        base64url_encode(written.as_bytes())
    }

    // decode reads an id back: an array of a string, a string, an integer
    // >= 1 and a string, or nothing.
    fn decode(id: &str) -> Option<DeliveryKey> {
        let bytes = base64url_decode(id);
        let value: Value = serde_json::from_str(std::str::from_utf8(&bytes).ok()?).ok()?;
        let [schema, instance, message, subject] = value.as_array()?.as_slice() else {
            return None;
        };
        Some(DeliveryKey {
            schema: schema.as_str()?.to_owned(),
            instance: instance.as_str()?.to_owned(),
            message: message.as_i64().filter(|message| *message >= 1)?,
            subject: subject.as_str()?.to_owned(),
        })
    }

    fn of(row: &Row) -> Result<DeliveryKey> {
        Ok(DeliveryKey {
            schema: row.text("schema")?.to_owned(),
            instance: row.text("id")?.to_owned(),
            message: row.i64("message")?,
            subject: row.text("subject")?.to_owned(),
        })
    }

    // values are the key's values after the namespace, as a statement
    // binds them.
    fn values(&self, namespace: &str) -> Vec<SqlValue> {
        vec![
            SqlValue::from(namespace),
            SqlValue::from(self.schema.as_str()),
            SqlValue::from(self.instance.as_str()),
            SqlValue::Integer(self.message),
            SqlValue::from(self.subject.as_str()),
        ]
    }
}

const KEYED: &str = "namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?";

/// The reasons of a delivery, from its two columns.
fn reasons(mention: bool, reply: bool) -> Value {
    let mut out = Vec::new();
    if mention {
        out.push(Value::from("mention"));
    }
    if reply {
        out.push(Value::from("reply"));
    }
    Value::Array(out)
}

pub(super) fn claim_deliveries(cx: Ncx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "claimDeliveries";
    let subjects: Option<Vec<String>> =
        params
            .get("subjects")
            .and_then(Value::as_array)
            .map(|list| {
                list.iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            });
    let prefix = params.get("prefix").and_then(Value::as_str);
    if subjects.is_some() && prefix.is_some() {
        return Err(invalid(OP, "/prefix", "must not be given with subjects"));
    }
    let configs = deliverable(cx, OP)?;
    let limit = integer(params.get("limit")).unwrap_or(CLAIM_LIMIT);
    let ttl = integer(params.get("ttlMs")).unwrap_or(CLAIM_TTL_MS);
    let now = cx.now();
    let token = uuid::Uuid::from_bytes(cx.uuids().next_bytes())
        .hyphenated()
        .to_string();
    let expires_at = now + ttl;
    let claimer = cx.principal().subject.as_str();
    let namespace = cx.namespace();

    let sql = cx.sql();
    let (deliveries, messages, reads, follows) = (
        sql.table(DELIVERIES)?,
        sql.table(MESSAGES)?,
        sql.table(READS)?,
        sql.table(FOLLOWS)?,
    );
    let mut schemas: Vec<&str> = configs.keys().map(String::as_str).collect();
    schemas.sort_unstable();
    let mut filter = format!(" AND d.schema IN ({})", placeholders(schemas.len()));
    let mut fixed: Vec<SqlValue> = vec![SqlValue::from(namespace), SqlValue::Integer(now)];
    fixed.extend(schemas.iter().map(|schema| SqlValue::from(*schema)));
    if let Some(subjects) = &subjects {
        filter.push_str(&format!(
            " AND d.subject IN ({})",
            placeholders(subjects.len())
        ));
        fixed.extend(
            subjects
                .iter()
                .map(|subject| SqlValue::from(subject.as_str())),
        );
    }
    if let Some(prefix) = prefix {
        filter.push_str(" AND substr(d.subject, 1, ?) = ?");
        fixed.push(SqlValue::Integer(prefix.chars().count() as i64));
        fixed.push(SqlValue::from(prefix));
    }

    let mut items: Vec<Value> = Vec::new();
    let mut read = 0;
    let mut past: Option<(i64, DeliveryKey)> = None;
    'pages: while (items.len() as i64) < limit && read < CLAIM_READ {
        let page = CLAIM_PAGE.min(CLAIM_READ - read);
        let mut params = fixed.clone();
        let keyset = match &past {
            Some((due_at, key)) => {
                params.push(SqlValue::Integer(*due_at));
                params.extend(key.values(namespace).into_iter().skip(1));
                " AND (d.due_at, d.schema, d.id, d.message, d.subject) > (?, ?, ?, ?, ?)"
            }
            None => "",
        };
        params.push(SqlValue::Integer(page));
        let rows = sql.all(
            format!(
                "SELECT d.schema, d.id, d.message, d.subject, d.mention, d.reply, d.state, d.attempts, d.due_at,
                        m.thread, m.author, m.via, m.state AS message_state, m.posted_at, m.stand, m.body,
                        CASE WHEN m.thread IS NULL THEN r.up_to ELSE f.read_up_to END AS marker
                   FROM {deliveries} d
                   LEFT JOIN {messages} m
                     ON m.namespace = d.namespace AND m.schema = d.schema AND m.id = d.id AND m.message = d.message
                   LEFT JOIN {reads} r
                     ON r.namespace = d.namespace AND r.schema = d.schema AND r.id = d.id AND r.subject = d.subject
                   LEFT JOIN {follows} f
                     ON f.namespace = d.namespace AND f.schema = d.schema AND f.id = d.id
                    AND f.thread = m.thread AND f.subject = d.subject
                  WHERE d.namespace = ? AND d.due_at IS NOT NULL AND d.due_at <= ?{filter}{keyset}
                  ORDER BY d.due_at, d.schema, d.id, d.message, d.subject
                  LIMIT ?"
            ),
            &params,
        )?;
        read += rows.len() as i64;
        for row in &rows {
            let key = DeliveryKey::of(row)?;
            let due_at = row.i64("due_at")?;
            past = Some((due_at, key.clone()));
            let message_state = row.opt_text("message_state")?;
            let stand = row.opt_i64("stand")?;
            let marker = row.opt_i64("marker")?;
            let state = row.text("state")?;
            let attempts = row.i64("attempts")?;
            let max_attempts = configs
                .get(&key.schema)
                .map_or(super::DEFAULT_MAX_ATTEMPTS, |config| config.max_attempts);
            let dropped = if !matches!(message_state, Some("posted" | "interrupted")) {
                Some("message_gone")
            } else if matches!((marker, stand), (Some(marker), Some(stand)) if marker >= stand) {
                Some("read")
            } else if state == "claimed" && attempts >= max_attempts {
                Some("claim_lapsed")
            } else {
                None
            };
            let mut values = Vec::new();
            let statement = match dropped {
                Some(error) => {
                    values.extend([SqlValue::from(error), SqlValue::Integer(now)]);
                    format!(
                        "UPDATE {deliveries} SET state = 'dropped', error = ?, due_at = NULL, token = NULL, settled_at = ?
                         WHERE {KEYED} AND due_at = ?"
                    )
                }
                None => {
                    values.extend([
                        SqlValue::from(token.as_str()),
                        SqlValue::from(claimer),
                        SqlValue::Integer(expires_at),
                    ]);
                    format!(
                        "UPDATE {deliveries} SET state = 'claimed', token = ?, claimed_by = ?, due_at = ?, attempts = attempts + 1
                         WHERE {KEYED} AND due_at = ?"
                    )
                }
            };
            values.extend(key.values(namespace));
            values.push(SqlValue::Integer(due_at));
            // A row a concurrent claim took is left.
            if sql.run(statement, &values)?.changes == 0 || dropped.is_some() {
                continue;
            }
            let body = row.opt_text("body")?.unwrap_or_default();
            let truncated = body.chars().count() > BODY;
            let body: String = body.chars().take(BODY).collect();
            let mut item = Map::new();
            item.insert("id".into(), Value::from(key.id()));
            item.insert("schema".into(), Value::from(key.schema.as_str()));
            item.insert("instanceId".into(), Value::from(key.instance.as_str()));
            item.insert("message".into(), Value::from(key.message));
            if let Some(thread) = row.opt_i64("thread")? {
                item.insert("thread".into(), Value::from(thread));
            }
            item.insert("subject".into(), Value::from(key.subject.as_str()));
            item.insert(
                "reasons".into(),
                reasons(row.i64("mention")? != 0, row.i64("reply")? != 0),
            );
            item.insert(
                "author".into(),
                Value::from(row.opt_text("author")?.unwrap_or_default()),
            );
            if let Some(via) = row.opt_text("via")? {
                item.insert("via".into(), Value::from(via));
            }
            item.insert(
                "state".into(),
                Value::from(message_state.unwrap_or_default()),
            );
            item.insert(
                "postedAt".into(),
                Value::from(row.opt_i64("posted_at")?.unwrap_or_default()),
            );
            item.insert("body".into(), Value::from(body));
            item.insert("truncated".into(), Value::from(truncated));
            item.insert("attempt".into(), Value::from(attempts + 1));
            items.push(Value::Object(item));
            if (items.len() as i64) >= limit {
                break 'pages;
            }
        }
        if (rows.len() as i64) < page {
            break;
        }
    }
    let claimed = !items.is_empty();
    let mut out = Map::new();
    out.insert(
        "token".into(),
        if claimed {
            Value::from(token)
        } else {
            Value::Null
        },
    );
    out.insert(
        "expiresAt".into(),
        if claimed {
            Value::from(expires_at)
        } else {
            Value::Null
        },
    );
    out.insert("items".into(), Value::Array(items));
    Ok(Value::Object(out))
}

/// When a failed delivery on its `attempts`th claim is due again:
/// `min(backoff * 2^(attempts - 1), 1 hour)` after `now`.
pub(super) fn next_attempt_at(now: i64, backoff: i64, attempts: i64) -> i64 {
    let doublings = u32::try_from((attempts - 1).clamp(0, 40)).unwrap_or(0);
    now + backoff
        .saturating_mul(1_i64 << doublings)
        .min(MAX_BACKOFF_MS)
}

pub(super) fn settle_deliveries(cx: Ncx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "settleDeliveries";
    let token = params
        .get("token")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let ids: Vec<&str> = params
        .get("ids")
        .and_then(Value::as_array)
        .map(|ids| ids.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    let outcome = params
        .get("outcome")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let error = params.get("error").and_then(Value::as_str);
    if outcome == "sent" && error.is_some() {
        return Err(invalid(OP, "/error", "must not be given with outcome sent"));
    }
    let configs = deliverable(cx, OP)?;
    let namespace = cx.namespace();
    let sql = cx.sql();
    let deliveries = sql.table(DELIVERIES)?;

    // 4: each id names a row of a schema the caller may settle.
    let mut found: Vec<(&str, DeliveryKey, Option<String>, i64)> = Vec::new();
    let mut unknown: Vec<&str> = Vec::new();
    for id in &ids {
        let row = match DeliveryKey::decode(id) {
            Some(key) if configs.contains_key(&key.schema) => sql
                .get(
                    format!("SELECT token, attempts FROM {deliveries} WHERE {KEYED}"),
                    &key.values(namespace),
                )?
                .map(|row| -> Result<_> {
                    Ok((
                        key,
                        row.opt_text("token")?.map(str::to_owned),
                        row.i64("attempts")?,
                    ))
                })
                .transpose()?,
            _ => None,
        };
        match row {
            Some((key, held, attempts)) => found.push((id, key, held, attempts)),
            None => unknown.push(id),
        }
    }
    if !unknown.is_empty() {
        return Err(veto(
            cx,
            OP,
            "unknown_delivery",
            format!(
                "{} names no delivery {} may settle in namespace {namespace}: {}",
                if unknown.len() == 1 { "an id" } else { "ids" },
                cx.principal().subject,
                unknown.join(", ")
            ),
            json!({ "ids": unknown }),
        ));
    }
    // 5: some row holds the token.
    let held = sql
        .get(
            format!("SELECT 1 AS held FROM {deliveries} WHERE namespace = ? AND token = ? LIMIT 1"),
            &[SqlValue::from(namespace), SqlValue::from(token)],
        )?
        .is_some();
    if !held {
        return Err(veto(
            cx,
            OP,
            "token_stale",
            format!("no delivery of namespace {namespace} holds token {token}: each it claimed was settled, or claimed again after the claim lapsed"),
            json!({ "token": token }),
        ));
    }
    // 6: every row holds it.
    let not_claimed: Vec<&str> = found
        .iter()
        .filter(|(_, _, held, _)| held.as_deref() != Some(token))
        .map(|(id, _, _, _)| *id)
        .collect();
    if !not_claimed.is_empty() {
        return Err(veto(
            cx,
            OP,
            "not_claimed",
            format!(
                "{} not held under token {token}: {}",
                if not_claimed.len() == 1 {
                    "a delivery is"
                } else {
                    "deliveries are"
                },
                not_claimed.join(", ")
            ),
            json!({ "token": token, "ids": not_claimed }),
        ));
    }

    let now = cx.now();
    let mut items = Vec::new();
    for (id, key, _, attempts) in &found {
        let config = configs.get(&key.schema);
        let max_attempts = config.map_or(super::DEFAULT_MAX_ATTEMPTS, |config| config.max_attempts);
        let backoff = config.map_or(super::DEFAULT_RETRY_BACKOFF_MS, |config| {
            config.retry_backoff_ms
        });
        let state = match outcome {
            "sent" => "sent",
            "failed" if *attempts < max_attempts => "failed",
            _ => "dropped",
        };
        let next = (state == "failed").then(|| next_attempt_at(now, backoff, *attempts));
        let kept_error = if state == "sent" { None } else { error };
        let mut values = vec![
            SqlValue::from(state),
            next.map_or(SqlValue::Null, SqlValue::Integer),
            kept_error.map_or(SqlValue::Null, SqlValue::from),
            SqlValue::Integer(now),
        ];
        values.extend(key.values(namespace));
        sql.run(
            format!(
                "UPDATE {deliveries} SET state = ?, due_at = ?, token = NULL, error = ?, settled_at = ?
                 WHERE {KEYED}"
            ),
            &values,
        )?;
        let mut item = Map::new();
        item.insert("id".into(), Value::from(*id));
        item.insert("state".into(), Value::from(state));
        item.insert("attempts".into(), Value::from(*attempts));
        if let Some(next) = next {
            item.insert("nextAttemptAt".into(), Value::from(next));
        }
        items.push(Value::Object(item));
    }
    let mut out = Map::new();
    out.insert("items".into(), Value::Array(items));
    Ok(Value::Object(out))
}

/// The schedule `prune`: the run's schema's deliveries made more than
/// `deliveryRetentionMs` ago that no live claim holds, oldest first, in
/// batches of 1000 for as long as a batch comes back full, at most 20
/// batches a run.
pub(super) fn prune(cx: &ScheduleCx<'_, CommentsConfig>) -> Result<()> {
    for _ in 0..PRUNE_BATCHES {
        if prune_batch(cx)? < PRUNE as usize {
            break;
        }
    }
    Ok(())
}

/// One batch of `prune`, oldest first: the 1000 oldest of the deliveries
/// it removes, and any others made in the same millisecond as the last of
/// them, in one statement each, through `comment_deliveries_created`. How
/// many it removed.
fn prune_batch(cx: &ScheduleCx<'_, CommentsConfig>) -> Result<usize> {
    const PRUNABLE: &str = "namespace = ? AND schema = ? AND created_at < ?
                AND (state <> 'claimed' OR due_at <= ?)";
    let table = cx.sql().table(DELIVERIES)?;
    let now = cx.now();
    let mut params = vec![
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::Integer(now - cx.config().delivery_retention_ms),
        SqlValue::Integer(now),
    ];
    // The 1000th oldest, when there are that many: the batch stops there.
    let mut last = params.clone();
    last.push(SqlValue::Integer(PRUNE - 1));
    let cutoff = cx.sql().get(
        format!(
            "SELECT created_at FROM {table} WHERE {PRUNABLE}
              ORDER BY created_at LIMIT 1 OFFSET ?"
        ),
        &last,
    )?;
    let through = match &cutoff {
        Some(row) => {
            params.push(SqlValue::Integer(row.i64("created_at")?));
            " AND created_at <= ?"
        }
        None => "",
    };
    let removed = cx.sql().run(
        format!("DELETE FROM {table} WHERE {PRUNABLE}{through}"),
        &params,
    )?;
    Ok(usize::try_from(removed.changes).unwrap_or(usize::MAX))
}
