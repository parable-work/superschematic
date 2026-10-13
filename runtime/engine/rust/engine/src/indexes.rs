//! The indexes of a schema's instance type over its own fields, the port of
//! `src/instances/indexes.ts` (D16, amended: an instance is found by a
//! unique field, and a list filters by equality; D58).
//!
//! `@unique` and `@key` on a field, and each entry of the type's `indexes`
//! (`@index`), become one index on `engine_instances`: led by the
//! instance's namespace, then the value of each key, partial on the
//! schema's rows in the namespace that holds it and on a value in every
//! key. A unique one is UNIQUE, so the database refuses a write that would
//! give a second instance of a namespace the same values. The index is
//! named by a digest of the holder, the schema and its keys
//! (`engine_unique_<16 hex>`, `engine_index_<16 hex>`), so publish finds it
//! again without a table of its own, and the name is the same on both
//! databases.
//!
//! A key's value is read from the row's JSON text ([`value_expression`]):
//! SQLite's `json_extract(data, '$."<key>"')`, and on Postgres
//! `(data::jsonb)->>'<key>'` cast to the field's type (D58), text for a
//! string, `numeric` for a number and `boolean` for a boolean, so `1` and
//! `1.0` are one value and `"1"` and `1` are two, as SQLite compares them.
//! A lookup or a filter that repeats the expression reaches the index.
//!
//! A key is a top-level own field whose value is a string, a number or a
//! boolean. A list, an object or a JSON value has no one value an index can
//! compare, so define refuses @unique, @key and @index on one, and on any
//! type but the instance type. A key whose JSON key holds `"`, `\` or a
//! control character is refused too: SQLite's JSON path has no escape for
//! them. Absent and null are no value, so any number of instances may lack
//! a unique field.
//!
//! A field an index covers is never stored by hash in the instance's row:
//! publish puts back inline any value a row holds by hash under a key a new
//! index covers, before it creates the index. publish drops the indexes
//! the live version has and the new one lacks, and creates the new one's
//! that the database lacks; a unique index whose stored instances already
//! share values refuses the publish and creates nothing. Engine migration
//! 10 creates the indexes of every live version published before it.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::Value;

use superschematic_engine_storage::{sql_string, Dialect, Row, Sql, SqlValue, Tx};

use crate::errors::{join_and, Result, SchemaIssue};
use crate::json;
use crate::registry::document::{
    array_depth, field_name, fields_of, flag, is_map, json_key, type_of, types_of, SchemaModel,
};
use crate::registry::fields::field_type;
use crate::values::{self, ValueHolder};

/// The JSON type of a value an index compares.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ScalarType {
    String,
    Number,
    Integer,
    Boolean,
}

impl ScalarType {
    fn of(name: &str) -> Option<ScalarType> {
        match name {
            "string" => Some(ScalarType::String),
            "number" => Some(ScalarType::Number),
            "integer" => Some(ScalarType::Integer),
            "boolean" => Some(ScalarType::Boolean),
            _ => None,
        }
    }
}

/// An index of the instance type's own fields.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct OwnIndex {
    /// Its SQL name: `engine_unique_<digest>` or `engine_index_<digest>`.
    pub name: String,
    pub unique: bool,
    /// The JSON keys of its fields, in order.
    pub keys: Vec<String>,
    /// Each key's type, which the Postgres expression casts to.
    pub types: Vec<ScalarType>,
    /// The names of its fields, in order, for messages.
    pub fields: Vec<String>,
}

/// A top-level own field an index or a filter compares.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScalarField {
    pub name: String,
    pub scalar: ScalarType,
}

