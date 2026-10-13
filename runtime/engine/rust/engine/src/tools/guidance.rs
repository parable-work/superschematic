//! Tool guidance, the port of `src/tools/guidance.ts` (D16, D58): what a
//! tool says about when to call it, in the members an operation's @docs
//! guidance has (`ir.ToolOperationGuidance`): useWhen, doNotUseWhen,
//! success and errors. The engine writes the guidance of its own tools and
//! of the operations every schema has, and each behavior the type composes
//! adds what its config says (`Hooks::guidance`).
//!
//! An operation's guidance is the engine's base for it, then what the
//! behavior that adds it says, then what each other behavior of the type
//! says about it, in list order. A member's sentences are joined by a
//! space. errors lists vetoes, each code once per behavior, its description
//! led by the behavior's name. The engine holds what a behavior returns to
//! its declaration: each error a veto code it lists, each key an operation
//! of the type, each text without surrounding whitespace; anything else is
//! a BehaviorError. A behavior's create parameters for its config are held
//! to the declaration's shape. All of it is computed once per published
//! version, which never changes.

use serde::Serialize;
use serde_json::{Map, Value};

use crate::behaviors::behavior::{
    BehaviorGuidance, DescribeTarget, DescribedTypeOperation, OperationGuidance,
};
use crate::behaviors::composition::{BoundBehavior, Composition};
use crate::behaviors::declaration::{
    create_params_schema_problem, OperationScope, BUILTIN_OPERATIONS,
};
use crate::errors::{Error, Result};

/// One refusal a tool's guidance lists.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GuidanceErrorEntry {
    pub code: String,
    pub description: String,
    pub common_correction: String,
}

/// A tool's guidance, as the tools document writes it
/// (`ir.ToolOperationGuidance`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolGuidance {
    pub use_when: String,
    pub do_not_use_when: String,
    pub success: String,
    pub errors: Vec<GuidanceErrorEntry>,
}

/// What a published version's behaviors say about it.
#[derive(Clone, Debug, Default)]
pub(crate) struct VersionGuidance {
    /// Each behavior's summary, by name.
    pub summaries: Vec<(String, String)>,
    /// Each operation's guidance, by name.
    pub operations: Vec<(String, ToolGuidance)>,
    /// The create parameters each behavior takes under its config, for one
    /// whose implementation narrows them.
    pub create_params: Vec<(String, Value)>,
}

impl VersionGuidance {
    pub fn summary(&self, behavior: &str) -> Option<&str> {
        self.summaries
            .iter()
            .find(|(name, _)| name == behavior)
            .map(|(_, summary)| summary.as_str())
    }

    pub fn operation(&self, name: &str) -> Option<&ToolGuidance> {
        self.operations
            .iter()
            .find(|(each, _)| each == name)
            .map(|(_, guidance)| guidance)
    }

    pub fn create_params(&self, behavior: &str) -> Option<&Value> {
        self.create_params
            .iter()
            .find(|(name, _)| name == behavior)
            .map(|(_, schema)| schema)
    }
}

