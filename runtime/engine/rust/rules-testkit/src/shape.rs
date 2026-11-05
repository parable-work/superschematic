//! The engine's shapes (D16, D58), which the recorded scenarios hold: a
//! schema document read as a [`TypeSpec`], a row written as the engine's
//! record, the change log written as the engine's events, and a refusal
//! written as the engine's error. The core knows no namespace, no version
//! number and no schema document, so where the engine's message names them
//! the host writes it from the refusal's details, as the core's README
//! says a host does.
//!
//! The engine's Links is a to-one relation on a table (D103): a document
//! that composes Links has each link with one target read as an own field
//! `<link>Id` holding the target's key, whose [`FieldSpec`] names the
//! relation and its target type, and no Links use. A record and an event
//! show the field as the engine's `Links.targets.<link>`, `{ schema, id }`,
//! and not among the row's own fields.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};

use superschematic_rules::{
    Action, Code, ConfigIssueKind, Core, FieldSpec, Refusal, SubRow, TypeSpec, UseSpec,
};

/// The engine's behavior whose links the replay reads as relations.
pub const LINKS: &str = "Links";

use crate::host::{Logged, StoredRow};

/// The namespace every scenario of the corpus writes in.
pub const NAMESPACE: &str = "default";

/// The advice the engine's incompatible-change message ends with.
const NEW_VERSION_ADVICE: &str = "A new version may only add optional fields and enum values, widen bounds, change documentation and make the behavior changes each behavior allows; publish any other change under a new schema name";

/// One own field a schema document declares, for the store's own-field
/// check, its normalization and a transcript's table.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FieldDef {
    pub name: String,
    pub required: bool,
    /// Its JSON type, from its `typeRef` ([`json_type`]).
    pub json_type: String,
    /// Its `typeRef`'s name (`string`, `Temporal.DateTime`).
    pub type_name: String,
    /// Whether its values are instants ([`is_date_time`]).
    pub date_time: bool,
}

/// A schema document read for the host: the type the core runs, and the
/// own fields the store checks.
#[derive(Clone, Debug, PartialEq)]
pub struct Defined {
    pub spec: TypeSpec,
    /// The name the document gives its instance type, which the engine's
    /// define refusals name where its other messages name the schema: the
    /// spec's name unless the document names them apart (schema `invoices`,
    /// type `Invoice`).
    pub type_name: String,
    pub fields: Vec<FieldDef>,
    /// Whether the document composes the engine's Links, whose links the
    /// spec holds as relations.
    pub links: bool,
    /// Why its Links cannot be a table's relations: a link that holds a
    /// list of targets or pins one; `None` when each link maps.
    pub unmapped: Option<String>,
}

/// Why a scenario stops at a define whose Links a relation cannot hold.
pub const UNMAPPED_LINKS: &str = "a link that holds a list of targets, or pins a revision or a release, which a to-one relation cannot hold";