/// The instance type's own fields whose value is a string, a number or a
/// boolean, by JSON key: the ones an index may cover and a list may filter
/// on. A field whose JSON key a JSON path cannot name is left out.
pub(crate) fn scalar_fields(
    document: &Value,
    instance_type: &str,
) -> BTreeMap<String, ScalarField> {
    let mut found = BTreeMap::new();
    let fields = fields_of(document, instance_type);
    for field in fields {
        let key = json_key(field);
        // The property of a JSON key two fields share is the last one's;
        // the field itself must hold one value.
        let last = fields
            .iter()
            .rev()
            .find(|candidate| json_key(candidate) == key)
            .unwrap_or(field);
        if array_depth(field) > 0 || is_map(field) {
            continue;
        }
        let Some(scalar) = ScalarType::of(&field_type(document, last)) else {
            continue;
        };
        if key_issue(key).is_none() {
            found.insert(
                key.to_owned(),
                ScalarField {
                    name: field_name(field).to_owned(),
                    scalar,
                },
            );
        }
    }
    found
}

/// Why a JSON path cannot name a key, or `None` when it can.
pub(crate) fn key_issue(key: &str) -> Option<String> {
    key.chars()
        .any(|c| c == '"' || c == '\\' || (c as u32) < 0x20)
        .then(|| {
            format!(
                "its JSON key {} holds a quote, a backslash or a control character, which a JSON path cannot name",
                json::quote(key)
            )
        })
}

/// What the engine refuses of a document's @unique, @key and @index: one
/// on a type other than the instance type, on a field that holds no
/// string, number or boolean, or naming a field the instance type does not
/// have.
pub(crate) fn index_issues(document: &Value, instance_type: &str) -> Vec<SchemaIssue> {
    let mut issues = Vec::new();
    let Some(types) = types_of(document) else {
        return issues;
    };
    let scalars = scalar_fields(document, instance_type);
    for type_name in json::js_keys(types) {
        let type_def = &types[type_name];
        let own = type_name == instance_type;
        let type_path = json::pointer(&["types", type_name]);
        let fields = fields_of(document, type_name);
        for (index, field) in fields.iter().enumerate() {
            for decorator in ["unique", "key"] {
                if !flag(field, decorator) {
                    continue;
                }
                let at = format!("{type_path}/fields/{index}/{decorator}");
                let label = format!("field {type_name}.{} is @{decorator}", field_name(field));
                if !own {
                    issues.push(SchemaIssue::new(
                        at,
                        format!(
                            "{label}, and the engine enforces @{decorator} on the instance type's own fields ({instance_type}) only"
                        ),
                    ));
                    continue;
                }
                if let Some(problem) = key_issue(json_key(field)) {
                    issues.push(SchemaIssue::new(at, format!("{label}, and {problem}")));
                } else if !scalars.contains_key(json_key(field)) {
                    issues.push(SchemaIssue::new(
                        at,
                        format!(
                            "{label}, and a unique field holds a string, a number or a boolean: a list, an object or a JSON value has no one value to compare"
                        ),
                    ));
                }
            }
        }
        let declared = type_def
            .get("indexes")
            .and_then(Value::as_array)
            .map_or(&[][..], Vec::as_slice);
        for (index, entry) in declared.iter().enumerate() {
            let at = format!("{type_path}/indexes/{index}");
            if !own {
                issues.push(SchemaIssue::new(
                    at,
                    format!(
                        "type {type_name} declares an index, and the engine indexes the instance type's own fields ({instance_type}) only"
                    ),
                ));
                continue;
            }
            let keys = index_keys(entry);
            if keys.is_empty() {
                issues.push(SchemaIssue::new(
                    format!("{at}/keys"),
                    "an index lists at least one field",
                ));
            }
            if keys.iter().collect::<BTreeSet<_>>().len() != keys.len() {
                issues.push(SchemaIssue::new(
                    format!("{at}/keys"),
                    "an index lists a field twice",
                ));
            }
            for (position, name) in keys.iter().enumerate() {
                let key_at = format!("{at}/keys/{position}");
                let Some(field) = fields
                    .iter()
                    .find(|candidate| field_name(candidate) == *name)
                else {
                    issues.push(SchemaIssue::new(
                        key_at,
                        format!("the index names {name}, which is not a field of {instance_type}"),
                    ));
                    continue;
                };
                if let Some(problem) = key_issue(json_key(field)) {
                    issues.push(SchemaIssue::new(
                        key_at,
                        format!("field {instance_type}.{name}: {problem}"),
                    ));
                } else if !scalars.contains_key(json_key(field)) {
                    issues.push(SchemaIssue::new(
                        key_at,
                        format!(
                            "field {instance_type}.{name} holds no string, number or boolean, and an index compares one value per field"
                        ),
                    ));
                }
            }
        }
    }
    issues
}

