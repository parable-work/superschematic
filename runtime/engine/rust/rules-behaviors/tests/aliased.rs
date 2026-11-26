//! Aliased on the pure core (D147), through the core's public API over a
//! source in memory that keeps rows, their sub-tables and the uses' shared
//! tables: the declaration file's members, among them the value a create
//! gives (`fromCreate`) and the shared table's holder; the config's
//! normalize; a create that takes its name, and one refused for a name
//! another row holds, as a name or an alias, with that row's key; rename,
//! which keeps the old name as an alias, makes an alias the name and only
//! rewrites one whose normalized form stays; addAlias and removeAlias, a
//! removed alias freeing its name for another row; the answers that change
//! nothing; resolve on the table; a delete freeing the row's names (D147's
//! shared delete); a name whose holder is gone taken over; two creates of
//! one name, the second run again; and the name no update sets.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Caller, Command, FieldSpec, Outcome, SharedRowChange, TypeSpec, UseSpec,
};
use superschematic_rules_behaviors::{aliased, core};
use support::{caller, changed, define, object, refused, result, veto_of, Host};

fn company(config: Value) -> TypeSpec {
    TypeSpec::new("Company")
        .with(UseSpec::on_field("name", "Aliased", config))
        .with_field(FieldSpec::new("city", "string"))
}

fn alice() -> Caller {
    caller("alice", &[])
}

fn host(config: Value) -> Host {
    Host::new(core(), company(config))
}

fn create(host: &mut Host, key: &str, own: Value) -> Outcome {
    host.create(&alice(), key, own)
}

fn invoke(host: &mut Host, key: &str, operation: &str, params: Value) -> Outcome {
    host.invoke(&alice(), key, "name", operation, params)
}

fn resolve(host: &mut Host, name: &str) -> Value {
    result(host.run(
        &alice(),
        support::START,
        Action::TableOperation {
            use_id: "name".into(),
            operation: aliased::RESOLVE.into(),
            params: json!({"name": name}),
        },
    ))
}

// names is the shared table's rows, by normalized form, each its holder.
fn names(host: &Host) -> Vec<(String, Value)> {
    host.memory
        .shared
        .iter()
        .filter(|((use_id, table, _), _)| use_id == "name" && table == aliased::NAMES)
        .map(|((_, _, key), row)| (key.clone(), row.values["holder"].clone()))
        .collect()
}

// aliases is a row's aliases, by normalized form, each as given.
fn aliases(host: &Host, key: &str) -> Vec<(String, Value)> {
    host.sub_rows(key, "name", aliased::ALIASES)
        .into_iter()
        .map(|row| (row.key, row.values["alias"].clone()))
        .collect()
}

fn name(host: &Host, key: &str) -> Value {
    host.row(key)
        .owned("name", "name")
        .cloned()
        .unwrap_or(Value::Null)
}

fn pairs(entries: &[(&str, &str)]) -> Vec<(String, Value)> {
    entries
        .iter()
        .map(|(key, value)| ((*key).to_owned(), Value::from(*value)))
        .collect()
}

/// The declaration file names the name its value field, which a create
/// gives, the sub-table aliases and the shared table names whose holder
/// holds a row's key; the core reads the value as one a create gives.
#[test]
fn the_declaration_names_its_value_its_aliases_and_its_shared_names() {
    let file: Value = serde_json::from_str(aliased::DECLARATION).expect("it parses");
    assert_eq!(file["value"], "name");
    assert_eq!(file["fields"][0]["fromCreate"], json!(true));
    assert_eq!(file["tables"][0]["name"], "aliases");
    assert_eq!(file["tables"][0]["key"], "normalized");
    assert_eq!(file["tables"][1]["name"], "names");
    assert_eq!(file["tables"][1]["scope"], "schema");
    assert_eq!(file["tables"][1]["fields"][1]["rowKey"], json!(true));
    let declared = core().declaration("Aliased").expect("registered").clone();
    assert_eq!(declared.value.as_deref(), Some("name"));
    assert!(declared.value_from_create);
    assert!(!declared.find_table("aliases").expect("aliases").shared);
    assert!(declared.find_table("names").expect("names").shared);
    assert_eq!(
        declared.find_table_operation("resolve").map(|op| op.writes),
        Some(false)
    );
    for operation in ["rename", "addAlias", "removeAlias"] {
        assert_eq!(
            declared.find_operation(operation).map(|op| op.writes),
            Some(true),
            "{operation}"
        );
    }
    assert_eq!(declared.vetoes, ["name_taken", "is_name"]);
}