/// Reads a schema document: its instance type (the type named as the
/// schema, else its first), its behaviors in list order, each a use with its
/// config as the document gives it (none where it gives none), on the field
/// an entry's `field` names (the IR's `BehaviorRef.field`: Reconcile on
/// `observed`, D104) or else on the type, and its own fields, which the type's spec lists too, each by its
/// `typeRef`'s name and whether its values are instants. Links is no use:
/// each link with one target is an own field `<link>Id`, a string, that
/// holds the target's key, after the document's own fields.
pub fn read_document(document: &Value) -> Result<Defined, String> {
    let name = document
        .get("name")
        .and_then(Value::as_str)
        .ok_or("a schema document names its schema")?;
    let types = document
        .get("types")
        .and_then(Value::as_object)
        .ok_or("a schema document holds its types")?;
    let (instance_type, shape) = types
        .get_key_value(name)
        .or_else(|| types.iter().next())
        .and_then(|(named, shape)| Some((named.clone(), shape.as_object()?)))
        .ok_or("a schema document holds a type")?;
    let mut spec = TypeSpec::new(name);
    let mut links = false;
    let mut unmapped = None;
    let mut related: Vec<(String, String)> = Vec::new();
    for entry in shape
        .get("behaviors")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        let behavior = entry
            .get("name")
            .and_then(Value::as_str)
            .ok_or("a behavior entry names its behavior")?;
        if behavior == LINKS {
            links = true;
            for (name, link) in entry
                .get("config")
                .and_then(|config| config.get("links"))
                .and_then(Value::as_object)
                .into_iter()
                .flatten()
            {
                let many = link.get("many") == Some(&Value::Bool(true));
                let pinned = link
                    .get("pinned")
                    .is_some_and(|pinned| !pinned.is_null() && pinned != &Value::Bool(false));
                if many || pinned {
                    unmapped = Some(UNMAPPED_LINKS.to_owned());
                }
                let target = link
                    .get("schema")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                related.push((name.clone(), target.to_owned()));
            }
            continue;
        }
        // A config left out is none: the core checks it as {} and refuses
        // it for a behavior that needs one, as the meta-schema did. An entry
        // that names a field is anchored on it.
        let config = entry.get("config").cloned();
        spec = spec.with(match entry.get("field").and_then(Value::as_str) {
            Some(field) => UseSpec::on_field(field, behavior, config),
            None => UseSpec::on_type(behavior, config),
        });
    }
    let mut fields: Vec<FieldDef> = shape
        .get("fields")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|field| {
            Some(FieldDef {
                name: field.get("name")?.as_str()?.to_owned(),
                required: field.get("required").and_then(Value::as_bool) == Some(true),
                json_type: json_type(field),
                type_name: type_name(field).to_owned(),
                date_time: is_date_time(field),
            })
        })
        .collect();
    for field in &fields {
        let mut own = FieldSpec::new(&field.name, &field.type_name);
        own.date_time = field.date_time;
        spec = spec.with_field(own);
    }
    for (link, target) in related {
        let field = link_field(&link);
        spec = spec.with_field(FieldSpec::relation(&field, "string", &link, &target));
        fields.push(FieldDef {
            name: field,
            required: false,
            json_type: "string".to_owned(),
            type_name: "string".to_owned(),
            date_time: false,
        });
    }
    Ok(Defined {
        spec,
        type_name: instance_type,
        fields,
        links,
        unmapped,
    })
}

/// The own field a link's key is held in: `<link>Id`.
pub fn link_field(link: &str) -> String {
    format!("{link}Id")
}

/// A row's own fields without the ones its links' keys are held in.
pub fn without_links(spec: &TypeSpec, own: &Map<String, Value>) -> Map<String, Value> {
    own.iter()
        .filter(|(name, _)| {
            spec.field(name)
                .is_none_or(|field| field.relation.is_none())
        })
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect()
}

/// A row's links as the engine's Links field: `{ targets: { <link>: {
/// schema, id } } }`, `{}` when it holds none; `None` for a type whose
/// document composed no Links, which has no relation.
pub fn links_of(spec: &TypeSpec, own: &Map<String, Value>) -> Option<Value> {
    let relations: Vec<&FieldSpec> = spec
        .fields
        .iter()
        .filter(|field| field.relation.is_some())
        .collect();
    if relations.is_empty() {
        return None;
    }
    let mut targets = Map::new();
    for field in relations {
        let (Some(relation), Some(Value::String(id))) = (&field.relation, own.get(&field.name))
        else {
            continue;
        };
        targets.insert(
            relation.name.clone(),
            json!({ "schema": relation.type_name, "id": id }),
        );
    }
    Some(if targets.is_empty() {
        json!({})
    } else {
        json!({ "targets": targets })
    })
}

