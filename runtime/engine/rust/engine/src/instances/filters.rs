//! A list's equality filters (`where`) and a lookup's key, the port of
//! `src/instances/filters.ts` (D16, amended: an instance is found by a
//! unique field, and a list filters by equality).
//!
//! `where` is a JSON object of field values: a member keeps the instances
//! whose field holds the value, a list of values any of them, and the
//! members together keep the instances every one keeps. A field is one of
//! the instance type's own top-level fields that holds a string, a number
//! or a boolean, by its key, or a field a behavior lets a list filter on,
//! which the behavior keeps in one of its columns, by its qualified name
//! (`Workflow.status`; milestone 5 registers behaviors). A value is of the
//! field's JSON type, or null, which keeps the instances whose field holds
//! no value, and a list holds 1 to 100 of them.
//!
//! A page keeps creation order (`position`) and reads in one of two ways:
//! through an index whose every key a member names (an own index, or a
//! behavior's index on the filter's column), one range read per
//! combination of the members' values past the cursor, at most 100 of
//! them, merged; or through the list index, scanning at most
//! [`FILTER_SCAN_ROWS`] instances past the cursor. Either way a page reads
//! a bounded number of rows, and the members no index serves are tested in
//! SQL on the rows read. A page holds the instances read that every member
//! keeps, up to its limit, and `next` is the position up to which every
//! instance has been read, so a page can hold fewer instances than its
//! limit, even none, while `next` is not null.
//!
//! Every comparison repeats [`crate::indexes::value_expression`], so it
//! reaches the index on both databases (D58). On SQLite a read names its
//! index (`INDEXED BY`), so it is that range and no other plan; Postgres's
//! planner picks the partial index the expression and its condition match.
//! A member on an own field no index covers also matches a value the row
//! holds by hash: a string whose JSON is longer than the value store's
//! least threshold is compared both as itself and by its hash.

use std::collections::BTreeSet;

use serde_json::{Map, Value};

use superschematic_engine_storage::{behavior::index_name, Dialect, SqlValue, Tx};

use crate::errors::{EngineError, Result};
use crate::indexes::{
    index_exists, ref_hash_expression, schema_rows, value_expression, OwnIndex, ScalarType,
};
use crate::json;
use crate::values::MIN_VALUE_THRESHOLD;

/// The most values one member of `where` lists.
pub const MAX_FILTER_VALUES: usize = 100;

/// The most combinations of values a page reads through one index.
pub const MAX_FILTER_RANGES: usize = 100;

/// The instances a filtered page reads past its cursor: every one it reads
/// when no index serves the filter, shared among its range reads when one
/// does (each reads at least two).
pub const FILTER_SCAN_ROWS: usize = 1000;

/// A field a list may filter on.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Filterable {
    pub key: String,
    pub scalar: ScalarType,
    /// The behavior whose field it is; `None` for an own field.
    pub behavior: Option<String>,
    /// For a behavior's field, the SQL name of the column that holds it.
    pub column: Option<String>,
    /// For a behavior's field, the SQL name of its index on that column.
    pub index: Option<String>,
    /// For a behavior's field, what the value is.
    pub description: Option<String>,
    /// For an own field, whether a row may hold a long value of it by
    /// hash: no own index covers it.
    pub spillable: bool,
}

/// A filter a behavior declares (milestone 5): its column, type, index
/// and description.
#[derive(Clone, Debug, PartialEq, Eq)]
#[allow(dead_code)] // milestone 5's behaviors declare filters
pub(crate) struct BehaviorFilter {
    pub field: String,
    pub column: String,
    pub scalar: ScalarType,
    pub index: Option<String>,
    pub description: Option<String>,
}

/// A behavior on the type, as [`filterables_of`] reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
#[allow(dead_code)] // milestone 5's behaviors declare filters
pub(crate) struct FilteringBehavior {
    pub name: String,
    pub prefix: String,
    pub filters: Vec<BehaviorFilter>,
}

