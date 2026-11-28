//! Search across tables on a SQLite file (D155, D186, #615), as the
//! TypeScript runtime's `searchOnSQLite` and `indexOnSQLite` and the Go
//! `crosstable` search there: the index is the same table, its audience a
//! JSON array of ids (D182), and its words an FTS5 table beside it
//! ([`SearchableDefinition::fts`], `search_index_fts`), which the index
//! table's triggers keep in the statement that writes an index row.
//!
//! - The words: the plan's FTS5 expression ([`super::query`], #740), each
//!   term a quoted FTS5 phrase of the words the `unicode61` tokenizer finds
//!   in it (folded to lower case and none stemmed, as Postgres's `simple`
//!   configuration reads them; diacritics removed where the build folds
//!   them), groups `OR` and excluded terms `NOT`, only in the four texts.
//! - The order: bm25 with the weights Postgres gives the title, the body,
//!   the conversation and the files (1, 0.4, 0.2, 0.1), best first, then
//!   the table and the key. A hit's `matched` is the first of the four
//!   texts that holds every word, and its snippet FTS5's of that text.
//! - Access is checked inside the query as on Postgres: the caller's
//!   audience through `json_each` over the index row's copy
//!   ([`audience_conditions`](crate::orm::audience_conditions)), and a
//!   grant's reach by an `EXISTS` back to each table with the conditions
//!   its SQLite repository applies (`Repository::conditions`); a table no
//!   grant reaches is left out.
//! - Embeddings re-rank and are stored on Postgres alone: a search or an
//!   indexing on SQLite that asks for them is refused.
//!
//! Each function runs on the connection it is given, in the caller's
//! transaction, and answers at once, as the SQLite repositories do: an
//! indexing writes, so its caller opens the transaction with
//! `TransactionBehavior::Immediate` (`Database::write`). [`search_routes`]
//! runs a search on a reading connection of the file, off the caller's
//! thread.

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::{Json, Router};
use http::{Method, StatusCode};
use rusqlite::Connection;
use serde_json::{json, Map, Value};

use super::shared::{quoted_of, request_of, search_sources_of, text_of};
use super::{
    clean, fts_query, hex_sha256, search_plan, search_tool_request, snippet_parts, whole, Indexed,
    SearchHit, SearchIndexing, SearchOptions, SearchRequest, SearchResult, SearchableDefinition,
    CLOSE, MAX_LIMIT, MAX_OFFSET, OPEN,
};
use crate::auth::{Authenticator, Principal};
use crate::orm::sqlite::{execute_rows, query_rows, Repositories, Tables};
use crate::orm::{audience_conditions, AccessCaller, Audience, Bound, Definition, Error, Marks};
use crate::response::error_response;
use crate::uuid62::{parse_uuid, uuid_to_base62};
use crate::ApiError;

/// bm25's weights of the FTS5 table's columns: the title, the body, the
/// conversation and the files as Postgres weighs A to D, and none for the
/// row's ref, table and key (D186).
const FTS_WEIGHTS: &str = "1.0, 0.4, 0.2, 0.1, 0.0, 0.0, 0.0";

/// The texts a search matches, the FTS5 table's first four columns, in the
/// order a hit's `matched` names them.
const FTS_TEXTS: [&str; 4] = ["title", "body", "comments", "files"];

/// The FTS5 table a definition with none names.
const FTS: &str = "\"search_index_fts\"";

/// The refusal of embeddings on SQLite.
fn embeddings_refused(what: &str) -> String {
    format!("{what}: embeddings re-rank and are stored on Postgres alone; a search on SQLite and D1 ranks by its words (D186)")
}

fn bound(value: Value) -> Bound {
    Bound {
        value,
        marks: Marks::TEXT,
    }
}

/// Appends `value` to `args` and answers its placeholder.
fn param(args: &mut Vec<Bound>, value: Value) -> String {
    args.push(bound(value));
    format!("?{}", args.len())
}

fn integer(value: Option<&Value>) -> i64 {
    match value {
        Some(Value::Number(n)) => n.as_i64().unwrap_or(0),
        Some(Value::String(s)) => s.parse().unwrap_or(0),
        _ => 0,
    }
}

/// A row's key as the file holds it: a UUID's hyphenated text.
fn db_key(d: &SearchableDefinition, key: &str) -> String {
    if d.uuid {
        parse_uuid(key).map_or_else(|| key.to_owned(), |uuid| uuid.hyphenated().to_string())
    } else {
        key.to_owned()
    }
}