// type_name is a field's typeRef's name; empty for a field with none.
fn type_name(field: &Value) -> &str {
    field
        .get("typeRef")
        .and_then(|type_ref| type_ref.get("name"))
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// Whether a field a schema document declares holds instants: one value,
/// not a list or a map, of a scalar the engine gives the JSON Schema format
/// `date-time` by its name, one whose last dotted segment is `DateTime`
/// (`Temporal.DateTime`; a `Temporal.Date` is a day, no instant), the rule
/// the engine's scalar traits apply to a scalar that declares no format.
pub fn is_date_time(field: &Value) -> bool {
    let type_ref = field.get("typeRef");
    let flag = |name: &str| {
        type_ref
            .and_then(|type_ref| type_ref.get(name))
            .and_then(Value::as_bool)
            == Some(true)
    };
    !flag("isArray")
        && !flag("isMap")
        && type_name(field)
            .rsplit('.')
            .next()
            .is_some_and(|last| last.eq_ignore_ascii_case("datetime"))
}

/// A write's own fields as the store keeps them: each instant a date-time
/// field holds as text in its canonical form, as the engine's parse of the
/// scalar stores it (D16): the fraction of a second written without the
/// zeros it ends with, and none when it is zero
/// (`2026-10-12T09:00:00.000Z` is stored `2026-10-12T09:00:00Z`). Text that
/// holds no instant, and every other field, stays as it is.
pub fn normalize_own(fields: &[FieldDef], data: &Map<String, Value>) -> Map<String, Value> {
    data.iter()
        .map(|(name, value)| {
            let instant = fields
                .iter()
                .any(|field| field.name == *name && field.date_time);
            let value = match value {
                Value::String(text) if instant => Value::from(canonical_instant(text)),
                _ => value.clone(),
            };
            (name.clone(), value)
        })
        .collect()
}

// canonical_instant writes a date-time's text with its fraction of a second
// cut to the digits that count; text that holds no instant stays.
fn canonical_instant(text: &str) -> String {
    if superschematic_rules_behaviors::parse_date_time(text).is_none() {
        return text.to_owned();
    }
    let Some(dot) = text.find('.') else {
        return text.to_owned();
    };
    let digits = text[dot + 1..]
        .find(|c: char| !c.is_ascii_digit())
        .map_or(text.len(), |end| dot + 1 + end);
    let fraction = text[dot + 1..digits].trim_end_matches('0');
    if fraction.is_empty() {
        format!("{}{}", &text[..dot], &text[digits..])
    } else {
        format!("{}.{fraction}{}", &text[..dot], &text[digits..])
    }
}

/// The JSON type of a field a schema document declares: `string`,
/// `number`, `integer` or `boolean` for the builtin its `typeRef` names, a
/// string for a `Temporal` or `Identity` scalar, `array` for a list or a
/// map's (`isArray`), and `object` for anything else.
pub fn json_type(field: &Value) -> String {
    let type_ref = field.get("typeRef");
    let flag = |name: &str| {
        type_ref
            .and_then(|type_ref| type_ref.get(name))
            .and_then(Value::as_bool)
            == Some(true)
    };
    if flag("isMap") {
        return "object".to_owned();
    }
    if flag("isArray") {
        return "array".to_owned();
    }
    let name = type_ref
        .and_then(|type_ref| type_ref.get("name"))
        .and_then(Value::as_str)
        .unwrap_or_default();
    match name {
        "string" | "number" | "integer" | "boolean" => name.to_owned(),
        _ if name.starts_with("Temporal.") || name.starts_with("Identity.") => "string".to_owned(),
        _ => "object".to_owned(),
    }
}

/// The own-field problems the store refuses a create's fields or an
/// update's patch for: a field the document does not declare, and a
/// required field a create leaves out or a patch clears.
pub fn own_field_problems(
    type_name: &str,
    fields: &[FieldDef],
    data: &Map<String, Value>,
    create: bool,
) -> Vec<String> {
    let mut problems = Vec::new();
    for (name, value) in data {
        let declared = fields.iter().any(|field| field.name == *name);
        if !declared && (create || !value.is_null()) {
            problems.push(format!("{type_name} has no field {name}"));
        }
    }
    for field in fields.iter().filter(|field| field.required) {
        let missing = match data.get(&field.name) {
            None => create,
            Some(value) => value.is_null(),
        };
        if missing {
            problems.push(format!("{type_name} requires field {}", field.name));
        }
    }
    problems
}

/// A raw write of behaviors' columns a test made with SQL of its own, read
/// as the owned fields it sets on one row.
#[derive(Clone, Debug, PartialEq)]
pub struct RawWrite {
    pub type_name: String,
    pub key: String,
    /// The fields it sets, by use id; null clears one.
    pub owned: BTreeMap<String, Map<String, Value>>,
}

/// Reads a statement as a raw write of the owned fields of one row, in the
/// engine's layout (D58): `UPDATE engine_instances SET <column> = <value>
/// [, ...] WHERE schema = '<type>' AND id = '<key>'`, each column
/// `bhv_<key>__<name>`, where the key is a use's behavior's name lowercased
/// with each run of other characters one `_` and the name a field its
/// declaration lists, in snake case (`bhv_workflow__entered_at` for
/// Workflow's `enteredAt`), and each value `NULL`, a number or a quoted
/// string. `live` is a type's live version. `None` for any other statement:
/// the replayer does not run SQL, it reads this one form.
pub fn raw_owned_write(
    sql: &str,
    core: &Core,
    live: &dyn Fn(&str) -> Option<TypeSpec>,
) -> Option<RawWrite> {
    let tokens = tokens(sql)?;
    let mut at = tokens.iter();
    let mut next = || at.next();
    let word = |token: Option<&Token>, expected: &str| matches!(token, Some(Token::Word(word)) if word.eq_ignore_ascii_case(expected));
    if !(word(next(), "UPDATE") && word(next(), "engine_instances") && word(next(), "SET")) {
        return None;
    }
    let mut assignments = Vec::new();
    loop {
        let Some(Token::Word(column)) = next() else {
            return None;
        };
        if next() != Some(&Token::Equals) {
            return None;
        }
        let value = literal(next()?)?;
        assignments.push((column.clone(), value));
        match next() {
            Some(Token::Comma) => continue,
            token if word(token, "WHERE") => break,
            _ => return None,
        }
    }
    let mut filters = BTreeMap::new();
    loop {
        let Some(Token::Word(column)) = next() else {
            return None;
        };
        if next() != Some(&Token::Equals) {
            return None;
        }
        let Some(Token::Text(value)) = next() else {
            return None;
        };
        filters.insert(column.to_ascii_lowercase(), value.clone());
        match next() {
            None => break,
            token if word(token, "AND") => continue,
            _ => return None,
        }
    }
    let (type_name, key) = (filters.remove("schema")?, filters.remove("id")?);
    if !filters.is_empty() {
        return None;
    }
    let spec = live(&type_name)?;
    let mut owned: BTreeMap<String, Map<String, Value>> = BTreeMap::new();
    for (column, value) in assignments {
        let (use_id, field) = spec.uses.iter().find_map(|entry| {
            let prefix = format!("bhv_{}__", storage_key(&entry.behavior));
            let name = column.strip_prefix(&prefix)?;
            let declaration = core.declaration(&entry.behavior)?;
            let field = declaration
                .fields
                .iter()
                .find(|field| snake(field) == name)?;
            Some((entry.id().to_owned(), field.clone()))
        })?;
        owned.entry(use_id).or_default().insert(field, value);
    }
    Some(RawWrite {
        type_name,
        key,
        owned,
    })
}

// A token of the one statement form raw_owned_write reads.
#[derive(Clone, Debug, PartialEq)]
enum Token {
    Word(String),
    Text(String),
    Number(String),
    Equals,
    Comma,
}

// tokens splits a statement into words, quoted strings ('' a quote within
// one), numbers, `=` and `,`; `None` for anything else.
fn tokens(sql: &str) -> Option<Vec<Token>> {
    let mut found = Vec::new();
    let mut chars = sql.trim().trim_end_matches(';').chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            c if c.is_whitespace() => {}
            '=' => found.push(Token::Equals),
            ',' => found.push(Token::Comma),
            '\'' => {
                let mut text = String::new();
                loop {
                    match chars.next()? {
                        '\'' if chars.peek() == Some(&'\'') => {
                            chars.next();
                            text.push('\'');
                        }
                        '\'' => break,
                        c => text.push(c),
                    }
                }
                found.push(Token::Text(text));
            }
            c if c.is_ascii_digit() || c == '-' => {
                let mut number = c.to_string();
                while let Some(&d) = chars.peek() {
                    if !(d.is_ascii_digit() || d == '.') {
                        break;
                    }
                    number.push(d);
                    chars.next();
                }
                found.push(Token::Number(number));
            }
            c if c.is_ascii_alphabetic() || c == '_' => {
                let mut word = c.to_string();
                while let Some(&d) = chars.peek() {
                    if !(d.is_ascii_alphanumeric() || d == '_') {
                        break;
                    }
                    word.push(d);
                    chars.next();
                }
                found.push(Token::Word(word));
            }
            _ => return None,
        }
    }
    Some(found)
}