/// A config takes normalize lower or slug, and nothing else.
#[test]
fn the_config_takes_its_normalize() {
    assert!(define(&core(), &company(json!({}))).is_ok());
    assert!(define(&core(), &company(json!({"normalize": "slug"}))).is_ok());
    let refused = define(&core(), &company(json!({"normalize": "upper"}))).unwrap_err();
    assert!(
        refused.message.contains("must be one of \"lower\", \"slug\""),
        "{}",
        refused.message
    );
}

/// A create gives the name, which the core hands the use: the row holds it
/// as its owned value, its normalized form is a row of names that holds the
/// row's key, and the own fields keep the rest.
#[test]
fn a_create_takes_its_name() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme  Group", "city": "Oslo"})));
    assert_eq!(name(&host, "c1"), json!("Acme  Group"));
    assert_eq!(host.row("c1").own, object(json!({"city": "Oslo"})));
    assert_eq!(names(&host), pairs(&[("acme group", "c1")]));
}

/// A create with no name, or one that names nothing, is refused as an
/// invalid instance.
#[test]
fn a_create_needs_a_name_that_names_something() {
    let mut host = host(json!({"normalize": "slug"}));
    let error = refused(create(&mut host, "c1", json!({"city": "Oslo"})));
    assert_eq!(error["code"], "invalid_instance");
    assert!(error["message"].as_str().unwrap().contains("name is required"));
    let error = refused(create(&mut host, "c1", json!({"name": "!!!"})));
    assert_eq!(error["code"], "invalid_instance");
    assert!(error["message"]
        .as_str()
        .unwrap()
        .contains("normalizes (slug) to nothing"));
    assert!(names(&host).is_empty());
}

/// A name another row holds, as its name or an alias, in its normalized
/// form, refuses a create, a rename and an addAlias, with that row's key.
#[test]
fn a_name_another_row_holds_is_refused_with_its_key() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    changed(invoke(&mut host, "c1", "addAlias", json!({"alias": "Acme Corp"})));
    let (code, details) = veto_of(create(&mut host, "c2", json!({"name": "ACME"})));
    assert_eq!(code, "name_taken");
    assert_eq!(
        details,
        json!({"name": "ACME", "normalized": "acme", "holder": "c1"})
    );
    let (code, details) = veto_of(create(&mut host, "c2", json!({"name": " acme   corp"})));
    assert_eq!(code, "name_taken");
    assert_eq!(details["holder"], "c1");
    changed(create(&mut host, "c2", json!({"name": "Globex"})));
    let (code, details) = veto_of(invoke(&mut host, "c2", "rename", json!({"name": "acme corp"})));
    assert_eq!(code, "name_taken");
    assert_eq!(details["holder"], "c1");
    let (code, _) = veto_of(invoke(&mut host, "c2", "addAlias", json!({"alias": "Acme"})));
    assert_eq!(code, "name_taken");
    assert_eq!(name(&host, "c2"), json!("Globex"));
    assert!(aliases(&host, "c2").is_empty());
}

