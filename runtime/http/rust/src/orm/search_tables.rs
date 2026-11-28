//! Search across tables (D155, D188): one search finds a row of any
//! `@searchable` table, for a UI and for an agent alike, as the TypeScript
//! runtime's `search-tables.ts` ranks, indexes and serves it, with its
//! statements, its ranking and its refusals.
//!
//! The database keeps one index table. Each `@searchable` table's rows have
//! a row each in it: the record's table and key, its title, its body, the
//! text of its conversation (Comments) and its files' extracted text
//! (`@attachments`), its scope, an optional embedding, and its effective
//! audience and agent access (D156). A trigger on the table deletes the
//! index row with its row, and copies a change of the row's audience into
//! it, in the writer's transaction; every write that changes the indexed
//! text enqueues the row on the declaration's queue, whose worker calls
//! [`index_search`]. So a search's access check is never behind the row,
//! and its text is behind it by the queue's drain.
//!
//! [`search_tables`] ranks every table's index rows in one query, so its
//! pages and its total are exact and a row the caller may not read never
//! shows, as a gap or otherwise:
//!
//! - The words: the query is read into a plan ([`query`], #740): quoted
//!   phrases, prefixes (`wal*`), excluded terms (`-oak`) and `OR` between
//!   terms, each term a phrase of the words Postgres's parser finds in it.
//!   A row matches when it holds a term of every group and no excluded
//!   term, in the definition's text search configuration (`simple`, or
//!   `search_unaccent`, which folds diacritics).
//! - The order: `ts_rank_cd` over the row's words, weighed by where each
//!   is (title A, body B, conversation C, files D), then the table and the
//!   key. With an embedding, the first [`RRF_WINDOW`] by text are re-ranked
//!   by reciprocal rank fusion of their text place and their place by
//!   cosine similarity, [`RRF_K`] the engine's constant. A query with no
//!   term to hold that brings a vector ranks by the vector alone: every
//!   index row the caller may read whose embedding is of the model, by
//!   cosine similarity (#740).
//! - Access: the caller's audience is checked on the index row's copied
//!   columns ([`audience_conditions`](super::audience_conditions)). A caller
//!   with grants, a party or an agent run, reaches a table's rows only
//!   through its grants' relation paths, which no copy can hold: for it
//!   each table's rows are kept by an `EXISTS` back to the table, with the
//!   conditions the table's own repository applies for that caller
//!   (`Repository::conditions`). A table no grant reaches is left out.
//! - `scope` is the app's filter: the value of the field each declaration
//!   names as its scope, copied into the index row.
//!
//! Embeddings re-rank through a model provider that embeds (D159,
//! `ModelProvider::embed`): `search_embeddings_of`, on Postgres alone.
//!
//! On a SQLite file ([`sqlite`], D186, #615) the index is the same table,
//! its audience a JSON array (D182), and its words an FTS5 table beside it
//! that the index table's triggers keep: a search matches the plan's FTS5
//! expression and ranks by bm25 with the weights Postgres gives
//! the title, the body, the conversation and the files, as the TypeScript
//! `searchOnSQLite` and the Go `crosstable` do. Embeddings are refused
//! there.

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

use super::Error;
use crate::BoxError;

/// An `@audience` table's resolved columns, quoted.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[allow(missing_docs)]
pub struct SearchAudience {
    pub effective: String,
    pub agents: String,
}

/// A table's Comments messages, whose posted bodies an index row holds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[allow(missing_docs)]
pub struct SearchComments {
    pub table: String,
    pub row: String,
    pub body: String,
    pub state: String,
    pub order: String,
}

/// A table's files, whose extracted text an index row holds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[allow(missing_docs)]
pub struct SearchFiles {
    pub table: String,
    pub row: String,
    pub text: String,
    pub state: String,
}

/// A `@searchable` table's index as its definition carries it (D155): the
/// index table, quoted, and the expression of an index row's weighed
/// words over it as `si`; the table, its key column and the key's SQL
/// type, and whether its wire form is base62; the columns an index row's
/// title, body and scope are of; a soft-deleting table's `deleted_at`; an
/// `@audience` table's resolved columns; and the sub-tables whose text it
/// holds.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct SearchableDefinition {
    pub index: String,
    pub document: String,
    pub table: String,
    pub key: String,
    pub key_type: String,
    #[serde(default)]
    pub uuid: bool,
    pub title: String,
    #[serde(default)]
    pub body: Vec<String>,
    #[serde(default)]
    pub scope: Option<String>,
    #[serde(default)]
    pub deleted_at: Option<String>,
    #[serde(default)]
    pub audience: Option<SearchAudience>,
    #[serde(default)]
    pub comments: Option<SearchComments>,
    #[serde(default)]
    pub files: Option<SearchFiles>,
    /// The FTS5 table the index's words are in, on SQLite (D186); none on
    /// Postgres, whose `document` weighs them.
    #[serde(default)]
    pub fts: Option<String>,
    /// The text search configuration the index's words are in on
    /// Postgres, `simple` when none; `search_unaccent` folds diacritics
    /// (#740).
    #[serde(default)]
    pub config: Option<String>,
}

/// A search.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SearchRequest {
    /// The query (#740): words and quoted phrases a row holds each of,
    /// prefixes (`wal*`), excluded terms (`-oak`) and `OR` between terms.
    pub q: String,
    /// Only these tables, by type name; every `@searchable` table the
    /// caller may read when absent.
    pub tables: Option<Vec<String>>,
    /// Only rows whose scope is one of these values.
    pub scope: Option<Vec<String>>,
    /// At most this many hits (20 unless set, at most 100) after `offset`
    /// (at most 10000).
    pub limit: Option<i64>,
    pub offset: Option<i64>,
    /// A query embedding, of [`SearchEmbeddings::model`], which re-ranks
    /// the first hits; the options' embeddings compute one when absent.
    pub vector: Option<Vec<f64>>,
}

/// A part of a snippet: text, which a match is marked in.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnippetPart {
    /// The text.
    pub text: String,
    /// A match of the words.
    #[serde(default, rename = "match", skip_serializing_if = "std::ops::Not::not")]
    pub matched: bool,
}