/// The fields a version's list filters on, in order: the instance type's
/// own scalar fields, then each behavior's filters by qualified name, with
/// the SQL names of their columns and indexes.
pub(crate) fn filterables_of(
    own: &[(String, ScalarType)],
    indexes: &[OwnIndex],
    behaviors: &[FilteringBehavior],
) -> Vec<Filterable> {
    let covered: BTreeSet<&str> = indexes
        .iter()
        .flat_map(|index| index.keys.iter().map(String::as_str))
        .collect();
    let mut found: Vec<Filterable> = Vec::new();
    let mut set =
        |filterable: Filterable| match found.iter_mut().find(|known| known.key == filterable.key) {
            Some(known) => *known = filterable,
            None => found.push(filterable),
        };
    for (key, scalar) in own {
        set(Filterable {
            key: key.clone(),
            scalar: *scalar,
            behavior: None,
            column: None,
            index: None,
            description: None,
            spillable: !covered.contains(key.as_str()),
        });
    }
    for behavior in behaviors {
        for filter in &behavior.filters {
            set(Filterable {
                key: format!("{}.{}", behavior.name, filter.field),
                scalar: filter.scalar,
                behavior: Some(behavior.name.clone()),
                column: Some(format!("{}{}", behavior.prefix, filter.column)),
                index: filter
                    .index
                    .as_ref()
                    .map(|index| index_name(&behavior.prefix, index)),
                description: filter.description.clone(),
                spillable: false,
            });
        }
    }
    found
}

/// A value a filter compares.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum FilterValue {
    Text(String),
    Number(f64),
    Boolean(bool),
}

impl FilterValue {
    /// The value as SQL compares it: a boolean as json_extract gives it, 1
    /// or 0; a number as an integer when it is one.
    pub fn bind(&self) -> SqlValue {
        match self {
            FilterValue::Text(text) => SqlValue::from(text.as_str()),
            FilterValue::Boolean(flag) => SqlValue::Integer(i64::from(*flag)),
            FilterValue::Number(number) => {
                if number.fract() == 0.0 && number.abs() < 9.007_199_254_740_992e15 {
                    SqlValue::Integer(*number as i64)
                } else {
                    SqlValue::Real(*number)
                }
            }
        }
    }
}

/// One member of `where`, checked.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct WhereTerm {
    pub filterable: Filterable,
    /// The values it keeps, null aside.
    pub values: Vec<FilterValue>,
    /// Whether it lists null: it keeps the instances whose field holds no
    /// value.
    pub none: bool,
}

/// Checks a list's `where` against the fields a version filters on and
/// returns its members; an empty or absent `where` filters nothing.
pub(crate) fn parse_where(
    filter: Option<&Value>,
    filterables: &[Filterable],
    schema: &str,
) -> Result<Vec<WhereTerm>, EngineError> {
    let members = match filter {
        None | Some(Value::Null) => return Ok(Vec::new()),
        Some(Value::Object(members)) => members,
        Some(_) => {
            return Err(EngineError::invalid_argument(
                "where is a JSON object of field values, by field",
            ))
        }
    };
    let mut terms = Vec::new();
    for key in json::js_keys(members) {
        let given = &members[key];
        let Some(filterable) = filterables.iter().find(|candidate| candidate.key == key) else {
            let known: Vec<&str> = filterables.iter().map(|known| known.key.as_str()).collect();
            return Err(EngineError::invalid_argument(format!(
                "where names {key}, which is not a field {schema} filters on; it filters on {}: an own field that holds a string, a number or a boolean, or a field a behavior lets a list filter on",
                if known.is_empty() { "none".to_owned() } else { known.join(", ") }
            )));
        };
        let values: Vec<&Value> = match given {
            Value::Array(list) => {
                if list.is_empty() || list.len() > MAX_FILTER_VALUES {
                    return Err(EngineError::invalid_argument(format!(
                        "where.{key} lists 1 to {MAX_FILTER_VALUES} values, got {}",
                        list.len()
                    )));
                }
                list.iter().collect()
            }
            one => vec![one],
        };
        let mut seen = BTreeSet::new();
        let mut kept = Vec::new();
        let mut none = false;
        for value in values {
            if value.is_null() {
                none = true;
                continue;
            }
            let checked = check_value(&format!("where.{key}"), filterable.scalar, value)?;
            if seen.insert(json::stringify(value)) {
                kept.push(checked);
            }
        }
        terms.push(WhereTerm {
            filterable: filterable.clone(),
            values: kept,
            none,
        });
    }
    Ok(terms)
}