fn index_keys(entry: &Value) -> Vec<&str> {
    entry
        .get("keys")
        .and_then(Value::as_array)
        .map(|keys| {
            keys.iter()
                .map(|key| key.as_str().unwrap_or_default())
                .collect()
        })
        .unwrap_or_default()
}

/// The indexes of a version's instance type, named for the namespace that
/// holds the schema: one per @unique or @key field, then one per entry of
/// `indexes`, each set of keys once, a unique one winning over a plain one
/// on the same keys. A field or an entry [`index_issues`] refuses, which a
/// version published before engine migration 10 may hold, is left out.
pub(crate) fn own_indexes(model: &SchemaModel, holder: &str) -> Vec<OwnIndex> {
    let document = &model.document;
    let Some(type_def) = type_of(document, &model.instance_type) else {
        return Vec::new();
    };
    let fields = fields_of(document, &model.instance_type);
    let scalars = scalar_fields(document, &model.instance_type);
    let mut declared: Vec<(bool, Vec<&Value>)> = Vec::new();
    for field in fields {
        if (flag(field, "unique") || flag(field, "key")) && scalars.contains_key(json_key(field)) {
            declared.push((true, vec![field]));
        }
    }
    let entries = type_def
        .get("indexes")
        .and_then(Value::as_array)
        .map_or(&[][..], Vec::as_slice);
    for entry in entries {
        let keys = index_keys(entry);
        let found: Vec<Option<&Value>> = keys
            .iter()
            .map(|name| fields.iter().find(|field| field_name(field) == *name))
            .collect();
        if found.is_empty()
            || found
                .iter()
                .any(|field| field.is_none_or(|field| !scalars.contains_key(json_key(field))))
            || keys.iter().collect::<BTreeSet<_>>().len() != found.len()
        {
            continue;
        }
        declared.push((flag(entry, "unique"), found.into_iter().flatten().collect()));
    }
    let mut by_keys: Vec<OwnIndex> = Vec::new();
    for (unique, fields) in declared {
        let keys: Vec<String> = fields
            .iter()
            .map(|field| json_key(field).to_owned())
            .collect();
        if let Some(position) = by_keys.iter().position(|index| index.keys == keys) {
            let known = &by_keys[position];
            if known.unique || !unique {
                continue;
            }
            by_keys[position] = own_index(holder, &model.name, unique, keys, &fields, &scalars);
            continue;
        }
        by_keys.push(own_index(
            holder,
            &model.name,
            unique,
            keys,
            &fields,
            &scalars,
        ));
    }
    by_keys
}

fn own_index(
    holder: &str,
    schema: &str,
    unique: bool,
    keys: Vec<String>,
    fields: &[&Value],
    scalars: &BTreeMap<String, ScalarField>,
) -> OwnIndex {
    OwnIndex {
        name: index_sql_name(holder, schema, unique, &keys),
        unique,
        types: keys
            .iter()
            .map(|key| {
                scalars
                    .get(key)
                    .map_or(ScalarType::String, |field| field.scalar)
            })
            .collect(),
        keys,
        fields: fields
            .iter()
            .map(|field| field_name(field).to_owned())
            .collect(),
    }
}

