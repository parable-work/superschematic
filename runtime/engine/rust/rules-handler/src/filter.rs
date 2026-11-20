//! A list's filter, sort and page (D117: "a filter, a sort and a search
//! answer as runtime's would"), over every row of a type, as a table's ORM
//! answers them (D90, D99, D112): a member per field holding the
//! operations of its kind (`eq`, `ne`, `in`, `notIn`, `ilike`, `gt`, `gte`,
//! `lt`, `lte`, `between`, `isNull`, `sqlutil.FilterOps`), a member named
//! for a to-one relation holding the target's filter, `allOf` and `anyOf`;
//! a sort by fields, a field with no value last in either direction, the
//! key breaking ties; and a page after a cursor, the last row's sort values
//! and key. A field's value is compared as its column would be: an instant
//! by its time, a UUID by its value, a number by its value, text by code
//! point. `ne` and `notIn` keep a row whose field holds no value, as D112's
//! negations do, and every other comparison leaves one out, as SQL's NULL
//! does.

use std::cmp::Ordering;
use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use superschematic_rules::Refusal;

use crate::document::{Kind, Model};
use crate::store::StoredRow;
use crate::values::{ilike, instant_of, order, same, uuid_of};

/// Where a filtered or sorted value sits in a row.
#[derive(Clone, Debug, PartialEq)]
pub enum Place {
    /// An own field, by its member.
    Own(String),
    /// A use's owned field, by use id and declared name.
    Owned(String, String),
    /// The row's key.
    Key,
}

impl Place {
    /// The value a row holds there, `None` for none.
    pub fn of<'r>(&self, row: &'r StoredRow) -> Option<&'r Value> {
        match self {
            Place::Own(name) => row.data.get(name),
            Place::Owned(use_id, name) => row.owned.get(use_id).and_then(|fields| fields.get(name)),
            Place::Key => None,
        }
        .filter(|value| !value.is_null())
    }
}

/// One condition of a compiled filter; a list of them holds together.
#[derive(Clone, Debug, PartialEq)]
pub enum Cond {
    /// `eq`, `ne`, `gt`, `gte`, `lt`, `lte` or `ilike` of a value.
    Compare {
        place: Place,
        kind: Kind,
        op: String,
        value: Value,
    },
    /// `in` or `notIn` of values (`notIn` when `negate`).
    Among {
        place: Place,
        kind: Kind,
        values: Vec<Value>,
        negate: bool,
    },
    /// `isNull`: the field holds no value (`null`), or holds one.
    Null { place: Place, null: bool },
    /// A to-one relation's target is a live row its filter keeps.
    Through {
        member: String,
        target: String,
        conds: Vec<Cond>,
    },
    /// Any of the branches holds, each a list that holds together.
    AnyOf(Vec<Vec<Cond>>),
}

fn refused(type_name: &str, message: impl std::fmt::Display) -> Refusal {
    Refusal::invalid_argument(format!("filter of {type_name}: {message}"))
}

/// What a filter may name of a type: each own field, each owned field by
/// the name a row's response gives it, and the key.
fn place_of(model: &Model, name: &str) -> Option<(Place, Kind, bool)> {
    if name == model.key.name {
        return Some((Place::Key, model.key.kind.clone(), false));
    }
    if let Some(field) = model.field(name) {
        return Some((
            Place::Own(field.name.clone()),
            field.kind.clone(),
            field.list,
        ));
    }
    model.owned_field(name).map(|owned| {
        (
            Place::Owned(owned.use_id.clone(), owned.name.clone()),
            owned.kind(),
            false,
        )
    })
}