/// Searches every `@searchable` table as `principal` on SQLite (D155,
/// D186), as the Postgres `search_tables` answers: the rows that hold
/// every word of `request.q`, of those `principal` may read, best first,
/// one page of them, and how many there are. A table a caller lacks the
/// permission of (`options.permissions`) is left out, as is every table a
/// caller with grants reaches no row of. `repositories` lend their
/// connection and session; whom they keep to is replaced by the principal.
/// A request's vector is refused as [`Error::Invalid`], and embeddings in
/// the options as [`Error::Definition`].
pub fn search_tables(
    repositories: &Repositories<'_>,
    principal: Option<&Principal>,
    request: &SearchRequest,
    options: &SearchOptions,
) -> Result<SearchResult, Error> {
    let limit = whole(request.limit, 20, 1, MAX_LIMIT, "limit")?;
    let offset = whole(request.offset, 0, 0, MAX_OFFSET, "offset")?;
    let plan = search_plan(&request.q);
    // A query with no term to hold matches nothing; a vector, which
    // would rank it, is refused below.
    if plan.all.is_empty() && request.vector.is_none() {
        return Ok(SearchResult::default());
    }
    let definition = repositories.definition();
    let caller = AccessCaller::of(principal);
    let connection = repositories.connection();
    let db = Repositories::new(connection, definition)?
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
                Some(permission) => principal.is_some_and(|p| p.permissions.contains(permission)),
                None => true,
            }
        })
        .collect();
    let Some((_, first)) = sources.first() else {
        return Ok(SearchResult::default());
    };
    if request.vector.is_some() {
        return Err(Error::Invalid(embeddings_refused("search_tables")));
    }
    if options.embeddings.is_some() {
        return Err(Error::Definition(embeddings_refused("search_tables")));
    }
    let Some(phrases) = fts_query(&plan) else {
        return Ok(SearchResult::default());
    };
    let fts = first.fts.as_deref().unwrap_or(FTS);
    let index = first.index.as_str();
    let all_texts = format!("{{{}}} : ({phrases})", FTS_TEXTS.join(" "));

    let mut args: Vec<Bound> = Vec::new();
    let matching = param(&mut args, Value::String(all_texts.clone()));
    let mut conditions = vec![format!("{fts} MATCH {matching}")];
    // The tables: for a caller with grants, each through its repository's
    // conditions for that caller, an EXISTS back to its rows.
    if caller.grants.is_none() {
        let names: Vec<String> = sources
            .iter()
            .map(|(t, _)| param(&mut args, Value::from(*t)))
            .collect();
        conditions.push(format!("si.source_table IN ({})", names.join(", ")));
    } else {
        let mut branches = Vec::new();
        for (table, d) in &sources {
            let kept = db.table(table)?.conditions(&mut args)?;
            if kept.len() == 1 && kept[0] == "FALSE" {
                continue;
            }
            let name = param(&mut args, Value::from(*table));
            let mut every = vec![format!("{}.{} = si.source_key", d.table, d.key)];
            every.extend(kept);
            branches.push(format!(
                "(si.source_table = {name} AND EXISTS (SELECT 1 FROM {} WHERE {}))",
                d.table,
                every.join(" AND ")
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
        let mut forms: Vec<String> = Vec::new();
        for value in scope {
            let mut add = |form: String| {
                if !forms.contains(&form) {
                    forms.push(form);
                }
            };
            add(value.clone());
            if let Some(uuid) = parse_uuid(value) {
                add(uuid.hyphenated().to_string());
            }
        }
        let placeholders: Vec<String> = forms
            .into_iter()
            .map(|form| param(&mut args, Value::String(form)))
            .collect();
        conditions.push(format!("si.scope IN ({})", placeholders.join(", ")));
    }
    let sql = format!(
        "WITH hits AS (
      SELECT si.source_table, si.source_key, si.title, {fts}.rowid AS fts, bm25({fts}, {FTS_WEIGHTS}) AS rank
      FROM {fts} JOIN {index} AS si ON si.source_table = {fts}.source_table AND si.source_key = {fts}.source_key
      WHERE {where_}
    ),
    ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank, h.source_table, h.source_key) AS place FROM hits AS h
    )
    SELECT c.total, p.source_table, p.source_key, p.title, p.fts, p.place
    FROM (SELECT count(*) AS total FROM hits) AS c
    LEFT JOIN (SELECT * FROM ranked ORDER BY place LIMIT {limit} OFFSET {offset}) AS p ON 1
    ORDER BY p.place",
        where_ = conditions.join(" AND "),
    );
    if let Some(trace) = repositories.session().trace {
        trace(&sql);
    }
    let rows = query_rows(connection, &sql, &args)?;
    let total = u64::try_from(integer(rows.first().and_then(|row| row.first()))).unwrap_or(0);
    let page: Vec<&Vec<Value>> = rows
        .iter()
        .filter(|row| matches!(row.get(1), Some(Value::String(_))))
        .collect();

    // Where the words matched first, and a snippet of each text, for the
    // page.
    let ids: Vec<i64> = page.iter().map(|row| integer(row.get(4))).collect();
    let mut matched: std::collections::BTreeMap<i64, usize> = std::collections::BTreeMap::new();
    let mut snippets: std::collections::BTreeMap<i64, Vec<Value>> =
        std::collections::BTreeMap::new();
    if !ids.is_empty() {
        let among = |from: usize| {
            (0..ids.len())
                .map(|i| format!("?{}", from + i))
                .collect::<Vec<_>>()
                .join(", ")
        };
        let mut texts: Vec<Bound> = Vec::new();
        let mut branches = Vec::new();
        for column in FTS_TEXTS {
            texts.push(bound(Value::String(format!("{column} : ({phrases})"))));
            let at = texts.len();
            texts.extend(ids.iter().map(|id| bound(Value::from(*id))));
            branches.push(format!(
                "SELECT '{column}' AS text, rowid AS fts FROM {fts} WHERE {fts} MATCH ?{at} AND rowid IN ({})",
                among(at + 1)
            ));
        }
        for row in query_rows(connection, &branches.join(" UNION ALL "), &texts)? {
            let id = integer(row.get(1));
            let text = text_of(row.first().unwrap_or(&Value::Null));
            let Some(place) = FTS_TEXTS.iter().position(|t| *t == text) else {
                continue;
            };
            let current = matched.entry(id).or_insert(place);
            *current = (*current).min(place);
        }
        let parts: Vec<String> = (0..FTS_TEXTS.len())
            .map(|i| format!("snippet({fts}, {i}, ?2, ?3, '', 24) AS s{i}"))
            .collect();
        let mut read = vec![
            bound(Value::String(all_texts)),
            bound(Value::String(OPEN.to_string())),
            bound(Value::String(CLOSE.to_string())),
        ];
        read.extend(ids.iter().map(|id| bound(Value::from(*id))));
        let sql = format!(
            "SELECT rowid AS fts, {} FROM {fts} WHERE {fts} MATCH ?1 AND rowid IN ({})",
            parts.join(", "),
            among(4)
        );
        for row in query_rows(connection, &sql, &read)? {
            snippets.insert(integer(row.first()), row);
        }
    }
    let offset = u64::try_from(offset).unwrap_or(0);
    let mut items = Vec::with_capacity(page.len());
    for (row, id) in page.iter().zip(&ids) {
        let table = text_of(&row[1]);
        let uuid = all.iter().any(|(name, d)| *name == table && d.uuid);
        let key = text_of(&row[2]);
        let key = if uuid {
            parse_uuid(&key).map_or(key, uuid_to_base62)
        } else {
            key
        };
        let place = matched.get(id).copied().unwrap_or(0);
        let snippet = snippets
            .get(id)
            .and_then(|row| row.get(place + 1))
            .map(text_of)
            .unwrap_or_default();
        items.push(SearchHit {
            table,
            key,
            title: text_of(&row[3]),
            matched: FTS_TEXTS[place].to_owned(),
            snippet: snippet_parts(&snippet),
            place: offset + items.len() as u64 + 1,
        });
    }
    let more = offset + (items.len() as u64) < total;
    Ok(SearchResult {
        items,
        total,
        more,
        reranked: false,
    })
}