// The engine's own guidance of a tool: useWhen, doNotUseWhen, success.
fn engine_texts(tool: &str) -> (&'static str, &'static str, &'static str) {
    match tool {
        "listSchemas" => (
            "Use to learn which schemas this namespace reaches and which of them have a live version.",
            "Do not use to read a schema's fields or operations; call describe_schema.",
            "Returns each schema's name, its live version and whether it has a draft.",
        ),
        "describeSchema" => (
            "Use before calling a schema's tools: it gives the JSON Schema of an instance, each behavior with its config and summary, and each operation with its parameters, result and guidance.",
            "Do not use to read instances; call the schema's get or list.",
            "Returns the describe document of the live version.",
        ),
        "defineSchema" => (
            "Use to store a schema-file document as the draft of its name, replacing the draft before it.",
            "Do not use to publish: a draft goes live only through the HTTP publish route.",
            "Returns the draft, with its name and hash; the live version is unchanged.",
        ),
        "listBehaviors" => (
            "Use before composing behaviors in a draft, to learn which behaviors this engine runs.",
            "Do not use to read a schema's config of its behaviors; call describe_schema.",
            "Returns each behavior's name, description, requirements, conflicts, fields and operations.",
        ),
        "describeBehavior" => (
            "Use to learn what a behavior takes before composing it: its config, its create parameters, its preconditions and the codes its vetoes carry.",
            "Do not use to read a schema's config of the behavior; call describe_schema.",
            "Returns the behavior's declaration, with its defaults filled in.",
        ),
        "getValue" => (
            "Use to read a large field an event, or an instance read with valueRefs, carries as a ref ({ \"$value\": <hash>, \"bytes\": <n> }), by its hash.",
            "Do not use to read an instance; call its schema's get, which returns every field inline unless valueRefs asks for refs.",
            "Returns the value with its hash and its size in bytes.",
        ),
        "search" => (
            "Use to find instances across every schema this namespace reaches that composes Search, by words, by a vector, or both.",
            "Do not use to page through one schema's instances; call its list.",
            "Returns hits best first, each naming its schema and how it matched, and next for the page after.",
        ),
        "listNamespaces" => (
            "Use to learn which namespaces you may see, and whether each is archived.",
            "Do not use to list a namespace's schemas; call list_schemas.",
            "Returns each namespace's name, origin (configured by the engine's options, or created), whether it is the shared namespace, and its state (active or archived).",
        ),
        "createNamespace" => (
            "Use to make a namespace for a new project: it starts with no schemas of its own and looks schema names up in the shared namespace after itself.",
            "Do not use to make a schema; call define_schema in the namespace. Do not use for a name that is a namespace already, archived or not: the create is refused (conflict).",
            "Returns the namespace, active, with who made it and when.",
        ),
        "archiveNamespace" => (
            "Use when a project made with create_namespace is finished: its schemas, instances and events stay readable, every write is refused (namespace_archived), and its reactions and schedules stop.",
            "Do not use on a namespace the engine's options configure, which is refused (conflict), or to remove data: nothing is removed.",
            "Returns the namespace, archived, with who archived it and when; archiving an archived namespace changes nothing.",
        ),
        "unarchiveNamespace" => (
            "Use to write again to an archived namespace.",
            "Do not use on an active namespace, which stays as it is.",
            "Returns the namespace, active; its reactions and schedules pick up where they stopped.",
        ),
        _ => ("", "", ""),
    }
}

/// The guidance of one of the engine's own tools, by the kind the tool
/// catalog names it with.
pub(crate) fn engine_guidance(tool: &str) -> ToolGuidance {
    let (use_when, do_not_use_when, success) = engine_texts(tool);
    ToolGuidance {
        use_when: use_when.to_owned(),
        do_not_use_when: do_not_use_when.to_owned(),
        success: success.to_owned(),
        errors: Vec::new(),
    }
}

/// What the engine knows of a version's own fields for its guidance: the
/// keys of each unique index, and the fields a list filters on.
#[derive(Clone, Debug, Default)]
pub(crate) struct FieldGuidance {
    pub unique: Vec<Vec<String>>,
    pub filters: Vec<String>,
}

// list joins names as a sentence does: a, b and c, or a, b or c.
fn list<S: AsRef<str>>(names: &[S], conjunction: &str) -> String {
    match names {
        [] => String::new(),
        [one] => one.as_ref().to_owned(),
        [rest @ .., last] => format!(
            "{} {conjunction} {}",
            rest.iter()
                .map(AsRef::as_ref)
                .collect::<Vec<_>>()
                .join(", "),
            last.as_ref()
        ),
    }
}

fn texts(use_when: String, do_not_use_when: String, success: String) -> OperationGuidance {
    OperationGuidance {
        use_when: Some(use_when),
        do_not_use_when: Some(do_not_use_when),
        success: Some(success),
        errors: Vec::new(),
    }
}

