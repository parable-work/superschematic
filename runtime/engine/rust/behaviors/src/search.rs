//! Search, the core's full-text and vector search (D16), the port of
//! `src/behaviors/core/search.ts`. The config names the type's own
//! top-level text fields to index, a string or a scalar whose values are
//! strings each, at most 16, optionally a weight for each, and optionally
//! vectors: their dimensions, a label for the model they come from and the
//! permission an embedder needs to settle them. The index is a full-text
//! index the behavior owns, with a column per indexed field in the
//! config's order, beside a table that gives each instance's row its
//! namespace, schema and id, and with vectors, the hash of the text its
//! vector is computed from and the vector. afterChange keeps both in the
//! transaction of every create, update, delete and operation that changes
//! an indexed field, so a search never sees a stale row.
//!
//! Full-text search is the engine's own (D58): the index is created with
//! `create_fulltext` and matched with `sql().fulltext()`, FTS5 on SQLite,
//! as the TypeScript engine has it, and a `tsvector` with a GIN index on
//! Postgres (`superschematic_engine_storage::fulltext`). A search's
//! matches are the same on both, but for the tokens Postgres's parser
//! keeps whole (an email address, a URL, a host, a path, a number with a
//! point); its order and its snippets are SQLite's `bm25` and `snippet`
//! there, and `ts_rank_cd` over the fields' weight classes and
//! `ts_headline` on Postgres. A query is FTS5's on both: plain words, or
//! with syntax fts5 an expression, which Postgres takes without NEAR
//! groups and `^`.
//!
//! search is schema-level and read-only. It answers only a caller who may
//! read the schema (the policy is asked for read on it, beside the read
//! with the operation's name every schema-level call asks), and only from
//! the instances of the caller's namespace: the namespace and the schema
//! are conditions of the query itself. A query is plain words by default:
//! each whitespace-separated word is a quoted FTS5 string, so quotes,
//! operators and column names in it are text, and an instance matches
//! when its indexed fields hold every word. syntax: fts5 passes the query
//! through as an FTS5 expression, without column filters; one the index
//! does not take is invalid_argument. Results come best first, then in the
//! order the instances were first indexed; rank is the place in that
//! order. Each carries a snippet of the indexed field with the most
//! matches.
//!
//! The index is shared by every schema and namespace of the database, so
//! SQLite's bm25 statistics are taken over all of them: the ids and
//! snippets a search returns are always the caller's own, but the order
//! among them can shift with another namespace's text, which is why a text
//! hit carries its place and not a score.
//!
//! Vectors (D16, amended: search's vectors) are the same on both
//! databases, since they are plain bytes and Rust over them. The engine
//! calls no embedding provider. An outside embedder pulls the instances
//! whose text has no vector yet (staleEmbeddings: each id, the indexed
//! fields' text joined by a blank line, and the SHA-256 of that text, the
//! model and the dimensions), computes the vectors, and settles them
//! (settleEmbeddings) with the hash it pulled. A settle stores a vector
//! only where the hash still holds: a write of the text since, or a
//! version that changes the model or the dimensions, moves the hash and
//! clears the vector, and a settle for it is skipped. A vector is stored
//! as little-endian 32-bit floats, scaled to unit length, so a cosine is a
//! dot product. A settle appends no event.
//!
//! A search with a vector ranks the instances whose vectors are settled by
//! cosine similarity to it, in a brute-force scan of the schema's vectors
//! in the namespace. With a query too, the first [`RRF_WINDOW`] of each
//! ranking are fused by reciprocal rank fusion: a hit scores
//! 1 / ([`RRF_K`] + its place) in each ranking it is in, and the scores
//! add. similar ranks the instances nearest to one by its own vector and a
//! full-text search for its longest words, fused the same way, leaving it
//! out; given text in place of an instance, it ranks the same way by the
//! text's longest words and the vector given with it, if any.
//!
//! afterConfigChange rebuilds a namespace's index of the schema when the
//! indexed fields change or the behavior is added, and drops it when the
//! behavior is removed; a change of weights alone needs neither, since
//! they apply when a search runs, on Postgres too, where each field keeps
//! a vector of its own. A version that adds vectors, or changes their
//! model or dimensions, hashes every instance's text and clears every
//! vector; one that removes them clears the hashes and the vectors.

use std::collections::{HashMap, HashSet};

use serde_json::{json, Map, Value};
use unicode_general_category::{get_general_category, GeneralCategory};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, Page, PublishCx, SchemaCx, SqlReader,
    SqlWriter, TableSpec,
};
use superschematic_engine::errors::SchemaIssue;
use superschematic_engine::json::{js_number, number_value, quote, sha256_hex, stringify};
use superschematic_engine::storage::fulltext::{FullTextHit, FullTextQuery, Snippets};
use superschematic_engine::storage::{Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, Hooks, InstanceChange, Result,
};

/// Search's vectors, as its config gives them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SearchVectors {
    /// How many numbers a vector holds.
    pub dimensions: i64,
    /// The label of the model the vectors come from, which the engine only
    /// compares.
    pub model: String,
    /// The permission an embedder needs to settle vectors.
    pub permission: String,
}

/// Search's config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct SearchConfig {
    /// The indexed fields, by JSON key, in the config's order: the index's
    /// columns.
    pub fields: Vec<String>,
    /// Each indexed field's weight, in the order of fields; 1 for one the
    /// config does not weigh.
    pub weights: Vec<f64>,
    /// The vectors it keeps; `None` when it keeps none.
    pub vectors: Option<SearchVectors>,
}

/// Reciprocal rank fusion's constant and window, the engine's, which its
/// search across schemas fuses with too (`superschematic_engine::search`).
pub use superschematic_engine::search::{RRF_K, RRF_WINDOW};

/// The words of an instance's text that similar searches for, at most: its
/// longest.
pub const SIMILAR_TERMS: usize = 32;

const NAME: &str = "Search";

// How many fields one schema indexes at most: the index's columns.
const MAX_FIELDS: usize = 16;

const COLUMNS: [&str; MAX_FIELDS] = [
    "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12", "f13", "f14", "f15",
    "f16",
];

// The marks a snippet's matches are wrapped in. Indexed text has them
// replaced by spaces, so a snippet's parts read back exactly.
const OPEN: &str = "\u{2}";
const CLOSE: &str = "\u{3}";

/// The words a snippet holds at most.
const SNIPPET_WORDS: u32 = 12;

// The shortest word similar searches for: shorter ones are mostly function
// words, each of which matches nearly every row.
const MIN_TERM_LENGTH: usize = 3;

// How many stored vectors the scan reads per statement, so a schema's
// vectors are never all in memory at once.
const SCAN_BATCH: i64 = 512;

