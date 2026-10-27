//! Comments' search (D77 step 5, `comments-contract.md`, "searchMessages",
//! "Keeping the index", "Migration 6"): the full-text index of the bodies
//! of the standing messages, one per engine, which the writes keep in their
//! own transactions; and `searchMessages`, a namespace operation that reads
//! it in one match kept to the schemas the caller may read
//! (`FullTextQuery::readable`), then the hits' messages in one statement.

use std::collections::HashMap;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{page, page_request, InstanceCx, NamespaceCx};
use superschematic_engine::storage::fulltext::{FullTextQuery, Snippets};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Error, Result};

use super::checks::{integer, invalid};
use super::storage::{key, keyed, placeholders, MESSAGES, SEARCH, SEARCH_TEXT, STANDS};
use super::{CommentsConfig, NAME};
use crate::search::{fts5_string, is_js_space, snippet_of};

type Ncx<'a, 'b> = &'a NamespaceCx<'b, CommentsConfig>;

/// The most hits a search reaches.
const MAX_HITS: i64 = 1000;
/// The most words of a query that count.
const MAX_WORDS: usize = 8;
/// The marks a snippet's matches are written between, Search's.
const OPEN: &str = "\u{2}";
const CLOSE: &str = "\u{3}";
/// The most words a snippet holds.
const SNIPPET_WORDS: u32 = 12;

/// Indexes a message that has come to stand: its keys' row, then its body
/// under that row.
pub(super) fn index<C>(
    cx: &InstanceCx<'_, C>,
    message: i64,
    thread: Option<i64>,
    body: &str,
) -> Result<()> {
    let search = cx.sql().table(SEARCH)?;
    let text = cx.sql().table(SEARCH_TEXT)?;
    cx.sql().run(
        format!(
            "INSERT INTO {search} (namespace, schema, id, message, thread_root) VALUES (?, ?, ?, ?, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(message),
                SqlValue::Integer(thread.unwrap_or(message)),
            ],
        ),
    )?;
    let row = row_of(cx, message)?.ok_or_else(|| {
        Error::behavior(NAME, format!("message {message} has no search row just written"))
    })?;
    cx.sql().run(
        format!("INSERT INTO {text} (rowid, body) VALUES (?, ?)"),
        &[SqlValue::Integer(row), SqlValue::from(body)],
    )?;
    Ok(())
}

// row_of is the message's row in the index, `None` when it has none.
fn row_of<C>(cx: &InstanceCx<'_, C>, message: i64) -> Result<Option<i64>> {
    let search = cx.sql().table(SEARCH)?;
    Ok(cx
        .sql()
        .get(
            format!(
                "SELECT row FROM {search} WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
            ),
            &keyed(cx, [SqlValue::Integer(message)]),
        )?
        .map(|found| found.i64("row"))
        .transpose()?)
}

/// Replaces an edited message's indexed body, under its row.
pub(super) fn reindex<C>(cx: &InstanceCx<'_, C>, message: i64, body: &str) -> Result<()> {
    let Some(row) = row_of(cx, message)? else {
        return Ok(());
    };
    let text = cx.sql().table(SEARCH_TEXT)?;
    cx.sql().run(
        format!("DELETE FROM {text} WHERE rowid = ?"),
        &[SqlValue::Integer(row)],
    )?;
    cx.sql().run(
        format!("INSERT INTO {text} (rowid, body) VALUES (?, ?)"),
        &[SqlValue::Integer(row), SqlValue::from(body)],
    )?;
    Ok(())
}

/// Removes the messages' bodies and keys from the index; a message that
/// has none (composing, or never stood) is passed over.
pub(super) fn unindex<C>(cx: &InstanceCx<'_, C>, messages: &[i64]) -> Result<()> {
    if messages.is_empty() {
        return Ok(());
    }
    let search = cx.sql().table(SEARCH)?;
    let text = cx.sql().table(SEARCH_TEXT)?;
    let list = placeholders(messages.len());
    let params = keyed(cx, messages.iter().map(|&id| SqlValue::Integer(id)));
    cx.sql().run(
        format!(
            "DELETE FROM {text} WHERE rowid IN (SELECT row FROM {search} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({list}))"
        ),
        &params,
    )?;
    cx.sql().run(
        format!(
            "DELETE FROM {search} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({list})"
        ),
        &params,
    )?;
    Ok(())
}