/// Checks a lookup's key: an object that names exactly the fields of one
/// unique index, each with one value of its type. It returns the index and
/// the values in its key order.
pub(crate) fn parse_lookup_key<'i>(
    key: &Value,
    indexes: &'i [OwnIndex],
    filterables: &[Filterable],
    schema: &str,
) -> Result<(&'i OwnIndex, Vec<FilterValue>), EngineError> {
    let unique: Vec<&OwnIndex> = indexes.iter().filter(|index| index.unique).collect();
    if unique.is_empty() {
        return Err(EngineError::invalid_argument(format!(
            "{schema} has no unique field to look an instance up by; find one with list and where"
        )));
    }
    let choices = unique
        .iter()
        .map(|index| index.keys.join(" and "))
        .collect::<Vec<_>>()
        .join("; ");
    let Value::Object(members) = key else {
        return Err(EngineError::invalid_argument(format!(
            "a lookup key is a JSON object of the values of one unique index of {schema}: {choices}"
        )));
    };
    let names: Vec<&str> = json::js_keys(members);
    let Some(index) = unique.into_iter().find(|candidate| {
        candidate.keys.len() == names.len()
            && candidate
                .keys
                .iter()
                .all(|name| names.contains(&name.as_str()))
    }) else {
        return Err(EngineError::invalid_argument(format!(
            "a lookup key names the fields of one unique index of {schema} ({choices}), not {}",
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        )));
    };
    let mut values = Vec::new();
    for name in &index.keys {
        let scalar = filterables
            .iter()
            .find(|filterable| filterable.key == *name)
            .map_or(ScalarType::String, |filterable| filterable.scalar);
        values.push(check_value(
            &format!("key.{name}"),
            scalar,
            &members[name.as_str()],
        )?);
    }
    Ok((index, values))
}

fn check_value(at: &str, scalar: ScalarType, value: &Value) -> Result<FilterValue, EngineError> {
    let fits = match (scalar, value) {
        (ScalarType::String, Value::String(text)) => Some(FilterValue::Text(text.clone())),
        (ScalarType::Boolean, Value::Bool(flag)) => Some(FilterValue::Boolean(*flag)),
        (ScalarType::Integer, Value::Number(number)) => {
            let read = json::number_value(number);
            (read.fract() == 0.0 && read.abs() <= 9_007_199_254_740_991.0)
                .then_some(FilterValue::Number(read))
        }
        (ScalarType::Number, Value::Number(number)) => {
            let read = json::number_value(number);
            read.is_finite().then_some(FilterValue::Number(read))
        }
        _ => None,
    };
    fits.ok_or_else(|| {
        EngineError::invalid_argument(format!(
            "{at} is {}{}, not {}",
            match scalar {
                ScalarType::Integer => "an integer".to_owned(),
                other => format!("a {}", other.name()),
            },
            if at.starts_with("where") {
                " or null, or a list of them"
            } else {
                ""
            },
            json::stringify(value)
        ))
    })
}

/// The condition on `engine_instances` of the instance a unique index's
/// values name; its parameters are the namespace, then the values in key
/// order.
pub(crate) fn lookup_condition(
    dialect: Dialect,
    holder: &str,
    schema: &str,
    index: &OwnIndex,
) -> String {
    format!(
        "{} AND namespace = ? AND {}",
        schema_rows(holder, schema),
        index
            .keys
            .iter()
            .zip(&index.types)
            .map(|(key, scalar)| format!("{} = ?", value_expression(dialect, key, *scalar)))
            .collect::<Vec<_>>()
            .join(" AND ")
    )
}

/// What a filtered page reads: the schema's rows in a namespace, its own
/// indexes and the members.
pub(crate) struct FilterPlan<'p> {
    pub namespace: &'p str,
    pub schema: &'p str,
    /// The namespace that holds the schema.
    pub holder: &'p str,
    pub indexes: &'p [OwnIndex],
    pub terms: &'p [WhereTerm],
}

// A range read: the rows of one combination of an index's values, or the
// list index's rows, past the cursor in position order.
struct Range {
    index: String,
    condition: String,
    params: Vec<SqlValue>,
}