// What an embedder's text joins the indexed fields with.
const JOINER: &str = "\n\n";

fn key(namespace: &str, schema: &str, id: &str) -> Vec<SqlValue> {
    vec![
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(id),
    ]
}

// is_text holds a field's JSON Schema to a string, required or not, that
// is no enum.
fn is_text(schema: &Value) -> bool {
    let Some(schema) = schema.as_object() else {
        return false;
    };
    if schema.contains_key("enum") {
        return false;
    }
    match schema.get("type") {
        Some(Value::String(kind)) => kind == "string",
        Some(Value::Array(kinds)) => {
            kinds.len() == 2 && kinds[0] == json!("string") && kinds[1] == json!("null")
        }
        _ => false,
    }
}

// text_of is a field's value as the index holds it: a string, its marks
// replaced by spaces; None for any other value.
fn text_of(value: Option<&Value>) -> Option<String> {
    value.and_then(Value::as_str).map(|text| {
        text.chars()
            .map(|c| match c {
                '\u{0}' | '\u{2}' | '\u{3}' => ' ',
                other => other,
            })
            .collect()
    })
}

fn same_fields(a: &SearchConfig, b: &SearchConfig) -> bool {
    a.fields == b.fields
}

// same_vectors reports whether two configs' vectors are computed alike; the
// permission only says who settles them.
fn same_vectors(a: Option<&SearchVectors>, b: Option<&SearchVectors>) -> bool {
    match (a, b) {
        (Some(a), Some(b)) => a.model == b.model && a.dimensions == b.dimensions,
        (a, b) => a.is_none() && b.is_none(),
    }
}

// changed reports whether an indexed field differs between two versions of
// an instance's own fields.
fn changed(config: &SearchConfig, before: &Map<String, Value>, after: &Map<String, Value>) -> bool {
    config
        .fields
        .iter()
        .any(|field| text_of(before.get(field)) != text_of(after.get(field)))
}

// texts is an instance's indexed fields as the index holds them.
fn texts(config: &SearchConfig, data: &Map<String, Value>) -> Vec<Option<String>> {
    config
        .fields
        .iter()
        .map(|field| text_of(data.get(field)))
        .collect()
}

// is_js_space is a character JavaScript's trim() and \s take: its
// WhiteSpace and LineTerminator, which differ from Unicode's White_Space
// in U+FEFF (JavaScript's) and U+0085 (Unicode's).
fn is_js_space(c: char) -> bool {
    matches!(
        c,
        '\t' | '\n' | '\u{b}' | '\u{c}' | '\r' | ' ' | '\u{a0}' | '\u{1680}' | '\u{2000}'
            ..='\u{200a}'
                | '\u{2028}'
                | '\u{2029}'
                | '\u{202f}'
                | '\u{205f}'
                | '\u{3000}'
                | '\u{feff}'
    )
}

fn is_blank(text: &str) -> bool {
    text.chars().all(is_js_space)
}

// composed is the text a vector is computed from: the indexed fields that
// hold more than whitespace, in the config's order, joined by a blank line.
fn composed<S: AsRef<str>>(values: &[Option<S>]) -> String {
    values
        .iter()
        .flatten()
        .map(AsRef::as_ref)
        .filter(|text| !is_blank(text))
        .collect::<Vec<_>>()
        .join(JOINER)
}

// source_hash is the hash a vector is computed under: of the text, the
// model and the dimensions. Text that holds nothing wants no vector.
fn source_hash(vectors: &SearchVectors, text: &str) -> Option<String> {
    (!text.is_empty()).then(|| {
        sha256_hex(&stringify(&json!([
            vectors.model,
            vectors.dimensions,
            text
        ])))
    })
}

// track records the hash of an instance's text under the config's vectors,
// and clears its vector when the hash moves. A hash that is null on both
// sides has not moved: `IS NOT` on SQLite, `IS DISTINCT FROM` on Postgres.
fn track(
    sql: &SqlWriter<'_>,
    vectors: &SearchVectors,
    (namespace, schema, id): (&str, &str, &str),
    values: &[Option<String>],
) -> Result<()> {
    let hash = source_hash(vectors, &composed(values)).map_or(SqlValue::Null, SqlValue::Text);
    let rows = sql.table("rows")?;
    let statement = |differs: &str| {
        format!(
            "UPDATE {rows} SET source_hash = ?, vector = NULL, settled_at = NULL, settled_by = NULL
             WHERE namespace = ? AND schema = ? AND id = ? AND source_hash {differs} ?"
        )
    };
    let mut params = vec![hash.clone()];
    params.extend(key(namespace, schema, id));
    params.push(hash);
    sql.run(
        Sql::per(statement("IS NOT"), statement("IS DISTINCT FROM")),
        &params,
    )?;
    Ok(())
}