// literal reads a value of the statement: NULL, a number or a string.
fn literal(token: &Token) -> Option<Value> {
    match token {
        Token::Word(word) if word.eq_ignore_ascii_case("NULL") => Some(Value::Null),
        Token::Text(text) => Some(Value::from(text.as_str())),
        Token::Number(number) => serde_json::from_str(number).ok(),
        _ => None,
    }
}

// storage_key is the key the engine first gives a behavior's storage: its
// name lowercased, each run of characters other than letters and digits
// one `_`.
fn storage_key(behavior: &str) -> String {
    let mut key = String::new();
    let mut run = false;
    for c in behavior.chars() {
        if c.is_ascii_alphanumeric() {
            key.push(c.to_ascii_lowercase());
            run = false;
        } else if !run {
            key.push('_');
            run = true;
        }
    }
    key
}

// snake is a field's name in snake case, as the engine's behaviors named
// their columns: `enteredAt` is `entered_at`.
fn snake(field: &str) -> String {
    let mut out = String::new();
    for c in field.chars() {
        if c.is_ascii_uppercase() {
            out.push('_');
            out.push(c.to_ascii_lowercase());
        } else {
            out.push(c);
        }
    }
    out
}

/// A row as the engine's record, with what this replayer compares: its id,
/// its own fields, and the fields each behavior owns under `behaviors`, by
/// use id, for each use whose behavior declares fields, a field with no
/// value left out, with the fields the behavior computes on a read at the
/// read's time ([`Core::read_fields_at`], from the row and its sub-table
/// rows, by use id and table, and the time), and its links as the engine's
/// `Links` field, their keys left out of its own fields.
pub fn record(
    core: &Core,
    spec: &TypeSpec,
    key: &str,
    row: &StoredRow,
    tables: &BTreeMap<String, BTreeMap<String, Vec<SubRow>>>,
    now: i64,
) -> Value {
    let mut behaviors = Map::new();
    for entry in &spec.uses {
        let declares = core
            .declaration(&entry.behavior)
            .is_some_and(|declaration| !declaration.fields.is_empty());
        if !declares {
            continue;
        }
        let mut fields: Map<String, Value> = row
            .owned
            .get(entry.id())
            .map(|fields| {
                fields
                    .iter()
                    .filter(|(_, value)| !value.is_null())
                    .map(|(field, value)| (field.clone(), value.clone()))
                    .collect()
            })
            .unwrap_or_default();
        let empty = BTreeMap::new();
        let computed = core
            .read_fields_at(
                spec,
                entry.id(),
                &row.as_row(),
                tables.get(entry.id()).unwrap_or(&empty),
                now,
            )
            .unwrap_or_default();
        fields.extend(computed);
        behaviors.insert(entry.id().to_owned(), Value::Object(fields));
    }
    if let Some(links) = links_of(spec, &row.own) {
        behaviors.insert(LINKS.to_owned(), links);
    }
    json!({
        "namespace": NAMESPACE,
        "schema": spec.name,
        "id": key,
        "data": without_links(spec, &row.own),
        "behaviors": behaviors,
    })
}

