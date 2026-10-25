//! The behaviors an engine can run, the port of `src/behaviors/registry.ts`:
//! implementations registered when it opens (`EngineOptions::behaviors`)
//! or later (`engine.behaviors().register`). Registering checks the
//! declaration (`declaration`), compiles its config, create parameter,
//! parameter, result and precondition schemas (`superschematic-schema-ir`'s
//! `ajv`), and refuses an implementation whose operations, schema-level
//! operations or fields are not exactly the ones its declaration names,
//! whose migrations are malformed, whose filters name a field it does not
//! declare or a column its migrations do not add, or whose schedules are
//! misnamed or too frequent. A name registers once.
//!
//! A behavior whose storage already exists (a schema that composes it was
//! published before) has its storage brought up to its latest migration
//! when it registers; a database whose ledger is ahead of the
//! implementation's migrations is refused. Otherwise its storage is created
//! when a schema that composes it is published (`ensure_storage`).

use std::sync::Arc;

use serde_json::Value;

use superschematic_engine_storage::behavior::{
    assign_key, column_problems, is_local_name, migration_set, prefix_of, stored_key,
    BehaviorMigration,
};
use superschematic_engine_storage::{migrate_in, Db, Dialect, Tx};
use superschematic_schema_ir::ajv::{AjvError, AjvSchema};

use super::declaration::{BehaviorDeclaration, OperationDeclaration, OperationScope};
use super::erased::{Erased, HookSet};
use crate::errors::{Error, Result, SchemaIssue};
use crate::json;

/// The deployment's invocation policy as registration holds a declaration
/// to it: the key a tool's policy is written under and its values, from
/// the engine's tool options (`EngineOptions.tools`).
#[derive(Clone, Debug)]
pub(crate) struct InvocationPolicy {
    pub key: String,
    pub values: Vec<String>,
}

impl Default for InvocationPolicy {
    fn default() -> Self {
        InvocationPolicy {
            key: "invocationPolicy".to_owned(),
            values: vec!["auto".to_owned(), "ask".to_owned()],
        }
    }
}

/// One declared operation with its compiled schemas.
pub(crate) struct OperationSpec {
    pub name: String,
    #[allow(dead_code)] // milestone 6's tools read it
    pub declaration: OperationDeclaration,
    pub writes: bool,
    pub scope: OperationScope,
    /// Whether it runs beside the instance (D77).
    pub beside: bool,
    /// Whether it appends an event: false only for a beside operation that
    /// declares `"log": false`.
    pub log: bool,
    pub params: AjvSchema,
    pub result: AjvSchema,
}

/// A field a behavior lets a list filter on, checked.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct RegisteredFilter {
    /// The declared field, or `<field>.<member>`.
    pub field: String,
    pub column: String,
    pub kind: String,
    pub description: Option<String>,
    /// The behavior's own name of an index on the column alone.
    pub index: Option<String>,
}

/// A registered implementation, checked and compiled.
pub(crate) struct RegisteredBehavior {
    pub name: String,
    pub declaration: BehaviorDeclaration,
    pub implementation: Arc<dyn Erased>,
    pub hooks: HookSet,
    /// The compiled configSchema; `None` when the behavior takes no config.
    pub config: Option<AjvSchema>,
    /// The compiled createParamsSchema; `None` when a create gives it none.
    pub create_params: Option<AjvSchema>,
    /// The compiled preconditionSchema; `None` when it takes none.
    pub precondition: Option<AjvSchema>,
    pub operations: Vec<OperationSpec>,
    /// Its declared fields, in declaration order.
    pub fields: Vec<String>,
    /// Every column its migrations add, by its own name.
    pub columns: Vec<String>,
    pub migrations: Vec<BehaviorMigration>,
    pub filters: Vec<RegisteredFilter>,
    /// The codes its declaration lists for its vetoes.
    pub veto_codes: Vec<String>,
}

impl RegisteredBehavior {
    /// The operation of a name.
    pub fn operation(&self, name: &str) -> Option<&OperationSpec> {
        self.operations
            .iter()
            .find(|operation| operation.name == name)
    }
}

