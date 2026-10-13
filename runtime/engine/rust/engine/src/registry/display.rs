//! A type's display (D48), the port of the checks in
//! `src/registry/display.ts`: what a UI, or an agent, reads to render its
//! instances, written with @display. The meta-schema holds its shape; at
//! define and publish the engine holds it to the document as the
//! compiler's loader does (`internal/loader/verify/display.go`), with the
//! same wording:
//!
//! - `titleField` names one of the type's own fields, by its name or its
//!   JSON key, that holds a single text value: a string primitive, or a
//!   scalar whose values are strings, not a list or a map, and neither
//!   secret nor uiHidden;
//! - each `summaryFields` entry names one of the type's own fields that is
//!   neither secret nor uiHidden, or a field one of its behaviors adds, by
//!   its qualified name (`Workflow.status`);
//! - `states` and `transitions` label the states and transitions of the
//!   type's Workflow, as its config lists them, so a type that does not
//!   compose Workflow, a nested type among them, takes neither.

use serde_json::{Map, Value};

use superschematic_schema_ir::{
    builtin_scalars, is_any_json_scalar, parse_schema_ir, structured_json_type,
};

use super::document::{
    array_depth, field_name, flag, is_map, ref_kind, scalar_key, text, type_name, types_of, RefKind,
};
use crate::composition::Composition;
use crate::errors::SchemaIssue;
use crate::json::{self, compare_utf16};

/// The core behavior whose states and transitions a display labels.
const WORKFLOW: &str = "Workflow";

/// The display issues of every type of a document whose instance type
/// composed, against the type and, for the instance type, its behaviors.
pub(crate) fn display_issues(
    document: &Value,
    instance_type: &str,
    composition: &Composition,
) -> Vec<SchemaIssue> {
    let mut issues = Vec::new();
    let Some(types) = types_of(document) else {
        return issues;
    };
    let mut names: Vec<&String> = types.keys().collect();
    names.sort_by(|a, b| compare_utf16(a, b));
    for type_name in names {
        let type_def = &types[type_name.as_str()];
        let Some(display) = type_def.get("display").and_then(Value::as_object) else {
            continue;
        };
        let composition = (type_name == instance_type).then_some(composition);
        type_issues(
            document,
            type_name,
            type_def,
            display,
            composition,
            &mut issues,
        );
    }
    issues
}