/// A filter compiled against a type and the types its relations reach.
pub fn compile(
    model: &Model,
    models: &BTreeMap<String, Model>,
    filter: &Value,
) -> Result<Vec<Cond>, Refusal> {
    let Value::Object(members) = filter else {
        if filter.is_null() {
            return Ok(Vec::new());
        }
        return Err(refused(&model.name, "a filter is a JSON object"));
    };
    let mut conds = Vec::new();
    for (name, member) in members {
        if member.is_null() {
            continue;
        }
        match name.as_str() {
            "allOf" => {
                let Value::Array(filters) = member else {
                    return Err(refused(&model.name, "allOf is a list of filters"));
                };
                for each in filters {
                    conds.extend(compile(model, models, each)?);
                }
            }
            "anyOf" => {
                let Value::Array(filters) = member else {
                    return Err(refused(&model.name, "anyOf is a list of filters"));
                };
                if filters.is_empty() {
                    continue;
                }
                let mut branches = Vec::new();
                let mut every = false;
                for each in filters {
                    let branch = compile(model, models, each)?;
                    if branch.is_empty() {
                        every = true;
                    }
                    branches.push(branch);
                }
                // A branch of no condition keeps every row, and so does
                // the whole (D90).
                if !every {
                    conds.push(Cond::AnyOf(branches));
                }
            }
            "searchText" => {
                return Err(refused(
                    &model.name,
                    "searchText is not served in development mode yet: a type's search fields (D62) are a later step",
                ))
            }
            _ => {
                if let Some(field) = model.relation(name) {
                    let target = field
                        .relation
                        .as_ref()
                        .map(|relation| relation.target.clone())
                        .unwrap_or_default();
                    let Some(target_model) = models.get(&target) else {
                        return Err(refused(
                            &model.name,
                            format!("{name} points at {target}, which has no published version"),
                        ));
                    };
                    conds.push(Cond::Through {
                        member: field.name.clone(),
                        conds: compile(target_model, models, member)?,
                        target,
                    });
                    continue;
                }
                let Some((place, kind, list)) = place_of(model, name) else {
                    return Err(refused(
                        &model.name,
                        format!("{} has no field {name} a filter takes", model.name),
                    ));
                };
                let ops = if list { &[][..] } else { kind.filter_ops() };
                let Value::Object(given) = member else {
                    return Err(refused(
                        &model.name,
                        format!("{name} takes an object of operations ({})", ops.join(", ")),
                    ));
                };
                for (op, value) in given {
                    if !ops.contains(&op.as_str()) {
                        return Err(refused(
                            &model.name,
                            format!(
                                "{name} takes no {op} (its operations: {})",
                                if ops.is_empty() { "none".to_owned() } else { ops.join(", ") }
                            ),
                        ));
                    }
                    if value.is_null() {
                        continue;
                    }
                    match op.as_str() {
                        "in" | "notIn" => {
                            let Value::Array(values) = value else {
                                return Err(refused(&model.name, format!("{name}.{op} is a list")));
                            };
                            // An empty list adds no condition, as the ORMs
                            // write none.
                            if values.is_empty() {
                                continue;
                            }
                            conds.push(Cond::Among {
                                place: place.clone(),
                                kind: kind.clone(),
                                values: values.clone(),
                                negate: op == "notIn",
                            });
                        }
                        "between" => {
                            let pair = value.as_array().filter(|pair| pair.len() == 2);
                            let Some(pair) = pair else {
                                return Err(refused(
                                    &model.name,
                                    format!("{name}.between is a list of two values"),
                                ));
                            };
                            for (op, value) in [("gte", &pair[0]), ("lte", &pair[1])] {
                                conds.push(Cond::Compare {
                                    place: place.clone(),
                                    kind: kind.clone(),
                                    op: op.to_owned(),
                                    value: value.clone(),
                                });
                            }
                        }
                        "isNull" => {
                            let Some(null) = value.as_bool() else {
                                return Err(refused(&model.name, format!("{name}.isNull is a boolean")));
                            };
                            conds.push(Cond::Null {
                                place: place.clone(),
                                null,
                            });
                        }
                        "ilike" if !value.is_string() => {
                            return Err(refused(&model.name, format!("{name}.ilike is a string")));
                        }
                        _ => conds.push(Cond::Compare {
                            place: place.clone(),
                            kind: kind.clone(),
                            op: op.clone(),
                            value: value.clone(),
                        }),
                    }
                }
            }
        }
    }
    Ok(conds)
}

/// The types whose rows a compiled filter reads through its relations.
pub fn targets(conds: &[Cond], out: &mut Vec<String>) {
    for cond in conds {
        match cond {
            Cond::Through { target, conds, .. } => {
                if !out.contains(target) {
                    out.push(target.clone());
                }
                targets(conds, out);
            }
            Cond::AnyOf(branches) => {
                for branch in branches {
                    targets(branch, out);
                }
            }
            _ => {}
        }
    }
}

