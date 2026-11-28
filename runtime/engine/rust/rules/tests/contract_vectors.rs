//! The core's JSON contract as values (D103): one value of each type a
//! host's adapter writes or reads, every optional member present and every
//! variant of each enum, as serde writes them, in
//! `runtime/http/testdata/rules_contract.json`, which the TypeScript HTTP
//! runtime's copy of the contract (`runtime/http/typescript/src/rules.ts`,
//! its `Core*` types) is held to by `src/rules-contract.test.ts` there.
//!
//! Each value is built from the Rust types, so a member a struct gains is
//! a compile error here until its value is given; each enum's variants, as
//! serde names them, are read from the error of a variant it does not
//! know, so a variant an enum gains fails the test until its value is
//! given. A normal run fails when the committed file differs from what this
//! writes; `UPDATE_RULES_CONTRACT=1 cargo test -p superschematic-rules
//! --test contract_vectors` rewrites it.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{json, Map, Value};

use superschematic_rules::{
    Action, Anchor, Answer, Answered, Answers, Assurance, Caller, ChangeSet, Code, Command,
    DeadlineChange, FieldSpec, KeyedRow, LinkedRows, LockKey, OutboxEvent, Outcome, ReadRequest,
    Refusal, RelationSpec, Row, RowChange, RowImage, RowRef, RowWrite, SharedRow, SharedRowChange,
    SharedRowWrite, Signal, Step, SubRow, SubRowChange, SubRowWrite, TableRef, TypeSpec, UseSpec,
    WriteKind,
};

fn object(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(map) => map,
        other => panic!("not an object: {other}"),
    }
}

fn owned() -> BTreeMap<String, Map<String, Value>> {
    BTreeMap::from([("status".to_owned(), object(json!({"status": "draft"})))])
}

fn use_spec() -> UseSpec {
    UseSpec {
        behavior: "Workflow".to_owned(),
        config: Some(json!({"states": ["draft", "paid"]})),
        anchor: Anchor::Field("status".to_owned()),
    }
}

fn relation_spec() -> RelationSpec {
    RelationSpec {
        name: "project".to_owned(),
        type_name: "Project".to_owned(),
    }
}

fn field_spec() -> FieldSpec {
    FieldSpec {
        name: "projectId".to_owned(),
        type_name: "uuid".to_owned(),
        date_time: true,
        relation: Some(relation_spec()),
        schema: Some(json!({"type": "string"})),
    }
}

fn type_spec() -> TypeSpec {
    TypeSpec {
        name: "Order".to_owned(),
        uses: vec![use_spec()],
        fields: vec![field_spec()],
    }
}

fn caller() -> Caller {
    Caller {
        subject: "alice".to_owned(),
        permissions: vec!["orders.ship".to_owned()],
        stands_in: true,
        acting_for: Some("bob".to_owned()),
        kind: Some("agent".to_owned()),
        acting_for_kind: Some("user".to_owned()),
        agent: Some("triage".to_owned()),
        assurance: Some(Assurance {
            signed_in_at: Some(1000),
            user_verified: true,
        }),
    }
}

fn row_image() -> RowImage {
    RowImage {
        own: object(json!({"title": "Desk"})),
        owned: owned(),
    }
}

fn row_change() -> RowChange {
    RowChange {
        write: WriteKind::Update,
        at: 1000,
        before: Some(row_image()),
        after: Some(row_image()),
    }
}