/// A change log entry as the engine's event, with what this replayer
/// compares: its kind, and its change: a create's `data` and `behaviors`,
/// an operation's `behavior`, `operation`, `params` and `patch`, an
/// update's `data` and `behaviors`, a deadline's `patch`, a patch's `data`
/// (the own fields it wrote) and `behaviors` (the owned ones it changed),
/// each left out when the write set none.
pub fn event(spec: Option<&TypeSpec>, logged: &Logged) -> Value {
    let mut owned = owned_object(logged);
    let written = logged.own();
    let own = match spec {
        Some(spec) => without_links(spec, &written),
        None => written.clone(),
    };
    let mut patch = Map::new();
    if !own.is_empty() {
        patch.insert("data".into(), Value::Object(own.clone()));
    }
    if owned.as_object().is_some_and(|owned| !owned.is_empty()) {
        patch.insert("behaviors".into(), owned.clone());
    }
    if let (Action::Create { .. }, Some(links)) = (
        &logged.command.action,
        spec.and_then(|spec| links_of(spec, &written)),
    ) {
        if let Some(owned) = owned.as_object_mut() {
            owned.insert(LINKS.to_owned(), links);
        }
    }
    let change = match &logged.command.action {
        Action::Create { .. } => json!({ "data": own, "behaviors": owned }),
        Action::Operation {
            use_id,
            operation,
            params,
            ..
        } => {
            let behavior = spec
                .and_then(|spec| spec.use_of(use_id))
                .map_or(use_id.as_str(), |(_, entry)| entry.behavior.as_str());
            json!({
                "behavior": behavior,
                "operation": operation,
                "params": params,
                "patch": patch,
            })
        }
        Action::Update { .. } => Value::Object(patch),
        Action::Deadline { .. } => json!({ "patch": patch }),
        _ => json!({}),
    };
    json!({
        "kind": logged.kind(),
        "schema": logged.type_name,
        "instanceId": logged.key,
        "actor": logged.command.caller.subject,
        "at": logged.command.now,
        "change": change,
    })
}

// owned_object is the owned fields a write changed, by use id, each use
// that changed none left out: the engine's event recorded what changed.
fn owned_object(logged: &Logged) -> Value {
    Value::Object(
        logged
            .changed()
            .into_iter()
            .map(|(use_id, fields)| (use_id, Value::Object(fields)))
            .collect(),
    )
}