/// A value as a field of a kind compares: an instant by its time, a UUID
/// by its value, anything else as it is.
#[derive(Clone, Debug, PartialEq)]
enum Comparable {
    Instant(i64),
    Uuid(u128),
    Plain(Value),
}

fn comparable(kind: &Kind, value: &Value) -> Comparable {
    match kind {
        Kind::DateTime => {
            instant_of(value).map_or(Comparable::Plain(value.clone()), Comparable::Instant)
        }
        Kind::Uuid => value
            .as_str()
            .and_then(uuid_of)
            .map_or(Comparable::Plain(value.clone()), Comparable::Uuid),
        _ => Comparable::Plain(value.clone()),
    }
}

fn compare(kind: &Kind, held: &Value, value: &Value) -> Option<Ordering> {
    match (comparable(kind, held), comparable(kind, value)) {
        (Comparable::Instant(a), Comparable::Instant(b)) => Some(a.cmp(&b)),
        (Comparable::Uuid(a), Comparable::Uuid(b)) => Some(a.cmp(&b)),
        (Comparable::Plain(a), Comparable::Plain(b)) => {
            let comparable = matches!(
                (&a, &b),
                (Value::Number(_), Value::Number(_))
                    | (Value::String(_), Value::String(_))
                    | (Value::Bool(_), Value::Bool(_))
            );
            if same(&a, &b) {
                Some(Ordering::Equal)
            } else if comparable {
                Some(order(&a, &b))
            } else {
                None
            }
        }
        _ => None,
    }
}

fn equal(kind: &Kind, held: &Value, value: &Value) -> bool {
    compare(kind, held, value) == Some(Ordering::Equal)
}

/// Whether a row keeps to every condition. `reach` holds the rows of each
/// type a relation reaches.
pub fn keeps(conds: &[Cond], row: &StoredRow, reach: &BTreeMap<String, &[StoredRow]>) -> bool {
    conds.iter().all(|cond| holds(cond, row, reach))
}

fn held(place: &Place, row: &StoredRow) -> Option<Value> {
    match place {
        Place::Key => Some(Value::String(row.id.clone())),
        _ => place.of(row).cloned(),
    }
}

fn holds(cond: &Cond, row: &StoredRow, reach: &BTreeMap<String, &[StoredRow]>) -> bool {
    match cond {
        Cond::Compare {
            place,
            kind,
            op,
            value,
        } => {
            let Some(held) = held(place, row) else {
                // NULL compares as nothing; ne keeps it (D112).
                return op == "ne";
            };
            match op.as_str() {
                "eq" => equal(kind, &held, value),
                "ne" => !equal(kind, &held, value),
                "ilike" => match (&held, value) {
                    (Value::String(text), Value::String(pattern)) => {
                        ilike(text, &format!("%{pattern}%"))
                    }
                    _ => false,
                },
                "gt" => compare(kind, &held, value) == Some(Ordering::Greater),
                "gte" => matches!(
                    compare(kind, &held, value),
                    Some(Ordering::Greater | Ordering::Equal)
                ),
                "lt" => compare(kind, &held, value) == Some(Ordering::Less),
                "lte" => matches!(
                    compare(kind, &held, value),
                    Some(Ordering::Less | Ordering::Equal)
                ),
                _ => false,
            }
        }
        Cond::Among {
            place,
            kind,
            values,
            negate,
        } => {
            let Some(held) = held(place, row) else {
                return *negate;
            };
            let among = values.iter().any(|value| equal(kind, &held, value));
            among != *negate
        }
        Cond::Null { place, null } => held(place, row).is_none() == *null,
        Cond::Through {
            member,
            target,
            conds,
        } => {
            let Some(key) = row.data.get(member).and_then(Value::as_str) else {
                return false;
            };
            let key = uuid_of(key).map_or_else(|| key.to_owned(), crate::values::base62);
            reach
                .get(target)
                .copied()
                .unwrap_or_default()
                .iter()
                .filter(|candidate| {
                    candidate.id == key
                        || uuid_of(&candidate.id).map(crate::values::base62).as_deref()
                            == Some(&key)
                })
                .any(|candidate| keeps(conds, candidate, reach))
        }
        Cond::AnyOf(branches) => branches.iter().any(|branch| keeps(branch, row, reach)),
    }
}