// index writes an instance's indexed fields, replacing what the index held
// for it, and with vectors the hash of its text.
fn index(
    sql: &SqlWriter<'_>,
    config: &SearchConfig,
    instance: (&str, &str, &str),
    data: &Map<String, Value>,
) -> Result<()> {
    let (namespace, schema, id) = instance;
    let rows = sql.table("rows")?;
    let text = sql.table("text")?;
    sql.run(
        format!("INSERT INTO {rows} (namespace, schema, id) VALUES (?, ?, ?) ON CONFLICT (namespace, schema, id) DO NOTHING"),
        &key(namespace, schema, id),
    )?;
    let row = sql
        .get(
            format!("SELECT row FROM {rows} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(namespace, schema, id),
        )?
        .ok_or_else(|| {
            Error::Internal("Search's row of an instance it just indexed is missing".to_owned())
        })?
        .i64("row")?;
    sql.run(
        format!("DELETE FROM {text} WHERE rowid = ?"),
        &[SqlValue::Integer(row)],
    )?;
    let columns = &COLUMNS[..config.fields.len()];
    let values = texts(config, data);
    let mut params = vec![SqlValue::Integer(row)];
    params.extend(
        values
            .iter()
            .map(|value| value.clone().map_or(SqlValue::Null, SqlValue::Text)),
    );
    sql.run(
        format!(
            "INSERT INTO {text} (rowid, {}) VALUES (?, {})",
            columns.join(", "),
            vec!["?"; columns.len()].join(", ")
        ),
        &params,
    )?;
    if let Some(vectors) = &config.vectors {
        track(sql, vectors, instance, &values)?;
    }
    Ok(())
}

fn unindex(sql: &SqlWriter<'_>, namespace: &str, schema: &str, id: &str) -> Result<()> {
    let rows = sql.table("rows")?;
    let found = sql.get(
        format!("SELECT row FROM {rows} WHERE namespace = ? AND schema = ? AND id = ?"),
        &key(namespace, schema, id),
    )?;
    if let Some(found) = found {
        let row = SqlValue::Integer(found.i64("row")?);
        sql.run(
            format!("DELETE FROM {} WHERE rowid = ?", sql.table("text")?),
            std::slice::from_ref(&row),
        )?;
        sql.run(format!("DELETE FROM {rows} WHERE row = ?"), &[row])?;
    }
    Ok(())
}

// clear drops a namespace's index of a schema, its vectors with it.
fn clear(sql: &SqlWriter<'_>, namespace: &str, schema: &str) -> Result<()> {
    let rows = sql.table("rows")?;
    let scope = [SqlValue::from(namespace), SqlValue::from(schema)];
    sql.run(
        format!(
            "DELETE FROM {} WHERE rowid IN (SELECT row FROM {rows} WHERE namespace = ? AND schema = ?)",
            sql.table("text")?
        ),
        &scope,
    )?;
    sql.run(
        format!("DELETE FROM {rows} WHERE namespace = ? AND schema = ?"),
        &scope,
    )?;
    Ok(())
}

// forget drops a namespace's vectors of a schema and their hashes.
fn forget(sql: &SqlWriter<'_>, namespace: &str, schema: &str) -> Result<()> {
    sql.run(
        format!(
            "UPDATE {} SET source_hash = NULL, vector = NULL, settled_at = NULL, settled_by = NULL WHERE namespace = ? AND schema = ?",
            sql.table("rows")?
        ),
        &[SqlValue::from(namespace), SqlValue::from(schema)],
    )?;
    Ok(())
}

fn params_error(operation: &str, issues: Vec<SchemaIssue>) -> Error {
    EngineError::operation_params(NAME, operation, issues).into()
}

// expression turns a query into the FTS5 expression it searches with, or
// None for a query with nothing to search for.
fn expression(query: &str, syntax: &str) -> Result<Option<String>> {
    let cleaned = query.replace('\0', " ");
    if is_blank(&cleaned) {
        return Ok(None);
    }
    if syntax == "fts5" {
        // A column filter would name the index's columns, not the type's
        // fields; outside a string, ':', '{' and '}' only start one.
        let mut quoted = false;
        for character in cleaned.chars() {
            if character == '"' {
                quoted = !quoted;
            } else if !quoted && matches!(character, ':' | '{' | '}') {
                return Err(params_error(
                    "search",
                    vec![SchemaIssue::new(
                        "/query",
                        "holds a column filter, which search does not take: a query searches every indexed field",
                    )],
                ));
            }
        }
        return Ok(Some(cleaned));
    }
    Ok(Some(
        cleaned
            .split(is_js_space)
            .filter(|word| !word.is_empty())
            .map(fts5_string)
            .collect::<Vec<_>>()
            .join(" "),
    ))
}

// fts5_string quotes a word as an FTS5 string.
fn fts5_string(word: &str) -> String {
    format!("\"{}\"", word.replace('"', "\"\""))
}

// is_word_character is a letter or a number (\p{L} or \p{N}).
fn is_word_character(c: char) -> bool {
    use GeneralCategory as G;
    matches!(
        get_general_category(c),
        G::UppercaseLetter
            | G::LowercaseLetter
            | G::TitlecaseLetter
            | G::ModifierLetter
            | G::OtherLetter
            | G::DecimalNumber
            | G::LetterNumber
            | G::OtherNumber
    )
}

// likeness is the FTS5 expression similar searches with: any of the text's
// SIMILAR_TERMS longest distinct words of at least MIN_TERM_LENGTH
// characters, among equally long ones the more frequent, then the earlier.
// bm25 weighs a rarer word more, and a longer word is mostly a rarer one.
// None when the text holds no such word.
fn likeness(text: &str) -> Option<String> {
    struct Seen {
        length: usize,
        count: usize,
        first: usize,
    }
    let lower = text.to_lowercase();
    let mut words: Vec<(String, Seen)> = Vec::new();
    let mut known: HashMap<String, usize> = HashMap::new();
    let mut position = 0;
    let mut current = String::new();
    let mut flush = |current: &mut String, position: &mut usize| {
        if current.is_empty() {
            return;
        }
        let word = std::mem::take(current);
        let length = word.chars().count();
        if length >= MIN_TERM_LENGTH {
            match known.get(&word) {
                Some(&at) => words[at].1.count += 1,
                None => {
                    known.insert(word.clone(), words.len());
                    words.push((
                        word,
                        Seen {
                            length,
                            count: 1,
                            first: *position,
                        },
                    ));
                }
            }
        }
        *position += 1;
    };
    for c in lower.chars() {
        if is_word_character(c) {
            current.push(c);
        } else {
            flush(&mut current, &mut position);
        }
    }
    flush(&mut current, &mut position);
    words.sort_by(|(_, a), (_, b)| {
        b.length
            .cmp(&a.length)
            .then(b.count.cmp(&a.count))
            .then(a.first.cmp(&b.first))
    });
    let chosen: Vec<String> = words
        .iter()
        .take(SIMILAR_TERMS)
        .map(|(word, _)| fts5_string(word))
        .collect();
    (!chosen.is_empty()).then(|| chosen.join(" OR "))
}

/// One part of a snippet: text, and whether it is a match.
#[derive(Clone, Debug, PartialEq, Eq)]
struct SnippetPart {
    text: String,
    matched: bool,
}

// snippet_of reads a snippet written with OPEN and CLOSE around each match.
fn snippet_of(value: Option<&str>) -> Vec<SnippetPart> {
    let mut parts = Vec::new();
    let mut text = String::new();
    let mut matched = false;
    for c in value.unwrap_or_default().chars() {
        if c == '\u{2}' || c == '\u{3}' {
            if !text.is_empty() {
                parts.push(SnippetPart {
                    text: std::mem::take(&mut text),
                    matched,
                });
            }
            matched = c == '\u{2}';
        } else {
            text.push(c);
        }
    }
    if !text.is_empty() {
        parts.push(SnippetPart { text, matched });
    }
    parts
}

// snippet is a text match's snippet: that of the indexed field with the
// most matches, the earliest in the config on a tie; none without a match.
fn snippet(hit: Option<&FullTextHit>, fields: &[String], out: &mut Map<String, Value>) {
    let mut best: Option<(&str, Vec<SnippetPart>, usize)> = None;
    for (position, field) in fields.iter().enumerate() {
        let parts = snippet_of(
            hit.and_then(|hit| hit.snippets.get(position))
                .and_then(Option::as_deref),
        );
        let matches = parts.iter().filter(|part| part.matched).count();
        if matches > 0 && best.as_ref().is_none_or(|(_, _, most)| matches > *most) {
            best = Some((field, parts, matches));
        }
    }
    if let Some((field, parts, _)) = best {
        out.insert("field".into(), Value::from(field));
        out.insert(
            "snippet".into(),
            Value::Array(
                parts
                    .into_iter()
                    .map(|part| json!({"text": part.text, "match": part.matched}))
                    .collect(),
            ),
        );
    }
}

/// A full-text match's row: its instance, its row and its snippets.
struct TextRow {
    id: String,
    row: i64,
    hit: FullTextHit,
}

// matches runs a full-text match over a namespace's index of a schema:
// count rows from offset in rank order, leaving out the row exclude names.
// A caller's FTS5 expression the index does not take is invalid_argument.
#[allow(clippy::too_many_arguments)]
fn matches(
    sql: &SqlReader<'_>,
    config: &SearchConfig,
    namespace: &str,
    schema: &str,
    expression: &str,
    syntax: &str,
    count: i64,
    offset: i64,
    exclude: Option<i64>,
) -> Result<Vec<TextRow>> {
    let weights: Vec<f64> = (0..MAX_FIELDS)
        .map(|position| config.weights.get(position).copied().unwrap_or(0.0))
        .collect();
    let filters = [
        ("namespace", SqlValue::from(namespace)),
        ("schema", SqlValue::from(schema)),
    ];
    let query = FullTextQuery {
        index: "text",
        columns: &COLUMNS,
        weights: &weights,
        snippets: Snippets {
            columns: config.fields.len(),
            open: OPEN,
            close: CLOSE,
            ellipsis: "...",
            words: SNIPPET_WORDS,
        },
        keys: "rows",
        key: "row",
        select: &["id"],
        filters: &filters,
        exclude,
        expression,
        caller: syntax == "fts5",
        limit: count,
        offset,
    };
    match sql.fulltext(&query)? {
        Ok(hits) => hits
            .into_iter()
            .map(|hit| {
                Ok(TextRow {
                    id: hit.row.text("id")?.to_owned(),
                    row: hit.row.i64("row")?,
                    hit,
                })
            })
            .collect(),
        Err(refusal) => Err(params_error(
            "search",
            vec![SchemaIssue::new(
                "/query",
                format!("is not an FTS5 expression this index takes: {refusal}"),
            )],
        )),
    }
}

// encode stores a vector as little-endian 32-bit floats.
fn encode(vector: &[f32]) -> Vec<u8> {
    vector
        .iter()
        .flat_map(|value| value.to_le_bytes())
        .collect()
}

// decode reads a stored vector; None for none.
fn decode(value: Option<&[u8]>) -> Option<Vec<f32>> {
    let bytes = value?;
    if bytes.is_empty() || bytes.len() % 4 != 0 {
        return None;
    }
    Some(
        bytes
            .as_chunks::<4>()
            .0
            .iter()
            .map(|chunk| f32::from_le_bytes(*chunk))
            .collect(),
    )
}

// unit holds a vector to the config's dimensions and to values a 32-bit
// float holds, and scales it to unit length; the error says why it cannot.
fn unit(values: &[Value], dimensions: i64) -> std::result::Result<Vec<f32>, String> {
    if i64::try_from(values.len()).ok() != Some(dimensions) {
        return Err(format!(
            "holds {} numbers, and the config's vectors hold {dimensions}",
            values.len()
        ));
    }
    let numbers: Vec<f64> = values
        .iter()
        .map(|value| match value {
            Value::Number(number) => number_value(number),
            _ => f64::NAN,
        })
        .collect();
    let mut vector: Vec<f32> = numbers.iter().map(|number| *number as f32).collect();
    if let Some(at) = vector.iter().position(|value| !value.is_finite()) {
        return Err(format!(
            "holds {} at {at}, which a 32-bit float cannot hold",
            js_number(numbers[at])
        ));
    }
    let sum: f64 = vector
        .iter()
        .map(|value| f64::from(*value) * f64::from(*value))
        .sum();
    let norm = sum.sqrt();
    if norm == 0.0 {
        return Err("is all zeros, which has no direction to compare".to_owned());
    }
    for value in &mut vector {
        *value = (f64::from(*value) / norm) as f32;
    }
    Ok(vector)
}

/// A stored vector near a query: its instance, its row and its cosine
/// similarity.
#[derive(Clone, Debug)]
struct Neighbour {
    id: String,
    row: i64,
    similarity: f64,
}

// nearest_vectors ranks a namespace's stored vectors of a schema against a
// unit vector, most similar first, then in the order the instances were
// first indexed, leaving out a row: the brute-force scan, which reads every
// settled vector of the schema in the namespace, SCAN_BATCH at a time
// through an index. An approximate index could stand behind the same call,
// at the cost of an exact ranking.
fn nearest_vectors(
    sql: &SqlReader<'_>,
    namespace: &str,
    schema: &str,
    query: &[f32],
    count: i64,
    exclude: Option<i64>,
) -> Result<Vec<Neighbour>> {
    let rows = sql.table("rows")?;
    let mut all: Vec<Neighbour> = Vec::new();
    let mut after = 0;
    loop {
        let batch = sql.all(
            format!(
                "SELECT row, id, vector FROM {rows}
                 WHERE namespace = ? AND schema = ? AND vector IS NOT NULL AND row > ? ORDER BY row LIMIT ?"
            ),
            &[
                SqlValue::from(namespace),
                SqlValue::from(schema),
                SqlValue::Integer(after),
                SqlValue::Integer(SCAN_BATCH),
            ],
        )?;
        for found in &batch {
            let row = found.i64("row")?;
            let vector = if Some(row) == exclude {
                None
            } else {
                decode(found.opt_blob("vector")?)
            };
            if let Some(vector) = vector.filter(|vector| vector.len() == query.len()) {
                let mut dot = 0.0_f64;
                for (stored, asked) in vector.iter().zip(query) {
                    dot += f64::from(*stored) * f64::from(*asked);
                }
                all.push(Neighbour {
                    id: found.text("id")?.to_owned(),
                    row,
                    similarity: dot,
                });
            }
        }
        if i64::try_from(batch.len()).unwrap_or(0) < SCAN_BATCH {
            break;
        }
        if let Some(last) = batch.last() {
            after = last.i64("row")?;
        }
    }
    all.sort_by(|a, b| {
        b.similarity
            .total_cmp(&a.similarity)
            .then(a.row.cmp(&b.row))
    });
    all.truncate(usize::try_from(count).unwrap_or(0));
    Ok(all)
}

/// A hit as fusion ranks it.
struct Fused {
    id: String,
    row: i64,
    score: f64,
    text_rank: Option<i64>,
    text: Option<FullTextHit>,
    vector_rank: Option<i64>,
    similarity: Option<f64>,
}

fn rrf(place: i64) -> f64 {
    1.0 / (RRF_K + place) as f64
}

// fuse merges a full-text ranking and a vector ranking by reciprocal rank
// fusion: the higher score first, then the better place in the text
// ranking, then in the vector ranking, then the earlier indexed.
fn fuse(text: Vec<TextRow>, near: Vec<Neighbour>) -> Vec<Fused> {
    let mut fused: Vec<Fused> = Vec::new();
    let mut by_id: HashMap<String, usize> = HashMap::new();
    for (position, row) in text.into_iter().enumerate() {
        let place = position as i64 + 1;
        by_id.insert(row.id.clone(), fused.len());
        fused.push(Fused {
            id: row.id,
            row: row.row,
            score: rrf(place),
            text_rank: Some(place),
            text: Some(row.hit),
            vector_rank: None,
            similarity: None,
        });
    }
    for (position, neighbour) in near.into_iter().enumerate() {
        let place = position as i64 + 1;
        let at = match by_id.get(&neighbour.id) {
            Some(&at) => at,
            None => {
                by_id.insert(neighbour.id.clone(), fused.len());
                fused.push(Fused {
                    id: neighbour.id.clone(),
                    row: neighbour.row,
                    score: 0.0,
                    text_rank: None,
                    text: None,
                    vector_rank: None,
                    similarity: None,
                });
                fused.len() - 1
            }
        };
        let hit = &mut fused[at];
        hit.score += rrf(place);
        hit.vector_rank = Some(place);
        hit.similarity = Some(neighbour.similarity);
    }
    let last = i64::MAX;
    fused.sort_by(|a, b| {
        b.score
            .total_cmp(&a.score)
            .then(
                a.text_rank
                    .unwrap_or(last)
                    .cmp(&b.text_rank.unwrap_or(last)),
            )
            .then(
                a.vector_rank
                    .unwrap_or(last)
                    .cmp(&b.vector_rank.unwrap_or(last)),
            )
            .then(a.row.cmp(&b.row))
    });
    fused
}

fn hit_head(id: &str, rank: i64) -> Map<String, Value> {
    let mut out = Map::new();
    out.insert("id".into(), Value::from(id));
    out.insert("rank".into(), Value::from(rank));
    out
}

fn ranked<T>(items: Vec<T>, after: i64) -> Vec<(T, i64)> {
    items
        .into_iter()
        .enumerate()
        .map(|(position, item)| (item, after + position as i64 + 1))
        .collect()
}

// text_page pages a full-text ranking read from offset after, with one row
// more than the page.
fn text_page(rows: Vec<TextRow>, limit: i64, after: i64, fields: &[String]) -> Value {
    let found = page(ranked(rows, after), limit, |(_, rank)| *rank);
    Page {
        items: found
            .items
            .into_iter()
            .map(|(row, rank)| {
                let mut out = hit_head(&row.id, rank);
                snippet(Some(&row.hit), fields, &mut out);
                Value::Object(out)
            })
            .collect(),
        next: found.next,
    }
    .to_json()
}

// vector_page pages a vector ranking read from the start.
fn vector_page(near: Vec<Neighbour>, limit: i64, after: i64) -> Value {
    let rest: Vec<Neighbour> = near
        .into_iter()
        .skip(usize::try_from(after).unwrap_or(0))
        .collect();
    let found = page(ranked(rest, after), limit, |(_, rank)| *rank);
    Page {
        items: found
            .items
            .into_iter()
            .map(|(neighbour, rank)| {
                let mut out = hit_head(&neighbour.id, rank);
                out.insert("score".into(), Value::from(rrf(rank)));
                out.insert(
                    "vector".into(),
                    json!({"rank": rank, "similarity": neighbour.similarity}),
                );
                Value::Object(out)
            })
            .collect(),
        next: found.next,
    }
    .to_json()
}

// fused_page pages a fused ranking.
fn fused_page(fused: Vec<Fused>, limit: i64, after: i64, fields: &[String]) -> Value {
    let window: Vec<Fused> = fused
        .into_iter()
        .skip(usize::try_from(after).unwrap_or(0))
        .take(usize::try_from(limit + 1).unwrap_or(0))
        .collect();
    let found = page(ranked(window, after), limit, |(_, rank)| *rank);
    Page {
        items: found
            .items
            .into_iter()
            .map(|(hit, rank)| {
                let mut out = hit_head(&hit.id, rank);
                out.insert("score".into(), Value::from(hit.score));
                snippet(hit.text.as_ref(), fields, &mut out);
                if let Some(text_rank) = hit.text_rank {
                    out.insert("text".into(), json!({ "rank": text_rank }));
                }
                if let (Some(vector_rank), Some(similarity)) = (hit.vector_rank, hit.similarity) {
                    out.insert(
                        "vector".into(),
                        json!({"rank": vector_rank, "similarity": similarity}),
                    );
                }
                Value::Object(out)
            })
            .collect(),
        next: found.next,
    }
    .to_json()
}

// may_read refuses a caller who may not read the schema: a search returns
// ids and text, and a settle says which ids exist.
fn may_read(cx: &SchemaCx<'_, SearchConfig>) -> Result<()> {
    if !cx.schemas().readable(cx.schema())? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not read {} in namespace {}",
                cx.principal().subject,
                cx.schema(),
                cx.namespace()
            ),
        )
        .into());
    }
    Ok(())
}