/// A refusal as the engine's error for the same case (the corpus's
/// `error`): the core's error JSON, with the engine's message and details
/// where they name what the core does not know: a define issue's path into
/// the schema document, a create parameter's path into the create's
/// request, a refused instance's namespace and version, an incompatible
/// change's live version and its advice, a missing row's namespace.
pub fn engine_error(refusal: &Refusal, type_name: &str, live_version: usize) -> Value {
    let mut error = refusal.error_json();
    let details = refusal.details.as_deref();
    let issues_of = || {
        details
            .and_then(|details| details.get("issues"))
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default()
    };
    match refusal.class.as_str() {
        "SchemaDocumentError" => {
            let issues = document_issues(type_name, &issues_of());
            error["message"] = Value::from(format!("schema: {}", listed(&issues)));
            error["details"] = json!({ "issues": issues });
        }
        "CreateParamsError" => {
            // The core's paths point into the create's parameters by use
            // id; the engine's into its request, under `behaviors`.
            let issues: Vec<Value> = issues_of()
                .iter()
                .map(|issue| {
                    json!({
                        "path": format!("/behaviors{}", text(issue, "path")),
                        "message": text(issue, "message"),
                    })
                })
                .collect();
            error["message"] = Value::from(format!("create of {type_name}: {}", listed(&issues)));
            error["details"] = json!({ "issues": issues });
        }
        "IncompatibleChangeError" => {
            let listed: Vec<&str> = details
                .and_then(|details| details.get("changes"))
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(|change| change.get("message").and_then(Value::as_str))
                .collect();
            error["message"] = Value::from(format!(
                "schema {type_name} in namespace {NAMESPACE}: a new version cannot replace version {live_version}: {}. {NEW_VERSION_ADVICE}",
                listed.join("; ")
            ));
        }
        "InstanceValidationError" => {
            let prefix = format!("{type_name}: ");
            if let Some(rest) = refusal.message.strip_prefix(&prefix) {
                error["message"] = Value::from(format!(
                    "{type_name} in namespace {NAMESPACE} (version {live_version}): {rest}"
                ));
            }
        }
        "EngineError"
            if refusal.code == Code::NotFound && refusal.message.ends_with(" does not exist") =>
        {
            error["message"] = Value::from(format!("{} in namespace {NAMESPACE}", refusal.message));
        }
        _ => {}
    }
    error
}

// document_issues writes the core's define issues as the engine's schema
// document validation wrote them: each at its path into the document
// (`/types/<type>/behaviors/<index>` and the issue's path into the entry).
// A use whose config's shape is refused also fails its entry's `then`
// branch, which the validator reports at the entry, and the validator's
// issues come by path, the entry's before its config's, as the engine
// sorted them; the core's issues of one path keep their order.
fn document_issues(type_name: &str, issues: &[Value]) -> Vec<Value> {
    let mut written: Vec<Value> = Vec::new();
    let mut branched: Vec<u64> = Vec::new();
    let shaped = issues
        .iter()
        .any(|issue| text(issue, "kind") == kind_name(ConfigIssueKind::Schema));
    for issue in issues {
        let index = issue.get("index").and_then(Value::as_u64).unwrap_or(0);
        let entry = format!("/types/{type_name}/behaviors/{index}");
        if text(issue, "kind") == kind_name(ConfigIssueKind::Schema) && !branched.contains(&index) {
            branched.push(index);
            written.push(json!({ "path": entry, "message": "must match \"then\" schema" }));
        }
        written.push(json!({
            "path": format!("{entry}{}", text(issue, "path")),
            "message": text(issue, "message"),
        }));
    }
    if shaped {
        written.sort_by_key(|issue| text(issue, "path"));
    }
    written
}

// kind_name is a define issue's kind as its JSON writes it.
fn kind_name(kind: ConfigIssueKind) -> String {
    serde_json::to_value(kind)
        .ok()
        .and_then(|value| value.as_str().map(str::to_owned))
        .unwrap_or_default()
}

// listed writes issues as the engine's messages list them: `<path>:
// <message>`, a path left out where it is empty, joined with `; `.
fn listed(issues: &[Value]) -> String {
    issues
        .iter()
        .map(|issue| {
            let path = text(issue, "path");
            if path.is_empty() {
                text(issue, "message")
            } else {
                format!("{path}: {}", text(issue, "message"))
            }
        })
        .collect::<Vec<_>>()
        .join("; ")
}