/// The behaviors an engine runs, by name.
#[derive(Clone, Default)]
pub(crate) struct BehaviorRegistry {
    behaviors: Vec<Arc<RegisteredBehavior>>,
}

impl BehaviorRegistry {
    /// The implementation registered under a name.
    pub fn lookup(&self, name: &str) -> Option<&Arc<RegisteredBehavior>> {
        self.behaviors.iter().find(|behavior| behavior.name == name)
    }

    /// The registered names, sorted as JavaScript sorts strings.
    pub fn names(&self) -> Vec<String> {
        let mut names: Vec<String> = self
            .behaviors
            .iter()
            .map(|behavior| behavior.name.clone())
            .collect();
        names.sort_by(|a, b| json::compare_utf16(a, b));
        names
    }

    /// The registry with one more implementation, checked: refused as the
    /// TypeScript engine refuses it (a TypeError), or a name taken. A
    /// behavior whose storage exists is migrated first.
    pub fn with(
        &self,
        implementation: Arc<dyn Erased>,
        invocation: &InvocationPolicy,
        db: &Db,
        now: i64,
    ) -> Result<BehaviorRegistry> {
        let behavior = compile(implementation, invocation, db.dialect())?;
        if self.lookup(&behavior.name).is_some() {
            return Err(Error::usage(format!(
                "behavior {} is already registered with this engine",
                behavior.name
            )));
        }
        let key = db.read(|tx| stored_key(tx, &behavior.name))?;
        if let Some(key) = key {
            db.migrate(
                &migration_set(&behavior.name, &prefix_of(&key), &behavior.migrations),
                now,
            )?;
        }
        let mut next = self.clone();
        next.behaviors.push(Arc::new(behavior));
        Ok(next)
    }
}

/// Creates a behavior's storage, or brings it up to date, in the publish's
/// transaction, and returns its prefix, so a failed publish leaves no
/// storage behind.
pub(crate) fn ensure_storage(
    tx: &Tx<'_>,
    behavior: &RegisteredBehavior,
    now: i64,
) -> Result<String> {
    let prefix = prefix_of(&assign_key(tx, &behavior.name, now)?);
    migrate_in(
        tx,
        &migration_set(&behavior.name, &prefix, &behavior.migrations),
        now,
    )?;
    Ok(prefix)
}

/// The ajv errors of a validation as issues at JSON pointers, in a defined
/// order rather than ajv's: by instance path, then keyword, then message,
/// each compared by code point (the byte order of their UTF-8), as the
/// TypeScript engine's `BehaviorRegistry.issues` sorts them. The messages
/// are ajv's, with the property an additionalProperties names and the
/// values an enum allows.
pub(crate) fn issues(errors: Vec<AjvError>) -> Vec<SchemaIssue> {
    let mut described: Vec<(String, String, String)> = errors
        .into_iter()
        .map(|error| {
            let message = match (&error.additional_property, &error.allowed_values) {
                (Some(property), _) => format!("{}: {property}", error.message),
                (_, Some(values)) => format!(
                    "{}: {}",
                    error.message,
                    values
                        .iter()
                        .map(json::stringify)
                        .collect::<Vec<_>>()
                        .join(", ")
                ),
                _ => error.message,
            };
            (error.instance_path, error.keyword, message)
        })
        .collect();
    described.sort();
    described
        .into_iter()
        .map(|(path, _, message)| SchemaIssue::new(path, message))
        .collect()
}

/// The issues of a value a compiled schema refuses, joined as a refusal's
/// detail writes them: `<path> <message>`, a path left out where it is
/// empty, joined with `; `.
pub(crate) fn detail(schema: &AjvSchema, value: &Value) -> String {
    issues(schema.errors(value))
        .iter()
        .map(|issue| {
            if issue.path.is_empty() {
                issue.message.clone()
            } else {
                format!("{} {}", issue.path, issue.message)
            }
        })
        .collect::<Vec<_>>()
        .join("; ")
}