// vectors_of is the config's vectors, or the refusal of an operation that
// needs them.
fn vectors_of<'c>(
    cx: &SchemaCx<'c, SearchConfig>,
    operation: &str,
    path: &str,
) -> Result<&'c SearchVectors> {
    cx.config().vectors.as_ref().ok_or_else(|| {
        params_error(
            operation,
            vec![SchemaIssue::new(
                path,
                format!(
                    "{}'s Search keeps no vectors: its config has none",
                    cx.schema()
                ),
            )],
        )
    })
}

// query_vector checks the vector a search or a similar is given against the
// config's and returns it at unit length.
fn query_vector(
    cx: &SchemaCx<'_, SearchConfig>,
    operation: &str,
    params: &Value,
) -> Result<Vec<f32>> {
    let vectors = vectors_of(cx, operation, "/vector")?;
    if let Some(model) = params.get("model") {
        if model.as_str() != Some(vectors.model.as_str()) {
            return Err(params_error(
                operation,
                vec![SchemaIssue::new(
                    "/model",
                    format!(
                        "is {}, and {}'s vectors come from {}",
                        stringify(model),
                        cx.schema(),
                        quote(&vectors.model)
                    ),
                )],
            ));
        }
    }
    let values = params
        .get("vector")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default();
    unit(values, vectors.dimensions)
        .map_err(|message| params_error(operation, vec![SchemaIssue::new("/vector", message)]))
}