fn text(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

#[cfg(test)]
mod tests {
    use super::*;
    use superschematic_rules::{ConfigIssue, Declaration, SchemaChange, SchemaIssue};

    struct Status;

    impl superschematic_rules::Behavior for Status {
        type Config = ();

        fn declaration(&self) -> Declaration {
            Declaration::new("Status.Flow")
                .field("status")
                .field("enteredAt")
        }

        fn parse_config(
            &self,
            _raw: &Value,
            _target: &superschematic_rules::ConfigTarget<'_>,
        ) -> Result<(), superschematic_rules::ConfigError> {
            Ok(())
        }
    }

    #[test]
    fn a_raw_write_of_behaviors_columns_reads_as_owned_fields() {
        let core = Core::new().with(Status);
        let order = TypeSpec::new("Order").with(UseSpec::on_type("Status.Flow", None));
        let live = |name: &str| (name == "Order").then(|| order.clone());
        assert_eq!(
            raw_owned_write(
                "UPDATE engine_instances SET bhv_status_flow__entered_at = NULL, bhv_status_flow__status = 'it''s' WHERE schema = 'Order' AND id = 'o1'",
                &core,
                &live,
            ),
            Some(RawWrite {
                type_name: "Order".into(),
                key: "o1".into(),
                owned: [(
                    "Status.Flow".to_owned(),
                    json!({"enteredAt": null, "status": "it's"}).as_object().cloned().unwrap()
                )]
                .into_iter()
                .collect(),
            })
        );
        for other in [
            "UPDATE engine_instances SET data = '{}'",
            "UPDATE engine_instances SET bhv_status_flow__colour = 1 WHERE schema = 'Order' AND id = 'o1'",
            "UPDATE engine_instances SET bhv_status_flow__status = 'x' WHERE schema = 'Note' AND id = 'o1'",
            "UPDATE engine_instances SET bhv_status_flow__status = 'x' WHERE id = 'o1'",
            "DELETE FROM engine_instances",
        ] {
            assert_eq!(raw_owned_write(other, &core, &live), None, "{other}");
        }
        assert_eq!(snake("enteredAt"), "entered_at");
        assert_eq!(storage_key("test.Dispatch"), "test_dispatch");
    }

    #[test]
    fn a_document_reads_as_a_type_with_its_uses_as_given() {
        let defined = read_document(&json!({
            "kind": "General",
            "name": "Order",
            "types": {"Order": {
                "name": "Order",
                "fields": [{"name": "title", "required": true}, {"name": "note"}],
                "behaviors": [{"name": "Workflow", "config": {"states": ["a"], "initial": "a"}}, {"name": "test.Dispatch"}]
            }}
        }))
        .unwrap();
        assert_eq!(defined.spec.name, "Order");
        assert_eq!(defined.type_name, "Order");
        assert_eq!(defined.spec.uses.len(), 2);
        assert_eq!(
            serde_json::to_value(&defined.spec.fields).unwrap(),
            json!([{"name": "title", "type": ""}, {"name": "note", "type": ""}])
        );
        assert_eq!(
            serde_json::to_string(&defined.spec.uses[0].config).unwrap(),
            r#"{"states":["a"],"initial":"a"}"#
        );
        assert_eq!(defined.spec.uses[1].config, None);
        assert_eq!(
            own_field_problems(
                "Order",
                &defined.fields,
                &json!({"status": "x"}).as_object().cloned().unwrap(),
                true
            ),
            vec![
                "Order has no field status".to_owned(),
                "Order requires field title".to_owned()
            ]
        );
        assert!(own_field_problems(
            "Order",
            &defined.fields,
            &json!({"status": null}).as_object().cloned().unwrap(),
            false
        )
        .is_empty());
    }

    #[test]
    fn a_behavior_entry_that_names_a_field_is_anchored_on_it() {
        let defined = read_document(&json!({
            "name": "Sandbox",
            "types": {"Sandbox": {
                "name": "Sandbox",
                "fields": [{"name": "name", "typeRef": {"name": "string"}, "required": true}],
                "behaviors": [
                    {"name": "Lease", "config": {}},
                    {"name": "Workflow", "field": "status", "config": {"states": ["a"], "transitions": []}},
                    {"name": "Reconcile", "field": "observed", "config": {"status": "status"}}
                ]
            }}
        }))
        .unwrap();
        let ids: Vec<(&str, Option<&str>)> = defined
            .spec
            .uses
            .iter()
            .map(|entry| (entry.id(), entry.field()))
            .collect();
        assert_eq!(
            ids,
            [
                ("Lease", None),
                ("status", Some("status")),
                ("observed", Some("observed"))
            ]
        );
        assert_eq!(
            defined.spec.uses[2],
            UseSpec::on_field("observed", "Reconcile", json!({"status": "status"}))
        );
        // The anchored fields are the uses', not own fields of the type.
        assert_eq!(defined.fields.len(), 1);
    }

    #[test]
    fn a_document_that_names_its_type_apart_from_its_schema_says_so() {
        let defined = read_document(&json!({
            "name": "invoices",
            "types": {"Invoice": {"name": "Invoice", "fields": [{"name": "title"}]}}
        }))
        .unwrap();
        assert_eq!(defined.spec.name, "invoices");
        assert_eq!(defined.type_name, "Invoice");
    }

    #[test]
    fn a_date_time_field_holds_instants_in_their_canonical_form() {
        let defined = read_document(&json!({
            "name": "Ticket",
            "types": {"Ticket": {"fields": [
                {"name": "title", "typeRef": {"name": "string"}},
                {"name": "visitAt", "typeRef": {"name": "Temporal.DateTime"}},
                {"name": "visitDay", "typeRef": {"name": "Temporal.Date"}},
                {"name": "visits", "typeRef": {"name": "Temporal.DateTime", "isArray": true}}
            ]}}
        }))
        .unwrap();
        assert_eq!(
            serde_json::to_value(&defined.spec.fields).unwrap(),
            json!([
                {"name": "title", "type": "string"},
                {"name": "visitAt", "type": "Temporal.DateTime", "dateTime": true},
                {"name": "visitDay", "type": "Temporal.Date"},
                {"name": "visits", "type": "Temporal.DateTime"}
            ])
        );
        let data = json!({
            "title": "2026-10-12T09:00:00.000Z",
            "visitAt": "2026-10-12T09:00:00.000Z"
        });
        assert_eq!(
            Value::Object(normalize_own(&defined.fields, data.as_object().unwrap())),
            json!({"title": "2026-10-12T09:00:00.000Z", "visitAt": "2026-10-12T09:00:00Z"})
        );
        for (text, canonical) in [
            ("2026-10-12T09:00:00.100Z", "2026-10-12T09:00:00.1Z"),
            ("2026-10-12T11:00:00+02:00", "2026-10-12T11:00:00+02:00"),
            ("2026-10-12T11:00:00.000+02:00", "2026-10-12T11:00:00+02:00"),
            ("not a time.000", "not a time.000"),
        ] {
            assert_eq!(canonical_instant(text), canonical, "{text}");
        }
    }

    #[test]
    fn a_refusal_reads_as_the_engines_error() {
        let issue = |kind: ConfigIssueKind, path: &str, message: &str| ConfigIssue {
            use_id: "Workflow".into(),
            index: 0,
            behavior: "Workflow".into(),
            kind,
            path: path.into(),
            message: message.into(),
        };
        let refusal = Refusal::invalid_config(
            "Order",
            vec![issue(
                ConfigIssueKind::Rule,
                "/config",
                "type Order: behavior Workflow config: no",
            )],
        );
        assert_eq!(
            engine_error(&refusal, "Order", 0),
            json!({
                "class": "SchemaDocumentError",
                "code": "invalid_schema",
                "message": "schema: /types/Order/behaviors/0/config: type Order: behavior Workflow config: no",
                "details": {"issues": [{"path": "/types/Order/behaviors/0/config", "message": "type Order: behavior Workflow config: no"}]}
            })
        );
        // A shape the validator refuses fails the entry's branch too; the
        // entry's issues come before its config's.
        let refusal = Refusal::invalid_config(
            "Order",
            vec![
                issue(
                    ConfigIssueKind::Schema,
                    "/config",
                    "unknown key \"terminal\"",
                ),
                issue(
                    ConfigIssueKind::Schema,
                    "",
                    "must have required property 'config'",
                ),
            ],
        );
        assert_eq!(
            engine_error(&refusal, "Order", 0)["details"],
            json!({"issues": [
                {"path": "/types/Order/behaviors/0", "message": "must match \"then\" schema"},
                {"path": "/types/Order/behaviors/0", "message": "must have required property 'config'"},
                {"path": "/types/Order/behaviors/0/config", "message": "unknown key \"terminal\""}
            ]})
        );
        let refusal = Refusal::create_params(
            "Order",
            vec![SchemaIssue::new(
                "/Workflow",
                "behavior Workflow takes no create parameters",
            )],
        );
        assert_eq!(
            engine_error(&refusal, "Order", 1),
            json!({"class": "CreateParamsError", "code": "invalid_argument", "message": "create of Order: /behaviors/Workflow: behavior Workflow takes no create parameters", "details": {"issues": [{"path": "/behaviors/Workflow", "message": "behavior Workflow takes no create parameters"}]}})
        );
        let refusal = Refusal::incompatible_change(
            "Order",
            vec![SchemaChange::new("Order.behaviors.Workflow", "it may not")],
        );
        assert_eq!(
            engine_error(&refusal, "Order", 2)["message"],
            json!(format!("schema Order in namespace default: a new version cannot replace version 2: it may not. {NEW_VERSION_ADVICE}"))
        );
    }
}