fn compile(
    implementation: Arc<dyn Erased>,
    invocation: &InvocationPolicy,
    dialect: Dialect,
) -> Result<RegisteredBehavior> {
    let declaration = match BehaviorDeclaration::parse(implementation.declaration()) {
        Ok(declaration) => declaration,
        Err(problems) => {
            let label = serde_json::from_str::<Value>(implementation.declaration())
                .ok()
                .and_then(|value| value.get("name").and_then(Value::as_str).map(str::to_owned))
                .unwrap_or_else(|| "implementation".to_owned());
            return Err(Error::usage(format!(
                "behavior {label} cannot register: its declaration: {}",
                problems.join("; ")
            )));
        }
    };
    let mut problems: Vec<String> = Vec::new();
    let schema =
        |value: &Value, what: &str, problems: &mut Vec<String>| match AjvSchema::compile(value) {
            Ok(compiled) => Some(compiled),
            Err(message) => {
                problems.push(format!("{what} does not compile: {message}"));
                None
            }
        };
    let config = declaration
        .config_schema
        .as_ref()
        .and_then(|value| schema(value, "configSchema", &mut problems));
    let create_params = declaration
        .create_params_schema
        .as_ref()
        .and_then(|value| schema(value, "createParamsSchema", &mut problems));
    let precondition = declaration
        .precondition_schema
        .as_ref()
        .and_then(|value| schema(value, "preconditionSchema", &mut problems));
    let hooks = implementation.hook_set();
    for operation in &declaration.operations {
        if let Some(policy) = &operation.invocation_policy {
            if !invocation.values.contains(policy) {
                problems.push(format!(
                    "operation {} invocationPolicy {} is not one of the {} values ({})",
                    operation.name,
                    json::quote(policy),
                    invocation.key,
                    invocation.values.join(", ")
                ));
            }
        }
    }
    let mut operations = Vec::new();
    for operation in &declaration.operations {
        let params = schema(
            &operation.params_schema,
            &format!("operation {} paramsSchema", operation.name),
            &mut problems,
        );
        let result = schema(
            &operation.result_schema,
            &format!("operation {} resultSchema", operation.name),
            &mut problems,
        );
        if let (Some(params), Some(result)) = (params, result) {
            operations.push(OperationSpec {
                name: operation.name.clone(),
                declaration: operation.clone(),
                writes: operation.writes,
                scope: operation.scope,
                beside: operation.beside,
                log: operation.log,
                params,
                result,
            });
        }
    }
    let scoped = |scope: OperationScope| -> Vec<String> {
        declaration
            .operations
            .iter()
            .filter(|operation| operation.scope == scope)
            .map(|operation| operation.name.clone())
            .collect()
    };
    match_names(
        "operations",
        &hooks.operations,
        &scoped(OperationScope::Instance),
        &mut problems,
    );
    match_names(
        "schemaOperations",
        &hooks.schema_operations,
        &scoped(OperationScope::Schema),
        &mut problems,
    );
    let fields: Vec<String> = declaration
        .fields
        .iter()
        .map(|field| field.name.clone())
        .collect();
    match_names("fields", &hooks.fields, &fields, &mut problems);
    problems.extend(hooks.problems.iter().cloned());
    if hooks.create_params_schema && declaration.create_params_schema.is_none() {
        problems.push("createParamsSchema narrows the create parameters its declaration takes, and its declaration takes none (no createParamsSchema)".to_owned());
    }
    let columns = check_migrations(&hooks.migrations, dialect, &mut problems);
    let filters = check_filters(&hooks, &fields, &columns, &mut problems);
    check_schedules(&hooks, &mut problems);
    if !problems.is_empty() {
        return Err(Error::usage(format!(
            "behavior {} cannot register: {}",
            declaration.name,
            problems.join("; ")
        )));
    }
    Ok(RegisteredBehavior {
        name: declaration.name.clone(),
        veto_codes: declaration
            .vetoes
            .iter()
            .map(|veto| veto.code.clone())
            .collect(),
        migrations: hooks.migrations.clone(),
        implementation,
        hooks,
        config,
        create_params,
        precondition,
        operations,
        fields,
        columns,
        filters,
        declaration,
    })
}