/// One term of a sort: a field, and whether it descends.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Sort {
    pub field: String,
    #[serde(default)]
    pub desc: bool,
}

impl Sort {
    /// A sort as a query writes it: `placedAt`, or `-placedAt` descending.
    pub fn parse(text: &str) -> Sort {
        match text.strip_prefix('-') {
            Some(field) => Sort {
                field: field.to_owned(),
                desc: true,
            },
            None => Sort {
                field: text.to_owned(),
                desc: false,
            },
        }
    }
}

/// A sort compiled against a type: each term's place and kind, and its
/// direction.
#[derive(Clone, Debug)]
pub struct Order {
    terms: Vec<(Place, Kind, bool)>,
    key_kind: Kind,
}

/// A sort compiled against a type.
pub fn ordering(model: &Model, sorts: &[Sort]) -> Result<Order, Refusal> {
    let mut terms = Vec::new();
    for sort in sorts {
        let Some((place, kind, list)) = place_of(model, &sort.field) else {
            return Err(Refusal::invalid_argument(format!(
                "sort of {}: {} has no field {}",
                model.name, model.name, sort.field
            )));
        };
        if list || !kind.sorts() {
            return Err(Refusal::invalid_argument(format!(
                "sort of {}: {} holds values no sort orders",
                model.name, sort.field
            )));
        }
        terms.push((place, kind, sort.desc));
    }
    Ok(Order {
        terms,
        key_kind: model.key.kind.clone(),
    })
}

impl Order {
    fn values(&self, row: &StoredRow) -> Vec<Option<Value>> {
        self.terms
            .iter()
            .map(|(place, _, _)| held(place, row))
            .collect()
    }

    fn key_order(&self, a: &str, b: &str) -> Ordering {
        if self.key_kind == Kind::Uuid {
            if let (Some(a), Some(b)) = (uuid_of(a), uuid_of(b)) {
                return a.cmp(&b);
            }
        }
        a.cmp(b)
    }

    /// How two rows' sort values and keys order: each term in its
    /// direction, a field with no value last in either, then the key in the
    /// first term's direction (ascending with no term).
    fn compare(&self, a: (&[Option<Value>], &str), b: (&[Option<Value>], &str)) -> Ordering {
        for (index, (_, kind, desc)) in self.terms.iter().enumerate() {
            let by = match (&a.0[index], &b.0[index]) {
                (None, None) => Ordering::Equal,
                (None, Some(_)) => Ordering::Greater,
                (Some(_), None) => Ordering::Less,
                (Some(x), Some(y)) => {
                    let by = compare(kind, x, y).unwrap_or_else(|| order(x, y));
                    if *desc {
                        by.reverse()
                    } else {
                        by
                    }
                }
            };
            if by != Ordering::Equal {
                return by;
            }
        }
        let by = self.key_order(a.1, b.1);
        if self.terms.first().is_some_and(|(_, _, desc)| *desc) {
            by.reverse()
        } else {
            by
        }
    }

    /// The rows in the sort's order.
    pub fn sort(&self, rows: &mut [&StoredRow]) {
        rows.sort_by(|a, b| self.compare((&self.values(a), &a.id), (&self.values(b), &b.id)));
    }

    /// The cursor after a row: its sort values and its key, as JSON text.
    pub fn cursor(&self, row: &StoredRow) -> String {
        let mut tuple: Vec<Value> = self
            .values(row)
            .into_iter()
            .map(|value| value.unwrap_or(Value::Null))
            .collect();
        tuple.push(Value::String(row.id.clone()));
        Value::Array(tuple).to_string()
    }