// nearest ranks the instances near a text and, when there is one, a
// vector, as similar does: a full-text search for the text's longest
// words, fused with the vector's ranking, leaving out a row (the
// instance's own, for similar of an instance).
fn nearest(
    cx: &SchemaCx<'_, SearchConfig>,
    text: &str,
    vector: Option<&[f32]>,
    limit: i64,
    after: i64,
    exclude: Option<i64>,
) -> Result<Value> {
    let config = cx.config();
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    let likeness = likeness(text);
    let Some(vector) = vector else {
        let Some(likeness) = likeness else {
            return Ok(Page::<Value> {
                items: Vec::new(),
                next: None,
            }
            .to_json());
        };
        let rows = matches(
            &sql,
            config,
            namespace,
            schema,
            &likeness,
            "words",
            limit + 1,
            after,
            exclude,
        )?;
        return Ok(text_page(rows, limit, after, &config.fields));
    };
    let Some(likeness) = likeness else {
        let near = nearest_vectors(&sql, namespace, schema, vector, after + limit + 1, exclude)?;
        return Ok(vector_page(near, limit, after));
    };
    let rows = matches(
        &sql, config, namespace, schema, &likeness, "words", RRF_WINDOW, 0, exclude,
    )?;
    let near = nearest_vectors(&sql, namespace, schema, vector, RRF_WINDOW, exclude)?;
    Ok(fused_page(fuse(rows, near), limit, after, &config.fields))
}