/// Removes the instance's bodies and keys from the index.
pub(super) fn unindex_instance<C>(cx: &InstanceCx<'_, C>) -> Result<()> {
    let search = cx.sql().table(SEARCH)?;
    let text = cx.sql().table(SEARCH_TEXT)?;
    cx.sql().run(
        format!(
            "DELETE FROM {text} WHERE rowid IN (SELECT row FROM {search} WHERE namespace = ? AND schema = ? AND id = ?)"
        ),
        &key(cx),
    )?;
    cx.sql().run(
        format!("DELETE FROM {search} WHERE namespace = ? AND schema = ? AND id = ?"),
        &key(cx),
    )?;
    Ok(())
}

/// The FTS5 expression of a query: NUL a space, split at JavaScript's
/// white space, the first eight words each an FTS5 string, joined by a
/// space (implicit AND). `None` when it holds no word.
pub(super) fn expression(query: &str) -> Option<String> {
    let cleaned = query.replace('\0', " ");
    let words: Vec<String> = cleaned
        .split(is_js_space)
        .filter(|word| !word.is_empty())
        .take(MAX_WORDS)
        .map(fts5_string)
        .collect();
    (!words.is_empty()).then(|| words.join(" "))
}

// A hit of the match, in its order.
struct Hit {
    schema: String,
    instance: String,
    message: i64,
    snippet: Option<String>,
}

pub(super) fn search_messages(cx: Ncx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "searchMessages";
    let schema = params.get("schema").and_then(Value::as_str);
    let instance = params.get("instanceId").and_then(Value::as_str);
    let thread = integer(params.get("thread"));
    if instance.is_some() && schema.is_none() {
        return Err(invalid(OP, "/instanceId", "needs schema"));
    }
    if thread.is_some() && instance.is_none() {
        return Err(invalid(OP, "/thread", "needs instanceId"));
    }
    let request = page_request(NAME, OP, params)?;
    let (limit, after) = (request.limit, request.after);
    let empty = || json!({ "items": [], "next": null });
    if after >= MAX_HITS {
        return Ok(empty());
    }
    let query = params
        .get("query")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let Some(expression) = expression(query) else {
        return Ok(empty());
    };
    let mut filters = vec![("namespace", SqlValue::from(cx.namespace()))];
    if let Some(schema) = schema {
        filters.push(("schema", SqlValue::from(schema)));
    }
    if let Some(instance) = instance {
        filters.push(("id", SqlValue::from(instance)));
    }
    if let Some(thread) = thread {
        filters.push(("thread_root", SqlValue::Integer(thread)));
    }
    let count = (limit + 1).min(MAX_HITS - after);
    let found = cx.sql().fulltext(&FullTextQuery {
        index: SEARCH_TEXT,
        columns: &["body"],
        weights: &[1.0],
        snippets: Snippets {
            columns: 1,
            open: OPEN,
            close: CLOSE,
            ellipsis: "...",
            words: SNIPPET_WORDS,
        },
        keys: SEARCH,
        key: "row",
        select: &["schema", "id", "message"],
        filters: &filters,
        readable: Some("schema"),
        exclude: None,
        expression: &expression,
        caller: false,
        limit: count,
        offset: after,
    })?;
    let hits = match found {
        Ok(hits) => hits,
        Err(refusal) => {
            return Err(Error::behavior(
                NAME,
                format!("its search expression is not one the index takes: {refusal}"),
            ))
        }
    };
    let hits = hits
        .into_iter()
        .enumerate()
        .map(|(position, hit)| {
            Ok((
                Hit {
                    schema: hit.row.text("schema")?.to_owned(),
                    instance: hit.row.text("id")?.to_owned(),
                    message: hit.row.i64("message")?,
                    snippet: hit.snippets.into_iter().next().flatten(),
                },
                after + position as i64 + 1,
            ))
        })
        .collect::<Result<Vec<_>>>()?;
    let paged = page(hits, limit, |(_, rank)| *rank);
    let messages = standing(cx, paged.items.iter().map(|(hit, _)| hit))?;
    let items = paged
        .items
        .into_iter()
        .filter_map(|(hit, rank)| {
            let found = messages.get(&(hit.schema.clone(), hit.instance.clone(), hit.message))?;
            Some(item(&hit, found, rank))
        })
        .collect();
    let mut out = Map::new();
    out.insert("items".into(), Value::Array(items));
    out.insert("next".into(), paged.next.map_or(Value::Null, Value::String));
    Ok(Value::Object(out))
}

