//! The search across a namespace's schemas (`engine.search`, the HTTP
//! route `searchSchemas` and the MCP tool `search`), the port of
//! `searchSchemas` in `src/behaviors/core/search.ts` (D16, D58). It runs
//! the core's Search behavior's `search` on every schema the namespace
//! reaches that composes Search and that the principal may read, each as
//! the principal, and fuses their rankings by reciprocal rank: each schema
//! ranks its own instances, the first [`RRF_WINDOW`] by text and by vector,
//! and a hit scores as search scores it, 1 / ([`RRF_K`] + its place) in
//! each ranking of its schema it is in, so one schema's best hit ties
//! another's. A vector ranks the schemas whose vectors come from its
//! model, with its dimensions, and the query alone ranks the others, or
//! none without one. A schema the principal may not read, whose search the
//! policy refuses, or whose behaviors this engine cannot run is skipped,
//! not refused, as the tools document leaves it out.
//!
//! The engine cannot depend on the behaviors crate, so it reaches Search
//! by name through the registry, its parameters through Search's
//! declaration, as the tools document does; an engine that does not run
//! Search answers `unavailable`. On Postgres each schema's ranking is
//! Postgres's (`ts_rank_cd`), and so is the order among equal scores.

use serde_json::{json, Map, Value};

use crate::behaviors::page_request;
use crate::behaviors::registry::issues;
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::json::number_value;
use crate::principal::Principal;
use crate::registry::{SchemaTarget, ValidateOptions};
use superschematic_schema_ir::ajv::AjvSchema;

/// Reciprocal rank fusion's constant: a hit scores 1 / (RRF_K + its place)
/// in each ranking it is in. The core's Search fuses with it too.
pub const RRF_K: i64 = 60;

/// How far into each ranking fusion reads: a search with both a query and
/// a vector, Search's similar, and a search across schemas fuse the first
/// RRF_WINDOW of each ranking they fuse.
pub const RRF_WINDOW: i64 = 200;

const NAME: &str = "Search";

/// The parameters of a search across a namespace's schemas, as a JSON
/// Schema (`SEARCH_SCHEMAS_PARAMS`): Search's search parameters, with a
/// vector only beside the model it was computed with, which picks the
/// schemas it ranks. `None` when the engine does not run Search.
pub fn search_schemas_params(engine: &Engine) -> Option<Value> {
    let registry = engine.catalog.registry();
    let search = registry.lookup(NAME)?;
    let operation = search
        .declaration
        .operations
        .iter()
        .find(|operation| operation.name == "search")?;
    let mut properties = operation
        .params_schema
        .get("properties")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    // Each replaces the declaration's in its place, as an object spread
    // does.
    properties.insert(
        "vector".to_owned(),
        json!({"description": "The vector to search for, computed with model: it ranks the schemas whose vectors come from that model, with as many dimensions.", "type": "array", "minItems": 1, "maxItems": 4096, "items": {"type": "number"}}),
    );
    properties.insert(
        "model".to_owned(),
        json!({"description": "The model the vector was computed with; required with a vector.", "type": "string", "minLength": 1, "maxLength": 200}),
    );
    Some(json!({
        "type": "object",
        "additionalProperties": false,
        "anyOf": [{"required": ["query"]}, {"required": ["vector"]}],
        "dependentRequired": {"vector": ["model"], "model": ["vector"]},
        "properties": properties,
    }))
}

// checked_params holds the parameters to search_schemas_params, refused as
// Search's search's parameters.
fn checked_params(engine: &Engine, params: &Value) -> Result<Value> {
    let Some(schema) = search_schemas_params(engine) else {
        return Err(EngineError::new(
            EngineErrorCode::Unavailable,
            "the search across a namespace's schemas runs on the core's Search behavior, which this engine does not run",
        )
        .into());
    };
    let compiled = AjvSchema::compile(&schema)
        .map_err(|message| Error::internal(format!("search's parameters: {message}")))?;
    let params = if params.is_null() {
        Value::Object(Map::new())
    } else {
        params.clone()
    };
    let refused = issues(compiled.errors(&params));
    if !refused.is_empty() {
        return Err(EngineError::operation_params(NAME, "search", refused).into());
    }
    Ok(params)
}

// search_config is the Search config a schema's live version gives, as the
// schema holds it; None when it does not compose Search, or the principal
// may not read it, or the engine cannot run it.
fn search_config(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    namespace: &str,
) -> Result<Option<Value>> {
    let options = ValidateOptions {
        namespace: Some(namespace),
        version: None,
    };
    match engine.schemas().behaviors(principal, schema, options) {
        Ok(composed) => Ok(composed
            .into_iter()
            .find(|behavior| behavior.name == NAME)
            .map(|behavior| behavior.config)),
        Err(error)
            if matches!(
                error.code(),
                Some(
                    EngineErrorCode::Unavailable
                        | EngineErrorCode::Forbidden
                        | EngineErrorCode::NotFound
                )
            ) =>
        {
            Ok(None)
        }
        Err(error) => Err(error),
    }
}