// own_text is an instance's text, as the index holds its fields.
fn own_text(sql: &SqlReader<'_>, config: &SearchConfig, row: i64) -> Result<String> {
    let columns = &COLUMNS[..config.fields.len()];
    let found = sql.get(
        format!(
            "SELECT {} FROM {} WHERE rowid = ?",
            columns.join(", "),
            sql.table("text")?
        ),
        &[SqlValue::Integer(row)],
    )?;
    let values: Vec<Option<String>> = columns
        .iter()
        .map(|column| {
            found
                .as_ref()
                .and_then(|row| row.get(column))
                .and_then(SqlValue::as_str)
                .map(str::to_owned)
        })
        .collect();
    Ok(composed(&values))
}

fn with_embedded(mut page: Value, embedded: bool) -> Value {
    if let Value::Object(members) = &mut page {
        members.insert("embedded".into(), Value::Bool(embedded));
    }
    page
}

fn search(_: &Search, cx: &SchemaCx<'_, SearchConfig>, params: &Value) -> Result<Value> {
    may_read(cx)?;
    let config = cx.config();
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    let request = page_request(NAME, "search", params)?;
    let (limit, after) = (request.limit, request.after);
    let syntax = params
        .get("syntax")
        .and_then(Value::as_str)
        .unwrap_or("words");
    let query = params.get("query").and_then(Value::as_str);
    let found = match query {
        Some(query) => expression(query, syntax)?,
        None => None,
    };
    if params.get("vector").is_none() {
        let Some(found) = found else {
            return Ok(Page::<Value> {
                items: Vec::new(),
                next: None,
            }
            .to_json());
        };
        let rows = matches(
            &sql,
            config,
            namespace,
            schema,
            &found,
            syntax,
            limit + 1,
            after,
            None,
        )?;
        return Ok(text_page(rows, limit, after, &config.fields));
    }
    let vector = query_vector(cx, "search", params)?;
    if query.is_none() {
        let near = nearest_vectors(&sql, namespace, schema, &vector, after + limit + 1, None)?;
        return Ok(vector_page(near, limit, after));
    }
    let rows = match found {
        Some(found) => matches(
            &sql, config, namespace, schema, &found, syntax, RRF_WINDOW, 0, None,
        )?,
        None => Vec::new(),
    };
    let near = nearest_vectors(&sql, namespace, schema, &vector, RRF_WINDOW, None)?;
    Ok(fused_page(fuse(rows, near), limit, after, &config.fields))
}