/// An index's name, by a digest of what it covers: the first 16 hex digits
/// of the SHA-256 of the holder, the schema, `unique` or `index` and the
/// keys, joined by NUL.
pub(crate) fn index_sql_name(holder: &str, schema: &str, unique: bool, keys: &[String]) -> String {
    let mut parts = vec![holder, schema, if unique { "unique" } else { "index" }];
    parts.extend(keys.iter().map(String::as_str));
    let digest = json::sha256_hex(&parts.join("\u{0}"));
    format!(
        "{}_{}",
        if unique {
            "engine_unique"
        } else {
            "engine_index"
        },
        &digest[..16]
    )
}

/// The SQL expression of an own field's value in `engine_instances.data`.
pub(crate) fn value_expression(dialect: Dialect, key: &str, scalar: ScalarType) -> String {
    match dialect {
        Dialect::Sqlite => format!(
            "json_extract(data, {})",
            sql_string(&format!("$.\"{key}\""))
        ),
        Dialect::Postgres => {
            let text = format!("((data::jsonb)->>{})", sql_string(key));
            match scalar {
                ScalarType::String => text,
                ScalarType::Number | ScalarType::Integer => format!("({text}::numeric)"),
                ScalarType::Boolean => format!("({text}::boolean)"),
            }
        }
    }
}

/// The condition on `engine_instances` an own index is partial on, as SQL
/// with literals, so a query that repeats it reaches the index: the
/// schema's rows in the namespace that holds it. Both are names the engine
/// checked.
pub(crate) fn schema_rows(holder: &str, schema: &str) -> String {
    format!(
        "schema = {} AND schema_namespace = {}",
        sql_string(schema),
        sql_string(holder)
    )
}

fn values_of(dialect: Dialect, index: &OwnIndex) -> Vec<String> {
    index
        .keys
        .iter()
        .zip(&index.types)
        .map(|(key, scalar)| value_expression(dialect, key, *scalar))
        .collect()
}

fn create_index_sql(dialect: Dialect, index: &OwnIndex, holder: &str, schema: &str) -> String {
    let values = values_of(dialect, index);
    format!(
        "CREATE {}INDEX \"{}\" ON engine_instances (namespace, {})\n    WHERE {} AND {}",
        if index.unique { "UNIQUE " } else { "" },
        index.name,
        values.join(", "),
        schema_rows(holder, schema),
        values
            .iter()
            .map(|value| format!("{value} IS NOT NULL"))
            .collect::<Vec<_>>()
            .join(" AND ")
    )
}

/// Whether the database holds an index of the name.
pub(crate) fn index_exists(tx: &Tx<'_>, name: &str) -> Result<bool> {
    Ok(tx
        .get(
            Sql::per(
                "SELECT 1 AS found FROM sqlite_master WHERE type = 'index' AND name = ?",
                "SELECT 1 AS found FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND indexname = ?",
            ),
            &[SqlValue::from(name)],
        )?
        .is_some())
}

/// Instances of one namespace that share the values of a unique index.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct UniqueClash {
    pub index: OwnIndex,
    pub namespace: String,
    /// The values they share, one per key, as the database reads them.
    pub values: Vec<SqlValue>,
    /// How many share them.
    pub count: i64,
}

/// For each unique index of `after` that `before` lacks, up to three sets
/// of values instances of the schema share, by namespace. It reads the rows
/// as stored: a value a row holds by hash compares as its ref.
pub(crate) fn unique_clashes(
    tx: &Tx<'_>,
    holder: &str,
    schema: &str,
    before: &[OwnIndex],
    after: &[OwnIndex],
) -> Result<Vec<UniqueClash>> {
    let mut clashes = Vec::new();
    for index in after {
        if index.unique && !before.iter().any(|known| known.name == index.name) {
            clashes.extend(clashes_of(tx, holder, schema, index)?);
        }
    }
    Ok(clashes)
}