fn type_issues(
    document: &Value,
    type_key: &str,
    type_def: &Value,
    display: &Map<String, Value>,
    composition: Option<&Composition>,
    issues: &mut Vec<SchemaIssue>,
) {
    let label = text(type_def, "name").unwrap_or_default();
    let path = json::pointer(&["types", type_key, "display"]);
    let fields: &[Value] = type_def
        .get("fields")
        .and_then(Value::as_array)
        .map_or(&[], Vec::as_slice);
    let own = |name: &str| {
        fields.iter().find(|field| {
            field_name(field) == name
                || text(field, "jsonTag").is_some_and(|tag| !tag.is_empty() && tag == name)
        })
    };
    let listed = if fields.is_empty() {
        String::new()
    } else {
        format!(
            " (fields: {})",
            fields.iter().map(field_name).collect::<Vec<_>>().join(", ")
        )
    };
    let shown = |field: &Value, member: &str, at: &str, issues: &mut Vec<SchemaIssue>| {
        if flag(field, "secret") {
            issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display {member} names field {}, which is secret",
                    field_name(field)
                ),
            ));
        } else if flag(field, "uiHidden") {
            issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display {member} names field {}, which is @uiHidden",
                    field_name(field)
                ),
            ));
        }
    };

    if let Some(title) = display.get("titleField").and_then(Value::as_str) {
        let at = format!("{path}/titleField");
        let owner = composition.and_then(|composition| composition.field_owner(title));
        match (own(title), owner) {
            (None, Some(owner)) => issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display titleField {} is a field behavior {owner} adds; a title is one of the type's own fields{listed}",
                    json::quote(title)
                ),
            )),
            (None, None) => issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display titleField {} is not a field of the type{listed}",
                    json::quote(title)
                ),
            )),
            (Some(field), _) if !text_field(document, field) => issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display titleField {} has type {}; a title is a single text value: a string, or a scalar whose values are strings",
                    field_name(field),
                    type_label(field)
                ),
            )),
            (Some(field), _) => shown(field, "titleField", &at, issues),
        }
    }

    if let Some(summary) = display.get("summaryFields").and_then(Value::as_array) {
        for (index, name) in summary.iter().enumerate() {
            let name = name.as_str().unwrap_or_default();
            let at = format!("{path}/summaryFields/{index}");
            if let Some(field) = own(name) {
                shown(field, "summaryFields", &at, issues);
                continue;
            }
            if composition.is_some_and(|composition| composition.field_owner(name).is_some()) {
                continue;
            }
            // A behavior's field named bare: say the name that reaches it.
            let qualified: Vec<String> = composition
                .map(|composition| {
                    composition
                        .behaviors
                        .iter()
                        .filter(|bound| bound.behavior.fields.iter().any(|field| field == name))
                        .map(|bound| format!("{}.{name}", bound.behavior.name))
                        .collect()
                })
                .unwrap_or_default();
            let hint = if qualified.is_empty() {
                String::new()
            } else {
                format!(
                    "; a behavior's field is named by its qualified name: {}",
                    qualified.join(" or ")
                )
            };
            issues.push(SchemaIssue::new(
                at,
                format!(
                    "type {label}: @display summaryFields lists {}, which is not a field of the type or of its behaviors{listed}{hint}",
                    json::quote(name)
                ),
            ));
        }
    }

    let sorted_keys = |key: &str| -> Vec<String> {
        let mut keys: Vec<String> = display
            .get(key)
            .and_then(Value::as_object)
            .map(|members| members.keys().cloned().collect())
            .unwrap_or_default();
        keys.sort_by(|a, b| compare_utf16(a, b));
        keys
    };
    let states = sorted_keys("states");
    let transitions = sorted_keys("transitions");
    if states.is_empty() && transitions.is_empty() {
        return;
    }
    // The Workflow config a display reads, as its parseConfig returns it:
    // the states and transitions as the schema lists them.
    let Some(workflow) = composition.and_then(|composition| composition.bound(WORKFLOW)) else {
        issues.push(SchemaIssue::new(
            path,
            format!("type {label}: @display states and transitions label a Workflow's; the type does not compose Workflow"),
        ));
        return;
    };
    let workflow_states: Vec<&str> = workflow
        .json
        .get("states")
        .and_then(Value::as_array)
        .map(|states| states.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    let moves: Vec<(&str, &str)> = workflow
        .json
        .get("transitions")
        .and_then(Value::as_array)
        .map(|moves| {
            moves
                .iter()
                .map(|entry| {
                    (
                        text(entry, "from").unwrap_or_default(),
                        text(entry, "to").unwrap_or_default(),
                    )
                })
                .collect()
        })
        .unwrap_or_default();
    for state in &states {
        if !workflow_states.contains(&state.as_str()) {
            issues.push(SchemaIssue::new(
                json::pointer(&["types", type_key, "display", "states", state.as_str()]),
                format!(
                    "type {label}: @display states labels {}, which is not a state of its Workflow ({})",
                    json::quote(state),
                    workflow_states.join(", ")
                ),
            ));
        }
    }
    for from in &transitions {
        let mut targets: Vec<String> = display["transitions"][from.as_str()]
            .as_object()
            .map(|members| members.keys().cloned().collect())
            .unwrap_or_default();
        targets.sort_by(|a, b| compare_utf16(a, b));
        for to in targets {
            if !moves.iter().any(|(start, end)| start == from && *end == to) {
                issues.push(SchemaIssue::new(
                    json::pointer(&["types", type_key, "display", "transitions", from.as_str(), to.as_str()]),
                    format!(
                        "type {label}: @display transitions labels the move from {from} to {to}, which is not a transition of its Workflow"
                    ),
                ));
            }
        }
    }
}

// Whether a field holds a single text value: a string primitive, or a
// scalar whose values are strings. The catalog gives a scalar that holds
// any JSON value, a JSON object or a JSON array the string primitive too,
// so the runtime's reading of it rules it out.
fn text_field(document: &Value, field: &Value) -> bool {
    if array_depth(field) > 0 || is_map(field) {
        return false;
    }
    let name = type_name(field);
    match ref_kind(document, name) {
        RefKind::Primitive => matches!(name, "string" | "String" | "ID"),
        RefKind::Scalar => {
            // A catalog scalar is the catalog's, whatever the document
            // declares.
            if let Some(builtin) = builtin_scalars().get(&scalar_key(name)) {
                return builtin.primitive == "String"
                    && !is_any_json_scalar(builtin)
                    && structured_json_type(builtin).is_none();
            }
            let declared = document
                .get("scalars")
                .and_then(|scalars| scalars.get(name));
            let Some(declared) =
                declared.filter(|declared| text(declared, "languagePrimitive") == Some("string"))
            else {
                return false;
            };
            let mut scalars = Map::new();
            scalars.insert(name.to_owned(), declared.clone());
            let mut wrapper = Map::new();
            wrapper.insert("scalars".to_owned(), Value::Object(scalars));
            parse_schema_ir(&Value::Object(wrapper))
                .ok()
                .and_then(|schema| schema.scalars.get(&scalar_key(name)).cloned())
                .is_some_and(|scalar| {
                    scalar.primitive == "String"
                        && !is_any_json_scalar(&scalar)
                        && structured_json_type(&scalar).is_none()
                })
        }
        _ => false,
    }
}