/// One page of a filter: the positions of the instances it keeps, in
/// order, at most `limit`, and the position the next page reads on from,
/// `None` after the last.
pub(crate) fn filtered_page(
    tx: &Tx<'_>,
    plan: &FilterPlan<'_>,
    after: i64,
    limit: usize,
) -> Result<(Vec<i64>, Option<i64>)> {
    let dialect = tx.dialect();
    let (ranges, covers) = driving_read(tx, plan)?;
    let residual: Vec<&WhereTerm> = plan
        .terms
        .iter()
        .enumerate()
        .filter(|(position, _)| !covers.contains(position))
        .map(|(_, term)| term)
        .collect();
    let (hit_sql, hit_params) = if residual.is_empty() {
        ("1".to_owned(), Vec::new())
    } else {
        conjunction(dialect, &residual)
    };
    // Each range reads its share of FILTER_SCAN_ROWS, and at least two
    // rows, so a unique index's range always reads to its end. With no
    // member left to test, a range needs no more than a page and one.
    let per = 2.max((FILTER_SCAN_ROWS.max(limit + 1)).div_ceil(ranges.len()));
    let read = if residual.is_empty() {
        (limit + 1).min(per)
    } else {
        per
    };
    // cut is the last position every range has read up to.
    let mut cut: Option<i64> = None;
    let mut rows: Vec<(i64, bool)> = Vec::new();
    for range in &ranges {
        let indexed = match dialect {
            Dialect::Sqlite => format!(" INDEXED BY \"{}\"", range.index),
            Dialect::Postgres => String::new(),
        };
        let mut params = hit_params.clone();
        params.extend(range.params.iter().cloned());
        params.push(SqlValue::Integer(after));
        params.push(SqlValue::Integer(read as i64));
        let found = tx.all(
            format!(
                "SELECT position, ({hit_sql}) AS hit FROM engine_instances{indexed}\n       WHERE {} AND position > ? ORDER BY position LIMIT ?",
                range.condition
            ),
            &params,
        )?;
        if found.len() == read {
            let last = found[found.len() - 1].i64("position")?;
            cut = Some(cut.map_or(last, |cut: i64| cut.min(last)));
        }
        for row in found {
            let hit = matches!(row.value("hit")?, SqlValue::Integer(1));
            rows.push((row.i64("position")?, hit));
        }
    }
    rows.sort_by_key(|(position, _)| *position);
    let mut kept = Vec::new();
    for (position, hit) in rows {
        if cut.is_some_and(|cut| position > cut) {
            break;
        }
        if hit {
            kept.push(position);
            if kept.len() > limit {
                kept.truncate(limit);
                let next = kept[limit - 1];
                return Ok((kept, Some(next)));
            }
        }
    }
    Ok((kept, cut))
}

struct Candidate {
    unique: bool,
    terms: Vec<usize>,
    combinations: usize,
    ranges: Vec<Range>,
}