// builtin_guidance is the engine's base for the operations it serves on
// the schema, on its instance type. takers names the behaviors that take
// create parameters.
fn builtin_guidance(
    type_name: &str,
    takers: &[String],
    fields: &FieldGuidance,
) -> Vec<(&'static str, OperationGuidance)> {
    let keys: Vec<String> = fields
        .unique
        .iter()
        .map(|index| list(index, "and"))
        .collect();
    let singles: Vec<&String> = fields
        .unique
        .iter()
        .filter(|index| index.len() == 1)
        .map(|index| &index[0])
        .collect();
    let together: Vec<String> = fields
        .unique
        .iter()
        .filter(|index| index.len() > 1)
        .map(|index| format!("{} together are unique", list(index, "and")))
        .collect();
    let mut claims: Vec<String> = Vec::new();
    match singles.as_slice() {
        [] => {}
        [one] => claims.push(format!("{one} is unique")),
        many => claims.push(format!("{} are each unique", list(many, "and"))),
    }
    claims.extend(together);
    let unique = (!claims.is_empty()).then(|| {
        format!(
            "{}: a value another {type_name} holds is refused (conflict).",
            claims.join("; ")
        )
    });
    let with_unique = |text: String| match &unique {
        None => text,
        Some(unique) => format!("{text} {unique}"),
    };
    let mut out = vec![
        (
            "create",
            texts(
                with_unique(format!(
                    "Use to create a new {type_name}: data holds its own fields, and id names it, or the engine makes one.{}",
                    if takers.is_empty() {
                        String::new()
                    } else {
                        format!(
                            " behaviors gives {} their create parameters, which hold from the create on.",
                            list(takers, "and")
                        )
                    }
                )),
                format!(
                    "Do not use to change one that exists; call update.{}",
                    if keys.is_empty() {
                        String::new()
                    } else {
                        format!(
                            " Do not create a {type_name} whose {} another holds; call lookup to find it.",
                            list(&keys, "or")
                        )
                    }
                ),
                "Returns the new instance with its id, its seq, its own fields in data and its behaviors' fields in behaviors, by behavior name.".to_owned(),
            ),
        ),
        (
            "get",
            texts(
                format!("Use when you have the id of the {type_name} to read."),
                format!(
                    "Do not use to find {type_name} instances; call {}list.",
                    if keys.is_empty() {
                        String::new()
                    } else {
                        format!("lookup with a {}, or ", list(&keys, "or"))
                    }
                ),
                "Returns the instance with its seq, its own fields in data and its behaviors' fields in behaviors, by behavior name.".to_owned(),
            ),
        ),
        (
            "list",
            texts(
                format!(
                    "Use to page through {type_name} instances, oldest first.{}",
                    if fields.filters.is_empty() {
                        String::new()
                    } else {
                        format!(
                            " where keeps the ones whose fields hold the values it gives, null meaning no value and a list of values any of them: {}; a behavior's field by its qualified name, the behavior's, a dot and the field's.",
                            list(&fields.filters, "or")
                        )
                    }
                ),
                format!(
                    "Do not use to read one instance whose id you have; call get.{}",
                    if keys.is_empty() {
                        String::new()
                    } else {
                        format!(" Do not use to find one by its {}; call lookup.", list(&keys, "or"))
                    }
                ),
                format!(
                    "Returns items and next; pass next as cursor for the page after, until it is null.{}",
                    if fields.filters.is_empty() {
                        ""
                    } else {
                        " With where, a page can hold fewer items than limit, even none, while next is not null: read on until it is."
                    }
                ),
            ),
        ),
        (
            "update",
            texts(
                with_unique(format!(
                    "Use to change the own fields of the {type_name} with the id, as a JSON merge patch; with expectedSeq, the update is refused if the instance changed since that seq."
                )),
                "Do not use to set a behavior's field: the patch holds own fields only, and a behavior's fields change only through its operations.".to_owned(),
                "Returns the instance as the patch left it, with its next seq.".to_owned(),
            ),
        ),
        (
            "delete",
            OperationGuidance {
                use_when: Some(format!(
                    "Use to delete the {type_name} with the id for good; with expectedSeq, the delete is refused if the instance changed since that seq."
                )),
                do_not_use_when: None,
                success: Some(
                    "Returns null; the instance and what its behaviors kept for it are gone."
                        .to_owned(),
                ),
                errors: Vec::new(),
            },
        ),
    ];
    if let Some(first) = fields.unique.first() {
        out.push((
            "lookup",
            texts(
                format!(
                    "Use when you know the {} of the {type_name} to read: key names the field{} and the value, as {{ {} }}; a value may hold a slash.",
                    list(&keys, "or"),
                    if fields.unique.iter().any(|index| index.len() > 1) {
                        "s of one unique index"
                    } else {
                        ""
                    },
                    first
                        .iter()
                        .map(|key| format!("\"{key}\": ..."))
                        .collect::<Vec<_>>()
                        .join(", ")
                ),
                "Do not use when you have the id; call get. Do not use to find instances by another field; call list with where.".to_owned(),
                "Returns the instance whose fields hold the values, with its seq, its own fields in data and its behaviors' fields in behaviors; not_found when none does.".to_owned(),
            ),
        ));
    }
    out
}