fn clashes_of(
    tx: &Tx<'_>,
    holder: &str,
    schema: &str,
    index: &OwnIndex,
) -> Result<Vec<UniqueClash>> {
    let dialect = tx.dialect();
    let values = values_of(dialect, index);
    let order = match dialect {
        Dialect::Sqlite => "namespace",
        Dialect::Postgres => "namespace COLLATE \"C\"",
    };
    let sql = format!(
        "SELECT namespace, {}, COUNT(*) AS n\n     FROM engine_instances\n     WHERE {} AND {}\n     GROUP BY namespace, {} HAVING COUNT(*) > 1\n     ORDER BY {order} LIMIT 3",
        values
            .iter()
            .enumerate()
            .map(|(position, value)| format!("{value} AS v{position}"))
            .collect::<Vec<_>>()
            .join(", "),
        schema_rows(holder, schema),
        values
            .iter()
            .map(|value| format!("{value} IS NOT NULL"))
            .collect::<Vec<_>>()
            .join(" AND "),
        values.join(", ")
    );
    tx.all(sql, &[])?
        .iter()
        .map(|row: &Row| {
            Ok(UniqueClash {
                index: index.clone(),
                namespace: row.text("namespace")?.to_owned(),
                values: (0..index.keys.len())
                    .map(|position| row.value(&format!("v{position}")).cloned())
                    .collect::<Result<_, _>>()?,
                count: row.i64("n")?,
            })
        })
        .collect()
}

/// Brings the database's indexes of a schema in line with a version being
/// published: drops each index of `before` that `after` lacks and creates
/// each of `after`'s the database lacks, once the values under its keys are
/// inline in every row. It returns the clashes of a unique index the stored
/// instances break, and then creates nothing; the caller refuses the
/// publish, whose transaction rolls the rest back.
pub(crate) fn sync_own_indexes(
    tx: &Tx<'_>,
    holder: &str,
    schema: &str,
    before: &[OwnIndex],
    after: &[OwnIndex],
) -> Result<Vec<UniqueClash>> {
    for index in before {
        if !after.iter().any(|kept| kept.name == index.name) {
            tx.exec(format!("DROP INDEX IF EXISTS \"{}\"", index.name))?;
        }
    }
    let mut missing = Vec::new();
    for index in after {
        if !index_exists(tx, &index.name)? {
            missing.push(index);
        }
    }
    if missing.is_empty() {
        return Ok(Vec::new());
    }
    let keys: BTreeSet<&str> = missing
        .iter()
        .flat_map(|index| index.keys.iter().map(String::as_str))
        .collect();
    inline_values(tx, holder, schema, &keys)?;
    let mut clashes = Vec::new();
    for index in missing.iter().filter(|index| index.unique) {
        clashes.extend(clashes_of(tx, holder, schema, index)?);
    }
    if !clashes.is_empty() {
        return Ok(clashes);
    }
    for index in missing {
        tx.exec(create_index_sql(tx.dialect(), index, holder, schema))?;
    }
    Ok(Vec::new())
}

// inline_values puts back, in every row of the schema, the value of each
// key the row holds by hash: an index compares values, and a row of a
// field an index covers keeps its value inline from now on. Only those
// members change; the row keeps its other refs and holds their values.
fn inline_values(tx: &Tx<'_>, holder: &str, schema: &str, keys: &BTreeSet<&str>) -> Result<()> {
    let pointers: BTreeSet<String> = keys.iter().map(|key| json::pointer(&[key])).collect();
    let rows = tx.all(
        format!(
            "SELECT namespace, id, data, value_refs FROM engine_instances WHERE {} AND value_refs IS NOT NULL",
            schema_rows(holder, schema)
        ),
        &[],
    )?;
    for row in rows {
        let refs = values::refs_of(row.opt_text("value_refs")?)?;
        let (filled, kept): (Vec<String>, Vec<String>) =
            refs.into_iter().partition(|at| pointers.contains(at));
        if filled.is_empty() {
            continue;
        }
        let stored: Value = serde_json::from_str(row.text("data")?).map_err(|error| {
            crate::errors::Error::internal(format!("instance data is not JSON: {error}"))
        })?;
        let data = values::fill(tx, &stored, &filled)?;
        let namespace = row.text("namespace")?;
        let id = row.text("id")?;
        tx.run(
            "UPDATE engine_instances SET data = ?, value_refs = ? WHERE namespace = ? AND schema = ? AND id = ?",
            &[
                SqlValue::from(json::stringify(&data)),
                SqlValue::from(values::refs_text(&kept)),
                SqlValue::from(namespace),
                SqlValue::from(schema),
                SqlValue::from(id),
            ],
        )?;
        let hashes: BTreeSet<String> = kept
            .iter()
            .filter_map(|at| {
                let tokens = json::pointer_tokens(at);
                let member = tokens.first().and_then(|key| data.get(key))?;
                values::ref_hash(member).map(str::to_owned)
            })
            .collect();
        values::hold(
            tx,
            &ValueHolder {
                namespace,
                schema,
                holder: "instance",
                id,
                key: "",
            },
            &hashes,
        )?;
    }
    Ok(())
}