fn actions() -> Vec<Action> {
    vec![
        Action::Define,
        Action::Publish {
            from: Some(type_spec()),
        },
        Action::Create {
            id: Some("o1".to_owned()),
            own: object(json!({"title": "Desk"})),
            params: object(json!({"status": {"to": "draft"}})),
        },
        Action::Update {
            key: "o1".to_owned(),
            patch: object(json!({"title": "Chair"})),
        },
        Action::Delete {
            key: "o1".to_owned(),
        },
        Action::Operation {
            key: "o1".to_owned(),
            use_id: "status".to_owned(),
            operation: "transition".to_owned(),
            params: json!({"to": "paid"}),
        },
        Action::TableOperation {
            use_id: "Numbering".to_owned(),
            operation: "reset".to_owned(),
            params: json!({"from": 1}),
        },
        Action::Deadline {
            key: "o1".to_owned(),
            use_id: "Timers".to_owned(),
            name: "respond".to_owned(),
            at: 2000,
            payload: json!({"attempt": 1}),
        },
        Action::Change {
            key: "o1".to_owned(),
            change: row_change(),
        },
        Action::Read {
            key: "o1".to_owned(),
        },
        Action::Signal {
            key: "o1".to_owned(),
            use_id: "Comments".to_owned(),
            name: "typing".to_owned(),
            data: json!({"draft": true}),
        },
        Action::NamespaceOperation {
            behavior: "Approvals".to_owned(),
            operation: "pending".to_owned(),
            params: json!({"limit": 10}),
        },
    ]
}

fn command() -> Command {
    Command::new(
        caller(),
        1000,
        Action::Delete {
            key: "o1".to_owned(),
        },
    )
    .with_request("r1")
    .with_preconditions(object(json!({"Lease": {"token": 3}})))
}

fn table_ref() -> TableRef {
    TableRef {
        use_id: "Approvals".to_owned(),
        table: "signOffs".to_owned(),
    }
}

fn row_request() -> ReadRequest {
    ReadRequest::Row {
        type_name: "Order".to_owned(),
        key: "o1".to_owned(),
    }
}

fn read_requests() -> Vec<ReadRequest> {
    vec![
        row_request(),
        ReadRequest::HasRows {
            type_name: "Order".to_owned(),
        },
        ReadRequest::Spec {
            type_name: "Order".to_owned(),
        },
        ReadRequest::SubRows {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            use_id: "Approvals".to_owned(),
            table: "signOffs".to_owned(),
        },
        ReadRequest::Linked {
            type_name: "Task".to_owned(),
            field: "projectId".to_owned(),
            key: "p1".to_owned(),
            after: Some("t1".to_owned()),
            limit: 500,
        },
        ReadRequest::Holding {
            type_name: "Job".to_owned(),
            use_id: "Lease".to_owned(),
            field: "holder".to_owned(),
            value: json!("alice"),
            after: Some("j1".to_owned()),
            limit: 500,
        },
        ReadRequest::Rows {
            type_name: "Order".to_owned(),
            equal: object(json!({"status": "open"})),
            tables: vec![table_ref()],
            after: Some("o1".to_owned()),
            limit: 500,
        },
        ReadRequest::SharedRow {
            type_name: "Order".to_owned(),
            use_id: "Numbering".to_owned(),
            table: "counts".to_owned(),
            key: "2026".to_owned(),
        },
        ReadRequest::Composing {
            behavior: "Approvals".to_owned(),
        },
        ReadRequest::Permissions {
            subjects: vec!["alice".to_owned(), "bob".to_owned()],
        },
    ]
}

fn row() -> Row {
    Row {
        own: object(json!({"title": "Desk"})),
        owned: owned(),
        version: 1,
    }
}

fn sub_row() -> SubRow {
    SubRow {
        key: "s1".to_owned(),
        values: object(json!({"by": "alice"})),
    }
}

fn keyed_row() -> KeyedRow {
    KeyedRow {
        key: "o1".to_owned(),
        row: row(),
        tables: BTreeMap::from([(
            "Approvals".to_owned(),
            BTreeMap::from([("signOffs".to_owned(), vec![sub_row()])]),
        )]),
    }
}

fn linked_rows() -> LinkedRows {
    LinkedRows {
        rows: vec![keyed_row()],
        more: true,
    }
}

fn shared_row() -> SharedRow {
    SharedRow {
        key: "2026".to_owned(),
        values: object(json!({"next": 7})),
        version: 3,
    }
}