/// What guidance and createParamsSchema are told about the type: lookup is
/// among its operations when it has a unique field.
pub(crate) fn describe_target(
    schema: &str,
    composition: &Composition,
    fields: &FieldGuidance,
) -> DescribeTarget {
    let mut operations: Vec<DescribedTypeOperation> = BUILTIN_OPERATIONS
        .iter()
        .filter(|name| **name != "lookup" || !fields.unique.is_empty())
        .map(|name| DescribedTypeOperation {
            name: (*name).to_owned(),
            behavior: None,
            writes: matches!(*name, "create" | "update" | "delete"),
            scope: OperationScope::Instance,
        })
        .collect();
    for bound in &composition.behaviors {
        for operation in &bound.behavior.operations {
            operations.push(DescribedTypeOperation {
                name: operation.name.clone(),
                behavior: Some(bound.behavior.name.clone()),
                writes: operation.writes,
                scope: operation.scope,
            });
        }
    }
    let mut configs = Map::new();
    for bound in &composition.behaviors {
        configs.insert(bound.behavior.name.clone(), bound.json.clone());
    }
    DescribeTarget {
        schema: schema.to_owned(),
        type_name: composition.type_name.clone(),
        behaviors: composition
            .behaviors
            .iter()
            .map(|bound| bound.behavior.name.clone())
            .collect(),
        configs,
        operations,
    }
}

/// Asks each behavior of a version's instance type for its guidance and
/// its create parameters under its config, checks what it returns, and
/// merges each operation's guidance: the engine's base, the owner's, then
/// the others' in list order.
pub(crate) fn version_guidance(
    schema: &str,
    composition: &Composition,
    fields: &FieldGuidance,
) -> Result<VersionGuidance> {
    let target = describe_target(schema, composition, fields);
    let mut said: Vec<(String, BehaviorGuidance)> = Vec::new();
    let mut out = VersionGuidance::default();
    for bound in &composition.behaviors {
        if let Some(guidance) = guidance_of(bound, &target)? {
            out.summaries
                .push((bound.behavior.name.clone(), guidance.summary.clone()));
            said.push((bound.behavior.name.clone(), guidance));
        }
        if let Some(narrowed) = create_params_of(bound, &target)? {
            out.create_params
                .push((bound.behavior.name.clone(), narrowed));
        }
    }
    let takers: Vec<String> = composition
        .behaviors
        .iter()
        .filter(|bound| bound.behavior.create_params.is_some())
        .map(|bound| bound.behavior.name.clone())
        .collect();
    let base = builtin_guidance(&composition.type_name, &takers, fields);
    for operation in &target.operations {
        let mut merged = Merged::default();
        merged.add(
            None,
            base.iter()
                .find(|(name, _)| *name == operation.name)
                .map(|(_, guidance)| guidance),
        );
        let mut order: Vec<&BoundBehavior> = Vec::new();
        if let Some(owner) = &operation.behavior {
            if let Some(bound) = composition.bound(owner) {
                order.push(bound);
            }
        }
        for bound in &composition.behaviors {
            if Some(&bound.behavior.name) != operation.behavior.as_ref() {
                order.push(bound);
            }
        }
        for bound in order {
            let guidance = said
                .iter()
                .find(|(name, _)| *name == bound.behavior.name)
                .and_then(|(_, guidance)| {
                    guidance
                        .operations
                        .iter()
                        .find(|(name, _)| *name == operation.name)
                        .map(|(_, guidance)| guidance)
                });
            merged.add(Some(bound), guidance);
        }
        out.operations.push((operation.name.clone(), merged.done()));
    }
    Ok(out)
}

#[derive(Default)]
struct Merged {
    use_when: Vec<String>,
    do_not_use_when: Vec<String>,
    success: Vec<String>,
    errors: Vec<GuidanceErrorEntry>,
    seen: Vec<(String, String)>,
}