/// Builds the index row of the row a `@searchable` declaration's queue
/// names (D155, D186) on `connection`, in the caller's transaction, as the
/// system, which the queue's worker calls for each message: the row read
/// by its key, its conversation's and files' text in their order, and its
/// index row written with the row's access columns, read by the write
/// itself, or deleted for a row that is gone or soft deleted. The index
/// table's triggers write its words into the FTS5 table in the same
/// statement. Embeddings in the options are refused, as
/// [`Error::Definition`].
pub fn index_search(
    connection: &Connection,
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
    if options.embeddings.is_some() {
        return Err(Error::Definition(embeddings_refused("index_search")));
    }
    let source_key = db_key(d, &message.source_key);
    let key_arg = [bound(Value::String(source_key.clone()))];
    let mut columns = vec![d.title.clone()];
    columns.extend(d.body.iter().cloned());
    columns.push(format!("CAST({} AS TEXT)", d.key));
    columns.push(match &d.scope {
        Some(scope) => format!("CAST({scope} AS TEXT)"),
        None => "NULL".to_owned(),
    });
    columns.push(match &d.deleted_at {
        Some(at) => format!("{at} IS NOT NULL"),
        None => "0".to_owned(),
    });
    let sql = format!(
        "SELECT {} FROM {} WHERE {} = ?1",
        columns.join(", "),
        d.table,
        d.key
    );
    let row = query_rows(connection, &sql, &key_arg)?.into_iter().next();
    let body_count = d.body.len();
    let Some(row) = row.filter(|row| integer(row.get(body_count + 3)) != 1) else {
        execute_rows(
            connection,
            &format!(
                "DELETE FROM {} WHERE source_table = ?1 AND source_key = ?2",
                d.index
            ),
            &[
                bound(Value::String(message.source_table.clone())),
                bound(Value::String(source_key)),
            ],
        )?;
        return Ok(Indexed::Removed);
    };
    let title = clean(row.first().map(text_of).as_deref()).unwrap_or_default();
    let parts: Vec<String> = row[1..=body_count]
        .iter()
        .filter(|v| !v.is_null())
        .map(text_of)
        .collect();
    let body = clean(Some(&parts.join("\n")));
    let key = text_of(&row[body_count + 1]);
    let scope = row[body_count + 2].clone();
    let aggregate = |sql: String| -> Result<Option<String>, Error> {
        let read = query_rows(connection, &sql, &key_arg)?;
        Ok(clean(
            read.first().and_then(|r| r.first()).and_then(Value::as_str),
        ))
    };
    let comments = match &d.comments {
        Some(c) => aggregate(format!(
            "SELECT group_concat(text, char(10)) AS text FROM (SELECT {body} AS text FROM {table} WHERE {row} = ?1 AND {state} IN ('posted', 'interrupted') AND {body} <> '' ORDER BY {order})",
            body = c.body,
            table = c.table,
            row = c.row,
            state = c.state,
            order = c.order,
        ))?,
        None => None,
    };
    let files = match &d.files {
        Some(f) => aggregate(format!(
            "SELECT group_concat(text, char(10)) AS text FROM (SELECT {text} AS text FROM {table} WHERE {row} = ?1 AND {state} = 'ready' AND {text} IS NOT NULL ORDER BY {text})",
            text = f.text,
            table = f.table,
            row = f.row,
            state = f.state,
        ))?,
        None => None,
    };
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
    // The access columns come from the row itself, read by the write.
    let (audience, agents) = match &d.audience {
        Some(a) => (
            format!("s.{}", quoted_of(&a.effective)),
            format!("s.{}", quoted_of(&a.agents)),
        ),
        None => ("NULL".to_owned(), "'followRole'".to_owned()),
    };
    let sql = format!(
        "INSERT INTO {index} (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, {audience}, {agents}, ?8, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM {table} AS s WHERE s.{key_column} = ?9
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = excluded.title, body = excluded.body, comments = excluded.comments, files = excluded.files, scope = excluded.scope,
         audience = excluded.audience, agents = excluded.agents, indexed_at = excluded.indexed_at,
         embedding = CASE WHEN {index}.text_hash = excluded.text_hash THEN {index}.embedding END,
         embedding_model = CASE WHEN {index}.text_hash = excluded.text_hash THEN {index}.embedding_model END,
         text_hash = excluded.text_hash",
        index = d.index,
        table = d.table,
        key_column = d.key,
    );
    let optional = |text: Option<String>| text.map_or(Value::Null, Value::String);
    execute_rows(
        connection,
        &sql,
        &[
            bound(Value::String(message.source_table.clone())),
            bound(Value::String(key)),
            bound(Value::String(title)),
            bound(optional(body)),
            bound(optional(comments)),
            bound(optional(files)),
            bound(if scope.is_null() {
                Value::Null
            } else {
                Value::String(text_of(&scope))
            }),
            bound(Value::String(hash)),
            bound(Value::String(source_key)),
        ],
    )?;
    Ok(Indexed::Indexed)
}