// match_names holds an implementation's operations or fields to exactly
// the names its declaration gives.
fn match_names(key: &str, implemented: &[String], declared: &[String], problems: &mut Vec<String>) {
    let missing: Vec<&str> = declared
        .iter()
        .filter(|name| !implemented.contains(name))
        .map(String::as_str)
        .collect();
    let mut extra: Vec<&str> = Vec::new();
    for name in implemented {
        if !declared.contains(name) && !extra.contains(&name.as_str()) {
            extra.push(name);
        }
    }
    if !missing.is_empty() {
        problems.push(format!(
            "its declaration names {key} it does not implement: {}",
            missing.join(", ")
        ));
    }
    if !extra.is_empty() {
        problems.push(format!(
            "it implements {key} its declaration does not name: {}",
            extra.join(", ")
        ));
    }
}

const FILTER_TYPES: [&str; 4] = ["string", "number", "integer", "boolean"];

// member_filter splits a filter on a member of a field, `<field>.<member>`
// with the member camelCase, into the field; None for any other name.
fn member_filter(name: &str) -> Option<&str> {
    let (field, member) = name.split_once('.')?;
    let bytes = member.as_bytes();
    (!field.is_empty()
        && !member.contains('.')
        && !bytes.is_empty()
        && bytes.len() <= 64
        && bytes[0].is_ascii_lowercase()
        && bytes.iter().all(u8::is_ascii_alphanumeric))
    .then_some(field)
}

// check_filters holds filters to declared fields, or to a member of one,
// each over a column the migrations add, with a JSON type a where compares
// and a description that is text; it returns them with the index a
// migration lists on the column alone.
fn check_filters(
    hooks: &HookSet,
    fields: &[String],
    columns: &[String],
    problems: &mut Vec<String>,
) -> Vec<RegisteredFilter> {
    let mut checked = Vec::new();
    for (name, filter) in &hooks.filters {
        let at = format!("filter {name}");
        let field = member_filter(name).unwrap_or(name);
        if !fields.iter().any(|declared| declared == field) {
            problems.push(format!(
                "{at} names a field its declaration does not ({}), or a member of one as <field>.<member>, the member camelCase",
                if fields.is_empty() { "none".to_owned() } else { fields.join(", ") }
            ));
            continue;
        }
        if !columns.contains(&filter.column) {
            problems.push(format!(
                "{at}: column is one of its migrations' columns ({})",
                if columns.is_empty() {
                    "none".to_owned()
                } else {
                    columns.join(", ")
                }
            ));
            continue;
        }
        if !FILTER_TYPES.contains(&filter.kind.as_str()) {
            problems.push(format!("{at}: type is one of {}", FILTER_TYPES.join(", ")));
            continue;
        }
        if filter
            .description
            .as_ref()
            .is_some_and(|description| description.trim().is_empty())
        {
            problems.push(format!("{at}: description is text when it is given"));
            continue;
        }
        let index = hooks
            .migrations
            .iter()
            .flat_map(|migration| migration.indexes.iter())
            .find(|(_, list)| list.len() == 1 && list[0] == filter.column)
            .map(|(index, _)| index.clone());
        checked.push(RegisteredFilter {
            field: name.clone(),
            column: filter.column.clone(),
            kind: filter.kind.clone(),
            description: filter.description.clone(),
            index,
        });
    }
    checked
}

// check_schedules holds schedules to camelCase names, each with an interval
// of at least MIN_SCHEDULE_MS or one the config gives.
fn check_schedules(hooks: &HookSet, problems: &mut Vec<String>) {
    for (name, every) in &hooks.schedules {
        let at = format!("schedule {name}");
        let bytes = name.as_bytes();
        if bytes.is_empty()
            || !bytes[0].is_ascii_lowercase()
            || !bytes.iter().all(u8::is_ascii_alphanumeric)
        {
            problems.push(format!(
                "{at}: a schedule name is camelCase (^[a-z][A-Za-z0-9]*$)"
            ));
        }
        if let Some(ms) = every {
            if *ms < super::behavior::MIN_SCHEDULE_MS || *ms > 9_007_199_254_740_991 {
                problems.push(format!(
                    "{at}: everyMs is an integer of at least {}, or a function of the config that returns one, got {ms}",
                    super::behavior::MIN_SCHEDULE_MS
                ));
            }
        }
    }
}