/// One hit.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SearchHit {
    /// The row's table, by type name.
    pub table: String,
    /// The row's key in its wire form.
    pub key: String,
    /// The row's title.
    pub title: String,
    /// Where the words matched first: title, body, comments or files.
    pub matched: String,
    /// A few words around the matches of that text, each match marked.
    pub snippet: Vec<SnippetPart>,
    /// Its place in the whole ranking, from 1.
    pub place: u64,
}

/// A page of hits.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SearchResult {
    /// The hits.
    pub items: Vec<SearchHit>,
    /// How many rows match, of those the caller may read.
    pub total: u64,
    /// Whether rows follow the page.
    pub more: bool,
    /// Whether embeddings re-ranked the first hits.
    pub reranked: bool,
}

/// What embeds texts: a model provider that embeds (D159), or a test's
/// stand-in.
#[async_trait]
pub trait Embedder: Send + Sync {
    /// One vector per text, in order.
    async fn embed(&self, texts: &[String]) -> Result<Vec<Vec<f64>>, BoxError>;
}

/// The embeddings a search re-ranks by and [`index_search`] computes: a
/// model's name, which an index row records with its embedding, and what
/// embeds texts. Off unless given: no index row is embedded and no search
/// re-ranked.
#[derive(Clone)]
pub struct SearchEmbeddings {
    /// The model's name.
    pub model: String,
    /// What embeds.
    pub embedder: Arc<dyn Embedder>,
    /// The most characters of a row's text it embeds; 8000 unless set.
    pub max_chars: Option<usize>,
}

impl std::fmt::Debug for SearchEmbeddings {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SearchEmbeddings")
            .field("model", &self.model)
            .field("max_chars", &self.max_chars)
            .finish_non_exhaustive()
    }
}

/// What a search and its indexing take beside the repositories.
#[derive(Clone, Debug, Default)]
pub struct SearchOptions {
    /// The permission each table's rows need, by type name: a caller
    /// without it finds none of them.
    pub permissions: BTreeMap<String, String>,
    /// Embeddings, off unless set.
    pub embeddings: Option<SearchEmbeddings>,
}

/// A message of a `@searchable` declaration's queue: a row to index again.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchIndexing {
    /// The row's table type.
    pub source_table: String,
    /// The row's key, as text.
    pub source_key: String,
}

/// What [`index_search`] did.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Indexed {
    /// It wrote the row's index row.
    Indexed,
    /// The row is gone or soft deleted, and its index row with it.
    Removed,
    /// The table is no `@searchable` table of the database.
    Unknown,
}

/// Reciprocal rank fusion's constant and window, the engine's
/// (`superschematic_engine::search`, D16).
pub const RRF_K: u32 = 60;
pub const RRF_WINDOW: u32 = 200;
/// The most words of a query that count.
pub const MAX_SEARCH_WORDS: usize = 16;
const MAX_WORD: usize = 200;
/// The most characters of each of an index row's texts.
pub const MAX_INDEXED_TEXT: usize = 262_144;
const MAX_LIMIT: i64 = 100;
const MAX_OFFSET: i64 = 10_000;
/// The marks a snippet's matches are between, which indexed text never
/// holds.
const OPEN: char = '\u{2}';
const CLOSE: char = '\u{3}';
/// `ts_rank_cd`'s weights of the classes D, C, B and A.
#[cfg(feature = "postgres")]
const WEIGHTS: &str = "'{0.1, 0.2, 0.4, 1}'::real[]";

fn is_mark(c: char) -> bool {
    matches!(c, '\u{0}' | OPEN | CLOSE)
}

/// The words of a query that count: its runs of characters that are not
/// white space, the first [`MAX_SEARCH_WORDS`], each at most 200
/// characters.
pub fn search_words(q: &str) -> Vec<String> {
    q.split(char::is_whitespace)
        .map(|word| word.chars().filter(|c| !is_mark(*c)).collect::<String>())
        .filter(|word| !word.is_empty())
        .take(MAX_SEARCH_WORDS)
        .map(|word| word.chars().take(MAX_WORD).collect())
        .collect()
}

#[cfg(any(feature = "postgres", feature = "sqlite"))]
fn whole(value: Option<i64>, fallback: i64, min: i64, max: i64, what: &str) -> Result<i64, Error> {
    match value {
        None => Ok(fallback),
        Some(value) if (min..=max).contains(&value) => Ok(value),
        Some(value) => Err(Error::Invalid(format!(
            "search_tables: {what} is a whole number from {min} to {max}, not {value}"
        ))),
    }
}

/// A vector of unit length, the zero vector as itself; a vector that
/// holds a number that is not finite is refused.
#[cfg(feature = "postgres")]
fn unit(vector: &[f64]) -> Result<Vec<f64>, Error> {
    let mut sum = 0.0;
    for x in vector {
        if !x.is_finite() {
            return Err(Error::Invalid(
                "an embedding holds finite numbers".to_owned(),
            ));
        }
        sum += x * x;
    }
    let norm = sum.sqrt();
    Ok(if norm == 0.0 {
        vec![0.0; vector.len()]
    } else {
        vector.iter().map(|x| x / norm).collect()
    })
}

/// A snippet's parts, from `ts_headline`'s or FTS5's `snippet` text with
/// the marks around each match.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
fn snippet_parts(text: &str) -> Vec<SnippetPart> {
    let mut parts = Vec::new();
    let mut rest = text;
    while !rest.is_empty() {
        let Some(open) = rest.find(OPEN) else {
            parts.push(SnippetPart {
                text: rest.to_owned(),
                matched: false,
            });
            break;
        };
        if open > 0 {
            parts.push(SnippetPart {
                text: rest[..open].to_owned(),
                matched: false,
            });
        }
        let after = open + OPEN.len_utf8();
        let close = rest[after..].find(CLOSE).map(|i| i + after);
        let end = close.unwrap_or(rest.len());
        if end > after {
            parts.push(SnippetPart {
                text: rest[after..end].to_owned(),
                matched: true,
            });
        }
        rest = match close {
            Some(close) => &rest[close + CLOSE.len_utf8()..],
            None => "",
        };
    }
    parts
}

/// An index row's text: its marks made spaces, at most
/// [`MAX_INDEXED_TEXT`] characters, none when it is blank.
#[cfg(any(feature = "postgres", feature = "sqlite"))]
fn clean(text: Option<&str>) -> Option<String> {
    let text = text?;
    let out: String = text
        .chars()
        .map(|c| if is_mark(c) { ' ' } else { c })
        .take(MAX_INDEXED_TEXT)
        .collect();
    (!out.trim().is_empty()).then_some(out)
}