fn answers_of_each_kind() -> Vec<Answer> {
    vec![
        Answer::Row(Some(row())),
        Answer::HasRows(true),
        Answer::Spec(Some(type_spec())),
        Answer::SubRows(vec![sub_row()]),
        Answer::Linked(linked_rows()),
        Answer::SharedRow(Some(shared_row())),
        Answer::Specs(vec![type_spec()]),
        Answer::Permissions(BTreeMap::from([(
            "alice".to_owned(),
            vec!["orders.ship".to_owned()],
        )])),
    ]
}

fn answered() -> Answered {
    Answered {
        request: row_request(),
        answer: Answer::Row(Some(row())),
        held: 2,
    }
}

fn answers() -> Answers {
    Answers {
        reads: vec![answered()],
        locks: vec![LockKey("Order:o1".to_owned())],
    }
}

fn codes() -> Vec<Code> {
    vec![
        Code::InvalidSchema,
        Code::IncompatibleChange,
        Code::NotFound,
        Code::InvalidArgument,
        Code::Forbidden,
        Code::InvalidInstance,
        Code::Conflict,
        Code::Vetoed,
        Code::Busy,
        Code::BehaviorError,
        Code::Internal,
    ]
}

fn refusal() -> Refusal {
    Refusal {
        code: Code::Vetoed,
        status: 409,
        class: "BehaviorVetoError".to_owned(),
        message: "the transition is not allowed".to_owned(),
        details: Some(Box::new(object(json!({"behavior": "Workflow"})))),
    }
}

fn row_ref() -> RowRef {
    RowRef {
        type_name: "Order".to_owned(),
        key: Some("o1".to_owned()),
    }
}

fn row_writes() -> Vec<RowWrite> {
    vec![
        RowWrite::Create {
            type_name: "Order".to_owned(),
            key: Some("o1".to_owned()),
            own: object(json!({"title": "Desk"})),
            owned: owned(),
        },
        RowWrite::Update {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            expected_version: 1,
            own: object(json!({"title": "Chair"})),
            owned: owned(),
        },
        RowWrite::Delete {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
            expected_version: 2,
        },
        RowWrite::Gone {
            type_name: "Order".to_owned(),
            key: "o1".to_owned(),
        },
    ]
}

fn sub_row_writes() -> Vec<SubRowWrite> {
    [
        SubRowChange::Insert {
            values: object(json!({"by": "alice"})),
        },
        SubRowChange::Update {
            values: object(json!({"by": "bob"})),
        },
        SubRowChange::Delete,
    ]
    .into_iter()
    .map(|change| SubRowWrite {
        row: row_ref(),
        use_id: "Approvals".to_owned(),
        table: "signOffs".to_owned(),
        key: "s1".to_owned(),
        change,
    })
    .collect()
}

fn shared_row_writes() -> Vec<SharedRowWrite> {
    [
        SharedRowChange::Insert {
            values: object(json!({"next": 1})),
        },
        SharedRowChange::Update {
            expected_version: 3,
            values: object(json!({"next": 8})),
        },
        SharedRowChange::Delete {
            expected_version: 4,
        },
    ]
    .into_iter()
    .map(|change| SharedRowWrite {
        type_name: "Order".to_owned(),
        use_id: "Numbering".to_owned(),
        table: "counts".to_owned(),
        key: "2026".to_owned(),
        change,
    })
    .collect()
}

fn outbox_event() -> OutboxEvent {
    OutboxEvent {
        row: row_ref(),
        kind: "approved".to_owned(),
        use_id: "Approvals".to_owned(),
        payload: json!({"by": "alice"}),
    }
}

fn deadline_changes() -> Vec<DeadlineChange> {
    vec![
        DeadlineChange::Arm {
            row: row_ref(),
            use_id: "Timers".to_owned(),
            name: "respond".to_owned(),
            at: 2000,
            payload: json!({"attempt": 1}),
        },
        DeadlineChange::Clear {
            row: row_ref(),
            use_id: "Timers".to_owned(),
            name: "respond".to_owned(),
        },
    ]
}