/// What a clash is, naming its namespace and values only where `holder`,
/// the namespace that publishes, holds the instances.
pub(crate) fn clash_message(clash: &UniqueClash, holder: &str, instance_type: &str) -> String {
    let what = if clash.index.fields.len() == 1 {
        format!(
            "field {instance_type}.{} becomes unique",
            clash.index.fields[0]
        )
    } else {
        format!(
            "the unique index on {} of {instance_type} is added",
            join_and(&clash.index.fields)
        )
    };
    if clash.namespace != holder {
        return format!(
            "{what}, and instances of a namespace that looks names up in {holder} share a value"
        );
    }
    let values: Vec<String> = clash
        .index
        .fields
        .iter()
        .zip(&clash.values)
        .map(|(field, value)| format!("{field} {}", shown(value)))
        .collect();
    format!(
        "{what}, and {} instances in namespace {} hold {}",
        clash.count,
        clash.namespace,
        join_and(&values)
    )
}

/// A value a database read, for a message: as `JSON.stringify` writes it,
/// cut short past 80 characters; a long value a row holds by hash is named
/// as such (a ref, which SQLite reads as the JSON text it stores and
/// Postgres as its `jsonb` text).
pub(crate) fn shown(value: &SqlValue) -> String {
    let text = match value {
        SqlValue::Null => "null".to_owned(),
        SqlValue::Integer(number) => json::js_number(*number as f64),
        SqlValue::Real(number) => json::js_number(*number),
        SqlValue::Text(text) if is_ref_text(text) => "a long value stored by hash".to_owned(),
        SqlValue::Text(text) => json::quote(text),
        SqlValue::Blob(_) => "null".to_owned(),
    };
    json::shown(&text)
}