#[cfg(any(feature = "postgres", feature = "sqlite"))]
fn hex_sha256(text: &str) -> String {
    ring::digest::digest(&ring::digest::SHA256, text.as_bytes())
        .as_ref()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// Search as an MCP tool (D155): what `tools/list` answers of it.
pub fn search_mcp_tool() -> Value {
    json!({
        "name": "search",
        "title": "Search",
        "description": "Finds the rows of every searchable table that hold all the given words, best first, of those the caller may read: each hit names its table and key, with its title and a snippet.",
        "inputSchema": {
            "type": "object",
            "additionalProperties": false,
            "required": ["q"],
            "properties": {
                "q": { "type": "string", "minLength": 1, "description": "The words to find; a row matches when it holds every one." },
                "tables": { "type": "array", "items": { "type": "string" }, "description": "Only these tables, by name." },
                "scope": { "type": "array", "items": { "type": "string" }, "description": "Only rows in this scope, the app's filter: a workspace or a project." },
                "limit": { "type": "integer", "minimum": 1, "maximum": MAX_LIMIT, "description": "At most this many hits; 20 unless set." },
                "offset": { "type": "integer", "minimum": 0, "maximum": MAX_OFFSET, "description": "Hits to skip, for the next page." }
            }
        },
        "annotations": { "readOnlyHint": true, "idempotentHint": true }
    })
}

/// The search a call of [`search_mcp_tool`] asks for, or the refusal of
/// arguments it cannot read.
pub fn search_tool_request(args: &Map<String, Value>) -> Result<SearchRequest, Error> {
    let Some(Value::String(q)) = args.get("q") else {
        return Err(Error::Invalid("q is required".to_owned()));
    };
    let strings = |value: Option<&Value>| -> Option<Vec<String>> {
        match value? {
            Value::Array(items) => Some(
                items
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect(),
            ),
            Value::String(one) => Some(vec![one.clone()]),
            _ => None,
        }
    };
    let number = |name: &str| -> Result<Option<i64>, Error> {
        match args.get(name) {
            Some(Value::Number(n)) => match n.as_i64() {
                Some(n) => Ok(Some(n)),
                None => Err(Error::Invalid(format!(
                    "search_tables: {name} is a whole number, not {n}"
                ))),
            },
            _ => Ok(None),
        }
    };
    Ok(SearchRequest {
        q: q.clone(),
        tables: strings(args.get("tables")),
        scope: strings(args.get("scope")),
        limit: number("limit")?,
        offset: number("offset")?,
        vector: None,
    })
}

#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use shared::*;

/// Search across tables on a SQLite file (D186, #615).
#[cfg(feature = "sqlite")]
pub mod sqlite;

pub mod query;
pub use query::{fts_query, postgres_query, search_plan, SearchPlan, SearchTerm, MAX_QUERY_TERMS};

#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod shared {
    use http::request::Parts;
    use serde_json::Value;

    use super::{SearchRequest, SearchableDefinition};
    use crate::orm::Definition;
    use crate::ApiError;

    /// The `@searchable` tables of `definition`, by type name, in the
    /// definition's order.
    pub fn search_sources_of(definition: &Definition) -> Vec<(&str, &SearchableDefinition)> {
        definition
            .tables
            .iter()
            .filter_map(|t| t.searchable.as_ref().map(|s| (t.name.as_str(), s)))
            .collect()
    }

    pub(super) fn text_of(value: &Value) -> String {
        match value {
            Value::String(text) => text.clone(),
            Value::Null => String::new(),
            other => other.to_string(),
        }
    }

    /// A qualified column's own name.
    pub(super) fn quoted_of(column: &str) -> &str {
        column.rsplit('.').next().unwrap_or(column)
    }

    fn decode(text: &str) -> String {
        let bytes = text.as_bytes();
        let mut out = Vec::with_capacity(bytes.len());
        let mut i = 0;
        while i < bytes.len() {
            match bytes[i] {
                b'+' => out.push(b' '),
                b'%' if i + 2 < bytes.len() => {
                    match text
                        .get(i + 1..i + 3)
                        .and_then(|h| u8::from_str_radix(h, 16).ok())
                    {
                        Some(byte) => {
                            out.push(byte);
                            i += 2;
                        }
                        None => out.push(b'%'),
                    }
                }
                other => out.push(other),
            }
            i += 1;
        }
        String::from_utf8_lossy(&out).into_owned()
    }

    /// The search a request of the route asks for:
    /// `?q&tables&scope&limit&offset`, a list comma-separated or
    /// repeated; a 400 for a number it cannot read.
    pub(super) fn request_of(parts: &Parts) -> Result<SearchRequest, ApiError> {
        let pairs: Vec<(String, String)> = parts
            .uri
            .query()
            .unwrap_or_default()
            .split('&')
            .filter(|pair| !pair.is_empty())
            .map(|pair| {
                let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
                (decode(name), decode(value))
            })
            .collect();
        let list = |name: &str| -> Vec<String> {
            pairs
                .iter()
                .filter(|(n, _)| n == name)
                .flat_map(|(_, v)| v.split(',').map(str::to_owned).collect::<Vec<_>>())
                .filter(|v| !v.is_empty())
                .collect()
        };
        let number = |name: &str| -> Result<Option<i64>, ApiError> {
            let Some((_, value)) = pairs.iter().find(|(n, _)| n == name) else {
                return Ok(None);
            };
            value.parse::<i64>().map(Some).map_err(|_| {
                ApiError::bad_request(format!(
                    "search_tables: {name} is a whole number, not {value}"
                ))
            })
        };
        let tables = list("tables");
        let scope = list("scope");
        Ok(SearchRequest {
            q: pairs
                .iter()
                .find(|(n, _)| n == "q")
                .map(|(_, v)| v.clone())
                .unwrap_or_default(),
            tables: (!tables.is_empty()).then_some(tables),
            scope: (!scope.is_empty()).then_some(scope),
            limit: number("limit")?,
            offset: number("offset")?,
            vector: None,
        })
    }
}

#[cfg(feature = "postgres")]
pub use pg::*;

#[cfg(feature = "postgres")]
mod pg {
    use std::sync::Arc;

