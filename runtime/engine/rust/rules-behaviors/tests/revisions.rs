//! Revisions on the pure core (D16, D103 step 4), through the core's public
//! API over a source in memory: the declaration's owned field, computed
//! field and sub-tables; the config held to its schema; a revision at a
//! create and at each change, none at a change of nothing, read a page at
//! a time; the review step's guard, its permission and its states; an
//! approval's write of the own fields through Cx::update, its revision kept
//! with its proposal; propose's evidence, checked against other types' rows
//! as the run reads them; and the config held while the table has rows.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{Action, Command, FieldSpec, Row, RowWrite, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{core, revisions};
use support::{caller, changed, define, refused, result, shape, veto_of, Host};

const USE: &str = "Revisions";

fn document(config: Value) -> TypeSpec {
    TypeSpec::new("Document")
        .with(UseSpec::on_type(USE, config))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("body", "string"))
}

fn reviewed() -> Host {
    Host::new(
        core(),
        document(json!({"review": {"permission": "documents.review"}})),
    )
}

fn revisions_of(host: &mut Host, key: &str, params: Value) -> Value {
    result(host.invoke(&caller("alice", &[]), key, USE, "listRevisions", params))
}

#[test]
fn the_declaration_keeps_its_number_owned_its_count_computed_and_two_sub_tables() {
    let registered = core();
    let declaration = registered.declaration(revisions::NAME).expect("registered");
    assert!(declaration.has_field(revisions::REVISION));
    assert!(!declaration.is_computed(revisions::REVISION));
    assert!(declaration.is_computed(revisions::PENDING_PROPOSALS));
    let tables: Vec<(&str, Option<&str>)> = declaration
        .tables
        .iter()
        .map(|table| (table.name.as_str(), table.key.as_deref()))
        .collect();
    assert_eq!(
        tables,
        [
            ("revisions", Some("revision")),
            ("proposals", Some("proposal"))
        ]
    );
    assert!(define(&core(), &document(json!({}))).is_ok());
    assert_eq!(
        shape(&core(), &document(json!({"review": {}, "keep": 3}))),
        [
            "/config: unknown key \"keep\"",
            "/config/review: must have required property 'permission'"
        ]
    );
}

#[test]
fn a_create_keeps_revision_one_and_each_change_the_next_and_a_change_of_nothing_none() {
    let mut host = reviewed();
    let alice = caller("alice", &[]);
    changed(host.create(&alice, "d1", json!({"title": "Plan"})));
    changed(host.update(&alice, "d1", json!({"body": "Draft."})));
    let same = changed(host.update(&alice, "d1", json!({"body": "Draft."})));
    assert!(same.sub_rows.is_empty(), "{:?}", same.sub_rows);
    assert_eq!(host.row("d1").owned[USE]["revision"], json!(2));
    let first = revisions_of(&mut host, "d1", json!({"limit": 1}));
    assert_eq!(first["items"][0]["data"], json!({"title": "Plan"}));
    let next = first["next"].as_str().expect("a cursor").to_owned();
    let second = revisions_of(&mut host, "d1", json!({"limit": 1, "cursor": next}));
    assert_eq!(
        second,
        json!({"items": [{"revision": 2, "data": {"title": "Plan", "body": "Draft."}, "createdBy": "alice", "createdAt": support::START}], "next": null})
    );
    let missing = refused(host.invoke(&alice, "d1", USE, "getRevision", json!({"revision": 3})));
    assert_eq!(
        (missing["code"].clone(), missing["message"].clone()),
        (
            json!("not_found"),
            json!("Document d1 has revisions 1 to 2, not revision 3")
        )
    );
}