fn is_ref_text(text: &str) -> bool {
    text.starts_with("{\"$value\"")
        || (text.starts_with('{')
            && serde_json::from_str::<Value>(text)
                .ok()
                .is_some_and(|value| values::ref_hash(&value).is_some()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn model(fields: Value, indexes: Value) -> SchemaModel {
        let document = json!({
            "kind": "General",
            "name": "Model",
            "types": {"Model": {"name": "Model", "fields": fields, "indexes": indexes}}
        });
        SchemaModel {
            name: "Model".into(),
            instance_type: "Model".into(),
            canonical: json::stringify(&document),
            document,
        }
    }

    #[test]
    fn an_index_is_named_by_a_digest_of_what_it_covers() {
        // The TypeScript engine's name for this index: the first 16 hex
        // digits of sha256("default\0Model\0unique\0slug").
        let expected = &json::sha256_hex("default\u{0}Model\u{0}unique\u{0}slug")[..16];
        assert_eq!(
            index_sql_name("default", "Model", true, &["slug".into()]),
            format!("engine_unique_{expected}")
        );
        assert!(
            index_sql_name("default", "Model", false, &["a".into(), "b".into()])
                .starts_with("engine_index_")
        );
    }

    #[test]
    fn a_unique_index_wins_over_a_plain_one_on_the_same_keys() {
        let indexes = own_indexes(
            &model(
                json!([
                    {"name": "slug", "typeRef": {"name": "string"}},
                    {"name": "rank", "typeRef": {"name": "number"}, "unique": true},
                    {"name": "tags", "typeRef": {"name": "string", "isArray": true}, "unique": true}
                ]),
                json!([{"keys": ["slug"]}, {"keys": ["slug"], "unique": true}, {"keys": ["rank", "slug"]}, {"keys": ["tags"]}]),
            ),
            "default",
        );
        let summary: Vec<(bool, Vec<String>, Vec<ScalarType>)> = indexes
            .iter()
            .map(|index| (index.unique, index.keys.clone(), index.types.clone()))
            .collect();
        assert_eq!(
            summary,
            [
                (true, vec!["rank".to_owned()], vec![ScalarType::Number]),
                (true, vec!["slug".to_owned()], vec![ScalarType::String]),
                (
                    false,
                    vec!["rank".to_owned(), "slug".to_owned()],
                    vec![ScalarType::Number, ScalarType::String]
                ),
            ]
        );
    }

    #[test]
    fn index_issues_name_what_the_engine_refuses() {
        let document = json!({
            "types": {
                "Model": {"name": "Model", "fields": [
                    {"name": "tags", "typeRef": {"name": "string", "isArray": true}, "unique": true},
                    {"name": "odd", "typeRef": {"name": "string"}, "jsonTag": "a\"b", "key": true}
                ], "indexes": [{"keys": []}, {"keys": ["ghost", "ghost"]}]},
                "Line": {"name": "Line", "fields": [{"name": "sku", "typeRef": {"name": "string"}, "unique": true}], "indexes": [{"keys": ["sku"]}]}
            }
        });
        let issues: Vec<(String, String)> = index_issues(&document, "Model")
            .into_iter()
            .map(|issue| (issue.path, issue.message))
            .collect();
        assert_eq!(
            issues,
            [
                ("/types/Model/fields/0/unique".into(), "field Model.tags is @unique, and a unique field holds a string, a number or a boolean: a list, an object or a JSON value has no one value to compare".into()),
                ("/types/Model/fields/1/key".into(), "field Model.odd is @key, and its JSON key \"a\\\"b\" holds a quote, a backslash or a control character, which a JSON path cannot name".into()),
                ("/types/Model/indexes/0/keys".into(), "an index lists at least one field".into()),
                ("/types/Model/indexes/1/keys".into(), "an index lists a field twice".into()),
                ("/types/Model/indexes/1/keys/0".into(), "the index names ghost, which is not a field of Model".into()),
                ("/types/Model/indexes/1/keys/1".into(), "the index names ghost, which is not a field of Model".into()),
                ("/types/Line/fields/0/unique".into(), "field Line.sku is @unique, and the engine enforces @unique on the instance type's own fields (Model) only".into()),
                ("/types/Line/indexes/0".into(), "type Line declares an index, and the engine indexes the instance type's own fields (Model) only".into()),
            ]
        );
    }

    #[test]
    fn a_keys_value_is_read_from_the_rows_json() {
        assert_eq!(
            value_expression(Dialect::Sqlite, "slug", ScalarType::String),
            "json_extract(data, '$.\"slug\"')"
        );
        assert_eq!(
            value_expression(Dialect::Postgres, "it's", ScalarType::Number),
            "(((data::jsonb)->>'it''s')::numeric)"
        );
        assert_eq!(shown(&SqlValue::from("models/plain")), "\"models/plain\"");
        assert_eq!(shown(&SqlValue::Real(2.0)), "2");
        let hash = "a".repeat(64);
        assert_eq!(
            shown(&SqlValue::from(format!(
                "{{\"bytes\": 3, \"$value\": \"{hash}\"}}"
            ))),
            "a long value stored by hash"
        );
    }
}