/// rename keeps the old name as an alias; renaming to an alias makes it the
/// name and the old name an alias in its place; one that changes only the
/// case keeps no alias; the name the row has is answered unchanged.
#[test]
fn rename_keeps_the_old_name_as_an_alias() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    assert_eq!(
        result(invoke(&mut host, "c1", "rename", json!({"name": "Acme Group"}))),
        json!({"name": "Acme Group", "alias": "Acme"})
    );
    assert_eq!(name(&host, "c1"), json!("Acme Group"));
    assert_eq!(aliases(&host, "c1"), pairs(&[("acme", "Acme")]));
    assert_eq!(
        names(&host),
        pairs(&[("acme", "c1"), ("acme group", "c1")])
    );
    // Back to the alias: it becomes the name, the other name an alias.
    assert_eq!(
        result(invoke(&mut host, "c1", "rename", json!({"name": "ACME"}))),
        json!({"name": "ACME", "alias": "Acme Group"})
    );
    assert_eq!(aliases(&host, "c1"), pairs(&[("acme group", "Acme Group")]));
    assert_eq!(
        names(&host),
        pairs(&[("acme", "c1"), ("acme group", "c1")])
    );
    // Only the case: the name is rewritten and no alias kept.
    assert_eq!(
        result(invoke(&mut host, "c1", "rename", json!({"name": "acme"}))),
        json!({"name": "acme"})
    );
    assert_eq!(aliases(&host, "c1"), pairs(&[("acme group", "Acme Group")]));
    let version = host.row("c1").version;
    assert_eq!(
        result(invoke(&mut host, "c1", "rename", json!({"name": "acme"}))),
        json!({"name": "acme", "unchanged": true})
    );
    assert_eq!(host.row("c1").version, version);
}

/// addAlias claims the name; removeAlias frees it, and another row may then
/// take it; the row's name is refused; one it holds, or not, answers
/// unchanged.
#[test]
fn a_removed_alias_frees_its_name() {
    let mut host = host(json!({"normalize": "slug"}));
    changed(create(&mut host, "c1", json!({"name": "Acme, Inc."})));
    assert_eq!(
        result(invoke(&mut host, "c1", "addAlias", json!({"alias": "Acme Co"}))),
        json!({"alias": "Acme Co"})
    );
    assert_eq!(
        result(invoke(&mut host, "c1", "addAlias", json!({"alias": "acme--co"}))),
        json!({"alias": "acme--co", "unchanged": true})
    );
    assert_eq!(
        result(invoke(&mut host, "c1", "addAlias", json!({"alias": "ACME INC"}))),
        json!({"alias": "ACME INC", "unchanged": true})
    );
    assert_eq!(
        names(&host),
        pairs(&[("acme-co", "c1"), ("acme-inc", "c1")])
    );
    let (code, details) = veto_of(invoke(&mut host, "c1", "removeAlias", json!({"alias": "acme inc"})));
    assert_eq!(code, "is_name");
    assert_eq!(details, json!({"name": "Acme, Inc."}));
    assert_eq!(
        result(invoke(&mut host, "c1", "removeAlias", json!({"alias": "Initech"}))),
        json!({"alias": "Initech", "unchanged": true})
    );
    assert_eq!(
        result(invoke(&mut host, "c1", "removeAlias", json!({"alias": "acme co"}))),
        json!({"alias": "Acme Co"})
    );
    assert!(aliases(&host, "c1").is_empty());
    assert_eq!(names(&host), pairs(&[("acme-inc", "c1")]));
    changed(create(&mut host, "c2", json!({"name": "ACME CO"})));
    assert_eq!(
        names(&host),
        pairs(&[("acme-co", "c2"), ("acme-inc", "c1")])
    );
}

/// resolve answers the row a name or an alias names, in its normalized
/// form, and none for a name no row holds.
#[test]
fn resolve_finds_the_row_a_name_names() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    changed(invoke(&mut host, "c1", "rename", json!({"name": "Acme Group"})));
    assert_eq!(
        resolve(&mut host, "  ACME group"),
        json!({"row": {"key": "c1", "name": "Acme Group", "alias": false}})
    );
    assert_eq!(
        resolve(&mut host, "acme"),
        json!({"row": {"key": "c1", "name": "Acme Group", "alias": true}})
    );
    assert_eq!(resolve(&mut host, "Globex"), json!({"row": null}));
}