    use axum::extract::{Request, State};
    use axum::response::{IntoResponse, Response};
    use axum::routing::any;
    use axum::{Json, Router};
    use deadpool_postgres::GenericClient;
    use http::{Method, StatusCode};
    use serde_json::{json, Map, Value};

    use super::super::postgres::{query_rows, Repositories, Tables};
    use super::super::{
        audience_conditions, AccessCaller, Audience, Bound, Definition, Error, Marks,
    };
    use super::shared::{quoted_of, request_of, text_of};
    use super::*;
    use crate::auth::{Authenticator, Principal};
    use crate::providers::{ModelEmbedRequest, ModelProvider};
    use crate::response::error_response;
    use crate::uuid62::{parse_uuid, uuid_to_base62};
    use crate::ApiError;

    fn bound(value: Value) -> Bound {
        Bound {
            value,
            marks: Marks::TEXT,
        }
    }

    /// Appends `value` to `args` and answers its placeholder.
    fn param(args: &mut Vec<Bound>, value: Value) -> String {
        args.push(bound(value));
        format!("${}", args.len())
    }

    /// Searches every `@searchable` table as `principal` (D155): the rows
    /// that hold every word of `request.q`, of those `principal` may read,
    /// best first, one page of them, and how many there are. A table a
    /// caller lacks the permission of (`options.permissions`) is left out,
    /// as is every table a caller with grants reaches no row of.
    /// `repositories` lend their client and session; whom they keep to is
    /// replaced by the principal.
    pub async fn search_tables<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        principal: Option<&Principal>,
        request: &SearchRequest,
        options: &SearchOptions,
    ) -> Result<SearchResult, Error> {
        let limit = whole(request.limit, 20, 1, MAX_LIMIT, "limit")?;
        let offset = whole(request.offset, 0, 0, MAX_OFFSET, "offset")?;
        let plan = search_plan(&request.q);
        // A query with no term to hold matches nothing, unless a vector
        // ranks it.
        if plan.all.is_empty() && request.vector.is_none() {
            return Ok(SearchResult::default());
        }
        let definition = repositories.definition();
        let caller = AccessCaller::of(principal);
        let db = Repositories::new(repositories.client(), definition)?
            .with_session(repositories.session().clone())
            .with_access(caller.clone());
        let all = search_sources_of(definition);
        for table in request.tables.iter().flatten() {
            if !all.iter().any(|(name, _)| name == table) {
                return Err(Error::Invalid(format!(
                    "search_tables: {table} is no @searchable table"
                )));
            }
        }
        let sources: Vec<(&str, &SearchableDefinition)> = all
            .iter()
            .copied()
            .filter(|(table, _)| {
                if request
                    .tables
                    .as_ref()
                    .is_some_and(|tables| !tables.iter().any(|t| t == table))
                {
                    return false;
                }
                match options.permissions.get(*table) {
                    Some(permission) => {
                        principal.is_some_and(|p| p.permissions.contains(permission))
                    }
                    None => true,
                }
            })
            .collect();
        let Some((_, first)) = sources.first() else {
            return Ok(SearchResult::default());
        };
        let (index, document) = (first.index.clone(), first.document.clone());
        let regconfig = format!(
            "'{}'::regconfig",
            first.config.as_deref().unwrap_or("simple")
        );

        let mut args: Vec<Bound> = Vec::new();
        let by_vector = plan.all.is_empty();
        let query = postgres_query(&plan, &regconfig, &mut |value| {
            param(&mut args, Value::String(value))
        });
        let mut conditions = if !by_vector {
            vec![format!("{document} @@ q.q")]
        } else if plan.none.is_empty() {
            Vec::new()
        } else {
            vec![format!("NOT ({document} @@ ({query}))")]
        };
        // The tables: for a caller with grants, each through its
        // repository's conditions for that caller, an EXISTS back to its
        // rows.
        if caller.grants.is_none() {
            let names = sources.iter().map(|(t, _)| Value::from(*t)).collect();
            let p = param(&mut args, Value::Array(names));
            conditions.push(format!("si.source_table = ANY({p}::text[])"));
        } else {
            let mut branches = Vec::new();
            for (table, d) in &sources {
                let kept = db.table(table)?.conditions(&mut args)?;
                if kept.len() == 1 && kept[0] == "FALSE" {
                    continue;
                }
                let name = param(&mut args, Value::from(*table));
                let mut all = vec![format!(
                    "{}.{} = si.source_key::{}",
                    d.table, d.key, d.key_type
                )];
                all.extend(kept);
                branches.push(format!(
                    "(si.source_table = {name} AND EXISTS (SELECT 1 FROM {} WHERE {}))",
                    d.table,
                    all.join(" AND ")
                ));
            }
            if branches.is_empty() {
                return Ok(SearchResult::default());
            }
            conditions.push(format!("({})", branches.join(" OR ")));
        }
        // The caller's audience, on the index row's copy of each row's.
        let copied = Audience {
            effective: "si.audience".to_owned(),
            agents: "si.agents".to_owned(),
            probe: None,
            resolve: None,
        };
        conditions.extend(audience_conditions(
            definition, &copied, &caller, &mut args, false,
        )?);
        if let Some(scope) = &request.scope {
            let mut forms: Vec<Value> = Vec::new();
            for value in scope {
                let mut add = |form: String| {
                    let form = Value::String(form);
                    if !forms.contains(&form) {
                        forms.push(form);
                    }
                };
                add(value.clone());
                if let Some(uuid) = parse_uuid(value) {
                    add(uuid.hyphenated().to_string());
                }
            }
            let p = param(&mut args, Value::Array(forms));
            conditions.push(format!("si.scope = ANY({p}::text[])"));
        }
        if by_vector {
            let Some(embeddings) = &options.embeddings else {
                return Err(Error::Invalid(
                    "search_tables: a vector needs embeddings, which say its model".to_owned(),
                ));
            };
            let vector = unit(request.vector.as_deref().unwrap_or_default())?;
            return search_by_vector(
                repositories.client(),
                &all,
                &index,
                conditions,
                args,
                vector,
                &embeddings.model,
                (limit, offset),
            )
            .await;
        }

        // The query's embedding, when embeddings are on or the request
        // brings one.
        let mut vector: Option<Vec<f64>> = None;
        let mut model: Option<String> = None;
        if let Some(given) = &request.vector {
            let Some(embeddings) = &options.embeddings else {
                return Err(Error::Invalid(
                    "search_tables: a vector needs embeddings, which say its model".to_owned(),
                ));
            };
            vector = Some(unit(given)?);
            model = Some(embeddings.model.clone());
        } else if let Some(embeddings) = &options.embeddings {
            // A model that fails leaves the text ranking as it is.
            if let Ok(embedded) = embeddings
                .embedder
                .embed(std::slice::from_ref(&request.q))
                .await
            {
                if let Some(first) = embedded.into_iter().next().filter(|v| !v.is_empty()) {
                    if let Ok(first) = unit(&first) {
                        vector = Some(first);
                        model = Some(embeddings.model.clone());
                    }
                }
            }
        }

        let ranked = "ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank DESC, h.source_table, h.source_key) AS text_place
      FROM hits AS h
    )";
        let mut order = "r.text_place".to_owned();
        let mut fused = String::new();
        let reranked = vector.is_some();
        if let (Some(vector), Some(model)) = (vector, model) {
            let v = param(
                &mut args,
                Value::Array(vector.into_iter().map(Value::from).collect()),
            );
            let m = param(&mut args, Value::String(model));
            fused = format!(
                ",
    scored AS (
      SELECT r.*, CASE WHEN r.text_place <= {RRF_WINDOW} AND r.embedding IS NOT NULL AND r.embedding_model = {m}
        AND cardinality(r.embedding) = cardinality({v}::float8[])
        THEN (SELECT sum(a * b) FROM unnest(r.embedding, {v}::float8[]) AS t(a, b)) END AS similarity
      FROM ranked AS r
    ),
    placed AS (
      SELECT s.*, CASE WHEN s.similarity IS NOT NULL
        THEN row_number() OVER (PARTITION BY s.similarity IS NOT NULL ORDER BY s.similarity DESC, s.text_place) END AS vector_place
      FROM scored AS s
    )"
            );
            order = format!(
                "(r.text_place > {RRF_WINDOW}), CASE WHEN r.text_place <= {RRF_WINDOW} THEN 1.0 / ({RRF_K} + r.text_place) + COALESCE(1.0 / ({RRF_K} + r.vector_place), 0) END DESC, r.text_place"
            );
        }
        let headline = format!(
            "'StartSel={OPEN}, StopSel={CLOSE}, MaxWords=24, MinWords=12, ShortWord=0, HighlightAll=false, MaxFragments=0'"
        );
        let matches =
            |column: &str| format!("to_tsvector({regconfig}, COALESCE(p.{column}, '')) @@ q.q");
        let (mt, mb, mc, mf) = (
            matches("title"),
            matches("body"),
            matches("comments"),
            matches("files"),
        );
        let from = if fused.is_empty() { "ranked" } else { "placed" };
        let sql = format!(
            "WITH q AS (SELECT {query} AS q),
    hits AS (
      SELECT si.source_table, si.source_key, si.title, si.body, si.comments, si.files, si.embedding, si.embedding_model,
        ts_rank_cd({WEIGHTS}, {document}, q.q) AS rank
      FROM {index} AS si CROSS JOIN q
      WHERE {where_}
    ),
    {ranked}{fused}
    SELECT c.total, p.source_table, p.source_key, p.title, p.matched,
      ts_headline({regconfig}, p.shown, q.q, {headline}) AS snippet
    FROM (SELECT count(*) AS total FROM hits) AS c
    CROSS JOIN q
    LEFT JOIN LATERAL (
      SELECT r.source_table, r.source_key, r.title, r.ord,
        CASE WHEN {mt} THEN 'title' WHEN {mb} THEN 'body' WHEN {mc} THEN 'comments' WHEN {mf} THEN 'files' ELSE 'title' END AS matched,
        CASE WHEN {mt} THEN p.title WHEN {mb} THEN p.body WHEN {mc} THEN p.comments WHEN {mf} THEN p.files ELSE p.title END AS shown
      FROM (
        SELECT r.*, row_number() OVER (ORDER BY {order}) AS ord
        FROM {from} AS r
        ORDER BY {order}
        LIMIT {limit} OFFSET {offset}
      ) AS r
      CROSS JOIN LATERAL (SELECT r.title, r.body, r.comments, r.files) AS p
    ) AS p ON TRUE
    ORDER BY p.ord",
            where_ = conditions.join(" AND "),
        );
        let rows = query_rows(repositories.client(), &sql, &args).await?;
        let total = rows
            .first()
            .and_then(|row| row.first())
            .map_or(0, |value| match value {
                Value::Number(n) => n.as_u64().unwrap_or(0),
                Value::String(s) => s.parse().unwrap_or(0),
                _ => 0,
            });
        let offset = u64::try_from(offset).unwrap_or(0);
        let mut items: Vec<SearchHit> = Vec::new();
        for row in &rows {
            let Some(Value::String(table)) = row.get(1) else {
                continue;
            };
            let uuid = all.iter().any(|(name, d)| name == table && d.uuid);
            let key = text_of(row.get(2).unwrap_or(&Value::Null));
            let key = if uuid {
                parse_uuid(&key).map_or(key, uuid_to_base62)
            } else {
                key
            };
            items.push(SearchHit {
                table: table.clone(),
                key,
                title: text_of(row.get(3).unwrap_or(&Value::Null)),
                matched: text_of(row.get(4).unwrap_or(&Value::Null)),
                snippet: snippet_parts(&text_of(row.get(5).unwrap_or(&Value::Null))),
                place: offset + items.len() as u64 + 1,
            });
        }
        let more = offset + (items.len() as u64) < total;
        Ok(SearchResult {
            items,
            total,
            more,
            reranked,
        })
    }

    /// A search by vector alone (#740), the TypeScript runtime's
    /// `searchByVector`: every index row `conditions` keep whose embedding
    /// is of `model` and of `vector`'s length, by cosine similarity, best
    /// first, then table and key; one page of them and how many there are,
    /// each matched by its title. `args` are the values `conditions` bind.
    #[allow(clippy::too_many_arguments)]
    async fn search_by_vector<C: GenericClient + Sync>(
        client: &C,
        all: &[(&str, &SearchableDefinition)],
        index: &str,
        mut conditions: Vec<String>,
        mut args: Vec<Bound>,
        vector: Vec<f64>,
        model: &str,
        (limit, offset): (i64, i64),
    ) -> Result<SearchResult, Error> {
        let v = param(
            &mut args,
            Value::Array(vector.into_iter().map(Value::from).collect()),
        );
        let m = param(&mut args, Value::String(model.to_owned()));
        conditions.push("si.embedding IS NOT NULL".to_owned());
        conditions.push(format!("si.embedding_model = {m}"));
        conditions.push(format!(
            "cardinality(si.embedding) = cardinality({v}::float8[])"
        ));
        let sql = format!(
            "WITH hits AS (
      SELECT si.source_table, si.source_key, si.title,
        (SELECT sum(a * b) FROM unnest(si.embedding, {v}::float8[]) AS t(a, b)) AS similarity
      FROM {index} AS si
      WHERE {where_}
    ),
    ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.similarity DESC, h.source_table, h.source_key) AS place FROM hits AS h
    )
    SELECT c.total, p.source_table, p.source_key, p.title, p.place
    FROM (SELECT count(*) AS total FROM hits) AS c
    LEFT JOIN (SELECT * FROM ranked ORDER BY place LIMIT {limit} OFFSET {offset}) AS p ON TRUE
    ORDER BY p.place",
            where_ = conditions.join(" AND "),
        );
        let rows = query_rows(client, &sql, &args).await?;
        let total = rows
            .first()
            .and_then(|row| row.first())
            .map_or(0, |value| match value {
                Value::Number(n) => n.as_u64().unwrap_or(0),
                Value::String(s) => s.parse().unwrap_or(0),
                _ => 0,
            });
        let offset = u64::try_from(offset).unwrap_or(0);
        let mut items: Vec<SearchHit> = Vec::new();
        for row in &rows {
            let Some(Value::String(table)) = row.get(1) else {
                continue;
            };
            let uuid = all.iter().any(|(name, d)| name == table && d.uuid);
            let key = text_of(row.get(2).unwrap_or(&Value::Null));
            let key = if uuid {
                parse_uuid(&key).map_or(key, uuid_to_base62)
            } else {
                key
            };
            let title = text_of(row.get(3).unwrap_or(&Value::Null));
            let snippet = if title.is_empty() {
                Vec::new()
            } else {
                vec![SnippetPart {
                    text: title.clone(),
                    matched: false,
                }]
            };
            items.push(SearchHit {
                table: table.clone(),
                key,
                title,
                matched: "title".to_owned(),
                snippet,
                place: offset + items.len() as u64 + 1,
            });
        }
        let more = offset + (items.len() as u64) < total;
        Ok(SearchResult {
            items,
            total,
            more,
            reranked: true,
        })
    }

    /// Builds the index row of the row a `@searchable` declaration's queue
    /// names (D155), as the system, which its worker calls for each
    /// message: the row's title, body, scope, conversation and files'
    /// text, and its access columns, read under a share lock of the row in
    /// a transaction of `client`'s, so a change of its audience waits for
    /// it and copies into the row it writes. A row that is gone, or soft
    /// deleted, has its index row deleted. With embeddings on, a row whose
    /// text changed is embedded after the transaction, and its embedding
    /// stored only while its text is still what was embedded; a model that
    /// fails is the error.
    pub async fn index_search<C: GenericClient + Sync>(
        client: &mut C,
        definition: &Definition,
        message: &SearchIndexing,
        options: &SearchOptions,
    ) -> Result<Indexed, Error> {
        let Some((_, d)) = search_sources_of(definition)
            .into_iter()
            .find(|(name, _)| *name == message.source_table)
        else {
            return Ok(Indexed::Unknown);
        };
        let failed =
            |err: &tokio_postgres::Error| super::super::postgres::failure("index_search", err);
        let source = [
            bound(Value::String(message.source_table.clone())),
            bound(Value::String(message.source_key.clone())),
        ];
        let delete = format!(
            "DELETE FROM {} WHERE source_table = $1 AND source_key = $2",
            d.index
        );
        let tx = client.transaction().await.map_err(|err| failed(&err))?;
        let written = {
            let mut columns = vec![d.title.clone()];
            columns.extend(d.body.iter().cloned());
            columns.push(format!("{}::text", d.key));
            columns.push(match &d.scope {
                Some(scope) => format!("{scope}::text"),
                None => "NULL::text".to_owned(),
            });
            columns.push(match &d.deleted_at {
                Some(at) => format!("{at} IS NOT NULL"),
                None => "FALSE".to_owned(),
            });
            let sql = format!(
                "SELECT {} FROM {} WHERE {} = $1::{} FOR SHARE",
                columns.join(", "),
                d.table,
                d.key,
                d.key_type
            );
            let key_arg = [bound(Value::String(message.source_key.clone()))];
            let row = query_rows(&tx, &sql, &key_arg).await?.into_iter().next();
            let body_count = d.body.len();
            match row {
                Some(row) if row.get(body_count + 3) != Some(&Value::Bool(true)) => {
                    let title = clean(row.first().and_then(Value::as_str)).unwrap_or_default();
                    let parts: Vec<String> = row[1..=body_count]
                        .iter()
                        .filter(|v| !v.is_null())
                        .map(text_of)
                        .collect();
                    let body = clean(Some(&parts.join("\n")));
                    let key = text_of(&row[body_count + 1]);
                    let scope = row[body_count + 2].clone();
                    let mut comments = None;
                    if let Some(c) = &d.comments {
                        let sql = format!(
                            "SELECT string_agg({body}, E'\\n' ORDER BY {order}) AS text FROM {table} WHERE {row} = $1::{key_type} AND {state} IN ('posted', 'interrupted') AND {body} <> ''",
                            body = c.body,
                            order = c.order,
                            table = c.table,
                            row = c.row,
                            key_type = d.key_type,
                            state = c.state,
                        );
                        let read = query_rows(&tx, &sql, &key_arg).await?;
                        comments = clean(read.first().and_then(|r| r[0].as_str()));
                    }
                    let mut files = None;
                    if let Some(f) = &d.files {
                        let sql = format!(
                            "SELECT string_agg({text}, E'\\n' ORDER BY {text}) AS text FROM {table} WHERE {row} = $1::{key_type} AND {state} = 'ready' AND {text} IS NOT NULL",
                            text = f.text,
                            table = f.table,
                            row = f.row,
                            key_type = d.key_type,
                            state = f.state,
                        );
                        let read = query_rows(&tx, &sql, &key_arg).await?;
                        files = clean(read.first().and_then(|r| r[0].as_str()));
                    }
                    let text = [
                        Some(title.clone()),
                        body.clone(),
                        comments.clone(),
                        files.clone(),
                    ]
                    .into_iter()
                    .flatten()
                    .collect::<Vec<_>>()
                    .join("\n");
                    let hash = hex_sha256(&text);
                    // The access columns come from the row itself, which the
                    // share lock holds as it is until the index row is
                    // written.
                    let (audience, agents) = match &d.audience {
                        Some(a) => (
                            format!("s.{}", quoted_of(&a.effective)),
                            format!("s.{}", quoted_of(&a.agents)),
                        ),
                        None => ("NULL::uuid[]".to_owned(), "'followRole'".to_owned()),
                    };
                    let sql = format!(
                        "INSERT INTO {index} AS i (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, {audience}, {agents}, $8::text, now() FROM {table} AS s WHERE s.{key} = $9::{key_type}
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
         audience = EXCLUDED.audience, agents = EXCLUDED.agents, indexed_at = EXCLUDED.indexed_at,
         embedding = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding END,
         embedding_model = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding_model END,
         text_hash = EXCLUDED.text_hash
       RETURNING embedding_model",
                        index = d.index,
                        table = d.table,
                        key = d.key,
                        key_type = d.key_type,
                    );
                    let optional = |text: Option<String>| text.map_or(Value::Null, Value::String);
                    let args = [
                        bound(Value::String(message.source_table.clone())),
                        bound(Value::String(key.clone())),
                        bound(Value::String(title)),
                        bound(optional(body)),
                        bound(optional(comments)),
                        bound(optional(files)),
                        bound(if scope.is_null() {
                            Value::Null
                        } else {
                            Value::String(text_of(&scope))
                        }),
                        bound(Value::String(hash.clone())),
                        bound(Value::String(key.clone())),
                    ];
                    let stored = query_rows(&tx, &sql, &args).await?;
                    let model = stored
                        .first()
                        .and_then(|r| r[0].as_str())
                        .map(str::to_owned);
                    Some((key, text, hash, model))
                }
                _ => {
                    query_rows(&tx, &delete, &source).await?;
                    None
                }
            }
        };
        tx.commit().await.map_err(|err| failed(&err))?;
        let Some((key, text, hash, model)) = written else {
            return Ok(Indexed::Removed);
        };
        if let Some(embeddings) = &options.embeddings {
            if model.as_deref() != Some(embeddings.model.as_str()) {
                let shown: String = text
                    .chars()
                    .take(embeddings.max_chars.unwrap_or(8000))
                    .collect();
                let vectors = embeddings.embedder.embed(&[shown]).await.map_err(|err| {
                    Error::Database(format!("index_search: the model failed: {err}"))
                })?;
                if let Some(vector) = vectors.into_iter().next().filter(|v| !v.is_empty()) {
                    let vector = unit(&vector)?;
                    let sql = format!(
                        "UPDATE {} SET embedding = $3::float8[], embedding_model = $4 WHERE source_table = $1 AND source_key = $2 AND text_hash = $5",
                        d.index
                    );
                    let args = [
                        bound(Value::String(message.source_table.clone())),
                        bound(Value::String(key)),
                        bound(Value::Array(vector.into_iter().map(Value::from).collect())),
                        bound(Value::String(embeddings.model.clone())),
                        bound(Value::String(hash)),
                    ];
                    query_rows(&*client, &sql, &args).await?;
                }
            }
        }
        Ok(Indexed::Indexed)
    }

    /// Builds the index row of every row of the database's `@searchable`
    /// tables, or of `tables`, as [`index_search`] does one, in key order,
    /// `batch` rows a read (500 unless set): what a table that became
    /// `@searchable` with rows in it needs once, and what a change of
    /// embeddings' model needs. Answers how many rows it indexed.
    pub async fn reindex_search<C: GenericClient + Sync>(
        client: &mut C,
        definition: &Definition,
        tables: Option<&[String]>,
        batch: Option<u32>,
        options: &SearchOptions,
    ) -> Result<u64, Error> {
        let batch = batch.unwrap_or(500).max(1);
        let mut count = 0;
        for (table, d) in search_sources_of(definition) {
            if tables.is_some_and(|tables| !tables.iter().any(|t| t == table)) {
                continue;
            }
            let mut after: Option<String> = None;
            loop {
                let (sql, args) = match &after {
                    None => (
                        format!("SELECT {key}::text FROM {table} ORDER BY {key} LIMIT {batch}", key = d.key, table = d.table),
                        Vec::new(),
                    ),
                    Some(after) => (
                        format!(
                            "SELECT {key}::text FROM {table} WHERE {key} > $1::{key_type} ORDER BY {key} LIMIT {batch}",
                            key = d.key,
                            table = d.table,
                            key_type = d.key_type
                        ),
                        vec![bound(Value::String(after.clone()))],
                    ),
                };
                let rows = query_rows(&*client, &sql, &args).await?;
                for row in &rows {
                    let message = SearchIndexing {
                        source_table: table.to_owned(),
                        source_key: text_of(&row[0]),
                    };
                    index_search(client, definition, &message, options).await?;
                    count += 1;
                }
                if rows.len() < batch as usize {
                    break;
                }
                after = rows.last().map(|row| text_of(&row[0]));
            }
        }
        Ok(count)
    }

    /// What embeds through a model provider (D159) under a model.
    struct ProviderEmbedder {
        provider: Arc<dyn ModelProvider>,
        model: String,
    }

    #[async_trait]
    impl Embedder for ProviderEmbedder {
        async fn embed(&self, texts: &[String]) -> Result<Vec<Vec<f64>>, BoxError> {
            let request = ModelEmbedRequest {
                model: Some(self.model.clone()),
                texts: texts.to_vec(),
            };
            match self.provider.embed(&request).await? {
                Some(embedding) => Ok(embedding.vectors),
                None => Err("the model provider does not embed".into()),
            }
        }
    }

    /// Search's embeddings over a model provider (D159) that embeds, under
    /// `model`: it asks the provider to embed no text first, and refuses
    /// one that answers it embeds nothing, as the TypeScript
    /// `searchEmbeddingsOf` refuses a provider with no `embed`.
    pub async fn search_embeddings_of(
        provider: Arc<dyn ModelProvider>,
        model: &str,
        max_chars: Option<usize>,
    ) -> Result<SearchEmbeddings, Error> {
        let probe = ModelEmbedRequest {
            model: Some(model.to_owned()),
            texts: Vec::new(),
        };
        match provider.embed(&probe).await {
            Ok(Some(_)) => {}
            Ok(None) => {
                return Err(Error::Invalid(
                    "search_embeddings_of: the model provider does not embed".to_owned(),
                ))
            }
            Err(err) => {
                return Err(Error::Invalid(format!(
                    "search_embeddings_of: the model provider failed: {err}"
                )))
            }
        }
        Ok(SearchEmbeddings {
            model: model.to_owned(),
            embedder: Arc::new(ProviderEmbedder {
                provider,
                model: model.to_owned(),
            }),
            max_chars,
        })
    }

    /// What [`search_routes`] serves from.
    #[derive(Clone)]
    pub struct SearchRoutes {
        /// The database's pool and definition.
        pub tables: Tables,
        /// Establishes the caller of a search: a session's user, a key's,
        /// a grant's party or agent run (`access::GrantAuthenticator`).
        pub authenticator: Arc<dyn Authenticator>,
        /// The path the route is at; `/api/search` unless set.
        pub base: Option<String>,
        /// The permissions each table's rows need, and embeddings.
        pub options: SearchOptions,
    }

    impl SearchRoutes {
        /// The route over `tables`, as `authenticator` establishes each
        /// caller, at `/api/search`.
        pub fn new(tables: Tables, authenticator: Arc<dyn Authenticator>) -> Self {
            Self {
                tables,
                authenticator,
                base: None,
                options: SearchOptions::default(),
            }
        }
    }

    /// The search route (D155), as a router the app merges into its own:
    /// `GET {base}?q&tables&scope&limit&offset`, the caller's search as
    /// `{ data: SearchResult }`; 401 with no caller, 400 for a request it
    /// cannot read, 405 for another method.
    pub fn search_routes(routes: SearchRoutes) -> Router {
        let base = routes
            .base
            .as_deref()
            .unwrap_or("/api/search")
            .trim_end_matches('/')
            .to_owned();
        Router::new()
            .route(&base, any(serve))
            .with_state(Arc::new(routes))
    }

    async fn serve(State(routes): State<Arc<SearchRoutes>>, request: Request) -> Response {
        match answer(&routes, request).await {
            Ok(response) => response,
            Err(err) => error_response(err),
        }
    }

    async fn answer(routes: &SearchRoutes, request: Request) -> Result<Response, ApiError> {
        let (parts, _) = request.into_parts();
        if parts.method != Method::GET {
            return Err(ApiError::method_not_allowed("search is a GET"));
        }
        let Some(principal) = routes.authenticator.authenticate(&parts).await? else {
            return Err(ApiError::unauthorized("Authentication required"));
        };
        let request = request_of(&parts)?;
        let definition = routes.tables.definition()?;
        let client = routes.tables.pool().get().await.map_err(|err| {
            ApiError::service_unavailable(format!("the database is unreachable: {err}"))
        })?;
        let repositories = Repositories::new(&client, definition)?;
        let result =
            search_tables(&repositories, Some(&principal), &request, &routes.options).await?;
        Ok((StatusCode::OK, Json(json!({ "data": result }))).into_response())
    }

    /// Runs a call of [`search_mcp_tool`] as `principal`: its result, or
    /// its refusal, an [`Error::Invalid`] for arguments it cannot read. An
    /// agent run calls it as the principal its run token's grant resolves
    /// to, so its hits keep to the rows its grants reach and its audience.
    pub async fn call_search_tool<C: GenericClient + Sync>(
        repositories: &Repositories<'_, C>,
        principal: Option<&Principal>,
        args: &Map<String, Value>,
        options: &SearchOptions,
    ) -> Result<SearchResult, Error> {
        let request = search_tool_request(args)?;
        search_tables(repositories, principal, &request, options).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_words_are_the_runs_that_are_not_white_space() {
        assert_eq!(search_words("  walnut \t sink\n"), vec!["walnut", "sink"]);
        assert_eq!(search_words("a\u{2}b"), vec!["ab"]);
        assert_eq!(search_words(&"w ".repeat(40)).len(), MAX_SEARCH_WORDS);
        assert!(search_words("   ").is_empty());
    }

    #[cfg(feature = "postgres")]
    #[test]
    fn a_snippet_marks_each_match() {
        let parts = snippet_parts("A \u{2}walnut\u{3} desk");
        assert_eq!(
            parts,
            vec![
                SnippetPart {
                    text: "A ".to_owned(),
                    matched: false
                },
                SnippetPart {
                    text: "walnut".to_owned(),
                    matched: true
                },
                SnippetPart {
                    text: " desk".to_owned(),
                    matched: false
                },
            ]
        );
        assert_eq!(
            serde_json::to_value(&parts[1]).unwrap(),
            json!({ "text": "walnut", "match": true })
        );
    }

    #[cfg(feature = "postgres")]
    #[test]
    fn an_index_row_holds_no_mark_and_no_blank() {
        assert_eq!(clean(Some("a\u{0}b")), Some("a b".to_owned()));
        assert_eq!(clean(Some("  ")), None);
        assert_eq!(clean(None), None);
        assert_eq!(unit(&[3.0, 4.0]).unwrap(), vec![0.6, 0.8]);
        assert_eq!(unit(&[0.0, 0.0]).unwrap(), vec![0.0, 0.0]);
        assert!(unit(&[f64::NAN]).is_err());
    }

    #[test]
    fn a_tool_call_reads_its_arguments() {
        let args = json!({ "q": "walnut", "tables": ["Unit"], "scope": "p1", "limit": 5 });
        let request = search_tool_request(args.as_object().unwrap()).unwrap();
        assert_eq!(request.tables, Some(vec!["Unit".to_owned()]));
        assert_eq!(request.scope, Some(vec!["p1".to_owned()]));
        assert_eq!(request.limit, Some(5));
        assert!(search_tool_request(&Map::new()).is_err());
    }
}