#[test]
fn review_needs_its_config_and_its_permission_and_an_approval_writes_through_an_update() {
    let wes = caller("wes", &[]);
    let rae = caller("rae", &["documents.review"]);
    let mut plain = Host::new(core(), document(json!({})));
    changed(plain.create(&wes, "d1", json!({"title": "Plan"})));
    let (code, _) =
        veto_of(plain.invoke(&wes, "d1", USE, "propose", json!({"patch": {"title": "X"}})));
    assert_eq!(code, "no_review");

    let mut host = reviewed();
    changed(host.create(&wes, "d1", json!({"title": "Plan"})));
    let nothing = refused(host.invoke(
        &wes,
        "d1",
        USE,
        "propose",
        json!({"patch": {"title": "Plan"}}),
    ));
    assert_eq!(
        nothing["details"]["issues"],
        json!([{"path": "/patch", "message": "changes nothing: Document d1 already has these fields"}])
    );
    let proposed = result(host.invoke(
        &wes,
        "d1",
        USE,
        "propose",
        json!({"patch": {"body": "Proposed."}, "note": "Fills it in."}),
    ));
    assert_eq!(
        [&proposed["id"], &proposed["state"], &proposed["base"]],
        [&json!(1), &json!("pending"), &json!(1)]
    );
    assert_eq!(host.row("d1").own.get("body"), None);
    assert_eq!(
        host.read_fields("d1", USE, "proposals")[revisions::PENDING_PROPOSALS],
        json!(1)
    );
    let forbidden = refused(host.invoke(&wes, "d1", USE, "approve", json!({"proposal": 1})));
    assert_eq!(
        forbidden["message"],
        "wes may not approve a proposal on Document d1: it needs permission documents.review"
    );
    let approval = changed(host.invoke(&rae, "d1", USE, "approve", json!({"proposal": 1})));
    let RowWrite::Update { own, owned, .. } = &approval.rows[0] else {
        panic!("{:?}", approval.rows)
    };
    assert_eq!(own.get("body"), Some(&json!("Proposed.")));
    assert_eq!(owned[USE]["revision"], json!(2));
    assert_eq!(
        [
            &approval.result["state"],
            &approval.result["reviewedBy"],
            &approval.result["revision"]
        ],
        [&json!("approved"), &json!("rae"), &json!(2)]
    );
    let kept = result(host.invoke(&rae, "d1", USE, "getRevision", json!({"revision": 2})));
    assert_eq!(
        [&kept["data"], &kept["proposal"], &kept["createdBy"]],
        [
            &json!({"title": "Plan", "body": "Proposed."}),
            &json!(1),
            &json!("rae")
        ]
    );
    let (code, details) = veto_of(host.invoke(&rae, "d1", USE, "approve", json!({"proposal": 1})));
    assert_eq!(
        (code.as_str(), details),
        ("not_pending", json!({"proposal": 1, "state": "approved"}))
    );
    let unknown = refused(host.invoke(&rae, "d1", USE, "reject", json!({"proposal": 7})));
    assert_eq!(
        unknown["details"]["issues"],
        json!([{"path": "/proposal", "message": "Document d1 has no proposal 7"}])
    );
}

#[test]
fn evidence_is_checked_against_the_rows_the_run_reads_and_kept_as_given() {
    let wes = caller("wes", &[]);
    let mut host = reviewed();
    let note = TypeSpec::new("Note").with_field(FieldSpec::new("title", "string"));
    host.memory.specs.insert("Note".into(), note);
    host.memory
        .specs
        .insert("Document".into(), host.spec.clone());
    host.memory.others.insert(
        ("Note".into(), "n1".into()),
        Row {
            own: support::object(json!({"title": "n1"})),
            owned: Default::default(),
            version: 1,
        },
    );
    changed(host.create(&wes, "d1", json!({"title": "Plan"})));
    changed(host.create(&wes, "d2", json!({"title": "Brief"})));
    changed(host.update(&wes, "d2", json!({"body": "v2"})));
    let cited = result(host.invoke(
        &wes,
        "d1",
        USE,
        "propose",
        json!({"patch": {"body": "Cited."}, "evidence": [{"schema": "Note", "id": "n1"}, {"schema": "Document", "id": "d2", "revision": 2}]}),
    ));
    assert_eq!(
        cited["evidence"],
        json!([{"schema": "Note", "id": "n1"}, {"schema": "Document", "id": "d2", "revision": 2}])
    );
    let refusal = refused(host.invoke(
        &wes,
        "d1",
        USE,
        "propose",
        json!({"patch": {"body": "Again."}, "evidence": [
            {"schema": "Nope", "id": "n2"},
            {"schema": "Note", "id": "n9"},
            {"schema": "Note", "id": "n1", "revision": 1},
            {"schema": "Document", "id": "d2", "revision": 3}
        ]}),
    ));
    assert_eq!(
        refusal["details"]["issues"],
        json!([
            {"path": "/evidence/0/schema", "message": "Nope is not a type of this database"},
            {"path": "/evidence/1/id", "message": "Note n9 does not exist"},
            {"path": "/evidence/2/revision", "message": "Note does not compose Revisions, so Note n1 has no revision to cite"},
            {"path": "/evidence/3/revision", "message": "Document d2 has revisions 1 to 2, not 3"}
        ])
    );
}

#[test]
fn revisions_stays_on_a_table_with_rows_and_its_review_may_change() {
    let alice = caller("alice", &[]);
    let mut host = reviewed();
    changed(host.create(&alice, "d1", json!({"title": "Plan"})));
    let before = host.spec.clone();
    let publish = |host: &mut Host, after: &TypeSpec| {
        let command = Command::new(
            alice.clone(),
            support::START,
            Action::Publish {
                from: Some(before.clone()),
            },
        );
        host.core
            .drive(after, &command, &mut host.memory)
            .into_result()
    };
    let removed = publish(&mut host, &TypeSpec::new("Document")).expect_err("refused");
    assert_eq!(removed.code.as_str(), "incompatible_change");
    assert!(
        removed.message.ends_with(
            "behavior Revisions cannot be removed from type Document, which has instances: the revisions and proposals its instances have would stay behind with nothing to delete them"
        ),
        "{}",
        removed.message
    );
    assert!(publish(&mut host, &document(json!({}))).is_ok());
}