fn signal() -> Signal {
    Signal {
        row: row_ref(),
        use_id: "Comments".to_owned(),
        name: "typing".to_owned(),
        data: json!({"draft": true}),
    }
}

fn change_set() -> ChangeSet {
    ChangeSet {
        rows: vec![row_writes().remove(1)],
        sub_rows: vec![sub_row_writes().remove(0)],
        shared_rows: vec![shared_row_writes().remove(1)],
        events: vec![outbox_event()],
        deadlines: vec![deadline_changes().remove(0)],
        signals: vec![signal()],
        result: json!({"ok": true}),
    }
}

fn outcomes() -> Vec<Outcome> {
    vec![
        Outcome::Refused(refusal()),
        Outcome::Changed(Box::new(change_set())),
    ]
}

fn steps() -> Vec<Step> {
    vec![
        Step::Read(vec![row_request()]),
        Step::Lock(vec![LockKey("Order:o1".to_owned())]),
        Step::Done(Outcome::Refused(refusal())),
    ]
}

fn values<T: Serialize>(values: &[T]) -> Value {
    serde_json::to_value(values).expect("a contract value serializes")
}

/// The values, by the name of the TypeScript type that copies each Rust type.
fn contract() -> Map<String, Value> {
    let mut out = Map::new();
    let mut put = |name: &str, value: Value| {
        out.insert(name.to_owned(), value);
    };
    put(
        "CoreAnchor",
        values(&[Anchor::Type, Anchor::Field("status".to_owned())]),
    );
    put("CoreUseSpec", values(&[use_spec()]));
    put("CoreFieldSpec", values(&[field_spec()]));
    put("CoreRelationSpec", values(&[relation_spec()]));
    put("CoreTypeSpec", values(&[type_spec()]));
    put("CoreCaller", values(&[caller()]));
    put("CoreAction", values(&actions()));
    put(
        "CoreWriteKind",
        values(&[WriteKind::Insert, WriteKind::Update, WriteKind::Delete]),
    );
    put("CoreRowImage", values(&[row_image()]));
    put("CoreRowChange", values(&[row_change()]));
    put("CoreCommand", values(&[command()]));
    put("CoreReadRequest", values(&read_requests()));
    put("CoreTableRef", values(&[table_ref()]));
    put("CoreRow", values(&[row()]));
    put("CoreSubRow", values(&[sub_row()]));
    put("CoreKeyedRow", values(&[keyed_row()]));
    put("CoreLinkedRows", values(&[linked_rows()]));
    put("CoreSharedRow", values(&[shared_row()]));
    put("CoreAnswer", values(&answers_of_each_kind()));
    put("CoreAnswered", values(&[answered()]));
    put("CoreAnswers", values(&[answers()]));
    put("CoreStep", values(&steps()));
    put("CoreOutcome", values(&outcomes()));
    put("CoreCode", values(&codes()));
    put("CoreRefusal", values(&[refusal()]));
    put(
        "CoreRowRef",
        values(&[
            row_ref(),
            RowRef {
                type_name: "Order".to_owned(),
                key: None,
            },
        ]),
    );
    put("CoreRowWrite", values(&row_writes()));
    put("CoreSubRowWrite", values(&sub_row_writes()));
    put("CoreSharedRowWrite", values(&shared_row_writes()));
    put("CoreOutboxEvent", values(&[outbox_event()]));
    put("CoreDeadlineChange", values(&deadline_changes()));
    put("CoreSignal", values(&[signal()]));
    put("CoreChangeSet", values(&[change_set()]));
    out
}

/// The variants of T as serde names them: what the error of a variant it
/// does not know expects, `a`, `a` or `b`, or one of `a`, `b`, `c`.
fn variants<T: DeserializeOwned + std::fmt::Debug>(unknown: Value) -> BTreeSet<String> {
    let error = serde_json::from_value::<T>(unknown).expect_err("an unknown variant is refused");
    let message = error.to_string();
    let expected = message
        .split_once("expected ")
        .unwrap_or_else(|| panic!("no expected variants in {message:?}"))
        .1;
    expected
        .split('`')
        .skip(1)
        .step_by(2)
        .map(str::to_owned)
        .collect()
}