// similar of an instance ranks by its own text and vector and leaves it
// out; of a text, by the text and the vector given with it, with no
// instance to leave out.
fn similar(_: &Search, cx: &SchemaCx<'_, SearchConfig>, params: &Value) -> Result<Value> {
    may_read(cx)?;
    let config = cx.config();
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    let request = page_request(NAME, "similar", params)?;
    let (limit, after) = (request.limit, request.after);
    if let Some(text) = params.get("text").and_then(Value::as_str) {
        let vector = match params.get("vector") {
            Some(_) => Some(query_vector(cx, "similar", params)?),
            None => None,
        };
        let found = nearest(cx, text, vector.as_deref(), limit, after, None)?;
        return Ok(with_embedded(found, vector.is_some()));
    }
    let id = params.get("id").and_then(Value::as_str).unwrap_or_default();
    let rows = sql.table("rows")?;
    let own = sql
        .get(
            format!("SELECT row, vector FROM {rows} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(namespace, schema, id),
        )?
        .ok_or_else(|| {
            Error::from(EngineError::new(
                EngineErrorCode::NotFound,
                format!("{schema} {id} does not exist in namespace {namespace}"),
            ))
        })?;
    let row = own.i64("row")?;
    let vector = match config.vectors {
        None => None,
        Some(_) => decode(own.opt_blob("vector")?),
    };
    let text = own_text(&sql, config, row)?;
    let found = nearest(cx, &text, vector.as_deref(), limit, after, Some(row))?;
    Ok(with_embedded(found, vector.is_some()))
}

fn stale_embeddings(_: &Search, cx: &SchemaCx<'_, SearchConfig>, params: &Value) -> Result<Value> {
    let vectors = vectors_of(cx, "staleEmbeddings", "")?;
    may_read(cx)?;
    let config = cx.config();
    let sql = cx.sql();
    let request = page_request(NAME, "staleEmbeddings", params)?;
    let columns = &COLUMNS[..config.fields.len()];
    let selected: Vec<String> = columns
        .iter()
        .map(|column| format!("t.{column} AS {column}"))
        .collect();
    let rows = sql.all(
        format!(
            "SELECT r.row AS row, r.id AS id, r.source_hash AS source_hash, {}
             FROM {} AS r JOIN {} AS t ON t.rowid = r.row
             WHERE r.namespace = ? AND r.schema = ? AND r.source_hash IS NOT NULL AND r.vector IS NULL AND r.row > ?
             ORDER BY r.row LIMIT ?",
            selected.join(", "),
            sql.table("rows")?,
            sql.table("text")?
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::Integer(request.after),
            SqlValue::Integer(request.limit + 1),
        ],
    )?;
    let numbered = rows
        .into_iter()
        .map(|row| Ok((row.i64("row")?, row)))
        .collect::<Result<Vec<_>>>()?;
    let found = page(numbered, request.limit, |(number, _)| *number);
    let items = found
        .items
        .iter()
        .map(|(_, row)| {
            let values: Vec<Option<&str>> = columns
                .iter()
                .map(|column| row.get(column).and_then(SqlValue::as_str))
                .collect();
            Ok(json!({
                "id": row.text("id")?,
                "text": composed(&values),
                "sourceHash": row.text("source_hash")?,
            }))
        })
        .collect::<Result<Vec<_>>>()?;
    let mut out = Map::new();
    out.insert("model".into(), Value::from(vectors.model.as_str()));
    out.insert("dimensions".into(), Value::from(vectors.dimensions));
    out.insert("items".into(), Value::Array(items));
    out.insert("next".into(), found.next.map_or(Value::Null, Value::String));
    Ok(Value::Object(out))
}

fn settle_embeddings(_: &Search, cx: &SchemaCx<'_, SearchConfig>, params: &Value) -> Result<Value> {
    let vectors = vectors_of(cx, "settleEmbeddings", "")?;
    may_read(cx)?;
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    let principal = cx.principal();
    if !cx.can(&vectors.permission)? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not settle the vectors of {schema} in namespace {namespace}: it needs {}",
                principal.subject, vectors.permission
            ),
        )
        .into());
    }
    let items = params
        .get("items")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default();
    let mut issues = Vec::new();
    let mut seen: HashSet<&str> = HashSet::new();
    let mut units: Vec<Option<Vec<f32>>> = Vec::with_capacity(items.len());
    for (position, item) in items.iter().enumerate() {
        let id = item.get("id").and_then(Value::as_str).unwrap_or_default();
        if !seen.insert(id) {
            issues.push(SchemaIssue::new(
                format!("/items/{position}/id"),
                format!("settles {id} a second time in one call"),
            ));
        }
        let values = item
            .get("vector")
            .and_then(Value::as_array)
            .map(Vec::as_slice)
            .unwrap_or_default();
        match unit(values, vectors.dimensions) {
            Ok(vector) => units.push(Some(vector)),
            Err(message) => {
                issues.push(SchemaIssue::new(
                    format!("/items/{position}/vector"),
                    message,
                ));
                units.push(None);
            }
        }
    }
    if !issues.is_empty() {
        return Err(params_error("settleEmbeddings", issues));
    }
    let rows = sql.table("rows")?;
    let mut skipped = Vec::new();
    let mut settled = 0_i64;
    for (item, vector) in items.iter().zip(units) {
        let id = item.get("id").and_then(Value::as_str).unwrap_or_default();
        let presented = item.get("sourceHash").and_then(Value::as_str);
        let found = sql.get(
            format!(
                "SELECT row, source_hash FROM {rows} WHERE namespace = ? AND schema = ? AND id = ?"
            ),
            &key(namespace, schema, id),
        )?;
        match found {
            None => skipped.push(json!({"id": id, "reason": "not_found"})),
            Some(found) if found.opt_text("source_hash")? != presented => {
                skipped.push(json!({"id": id, "reason": "moved"}));
            }
            Some(found) => {
                sql.run(
                    format!("UPDATE {rows} SET vector = ?, settled_at = ?, settled_by = ? WHERE row = ?"),
                    &[
                        SqlValue::Blob(encode(&vector.unwrap_or_default())),
                        SqlValue::Integer(cx.now()),
                        SqlValue::from(principal.subject.as_str()),
                        SqlValue::Integer(found.i64("row")?),
                    ],
                )?;
                settled += 1;
            }
        }
    }
    Ok(json!({"settled": settled, "skipped": skipped}))
}

fn after_config_change(_: &Search, cx: &PublishCx<'_, SearchConfig>) -> Result<()> {
    let sql = cx.sql();
    let (namespace, schema) = (cx.namespace(), cx.schema());
    if let (Some(config), Some(before)) = (cx.config(), cx.before()) {
        if same_fields(before, config) {
            if same_vectors(before.vectors.as_ref(), config.vectors.as_ref()) {
                return Ok(());
            }
            return match &config.vectors {
                None => forget(&sql, namespace, schema),
                Some(vectors) => cx.each_instance(|instance| {
                    track(
                        &sql,
                        vectors,
                        (namespace, schema, &instance.id),
                        &texts(config, &instance.data),
                    )
                }),
            };
        }
    }
    clear(&sql, namespace, schema)?;
    if let Some(config) = cx.config() {
        cx.each_instance(|instance| {
            index(
                &sql,
                config,
                (namespace, schema, &instance.id),
                &instance.data,
            )
        })?;
    }
    Ok(())
}

/// Search: full-text and vector search over a type's own text fields.
#[derive(Clone, Copy, Debug, Default)]
pub struct Search;