// check_migrations holds migrations to 1, 2, 3, ... with names,
// well-formed columns each added once, and indexes each named once over
// columns that migration or an earlier one adds; it returns the columns
// they add.
fn check_migrations(
    migrations: &[BehaviorMigration],
    dialect: Dialect,
    problems: &mut Vec<String>,
) -> Vec<String> {
    let mut columns: Vec<String> = Vec::new();
    let mut indexes: Vec<String> = Vec::new();
    for (index, migration) in migrations.iter().enumerate() {
        let at = format!("migration {}", index + 1);
        if usize::try_from(migration.version).ok() != Some(index + 1) {
            problems.push(format!(
                "migrations are numbered 1, 2, 3, ... in order; position {} holds version {}",
                index + 1,
                migration.version
            ));
        }
        if migration.name.is_empty() {
            problems.push(format!("{at} needs a name"));
        }
        for (name, spec) in &migration.columns {
            problems.extend(
                column_problems(name, spec, dialect)
                    .into_iter()
                    .map(|problem| format!("{at}: {problem}")),
            );
            if columns.contains(name) {
                problems.push(format!(
                    "{at} adds column {name}, which an earlier migration added"
                ));
            }
            columns.push(name.clone());
        }
        for (name, list) in &migration.indexes {
            let label = format!("{at} index {name}");
            if !is_local_name(name) {
                problems.push(format!(
                    "{label}: an index name matches ^[a-z][a-z0-9_]{{0,62}}$"
                ));
            }
            if indexes.contains(name) {
                problems.push(format!(
                    "{at} adds index {name}, which an earlier migration added"
                ));
            }
            indexes.push(name.clone());
            if list.is_empty() {
                problems.push(format!(
                    "{label} is a non-empty list of the behavior's column names"
                ));
                continue;
            }
            let unknown: Vec<&str> = list
                .iter()
                .filter(|column| !columns.contains(column))
                .map(String::as_str)
                .collect();
            if !unknown.is_empty() {
                problems.push(format!(
                    "{label} names {}, which no migration up to this one adds; an index lists the behavior's own columns by its own names ({})",
                    unknown.join(", "),
                    if columns.is_empty() { "none so far".to_owned() } else { columns.join(", ") }
                ));
            }
            let mut seen: Vec<&String> = Vec::new();
            if list.iter().any(|column| {
                let repeated = seen.contains(&column);
                seen.push(column);
                repeated
            }) {
                problems.push(format!("{label} lists a column twice"));
            }
        }
    }
    columns
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn issues_come_by_path_then_keyword_then_message_in_ajvs_words() {
        let schema = AjvSchema::compile(&json!({
            "type": "object", "additionalProperties": false,
            "properties": {"by": {"type": "integer", "minimum": 1}, "kind": {"enum": ["a", 1]}}
        }))
        .unwrap();
        let listed: Vec<(String, String)> =
            issues(schema.errors(&json!({"zz": 1, "by": 0.5, "kind": "c"})))
                .into_iter()
                .map(|issue| (issue.path, issue.message))
                .collect();
        assert_eq!(
            listed,
            [
                ("".into(), "must NOT have additional properties: zz".into()),
                ("/by".into(), "must be >= 1".into()),
                ("/by".into(), "must be integer".into()),
                (
                    "/kind".into(),
                    "must be equal to one of the allowed values: \"a\", 1".into()
                ),
            ]
        );
        assert_eq!(detail(&schema, &json!({"by": 0})), "/by must be >= 1");
    }

    #[test]
    fn a_member_filter_names_its_field() {
        assert_eq!(member_filter("tag.owner"), Some("tag"));
        assert_eq!(member_filter("tag"), None);
        assert_eq!(member_filter("tag.Owner"), None);
        assert_eq!(member_filter("a.b.c"), None);
    }
}