/// A delete through the core frees the row's names, its name and each
/// alias, and another row may take them.
#[test]
fn a_delete_frees_the_rows_names() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    changed(invoke(&mut host, "c1", "addAlias", json!({"alias": "Acme Co"})));
    changed(create(&mut host, "c2", json!({"name": "Globex"})));
    let changes = changed(host.delete(&alice(), "c1"));
    let deleted: Vec<&str> = changes
        .shared_rows
        .iter()
        .filter(|write| matches!(write.change, SharedRowChange::Delete { .. }))
        .map(|write| write.key.as_str())
        .collect();
    assert_eq!(deleted, ["acme", "acme co"]);
    assert_eq!(names(&host), pairs(&[("globex", "c2")]));
    changed(invoke(&mut host, "c2", "addAlias", json!({"alias": "Acme"})));
    assert_eq!(resolve(&mut host, "acme")["row"]["key"], "c2");
}

/// A name whose holder is no row (deleted past the core) is taken over,
/// held to the version it was read at.
#[test]
fn a_name_whose_holder_is_gone_is_taken_over() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    host.memory.rows.remove("c1");
    assert_eq!(resolve(&mut host, "acme"), json!({"row": null}));
    let changes = changed(create(&mut host, "c2", json!({"name": "ACME"})));
    assert!(matches!(
        changes.shared_rows[0].change,
        SharedRowChange::Update {
            expected_version: 1,
            ..
        }
    ));
    assert_eq!(names(&host), pairs(&[("acme", "c2")]));
}

/// Two creates that read one name at once: each change set inserts its row
/// of names, which a host applies to one row, so the second inserts
/// nothing and runs again, when it finds the name taken.
#[test]
fn two_creates_of_one_name_run_one_after_the_other() {
    let core = core();
    let spec = company(json!({}));
    let mut first = support::Memory::default();
    let command = |key: &str| {
        Command::new(
            alice(),
            support::START,
            Action::Create {
                id: Some(key.to_owned()),
                own: object(json!({"name": "Acme"})),
                params: Default::default(),
            },
        )
        .with_request(key)
    };
    let a = core.drive(&spec, &command("c1"), &mut first);
    let b = core.drive(&spec, &command("c2"), &mut first);
    for outcome in [&a, &b] {
        let Outcome::Changed(changes) = outcome else {
            panic!("{outcome:?}")
        };
        assert!(matches!(
            changes.shared_rows[0].change,
            SharedRowChange::Insert { .. }
        ));
        assert_eq!(changes.shared_rows[0].key, "acme");
    }
    // The first applies; the second, run again from fresh state, is refused.
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    let (code, details) = veto_of(create(&mut host, "c2", json!({"name": "Acme"})));
    assert_eq!(code, "name_taken");
    assert_eq!(details["holder"], "c1");
}

/// No update sets the name: only rename does.
#[test]
fn no_update_sets_the_name() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    let error = refused(host.update(&alice(), "c1", json!({"name": "Globex"})));
    assert_eq!(error["code"], "invalid_instance");
    assert!(error["message"]
        .as_str()
        .unwrap()
        .contains("name is behavior Aliased's field"));
    changed(host.update(&alice(), "c1", json!({"city": "Bergen"})));
    assert_eq!(name(&host, "c1"), json!("Acme"));
}

/// The parameters are held to their schemas before anything reads.
#[test]
fn the_parameters_are_checked() {
    let mut host = host(json!({}));
    changed(create(&mut host, "c1", json!({"name": "Acme"})));
    let error = refused(invoke(&mut host, "c1", "rename", json!({})));
    assert_eq!(error["code"], "invalid_argument");
    assert!(error["message"]
        .as_str()
        .unwrap()
        .contains("must have required property 'name'"));
    let error = refused(invoke(&mut host, "c1", "addAlias", json!({"alias": "", "x": 1})));
    assert_eq!(error["code"], "invalid_argument");
    let long = "a".repeat(257);
    let error = refused(invoke(&mut host, "c1", "addAlias", json!({"alias": long})));
    assert!(error["message"]
        .as_str()
        .unwrap()
        .contains("must NOT have more than 256 characters"));
}