impl Merged {
    fn add(&mut self, bound: Option<&BoundBehavior>, guidance: Option<&OperationGuidance>) {
        let Some(guidance) = guidance else {
            return;
        };
        for (text, into) in [
            (&guidance.use_when, &mut self.use_when),
            (&guidance.do_not_use_when, &mut self.do_not_use_when),
            (&guidance.success, &mut self.success),
        ] {
            if let Some(text) = text.as_ref().filter(|text| !text.is_empty()) {
                into.push(text.clone());
            }
        }
        let Some(bound) = bound else {
            return;
        };
        let name = &bound.behavior.name;
        for error in &guidance.errors {
            let key = (name.clone(), error.code.clone());
            if self.seen.contains(&key) {
                continue;
            }
            self.seen.push(key);
            let declared = bound
                .behavior
                .declaration
                .vetoes
                .iter()
                .find(|veto| veto.code == error.code)
                .and_then(|veto| veto.description.clone());
            let description = error
                .description
                .clone()
                .or(declared)
                .unwrap_or_else(|| format!("a veto with code {}.", error.code));
            self.errors.push(GuidanceErrorEntry {
                code: error.code.clone(),
                description: format!("{name}: {description}"),
                common_correction: error.common_correction.clone(),
            });
        }
    }

    fn done(self) -> ToolGuidance {
        ToolGuidance {
            use_when: self.use_when.join(" "),
            do_not_use_when: self.do_not_use_when.join(" "),
            success: self.success.join(" "),
            errors: self.errors,
        }
    }
}

// guidance_of asks a behavior for its guidance under its config, and holds
// the answer to its declaration and the type's operations.
fn guidance_of(bound: &BoundBehavior, target: &DescribeTarget) -> Result<Option<BehaviorGuidance>> {
    let name = bound.behavior.name.as_str();
    let Some(answer) = bound
        .behavior
        .implementation
        .guidance(&bound.config, target)
    else {
        return Ok(None);
    };
    let guidance = answer?;
    let summary = text(name, "summary", Some(&guidance.summary))?;
    if summary.is_none_or(str::is_empty) {
        return Err(Error::behavior(
            name,
            "guidance returns a summary: what it does on the type under its config",
        ));
    }
    let codes = &bound.behavior.veto_codes;
    for (operation, said) in &guidance.operations {
        if !target.operations.iter().any(|each| each.name == *operation) {
            return Err(Error::behavior(
                name,
                format!(
                    "guidance names operation {operation}, which {} does not have ({})",
                    target.type_name,
                    target
                        .operations
                        .iter()
                        .map(|each| each.name.as_str())
                        .collect::<Vec<_>>()
                        .join(", ")
                ),
            ));
        }
        let at = format!("guidance of {operation}");
        for error in &said.errors {
            if !codes.contains(&error.code) {
                return Err(Error::behavior(
                    name,
                    format!(
                        "{at}: an error's code is a veto code its declaration lists ({}), not {}",
                        if codes.is_empty() {
                            "none".to_owned()
                        } else {
                            codes.join(", ")
                        },
                        crate::json::quote(&error.code)
                    ),
                ));
            }
            text(
                name,
                &format!("{at}: the description of {}", error.code),
                error.description.as_deref(),
            )?;
            let correction = text(
                name,
                &format!("{at}: the commonCorrection of {}", error.code),
                Some(&error.common_correction),
            )?;
            if correction.is_none_or(str::is_empty) {
                return Err(Error::behavior(
                    name,
                    format!("{at}: error {} needs a commonCorrection", error.code),
                ));
            }
        }
        text(name, &format!("{at}: useWhen"), said.use_when.as_deref())?;
        text(
            name,
            &format!("{at}: doNotUseWhen"),
            said.do_not_use_when.as_deref(),
        )?;
        text(name, &format!("{at}: success"), said.success.as_deref())?;
    }
    Ok(Some(guidance))
}

// text holds a guidance text to a string without surrounding whitespace.
fn text<'t>(name: &str, at: &str, value: Option<&'t str>) -> Result<Option<&'t str>> {
    match value {
        Some(text) if text.trim() != text => Err(Error::behavior(
            name,
            format!("{at} is a string without surrounding whitespace"),
        )),
        other => Ok(other),
    }
}

// create_params_of asks a behavior for the create parameters it takes
// under its config, held to the declaration's shape.
fn create_params_of(bound: &BoundBehavior, target: &DescribeTarget) -> Result<Option<Value>> {
    let Some(answer) = bound
        .behavior
        .implementation
        .create_params_schema(&bound.config, target)
    else {
        return Ok(None);
    };
    let schema = answer?;
    if let Some(problem) = create_params_schema_problem(&schema) {
        return Err(Error::behavior(
            &bound.behavior.name,
            format!("{problem}, and so must what its createParamsSchema returns for a config"),
        ));
    }
    Ok(Some(schema))
}