// type_label writes a field's type as a message names it.
fn type_label(field: &Value) -> String {
    let label = format!(
        "{}{}",
        type_name(field),
        match array_depth(field) {
            0 => "",
            1 => "[]",
            _ => "[][]",
        }
    );
    if is_map(field) {
        format!("a map of {label}")
    } else {
        label
    }
}

// The display checks of display.test.ts, which compose Workflow and
// Comments: their implementations arrive in milestone 5, so these tests
// register stand-ins that add the fields the real ones declare, and the
// Workflow config a display reads is the schema's.
#[cfg(all(test, feature = "sqlite"))]
mod tests {
    use serde_json::{json, Value};

    use crate::composition::{BehaviorRegistry, RegisteredBehavior};
    use crate::engine::{Engine, EngineOptions};
    use crate::errors::Error;
    use crate::principal::Principal;
    use crate::registry::DefineOptions;

    struct Open {
        engine: Engine,
        dir: std::path::PathBuf,
    }

    impl Drop for Open {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.dir);
        }
    }

    fn open() -> Open {
        use std::sync::atomic::{AtomicU64, Ordering};
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let dir = std::env::temp_dir().join(format!(
            "engine-display-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let mut behaviors = BehaviorRegistry::default();
        for (name, field) in [("Workflow", "status"), ("Comments", "commentCount")] {
            behaviors.register(RegisteredBehavior {
                name: name.into(),
                fields: vec![field.into()],
            });
        }
        let mut options = EngineOptions::sqlite(dir.join("engine.db"));
        options.policy = Some(crate::Policy::allow_all());
        let engine = Engine::open_with(options, behaviors).unwrap();
        Open { engine, dir }
    }

    fn alice() -> Principal {
        Principal::new("alice", Vec::<String>::new())
    }

    fn tickets(change: impl FnOnce(&mut Value)) -> Value {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../../../internal/loader/testdata/services/fixture-display-json/src/ticket.schema.json"
        );
        let mut document: Value =
            serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
        change(&mut document["types"]["Ticket"]);
        document
    }

    fn refusals(document: &Value) -> Vec<(String, String)> {
        let opened = open();
        match opened
            .engine
            .schemas()
            .define(&alice(), document, DefineOptions::default())
        {
            Err(Error::Engine(error)) => error
                .issues()
                .iter()
                .map(|issue| (issue.path.clone(), issue.message.clone()))
                .collect(),
            other => panic!("expected a refused document, got {other:?}"),
        }
    }

    fn defines(document: &Value) {
        let opened = open();
        opened
            .engine
            .schemas()
            .define(&alice(), document, DefineOptions::default())
            .unwrap();
    }

    fn pair(path: &str, message: &str) -> (String, String) {
        (path.to_owned(), message.to_owned())
    }

    #[test]
    fn the_fixture_defines() {
        defines(&tickets(|_| {}));
    }

    #[test]
    fn its_title_is_one_of_the_types_own_single_text_fields_shown_to_a_reader() {
        let path = "/types/Ticket/display/titleField";
        let title = |name: &str| tickets(|ticket| ticket["display"]["titleField"] = json!(name));
        assert_eq!(
            refusals(&title("nope")),
            [pair(path, "type Ticket: @display titleField \"nope\" is not a field of the type (fields: title, assignee, body)")]
        );
        assert_eq!(
            refusals(&title("Workflow.status")),
            [pair(path, "type Ticket: @display titleField \"Workflow.status\" is a field behavior Workflow adds; a title is one of the type's own fields (fields: title, assignee, body)")]
        );
        // A bare status names an own field, and Ticket has none.
        assert_eq!(
            refusals(&title("status")),
            [pair(path, "type Ticket: @display titleField \"status\" is not a field of the type (fields: title, assignee, body)")]
        );
        let typed = |type_ref: Value, extra: Value| {
            tickets(|ticket| {
                let mut field = json!({"name": "other", "typeRef": type_ref});
                if let Value::Object(extra) = extra {
                    field.as_object_mut().unwrap().extend(extra);
                }
                ticket["fields"].as_array_mut().unwrap().push(field);
                ticket["display"]["titleField"] = json!("other");
            })
        };
        assert_eq!(
            refusals(&typed(json!({"name": "string", "isArray": true}), json!({}))),
            [pair(path, "type Ticket: @display titleField other has type string[]; a title is a single text value: a string, or a scalar whose values are strings")]
        );
        assert!(refusals(&typed(json!({"name": "number"}), json!({})))[0]
            .1
            .contains("titleField other has type number;"));
        assert!(
            refusals(&typed(json!({"name": "Generic.Int64"}), json!({})))[0]
                .1
                .contains("titleField other has type Generic.Int64;")
        );
        // The catalog gives these the string primitive; their values are JSON.
        for name in ["Generic.JSON", "Generic.StringMap", "Embedding.Vector"] {
            assert!(
                refusals(&typed(json!({"name": name}), json!({})))[0]
                    .1
                    .contains(&format!("titleField other has type {name};")),
                "{name}"
            );
        }
        assert_eq!(
            refusals(&typed(json!({"name": "string"}), json!({"secret": true}))),
            [pair(
                path,
                "type Ticket: @display titleField names field other, which is secret"
            )]
        );
        assert_eq!(
            refusals(&typed(json!({"name": "string"}), json!({"uiHidden": true}))),
            [pair(
                path,
                "type Ticket: @display titleField names field other, which is @uiHidden"
            )]
        );
        // A scalar whose values are strings is text.
        defines(&typed(json!({"name": "Identity.Name"}), json!({})));
        defines(&typed(json!({"name": "Text.Markdown"}), json!({})));
    }

    #[test]
    fn its_summary_fields_are_the_types_own_fields_or_its_behaviors_by_qualified_name() {
        let document = tickets(|ticket| {
            ticket["display"]["summaryFields"] = json!([
                "Workflow.status",
                "Comments.commentCount",
                "status",
                "ghost"
            ]);
        });
        assert_eq!(
            refusals(&document),
            [
                // A behavior's field by its name alone is no own field of the type.
                pair(
                    "/types/Ticket/display/summaryFields/2",
                    "type Ticket: @display summaryFields lists \"status\", which is not a field of the type or of its behaviors (fields: title, assignee, body); a behavior's field is named by its qualified name: Workflow.status"
                ),
                pair(
                    "/types/Ticket/display/summaryFields/3",
                    "type Ticket: @display summaryFields lists \"ghost\", which is not a field of the type or of its behaviors (fields: title, assignee, body)"
                ),
            ]
        );
    }

    #[test]
    fn its_states_and_transitions_are_its_workflow() {
        let document = tickets(|ticket| {
            let display = &mut ticket["display"];
            display["states"]["lost"] = json!({"label": "Lost"});
            display["transitions"]["done"] = json!({"todo": "Reopen"});
            display["transitions"]["todo"]["review"] = json!("Skip ahead");
        });
        assert_eq!(
            refusals(&document),
            [
                pair("/types/Ticket/display/states/lost", "type Ticket: @display states labels \"lost\", which is not a state of its Workflow (todo, implementing, review, done, dropped)"),
                pair("/types/Ticket/display/transitions/done/todo", "type Ticket: @display transitions labels the move from done to todo, which is not a transition of its Workflow"),
                pair("/types/Ticket/display/transitions/todo/review", "type Ticket: @display transitions labels the move from todo to review, which is not a transition of its Workflow"),
            ]
        );
        let without = tickets(|ticket| {
            ticket["behaviors"] = json!([{"name": "Comments"}]);
            ticket["display"]["summaryFields"] = json!(["assignee"]);
        });
        assert_eq!(
            refusals(&without),
            [pair("/types/Ticket/display", "type Ticket: @display states and transitions label a Workflow's; the type does not compose Workflow")]
        );
    }

    #[test]
    fn a_nested_type_takes_a_display_of_its_fields_and_no_states() {
        let nested = |display: Value| {
            json!({
                "kind": "General",
                "name": "Order",
                "types": {
                    "Order": {"name": "Order", "role": "EmbeddedStruct", "fields": [{"name": "line", "typeRef": {"name": "Line"}}]},
                    "Line": {"name": "Line", "role": "EmbeddedStruct", "fields": [{"name": "sku", "typeRef": {"name": "string"}}], "display": display}
                }
            })
        };
        defines(&nested(
            json!({"noun": "Line", "titleField": "sku", "summaryFields": ["sku"]}),
        ));
        assert_eq!(
            refusals(&nested(json!({"states": {"open": {"label": "Open"}}}))),
            [pair("/types/Line/display", "type Line: @display states and transitions label a Workflow's; the type does not compose Workflow")]
        );
    }

    #[test]
    fn the_meta_schema_holds_its_shape() {
        for display in [
            json!({}),
            json!({"noun": " "}),
            json!({"states": {"todo": {"tone": "blue"}}}),
            json!({"states": {"todo": {}}}),
            json!({"states": {"to do": {"label": "To do"}}}),
            json!({"transitions": {"todo": {}}}),
            json!({"summaryFields": ["title", "title"]}),
            json!({"summaryFields": []}),
            json!({"colour": "red"}),
        ] {
            let issues = refusals(&tickets(|ticket| ticket["display"] = display.clone()));
            assert!(!issues.is_empty(), "{display}");
            assert!(
                issues
                    .iter()
                    .all(|(path, _)| path.starts_with("/types/Ticket/display")),
                "{issues:?}"
            );
        }
    }
}