// driving_read picks the index a page reads through: a unique own index
// first, then the one with the fewest combinations of values, of the own
// indexes whose every key a member names and the behaviors' indexes on a
// member's column; with none, the list index. It returns its range reads
// and the members it serves, by position in the plan's terms.
fn driving_read(tx: &Tx<'_>, plan: &FilterPlan<'_>) -> Result<(Vec<Range>, BTreeSet<usize>)> {
    let dialect = tx.dialect();
    let own_term = |key: &str| {
        plan.terms
            .iter()
            .enumerate()
            .rev()
            .find(|(_, term)| term.filterable.behavior.is_none() && term.filterable.key == key)
            .map(|(position, _)| position)
    };
    let mut best: Option<Candidate> = None;
    let mut consider = |candidate: Candidate| {
        if candidate.combinations > MAX_FILTER_RANGES {
            return;
        }
        let better = match &best {
            None => true,
            Some(best) => {
                (candidate.unique && !best.unique)
                    || (candidate.unique == best.unique
                        && candidate.combinations < best.combinations)
            }
        };
        if better {
            best = Some(candidate);
        }
    };
    for index in plan.indexes {
        let terms: Vec<Option<usize>> = index.keys.iter().map(|key| own_term(key)).collect();
        // The index is partial on a value in each field: it holds no
        // instance a member listing null keeps.
        if terms
            .iter()
            .any(|term| term.is_none_or(|position| plan.terms[position].none))
            || !index_exists(tx, &index.name)?
        {
            continue;
        }
        let covered: Vec<usize> = terms.into_iter().flatten().collect();
        let combinations = covered
            .iter()
            .map(|position| plan.terms[*position].values.len())
            .product();
        let lists: Vec<&[FilterValue]> = covered
            .iter()
            .map(|position| plan.terms[*position].values.as_slice())
            .collect();
        let condition = lookup_condition(dialect, plan.holder, plan.schema, index);
        let ranges = if combinations > MAX_FILTER_RANGES {
            Vec::new()
        } else {
            combinations_of(&lists)
                .into_iter()
                .map(|values| {
                    let mut params = vec![SqlValue::from(plan.namespace)];
                    params.extend(values.iter().map(|value| value.bind()));
                    Range {
                        index: index.name.clone(),
                        condition: condition.clone(),
                        params,
                    }
                })
                .collect()
        };
        consider(Candidate {
            unique: index.unique,
            terms: covered,
            combinations,
            ranges,
        });
    }
    for (position, term) in plan.terms.iter().enumerate() {
        let (Some(column), Some(index)) = (&term.filterable.column, &term.filterable.index) else {
            continue;
        };
        if !index_exists(tx, index)? {
            continue;
        }
        let mut ranges: Vec<Range> = term
            .values
            .iter()
            .map(|value| Range {
                index: index.clone(),
                condition: format!("namespace = ? AND schema = ? AND \"{column}\" = ?"),
                params: vec![
                    SqlValue::from(plan.namespace),
                    SqlValue::from(plan.schema),
                    value.bind(),
                ],
            })
            .collect();
        if term.none {
            ranges.push(Range {
                index: index.clone(),
                condition: format!("namespace = ? AND schema = ? AND \"{column}\" IS NULL"),
                params: vec![SqlValue::from(plan.namespace), SqlValue::from(plan.schema)],
            });
        }
        consider(Candidate {
            unique: false,
            terms: vec![position],
            combinations: term.values.len() + usize::from(term.none),
            ranges,
        });
    }
    Ok(match best {
        None => (
            vec![Range {
                index: "engine_instances_list".to_owned(),
                condition: "namespace = ? AND schema = ?".to_owned(),
                params: vec![SqlValue::from(plan.namespace), SqlValue::from(plan.schema)],
            }],
            BTreeSet::new(),
        ),
        Some(best) => (best.ranges, best.terms.into_iter().collect()),
    })
}

// combinations_of lists every choice of one value from each list.
fn combinations_of(lists: &[&[FilterValue]]) -> Vec<Vec<FilterValue>> {
    lists.iter().fold(vec![Vec::new()], |partial, values| {
        partial
            .iter()
            .flat_map(|prefix| {
                values.iter().map(move |value| {
                    let mut next = prefix.clone();
                    next.push(value.clone());
                    next
                })
            })
            .collect()
    })
}

// conjunction is the SQL that tests every member on a row, with its
// parameters in order.
fn conjunction(dialect: Dialect, terms: &[&WhereTerm]) -> (String, Vec<SqlValue>) {
    let mut parts = Vec::new();
    let mut params = Vec::new();
    for term in terms {
        let filterable = &term.filterable;
        let expression = match &filterable.column {
            Some(column) => format!("\"{column}\""),
            None => value_expression(dialect, &filterable.key, filterable.scalar),
        };
        let mut alternatives = Vec::new();
        if !term.values.is_empty() {
            alternatives.push(format!(
                "{expression} IN ({})",
                vec!["?"; term.values.len()].join(", ")
            ));
            params.extend(term.values.iter().map(FilterValue::bind));
        }
        if term.none {
            alternatives.push(format!("{expression} IS NULL"));
        }
        if filterable.spillable {
            let hashes: Vec<String> = term
                .values
                .iter()
                .filter_map(|value| match value {
                    FilterValue::Text(text) => Some(text),
                    _ => None,
                })
                .filter(|text| json::quote(text).len() as u64 > MIN_VALUE_THRESHOLD)
                .map(|text| json::sha256_hex(&json::quote(text)))
                .collect();
            if !hashes.is_empty() {
                alternatives.push(format!(
                    "{} IN ({})",
                    ref_hash_expression(dialect, &filterable.key),
                    vec!["?"; hashes.len()].join(", ")
                ));
                params.extend(hashes.into_iter().map(SqlValue::from));
            }
        }
        parts.push(if alternatives.len() == 1 {
            alternatives.remove(0)
        } else {
            format!("({})", alternatives.join(" OR "))
        });
    }
    (parts.join(" AND "), params)
}