impl Behavior for Search {
    type Config = SearchConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Search.behavior.json")
    }

    // The configSchema holds the shape; this holds the fields to the
    // type's own text fields, and the weights to the fields.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<SearchConfig> {
        let fields: Vec<String> = raw
            .get("fields")
            .and_then(Value::as_array)
            .map(|fields| {
                fields
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default();
        let schemas = target.field_schemas();
        for field in &fields {
            let Some(schema) = schemas.get(field) else {
                return Err(Error::config(format!(
                    "fields: {} has no field {field}",
                    target.type_name()
                )));
            };
            if !is_text(schema) {
                return Err(Error::config(format!(
                    "fields: {}.{field} is not a string, or a scalar whose values are strings",
                    target.type_name()
                )));
            }
        }
        let weighed = raw.get("weights").and_then(Value::as_object);
        for field in weighed.into_iter().flat_map(Map::keys) {
            if !fields.contains(field) {
                return Err(Error::config(format!(
                    "weights: {field} is not an indexed field (fields: {})",
                    fields.join(", ")
                )));
            }
        }
        let weights = fields
            .iter()
            .map(
                |field| match weighed.and_then(|weights| weights.get(field)) {
                    Some(Value::Number(number)) => number_value(number),
                    _ => 1.0,
                },
            )
            .collect();
        let vectors = raw
            .get("vectors")
            .and_then(Value::as_object)
            .map(|vectors| SearchVectors {
                dimensions: vectors
                    .get("dimensions")
                    .and_then(Value::as_number)
                    .map_or(0, |number| number_value(number) as i64),
                model: vectors
                    .get("model")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                permission: vectors
                    .get("permission")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
            });
        Ok(SearchConfig {
            fields,
            weights,
            vectors,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            // Fields, weights and vectors may change, and the behavior may
            // be added to or removed from a schema with instances:
            // afterConfigChange rebuilds or drops the index, and hashes the
            // text again or forgets the vectors.
            config_change: Some(|_, _, _, _| Ok(None)),
            after_config_change: Some(after_config_change),
            guidance: Some(|_, config, target| crate::guidance::search::guidance(config, target)),
            migrations: vec![
                // Each instance's row, which keys its text in the index.
                BehaviorMigration::new(1, "index")
                    .table(
                        TableSpec::new("rows")
                            .row_id("row")
                            .column("namespace", ColumnSpec::text().not_null())
                            .column("schema", ColumnSpec::text().not_null())
                            .column("id", ColumnSpec::text().not_null())
                            .unique(&["namespace", "schema", "id"]),
                    )
                    .up(|sql| sql.create_fulltext("text", &COLUMNS)),
                // Each instance's vector: the hash of the text it is
                // computed from (null without vectors, and for text that
                // holds nothing), the vector (null until it is settled
                // under that hash), and who settled it and when. One index
                // finds what an embedder pulls, the other what a scan
                // reads.
                BehaviorMigration::new(2, "vectors").up(|sql| {
                    let rows = sql.table("rows")?;
                    let stale = sql.table("rows_stale")?;
                    let embedded = sql.table("rows_embedded")?;
                    sql.run(format!("ALTER TABLE {rows} ADD COLUMN source_hash TEXT"), &[])?;
                    sql.run(
                        Sql::per(
                            format!("ALTER TABLE {rows} ADD COLUMN vector BLOB"),
                            format!("ALTER TABLE {rows} ADD COLUMN vector BYTEA"),
                        ),
                        &[],
                    )?;
                    sql.run(
                        Sql::per(
                            format!("ALTER TABLE {rows} ADD COLUMN settled_at INTEGER"),
                            format!("ALTER TABLE {rows} ADD COLUMN settled_at BIGINT"),
                        ),
                        &[],
                    )?;
                    sql.run(format!("ALTER TABLE {rows} ADD COLUMN settled_by TEXT"), &[])?;
                    sql.run(
                        format!("CREATE INDEX {stale} ON {rows} (namespace, schema, row) WHERE source_hash IS NOT NULL AND vector IS NULL"),
                        &[],
                    )?;
                    sql.run(
                        format!("CREATE INDEX {embedded} ON {rows} (namespace, schema, row) WHERE vector IS NOT NULL"),
                        &[],
                    )?;
                    Ok(())
                }),
            ],
            schema_operations: vec![
                ("search", search),
                ("similar", similar),
                ("staleEmbeddings", stale_embeddings),
                ("settleEmbeddings", settle_embeddings),
            ],
            after_change: Some(|_, cx, change| {
                let config = cx.config();
                let sql = cx.sql();
                let instance = (cx.namespace(), cx.schema(), cx.id());
                match change {
                    InstanceChange::Create => index(&sql, config, instance, &*cx.data()?),
                    InstanceChange::Delete => unindex(&sql, instance.0, instance.1, instance.2),
                    InstanceChange::Update { before, .. } => {
                        let data = cx.data()?;
                        if changed(config, before, &data) {
                            index(&sql, config, instance, &data)?;
                        }
                        Ok(())
                    }
                    InstanceChange::Operation { before, .. } => {
                        if let Some(before) = before {
                            let data = cx.data()?;
                            if changed(config, before, &data) {
                                index(&sql, config, instance, &data)?;
                            }
                        }
                        Ok(())
                    }
                }
            }),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_query_of_plain_words_quotes_each_word() {
        let words = |query: &str| expression(query, "words").expect("words");
        assert_eq!(words("macros NOT"), Some("\"macros\" \"NOT\"".to_owned()));
        assert_eq!(words("\"const"), Some("\"\"\"const\"".to_owned()));
        assert_eq!(words(" \u{0} \u{feff}"), None);
        assert_eq!(words("a\u{a0}b"), Some("\"a\" \"b\"".to_owned()));
        assert_eq!(
            expression("title: x", "fts5").map_err(|error| error.to_string()),
            Err("operation search of behavior Search: /query: holds a column filter, which search does not take: a query searches every indexed field".to_owned())
        );
        assert_eq!(
            expression("\"title: x\"", "fts5").expect("fts5"),
            Some("\"title: x\"".to_owned())
        );
    }

    #[test]
    fn similar_searches_for_the_longest_words_then_the_more_frequent_then_the_earlier() {
        assert_eq!(
            likeness("Walnut desk\n\nSolid walnut with brass legs."),
            Some(
                "\"walnut\" OR \"solid\" OR \"brass\" OR \"desk\" OR \"with\" OR \"legs\""
                    .to_owned()
            )
        );
        assert_eq!(likeness("a b"), None);
        assert_eq!(
            likeness("Ünïcödé à la carte"),
            Some("\"ünïcödé\" OR \"carte\"".to_owned())
        );
    }

    #[test]
    fn a_vector_is_held_to_its_dimensions_and_to_32_bit_floats_and_scaled_to_unit_length() {
        let numbers =
            |values: &[f64]| -> Vec<Value> { values.iter().map(|value| json!(value)).collect() };
        assert_eq!(unit(&numbers(&[0.0, 2.0, 0.0]), 3), Ok(vec![0.0, 1.0, 0.0]));
        assert_eq!(
            unit(&numbers(&[1.0, 0.0]), 3),
            Err("holds 2 numbers, and the config's vectors hold 3".to_owned())
        );
        assert_eq!(
            unit(&numbers(&[1.0, 1e39, 0.0]), 3),
            Err("holds 1e+39 at 1, which a 32-bit float cannot hold".to_owned())
        );
        assert_eq!(
            unit(&numbers(&[0.0, 0.0, 0.0]), 3),
            Err("is all zeros, which has no direction to compare".to_owned())
        );
        let stored = encode(&[1.0, -0.5]);
        assert_eq!(stored, [0, 0, 128, 63, 0, 0, 0, 191]);
        assert_eq!(decode(Some(&stored)), Some(vec![1.0, -0.5]));
        assert_eq!(decode(Some(&[1, 2, 3])), None);
    }

    #[test]
    fn a_hash_covers_the_model_the_dimensions_and_the_text() {
        let vectors = SearchVectors {
            dimensions: 3,
            model: "test-3d".to_owned(),
            permission: "notes.embed".to_owned(),
        };
        assert_eq!(
            source_hash(&vectors, "Walnut desk").as_deref(),
            Some("2c11ae39415ae64f66f2085dcc23c550770cf9f7690c70237d2e6bbda44670a0")
        );
        assert_eq!(source_hash(&vectors, ""), None);
        assert_eq!(
            composed(&[Some("  "), None, Some("Oak chair"), Some("\n")]),
            "Oak chair"
        );
    }
}