/// Searches every schema the namespace reaches that composes Search and
/// that the principal may read, and fuses their rankings (the module's
/// comment): `{ items, next }`, each hit its schema, id, rank and score,
/// and how it matched.
pub(crate) fn search_schemas(
    engine: &Engine,
    principal: &Principal,
    params: &Value,
    namespace: &str,
) -> Result<Value> {
    let checked = checked_params(engine, params)?;
    let request = page_request(NAME, "search", &checked)?;
    let query = checked.get("query").and_then(Value::as_str);
    let vector = checked.get("vector").and_then(Value::as_array);
    let model = checked.get("model");
    let mut found: Vec<(Map<String, Value>, f64, i64)> = Vec::new();
    for summary in engine.schemas().list(
        principal,
        SchemaTarget {
            namespace: Some(namespace),
        },
    )? {
        if summary.live_version.is_none() {
            continue;
        }
        let Some(config) = search_config(engine, principal, &summary.name, namespace)? else {
            continue;
        };
        let vectors = config.get("vectors");
        let ranks_vector = vector.is_some_and(|vector| {
            vectors.and_then(|vectors| vectors.get("model")) == model
                && vectors
                    .and_then(|vectors| vectors.get("dimensions"))
                    .and_then(Value::as_number)
                    .map(number_value)
                    == Some(vector.len() as f64)
        });
        if query.is_none() && !ranks_vector {
            continue;
        }
        let mut call = Map::new();
        if let Some(query) = query {
            call.insert("query".into(), Value::from(query));
            if let Some(syntax) = checked.get("syntax") {
                call.insert("syntax".into(), syntax.clone());
            }
        }
        if ranks_vector {
            call.insert(
                "vector".into(),
                Value::Array(vector.cloned().unwrap_or_default()),
            );
            call.insert("model".into(), model.cloned().unwrap_or(Value::Null));
        }
        let limit = if ranks_vector && query.is_some() {
            2 * RRF_WINDOW
        } else {
            RRF_WINDOW
        };
        call.insert("limit".into(), Value::from(limit));
        let ranked = match engine.instances().invoke_schema(
            principal,
            &summary.name,
            "search",
            &Value::Object(call),
            Some(namespace),
        ) {
            Ok(ranked) => ranked,
            Err(error) if error.code() == Some(EngineErrorCode::Forbidden) => continue,
            Err(error) => return Err(error),
        };
        let items = ranked
            .get("items")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        for item in items {
            let Value::Object(item) = item else { continue };
            let place = item.get("rank").and_then(Value::as_i64).unwrap_or(0);
            let score = item
                .get("score")
                .and_then(Value::as_number)
                .map_or_else(|| 1.0 / (RRF_K + place) as f64, number_value);
            let mut hit = Map::new();
            hit.insert("schema".into(), Value::from(summary.name.as_str()));
            hit.insert("id".into(), item.get("id").cloned().unwrap_or(Value::Null));
            hit.insert("rank".into(), Value::from(0));
            hit.insert("score".into(), Value::from(score));
            for (key, value) in item {
                if !matches!(key.as_str(), "id" | "rank" | "score") {
                    hit.insert(key, value);
                }
            }
            if !ranks_vector {
                hit.insert("text".into(), json!({ "rank": place }));
            }
            found.push((hit, score, place));
        }
    }
    // The list is by schema name and the sort is stable, so equal scores
    // keep the better place in their schema, then the schema's name.
    found.sort_by(|(_, a, a_place), (_, b, b_place)| b.total_cmp(a).then(a_place.cmp(b_place)));
    let after = usize::try_from(request.after).unwrap_or(0);
    let window: Vec<Map<String, Value>> = found
        .into_iter()
        .skip(after)
        .take(usize::try_from(request.limit + 1).unwrap_or(0))
        .map(|(hit, _, _)| hit)
        .collect();
    let numbered: Vec<(Map<String, Value>, i64)> = window
        .into_iter()
        .enumerate()
        .map(|(position, mut hit)| {
            let rank = request.after + position as i64 + 1;
            hit.insert("rank".into(), Value::from(rank));
            (hit, rank)
        })
        .collect();
    let page = crate::behaviors::page(numbered, request.limit, |(_, rank)| *rank);
    Ok(crate::behaviors::Page {
        items: page
            .items
            .into_iter()
            .map(|(hit, _)| Value::Object(hit))
            .collect(),
        next: page.next,
    }
    .to_json())
}