// A found message's members, as the second statement reads them.
struct Found {
    thread: Option<i64>,
    author: String,
    for_subject: Option<String>,
    via: Option<String>,
    state: String,
    posted_at: i64,
    edited_at: Option<i64>,
}

type MessageKey = (String, String, i64);

// standing reads the hits' messages that stand, in one statement.
fn standing<'h>(
    cx: Ncx<'_, '_>,
    hits: impl Iterator<Item = &'h Hit>,
) -> Result<HashMap<MessageKey, Found>> {
    let mut params = vec![SqlValue::from(cx.namespace())];
    let mut groups = Vec::new();
    for hit in hits {
        groups.push("(schema = ? AND id = ? AND message = ?)");
        params.extend([
            SqlValue::from(hit.schema.as_str()),
            SqlValue::from(hit.instance.as_str()),
            SqlValue::Integer(hit.message),
        ]);
    }
    let mut out = HashMap::new();
    if groups.is_empty() {
        return Ok(out);
    }
    let messages = cx.sql().table(MESSAGES)?;
    let rows = cx.sql().all(
        format!(
            "SELECT schema, id, message, thread, author, for_subject, via, state, posted_at, edited_at
               FROM {messages}
              WHERE namespace = ? AND {STANDS} AND ({})",
            groups.join(" OR ")
        ),
        &params,
    )?;
    for row in &rows {
        out.insert(
            (
                row.text("schema")?.to_owned(),
                row.text("id")?.to_owned(),
                row.i64("message")?,
            ),
            Found {
                thread: row.opt_i64("thread")?,
                author: row.text("author")?.to_owned(),
                for_subject: row.opt_text("for_subject")?.map(str::to_owned),
                via: row.opt_text("via")?.map(str::to_owned),
                state: row.text("state")?.to_owned(),
                posted_at: row.opt_i64("posted_at")?.unwrap_or_default(),
                edited_at: row.opt_i64("edited_at")?,
            },
        );
    }
    Ok(out)
}

// item writes a hit's members in the contract's order.
fn item(hit: &Hit, found: &Found, rank: i64) -> Value {
    let mut out = Map::new();
    out.insert("schema".into(), Value::from(hit.schema.as_str()));
    out.insert("instanceId".into(), Value::from(hit.instance.as_str()));
    out.insert("message".into(), Value::from(hit.message));
    if let Some(thread) = found.thread {
        out.insert("thread".into(), Value::from(thread));
    }
    out.insert("author".into(), Value::from(found.author.as_str()));
    if let Some(subject) = &found.for_subject {
        out.insert("for".into(), Value::from(subject.as_str()));
    }
    if let Some(via) = &found.via {
        out.insert("via".into(), Value::from(via.as_str()));
    }
    out.insert("state".into(), Value::from(found.state.as_str()));
    out.insert("postedAt".into(), Value::from(found.posted_at));
    if let Some(at) = found.edited_at {
        out.insert("editedAt".into(), Value::from(at));
    }
    out.insert("rank".into(), Value::from(rank));
    out.insert(
        "snippet".into(),
        Value::Array(
            snippet_of(hit.snippet.as_deref())
                .into_iter()
                .map(|part| {
                    let mut one = Map::new();
                    one.insert("text".into(), Value::from(part.text));
                    one.insert("match".into(), Value::from(part.matched));
                    Value::Object(one)
                })
                .collect(),
        ),
    );
    Value::Object(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_query_is_its_first_eight_words_each_quoted() {
        assert_eq!(expression("walnut desk"), Some("\"walnut\" \"desk\"".to_owned()));
        assert_eq!(
            expression("say \"hi\" AND * NEAR"),
            Some("\"say\" \"\"\"hi\"\"\" \"AND\" \"*\" \"NEAR\"".to_owned())
        );
        assert_eq!(
            expression("a b c d e f g h i j"),
            Some("\"a\" \"b\" \"c\" \"d\" \"e\" \"f\" \"g\" \"h\"".to_owned())
        );
        assert_eq!(expression("\0 \u{a0}\u{feff}"), None);
    }
}