/// The variant each value is: its kind, its one member, or the string it is.
fn tags(values: &Value, tagged: bool) -> BTreeSet<String> {
    values
        .as_array()
        .expect("a list of values")
        .iter()
        .map(|value| match value {
            Value::String(text) => text.clone(),
            Value::Object(map) if tagged => map["kind"].as_str().expect("a kind").to_owned(),
            Value::Object(map) if map.len() == 1 => map.keys().next().expect("a member").clone(),
            other => panic!("no variant in {other}"),
        })
        .collect()
}

#[test]
fn every_variant_has_a_value() {
    let contract = contract();
    let tagged = json!({"kind": "-unknown-"});
    let external = json!({"-unknown-": null});
    let text = json!("-unknown-");
    let cases = [
        ("CoreAction", variants::<Action>(tagged.clone()), true),
        (
            "CoreReadRequest",
            variants::<ReadRequest>(tagged.clone()),
            true,
        ),
        ("CoreRowWrite", variants::<RowWrite>(tagged.clone()), true),
        (
            "CoreDeadlineChange",
            variants::<DeadlineChange>(tagged.clone()),
            true,
        ),
        ("CoreAnswer", variants::<Answer>(external.clone()), false),
        ("CoreStep", variants::<Step>(external.clone()), false),
        ("CoreOutcome", variants::<Outcome>(external), false),
        ("CoreCode", variants::<Code>(text.clone()), false),
        ("CoreWriteKind", variants::<WriteKind>(text.clone()), false),
    ];
    for (name, want, tagged) in cases {
        assert_eq!(tags(&contract[name], tagged), want, "{name}");
    }
    // Anchor's field variant is an object of its one member, its type a string.
    let anchors: BTreeSet<String> = contract["CoreAnchor"]
        .as_array()
        .expect("a list of anchors")
        .iter()
        .map(|value| match value {
            Value::String(text) => text.clone(),
            Value::Object(map) => map.keys().next().expect("a member").clone(),
            other => panic!("no anchor in {other}"),
        })
        .collect();
    assert_eq!(anchors, variants::<Anchor>(text), "CoreAnchor");
    let changes = |name: &str| -> Value {
        Value::Array(
            contract[name]
                .as_array()
                .expect("a list of writes")
                .iter()
                .map(|write| write["change"].clone())
                .collect(),
        )
    };
    assert_eq!(
        tags(&changes("CoreSubRowWrite"), true),
        variants::<SubRowChange>(tagged.clone()),
        "CoreSubRowWrite's change"
    );
    assert_eq!(
        tags(&changes("CoreSharedRowWrite"), true),
        variants::<SharedRowChange>(tagged),
        "CoreSharedRowWrite's change"
    );
}

fn fixture_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../http/testdata/rules_contract.json")
}

#[test]
fn the_fixture_is_current() {
    let document = json!({
        "comment": "The behaviors' core's JSON contract as values (D103): one value of each type a host's adapter writes or reads, by the name of its TypeScript copy in runtime/http/typescript/src/rules.ts, every optional member present and every variant of each enum, as superschematic-rules (runtime/engine/rust/rules) writes them. runtime/http/typescript's src/rules-contract.test.ts holds its Core* types to them. UPDATE_RULES_CONTRACT=1 cargo test -p superschematic-rules --test contract_vectors writes it.",
        "values": contract(),
    });
    let want = serde_json::to_string_pretty(&document).expect("the document serializes") + "\n";
    let path = fixture_path();
    if std::env::var("UPDATE_RULES_CONTRACT").is_ok_and(|value| value == "1") {
        std::fs::write(&path, &want).expect("the fixture is written");
        return;
    }
    let have = std::fs::read_to_string(&path).unwrap_or_default();
    assert!(
        have == want,
        "{} is stale: run UPDATE_RULES_CONTRACT=1 cargo test -p superschematic-rules --test contract_vectors",
        path.display()
    );
}