/// A filter value as `where` writes it, for the JSON of a term (tests).
#[cfg(test)]
fn value_json(value: &FilterValue) -> Value {
    match value {
        FilterValue::Text(text) => Value::from(text.as_str()),
        FilterValue::Number(number) => {
            serde_json::from_str(&json::js_number(*number)).unwrap_or(Value::Null)
        }
        FilterValue::Boolean(flag) => Value::Bool(*flag),
    }
}

/// `where` with each value on an own field put in the form a write stores
/// it, by the version's normalize: an email lowercased finds the instance
/// created with it in capitals. A value on a behavior's field, or of the
/// wrong type, is left as it is.
pub(crate) fn normalized_filter(
    given: Option<&Value>,
    normalize: impl Fn(&str, &Value) -> Value,
) -> Option<Value> {
    let given = given?;
    let Value::Object(members) = given else {
        return Some(given.clone());
    };
    let mut out = Map::new();
    for key in json::js_keys(members) {
        let value = &members[key];
        let normalized = match value {
            Value::Array(list) => {
                Value::Array(list.iter().map(|each| normalize(key, each)).collect())
            }
            one => normalize(key, one),
        };
        out.insert(key.to_owned(), normalized);
    }
    Some(Value::Object(out))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn own(key: &str, scalar: ScalarType) -> Filterable {
        Filterable {
            key: key.into(),
            scalar,
            behavior: None,
            column: None,
            index: None,
            description: None,
            spillable: true,
        }
    }

    #[test]
    fn where_is_checked_against_the_fields_a_version_filters_on() {
        let filterables = [
            own("kind", ScalarType::String),
            own("rank", ScalarType::Integer),
            own("urgent", ScalarType::Boolean),
        ];
        let terms = parse_where(
            Some(&json!({"kind": ["a", "b", "a", null], "rank": 2.0})),
            &filterables,
            "Job",
        )
        .unwrap();
        assert_eq!(
            terms[0].values.iter().map(value_json).collect::<Vec<_>>(),
            [json!("a"), json!("b")]
        );
        assert!(terms[0].none);
        assert_eq!(terms[1].values, [FilterValue::Number(2.0)]);
        let refused = |filter: Value| {
            parse_where(Some(&filter), &filterables, "Job")
                .unwrap_err()
                .message()
                .to_owned()
        };
        assert_eq!(refused(json!({"title": "x"})), "where names title, which is not a field Job filters on; it filters on kind, rank, urgent: an own field that holds a string, a number or a boolean, or a field a behavior lets a list filter on");
        assert_eq!(
            refused(json!({"kind": 1})),
            "where.kind is a string or null, or a list of them, not 1"
        );
        assert_eq!(
            refused(json!({"rank": 1.5})),
            "where.rank is an integer or null, or a list of them, not 1.5"
        );
        assert_eq!(
            refused(json!({"urgent": "yes"})),
            "where.urgent is a boolean or null, or a list of them, not \"yes\""
        );
        assert_eq!(
            refused(json!({"kind": []})),
            "where.kind lists 1 to 100 values, got 0"
        );
        assert_eq!(
            refused(json!(["kind"])),
            "where is a JSON object of field values, by field"
        );
        assert!(parse_where(Some(&json!({})), &filterables, "Job")
            .unwrap()
            .is_empty());
        assert!(parse_where(None, &filterables, "Job").unwrap().is_empty());
        assert!(parse_where(Some(&json!({"x": 1})), &[], "Job")
            .unwrap_err()
            .message()
            .contains("it filters on none"));
    }

    #[test]
    fn values_bind_as_sql_compares_them() {
        assert_eq!(FilterValue::Boolean(true).bind(), SqlValue::Integer(1));
        assert_eq!(FilterValue::Number(2.0).bind(), SqlValue::Integer(2));
        assert_eq!(FilterValue::Number(2.5).bind(), SqlValue::Real(2.5));
        assert_eq!(
            combinations_of(&[
                &[FilterValue::Number(1.0), FilterValue::Number(2.0)],
                &[FilterValue::Boolean(true)]
            ])
            .len(),
            2
        );
    }
}