/// Builds the index row of every row of the database's `@searchable`
/// tables, or of `tables`, as [`index_search`] does one, in key order,
/// `batch` rows a read (500 unless set), on `connection` in the caller's
/// transaction: what a table that became `@searchable` with rows in it
/// needs once. The next page starts after the key as the table holds it.
/// Answers how many rows it indexed.
pub fn reindex_search(
    connection: &Connection,
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
        let mut after: Option<Value> = None;
        loop {
            let (sql, args) = match &after {
                None => (
                    format!(
                        "SELECT CAST({key} AS TEXT), {key} FROM {table} ORDER BY {key} LIMIT {batch}",
                        key = d.key,
                        table = d.table
                    ),
                    Vec::new(),
                ),
                Some(after) => (
                    format!(
                        "SELECT CAST({key} AS TEXT), {key} FROM {table} WHERE {key} > ?1 ORDER BY {key} LIMIT {batch}",
                        key = d.key,
                        table = d.table
                    ),
                    vec![bound(after.clone())],
                ),
            };
            let rows = query_rows(connection, &sql, &args)?;
            for row in &rows {
                let message = SearchIndexing {
                    source_table: table.to_owned(),
                    source_key: text_of(&row[0]),
                };
                index_search(connection, definition, &message, options)?;
                count += 1;
            }
            if rows.len() < batch as usize {
                break;
            }
            after = rows.last().map(|row| row[1].clone());
        }
    }
    Ok(count)
}