    /// The rows after a cursor, in the sort's order.
    pub fn after<'r>(
        &self,
        rows: Vec<&'r StoredRow>,
        cursor: &str,
        type_name: &str,
    ) -> Result<Vec<&'r StoredRow>, Refusal> {
        let wrong = || {
            Refusal::invalid_argument(format!(
                "list of {type_name}: the cursor is not one this sort returned"
            ))
        };
        let tuple: Vec<Value> = serde_json::from_str(cursor).map_err(|_| wrong())?;
        if tuple.len() != self.terms.len() + 1 {
            return Err(wrong());
        }
        let Some(Value::String(key)) = tuple.last() else {
            return Err(wrong());
        };
        let values: Vec<Option<Value>> = tuple[..self.terms.len()]
            .iter()
            .map(|value| (!value.is_null()).then(|| value.clone()))
            .collect();
        Ok(rows
            .into_iter()
            .filter(|row| {
                self.compare((&self.values(row), &row.id), (&values, key)) == Ordering::Greater
            })
            .collect())
    }
}

/// A row as a type's response gives it, as a table's row reads: its key,
/// its own fields in the type's order, each use's owned fields by the
/// names the table gives them (an instant as `toISOString`'s text), and
/// `_version`, the version a write is held to.
pub fn render(model: &Model, row: &StoredRow) -> Value {
    let mut out = Map::new();
    out.insert(model.key.name.clone(), Value::String(row.id.clone()));
    for field in &model.fields {
        if let Some(value) = row.data.get(&field.name).filter(|value| !value.is_null()) {
            out.insert(field.name.clone(), value.clone());
        }
    }
    for owned in &model.owned {
        let Some(value) = row
            .owned
            .get(&owned.use_id)
            .and_then(|fields| fields.get(&owned.name))
            .filter(|value| !value.is_null())
        else {
            continue;
        };
        let value = match (owned.kind(), value.as_i64()) {
            (Kind::DateTime, Some(ms)) => {
                crate::values::iso_of(ms).map_or_else(|| value.clone(), Value::String)
            }
            _ => value.clone(),
        };
        out.insert(owned.field.clone(), value);
    }
    out.insert("_version".to_owned(), Value::from(row.version));
    Value::Object(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::declarations::Declarations;
    use crate::document::read;
    use serde_json::json;

    fn models() -> BTreeMap<String, Model> {
        let declarations = Declarations::ported();
        let task = read(
            &json!({
                "name": "Task",
                "behaviors": [{"name": "Workflow", "field": "status", "config": {"states": ["todo", "done"], "transitions": [{"from": "todo", "to": "done"}]}}],
                "fields": [
                    {"name": "title", "typeRef": {"name": "string"}},
                    {"name": "points", "typeRef": {"name": "Generic.Int64"}},
                    {"name": "due", "typeRef": {"name": "Temporal.DateTime"}},
                    {"name": "project", "typeRef": {"name": "Project"}, "relation": {"type": "Project"}}
                ]
            }),
            &declarations,
        )
        .expect("Task reads");
        let project = read(
            &json!({"name": "Project", "fields": [{"name": "name", "typeRef": {"name": "string"}}]}),
            &declarations,
        )
        .expect("Project reads");
        BTreeMap::from([("Task".to_owned(), task), ("Project".to_owned(), project)])
    }

    fn row(id: &str, data: Value, status: &str) -> StoredRow {
        StoredRow {
            id: id.to_owned(),
            version: 1,
            data: data.as_object().cloned().unwrap_or_default(),
            owned: BTreeMap::from([(
                "status".to_owned(),
                json!({"status": status, "enteredAt": 1000})
                    .as_object()
                    .cloned()
                    .unwrap_or_default(),
            )]),
        }
    }

    fn kept(filter: Value) -> Vec<String> {
        let models = models();
        let tasks = vec![
            row(
                "t1",
                json!({"title": "Write", "points": 3, "due": "2026-01-01T00:00:00.000Z", "projectId": "p1"}),
                "todo",
            ),
            row(
                "t2",
                json!({"title": "review", "points": 5, "projectId": "p2"}),
                "done",
            ),
            row(
                "t3",
                json!({"title": "Ship it", "due": "2026-03-01T00:00:00.000Z"}),
                "todo",
            ),
        ];
        let projects = vec![
            StoredRow {
                id: "p1".into(),
                version: 1,
                data: json!({"name": "Alpha"})
                    .as_object()
                    .cloned()
                    .unwrap_or_default(),
                owned: BTreeMap::new(),
            },
            StoredRow {
                id: "p2".into(),
                version: 1,
                data: json!({"name": "Beta"})
                    .as_object()
                    .cloned()
                    .unwrap_or_default(),
                owned: BTreeMap::new(),
            },
        ];
        let conds = compile(&models["Task"], &models, &filter).expect("the filter compiles");
        let reach: BTreeMap<String, &[StoredRow]> = BTreeMap::from([
            ("Project".to_owned(), projects.as_slice()),
            ("Task".to_owned(), tasks.as_slice()),
        ]);
        tasks
            .iter()
            .filter(|row| keeps(&conds, row, &reach))
            .map(|row| row.id.clone())
            .collect()
    }

    #[test]
    fn a_filter_answers_as_a_tables_does() {
        assert_eq!(kept(json!({"points": {"gte": 3}})), ["t1", "t2"]);
        assert_eq!(
            kept(json!({"points": {"ne": 3}})),
            ["t2", "t3"],
            "ne keeps no value"
        );
        assert_eq!(kept(json!({"points": {"notIn": [5]}})), ["t1", "t3"]);
        assert_eq!(kept(json!({"points": {"in": []}})), ["t1", "t2", "t3"]);
        assert_eq!(kept(json!({"title": {"ilike": "RE"}})), ["t2"]);
        assert_eq!(
            kept(json!({"due": {"gt": "2026-02-01T00:00:00+00:00"}})),
            ["t3"]
        );
        assert_eq!(kept(json!({"due": {"isNull": true}})), ["t2"]);
        assert_eq!(kept(json!({"status": {"eq": "todo"}})), ["t1", "t3"]);
        assert_eq!(
            kept(json!({"statusEnteredAt": {"lte": "1970-01-01T00:00:01Z"}})),
            ["t1", "t2", "t3"]
        );
        assert_eq!(kept(json!({"project": {"name": {"eq": "Beta"}}})), ["t2"]);
        assert_eq!(
            kept(json!({"anyOf": [{"points": {"eq": 5}}, {"title": {"eq": "Ship it"}}]})),
            ["t2", "t3"]
        );
        assert_eq!(
            kept(
                json!({"allOf": [{"anyOf": [{"points": {"eq": 3}}, {"points": {"eq": 5}}]}, {"status": {"eq": "done"}}]})
            ),
            ["t2"]
        );
        assert_eq!(kept(json!({"anyOf": [{}, {"points": {"eq": 5}}]})).len(), 3);
        assert_eq!(kept(json!({"points": {"between": [4, 9]}})), ["t2"]);
    }

    #[test]
    fn a_filter_refuses_what_a_field_does_not_take() {
        let models = models();
        for filter in [
            json!({"title": {"gt": "a"}}),
            json!({"colour": {"eq": "red"}}),
            json!({"searchText": "x"}),
            json!({"points": {"in": 3}}),
        ] {
            assert!(
                compile(&models["Task"], &models, &filter).is_err(),
                "{filter}"
            );
        }
    }

    #[test]
    fn a_sort_puts_no_value_last_and_pages_by_its_cursor() {
        let models = models();
        let rows = [
            row("t1", json!({"points": 3}), "todo"),
            row("t2", json!({}), "todo"),
            row("t3", json!({"points": 1}), "todo"),
            row("t4", json!({"points": 3}), "todo"),
        ];
        for (desc, expected) in [
            (false, ["t3", "t1", "t4", "t2"]),
            (true, ["t4", "t1", "t3", "t2"]),
        ] {
            let order = ordering(
                &models["Task"],
                &[Sort {
                    field: "points".into(),
                    desc,
                }],
            )
            .expect("a sort");
            let mut sorted: Vec<&StoredRow> = rows.iter().collect();
            order.sort(&mut sorted);
            let ids: Vec<&str> = sorted.iter().map(|row| row.id.as_str()).collect();
            assert_eq!(ids, expected);
            let cursor = order.cursor(sorted[1]);
            let rest = order
                .after(sorted.clone(), &cursor, "Task")
                .expect("the cursor reads");
            let ids: Vec<&str> = rest.iter().map(|row| row.id.as_str()).collect();
            assert_eq!(ids, &expected[2..]);
        }
        assert!(ordering(&models["Task"], &[Sort::parse("-nothing")]).is_err());
    }
}