/// Runs a call of the search's MCP tool as `principal` on SQLite: its
/// result, or its refusal, an [`Error::Invalid`] for arguments it cannot
/// read. An agent run calls it as the principal its run token's grant
/// resolves to, so its hits keep to the rows its grants reach and its
/// audience.
pub fn call_search_tool(
    repositories: &Repositories<'_>,
    principal: Option<&Principal>,
    args: &Map<String, Value>,
    options: &SearchOptions,
) -> Result<SearchResult, Error> {
    let request = search_tool_request(args)?;
    search_tables(repositories, principal, &request, options)
}

/// What [`search_routes`] serves from: the Postgres `SearchRoutes` over a
/// SQLite file.
#[derive(Clone)]
pub struct SearchRoutes {
    /// The file and the database's definition.
    pub tables: Tables,
    /// Establishes the caller of a search: a session's user, a key's, a
    /// grant's party (`access::sqlite::GrantAuthenticator`).
    pub authenticator: Arc<dyn Authenticator>,
    /// The path the route is at; `/api/search` unless set.
    pub base: Option<String>,
    /// The permissions each table's rows need; no embeddings on SQLite.
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

/// The search route on a SQLite file (D155, #615), the Postgres
/// `search_routes`' path and answers: `GET {base}?q&tables&scope&limit&offset`,
/// the caller's search as `{ data: SearchResult }`; 401 with no caller,
/// 400 for a request it cannot read, 405 for another method.
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
    match answer(routes, request).await {
        Ok(response) => response,
        Err(err) => error_response(err),
    }
}

async fn answer(routes: Arc<SearchRoutes>, request: Request) -> Result<Response, ApiError> {
    let (parts, _) = request.into_parts();
    if parts.method != Method::GET {
        return Err(ApiError::method_not_allowed("search is a GET"));
    }
    let Some(principal) = routes.authenticator.authenticate(&parts).await? else {
        return Err(ApiError::unauthorized("Authentication required"));
    };
    let request = request_of(&parts)?;
    let database = Arc::clone(routes.tables.database());
    let result = database
        .read_async(move |connection| -> Result<SearchResult, ApiError> {
            let repositories = Repositories::new(connection, routes.tables.definition()?)?;
            Ok(search_tables(
                &repositories,
                Some(&principal),
                &request,
                &routes.options,
            )?)
        })
        .await?;
    Ok((StatusCode::OK, Json(json!({ "data": result }))).into_response())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_term_with_a_letter_or_a_digit_is_a_quoted_phrase() {
        let fts = |q: &str| fts_query(&search_plan(q));
        assert_eq!(
            fts("walnut 2026 o\"k"),
            Some("\"walnut\" AND \"2026\" AND \"o\" AND \"k\"".to_owned())
        );
        assert_eq!(fts("-- ... walnut"), Some("\"walnut\"".to_owned()));
        assert_eq!(fts("-- ..."), None);
    }
}
