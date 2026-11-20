//! The handler over the in-memory store (D117's development mode): define
//! and publish, the instance requests, a behavior's operations, a list's
//! filters, sorts and pages, unique fields and relations as a table answers
//! them, and a publish with no compatibility rule that refuses the rows it
//! no longer describes unless it discards them.

mod common;

use common::{admin, object, Host};
use serde_json::{json, Map, Value};
use superschematic_rules::{Caller, Code};
use superschematic_rules_handler::filter::Sort;
use superschematic_rules_handler::Request;

const HOUR: i64 = 3_600_000;

fn order() -> Value {
    json!({
        "kind": "General",
        "name": "Order",
        "types": {"Order": {
            "name": "Order",
            "behaviors": [{"name": "Workflow", "field": "status", "config": {
                "states": ["draft", "paid", "shipped"],
                "transitions": [{"from": "draft", "to": "paid"}, {"from": "paid", "to": "shipped", "permission": "orders.ship"}]
            }}],
            "fields": [
                {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 40},
                {"name": "quantity", "typeRef": {"name": "Generic.Int64"}, "validateMin": 1},
                {"name": "placedAt", "typeRef": {"name": "Temporal.DateTime"}},
                {"name": "reference", "typeRef": {"name": "string"}, "unique": true}
            ]
        }}
    })
}

fn transition(id: &str, to: &str) -> Request {
    Request::Operation {
        type_name: "Order".into(),
        id: id.into(),
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({"to": to}),
        preconditions: Map::new(),
    }
}

#[test]
fn a_type_is_defined_published_and_its_rows_written_read_listed_and_deleted() {
    let mut host = Host::new();
    let defined = host.ok(&Request::Define { document: order() });
    assert_eq!(defined, json!({"type": "Order", "defined": true}));
    // A draft serves nothing until it is published.
    assert_eq!(
        host.create("Order", json!({"title": "Desk"}))
            .unwrap_err()
            .code,
        Code::NotFound
    );
    let published = host.ok(&Request::Publish {
        type_name: "Order".into(),
        discard: false,
    });
    assert_eq!(
        published,
        json!({"type": "Order", "version": 1, "rewritten": 0, "discarded": []})
    );

    let created = host
        .create(
            "Order",
            json!({"title": "Desk", "quantity": 2, "placedAt": "2026-02-01T01:00:00+01:00"}),
        )
        .expect("created");
    let id = created["id"].as_str().expect("an id").to_owned();
    assert!(id.len() <= 22, "the key is a UUID's base62 form: {id}");
    assert_eq!(created["status"], "draft");
    assert_eq!(created["statusEnteredAt"], "1970-01-01T00:16:40.000Z");
    assert_eq!(created["placedAt"], "2026-02-01T00:00:00.000Z");
    assert_eq!(created["_version"], 1);

    let updated = host
        .update("Order", &id, json!({"quantity": 3, "placedAt": null}))
        .expect("updated");
    assert_eq!(updated["quantity"], 3);
    assert!(updated.get("placedAt").is_none(), "a null clears a field");
    assert_eq!(updated["_version"], 2);
    assert_eq!(host.get("Order", &id).expect("read"), updated);

    let moved = host.ok(&transition(&id, "paid"));
    assert_eq!(moved["from"], "draft");
    assert_eq!(moved["to"], "paid");
    assert_eq!(host.get("Order", &id).expect("read")["status"], "paid");

    host.create("Order", json!({"title": "Chair"}))
        .expect("created");
    assert_eq!(
        host.list("Order", json!({"status": {"eq": "paid"}})),
        ["Desk"]
    );

    let deleted = host.ok(&Request::Delete {
        type_name: "Order".into(),
        id: id.clone(),
        preconditions: Map::new(),
    });
    assert_eq!(deleted, json!({"deleted": [{"type": "Order", "id": id}]}));
    assert_eq!(host.get("Order", &id).unwrap_err().code, Code::NotFound);
    assert_eq!(host.list("Order", Value::Null), ["Chair"]);
}

#[test]
fn describe_gives_the_type_as_development_mode_runs_it() {
    let mut host = Host::new();
    host.publish(order());
    let described = host.ok(&Request::Describe {
        type_name: "Order".into(),
    });
    assert_eq!(described["mode"], "development");
    assert_eq!(described["version"], 1);
    assert_eq!(
        described["key"],
        json!({"name": "id", "type": "Identity.UUID", "auto": true})
    );
    let owned: Vec<&str> = described["owned"]
        .as_array()
        .expect("owned")
        .iter()
        .map(|owned| owned["name"].as_str().unwrap_or_default())
        .collect();
    assert_eq!(owned, ["status", "statusEnteredAt"]);
    assert_eq!(described["uses"][0]["operations"][0]["name"], "transition");
    assert_eq!(
        described["fields"][0]["filter"],
        json!(["eq", "ne", "in", "notIn", "ilike", "isNull"])
    );
    assert_eq!(described["uniques"], json!([["reference"]]));
}

#[test]
fn a_behavior_operation_runs_through_the_core_with_its_permissions() {
    let mut host = Host::new();
    host.publish(order());
    let id = host
        .create("Order", json!({"title": "Desk"}))
        .expect("created")["id"]
        .as_str()
        .expect("an id")
        .to_owned();
    let vetoed = host.refused(&transition(&id, "shipped"));
    assert_eq!(vetoed.code, Code::Vetoed);
    host.ok(&transition(&id, "paid"));
    let clerk = Caller::new("clerk", ["orders.read"]);
    let forbidden = host.as_(&clerk, &transition(&id, "shipped")).unwrap_err();
    assert_eq!(forbidden.code, Code::Forbidden);
    let shipper = Caller::new("sam", ["orders.ship"]);
    host.as_(&shipper, &transition(&id, "shipped"))
        .expect("shipped");
    assert_eq!(host.get("Order", &id).expect("read")["status"], "shipped");
    let unknown = host.refused(&Request::Operation {
        type_name: "Order".into(),
        id: id.clone(),
        use_id: "status".into(),
        operation: "teleport".into(),
        params: Value::Null,
        preconditions: Map::new(),
    });
    assert_eq!(unknown.code, Code::NotFound);
}

#[test]
fn a_write_is_held_to_its_fields_and_refuses_an_owned_one() {
    let mut host = Host::new();
    host.publish(order());
    let refused = host
        .create(
            "Order",
            json!({"quantity": 0, "status": "paid", "colour": "red", "placedAt": "soon"}),
        )
        .unwrap_err();
    assert_eq!(refused.code, Code::InvalidInstance);
    let rules: Vec<(String, String)> = refused.details.as_ref().expect("details")["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            (
                issue["path"].as_str().unwrap_or_default().to_owned(),
                issue["rule"].as_str().unwrap_or_default().to_owned(),
            )
        })
        .collect();
    assert_eq!(
        rules,
        [
            ("/status".to_owned(), "owned".to_owned()),
            ("/colour".to_owned(), "unknown_field".to_owned()),
            ("/title".to_owned(), "required".to_owned()),
            ("/quantity".to_owned(), "minimum".to_owned()),
            ("/placedAt".to_owned(), "format".to_owned()),
        ]
    );
    let id = host
        .create("Order", json!({"title": "Desk"}))
        .expect("created")["id"]
        .as_str()
        .expect("an id")
        .to_owned();
    let cleared = host
        .update("Order", &id, json!({"title": null}))
        .unwrap_err();
    assert_eq!(cleared.code, Code::InvalidInstance);
    let owned = host
        .update("Order", &id, json!({"statusEnteredAt": 5}))
        .unwrap_err();
    assert_eq!(owned.code, Code::InvalidInstance);
    assert_eq!(
        host.get("Order", &id).expect("read")["_version"],
        1,
        "nothing written"
    );
}

#[test]
fn a_unique_field_refuses_a_second_holder_as_a_unique_index_does() {
    let mut host = Host::new();
    let mut document = order();
    document["types"]["Order"]["indexes"] =
        json!([{"keys": ["title", "quantity"], "unique": true}]);
    host.publish(document);
    let first = host
        .create("Order", json!({"title": "Desk", "reference": "r1"}))
        .expect("created");
    let taken = host
        .create("Order", json!({"title": "Lamp", "reference": "r1"}))
        .unwrap_err();
    assert_eq!(taken.code, Code::Conflict);
    assert_eq!(
        taken.details.as_ref().expect("details")["holder"],
        first["id"]
    );
    // A field with no value holds no unique key, as NULLs are distinct.
    host.create("Order", json!({"title": "Lamp"}))
        .expect("created");
    host.create("Order", json!({"title": "Lamp"}))
        .expect("a second with no quantity");
    host.create("Order", json!({"title": "Lamp", "quantity": 2}))
        .expect("created");
    let pair = host
        .create("Order", json!({"title": "Lamp", "quantity": 2}))
        .unwrap_err();
    assert_eq!(pair.code, Code::Conflict);
    let id = first["id"].as_str().expect("an id");
    host.update("Order", id, json!({"reference": "r1"}))
        .expect("its own value is no conflict");
    let lamp = host
        .create("Order", json!({"title": "Shade", "reference": "r2"}))
        .expect("created");
    let moved = host
        .update(
            "Order",
            lamp["id"].as_str().expect("an id"),
            json!({"reference": "r1"}),
        )
        .unwrap_err();
    assert_eq!(moved.code, Code::Conflict);
}

fn project() -> Value {
    json!({"name": "Project", "fields": [
        {"name": "id", "typeRef": {"name": "string"}, "key": true},
        {"name": "name", "typeRef": {"name": "string"}, "required": true}
    ]})
}

fn task(on_delete: &str) -> Value {
    json!({"name": "Task", "fields": [
        {"name": "title", "typeRef": {"name": "string"}, "required": true},
        {"name": "points", "typeRef": {"name": "Generic.Int64"}},
        {"name": "project", "typeRef": {"name": "Project"}, "relation": {"type": "Project", "onDelete": on_delete}}
    ]})
}

#[test]
fn a_relation_refuses_an_id_no_row_has_and_its_targets_delete_cascades() {
    let mut host = Host::new();
    // A relation to a type with no published version is refused at publish.
    host.ok(&Request::Define { document: task("") });
    let unpublished = host.refused(&Request::Publish {
        type_name: "Task".into(),
        discard: false,
    });
    assert_eq!(unpublished.code, Code::InvalidSchema);
    host.publish(project());
    host.publish(task(""));
    host.create("Project", json!({"id": "p1", "name": "Launch"}))
        .expect("created");
    let dangling = host
        .create("Task", json!({"title": "Write", "projectId": "p9"}))
        .unwrap_err();
    assert_eq!(dangling.code, Code::InvalidInstance);
    assert!(
        dangling
            .message
            .contains("Project p9, which does not exist"),
        "{}",
        dangling.message
    );
    let write = host
        .create("Task", json!({"title": "Write", "projectId": "p1"}))
        .expect("created");
    host.create("Task", json!({"title": "Loose"}))
        .expect("a relation may hold none");
    let deleted = host.ok(&Request::Delete {
        type_name: "Project".into(),
        id: "p1".into(),
        preconditions: Map::new(),
    });
    assert_eq!(
        deleted,
        json!({"deleted": [{"type": "Project", "id": "p1"}, {"type": "Task", "id": write["id"]}]})
    );
    assert_eq!(host.list("Task", Value::Null), ["Loose"]);
}

#[test]
fn a_relation_that_sets_null_or_restricts_answers_its_targets_delete_so() {
    let mut host = Host::new();
    host.publish(project());
    host.publish(task("SET NULL"));
    host.create("Project", json!({"id": "p1", "name": "Launch"}))
        .expect("created");
    let write = host
        .create("Task", json!({"title": "Write", "projectId": "p1"}))
        .expect("created");
    host.ok(&Request::Delete {
        type_name: "Project".into(),
        id: "p1".into(),
        preconditions: Map::new(),
    });
    let kept = host
        .get("Task", write["id"].as_str().expect("an id"))
        .expect("kept");
    assert!(kept.get("projectId").is_none());
    assert_eq!(kept["_version"], 2, "a SET NULL is an update");

    host.publish(task("RESTRICT"));
    host.create("Project", json!({"id": "p2", "name": "Ship"}))
        .expect("created");
    host.create("Task", json!({"title": "Pack", "projectId": "p2"}))
        .expect("created");
    let restricted = host.refused(&Request::Delete {
        type_name: "Project".into(),
        id: "p2".into(),
        preconditions: Map::new(),
    });
    assert_eq!(restricted.code, Code::Conflict);
    assert!(host.get("Project", "p2").is_ok(), "nothing deleted");
}

#[test]
fn a_list_filters_sorts_and_pages_as_a_tables_orm_does() {
    let mut host = Host::new();
    host.publish(project());
    host.publish(task(""));
    host.create("Project", json!({"id": "p1", "name": "Alpha"}))
        .expect("created");
    host.create("Project", json!({"id": "p2", "name": "Beta"}))
        .expect("created");
    for (title, points, project) in [
        ("a", Some(3), "p1"),
        ("b", None, "p2"),
        ("c", Some(1), "p1"),
        ("d", Some(3), "p2"),
        ("e", Some(8), "p2"),
    ] {
        let mut data = json!({"title": title, "projectId": project});
        if let Some(points) = points {
            data["points"] = json!(points);
        }
        host.create("Task", data).expect("created");
    }
    assert_eq!(
        host.list(
            "Task",
            json!({"project": {"name": {"eq": "Beta"}}, "points": {"lt": 8}})
        ),
        ["d"]
    );
    assert_eq!(
        host.list("Task", json!({"points": {"ne": 3}})).len(),
        3,
        "ne keeps no value"
    );
    let page = |host: &mut Host, cursor: Option<String>| {
        host.ok(&Request::List {
            type_name: "Task".into(),
            filter: Value::Null,
            sort: vec![Sort {
                field: "points".into(),
                desc: true,
            }],
            limit: Some(2),
            cursor,
        })
    };
    let first = page(&mut host, None);
    let titles = |page: &Value| -> Vec<String> {
        page["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["title"].as_str().unwrap_or_default().to_owned())
            .collect()
    };
    let mut seen = titles(&first);
    let mut next = first["next"].as_str().map(str::to_owned);
    while let Some(cursor) = next {
        let more = page(&mut host, Some(cursor));
        seen.extend(titles(&more));
        next = more["next"].as_str().map(str::to_owned);
    }
    // Points descending, no value last, ties by key (in the first term's
    // direction).
    assert_eq!(&seen[0], "e");
    assert_eq!(seen.len(), 5);
    assert_eq!(&seen[3], "c");
    assert_eq!(&seen[4], "b");
    let wrong = host.refused(&Request::List {
        type_name: "Task".into(),
        filter: json!({"title": {"gt": "a"}}),
        sort: Vec::new(),
        limit: None,
        cursor: None,
    });
    assert_eq!(wrong.code, Code::InvalidArgument);
}

fn note(fields: Value) -> Value {
    json!({"name": "Note", "fields": fields})
}

#[test]
fn a_publish_renames_retypes_removes_and_tightens_with_no_compatibility_rule() {
    let mut host = Host::new();
    host.publish(note(json!([
        {"name": "title", "typeRef": {"name": "string"}, "required": true},
        {"name": "size", "typeRef": {"name": "Generic.Int64"}},
        {"name": "body", "typeRef": {"name": "string"}}
    ])));
    let short = host
        .create("Note", json!({"title": "Hi", "size": 1, "body": "x"}))
        .expect("created");
    let long = host
        .create("Note", json!({"title": "A long title", "body": "y"}))
        .expect("created");

    // A removal and a rename: body's values go with it, and the rows are
    // rewritten at their versions.
    let renamed = host.publish(note(json!([
        {"name": "title", "typeRef": {"name": "string"}, "required": true},
        {"name": "size", "typeRef": {"name": "Generic.Int64"}},
        {"name": "text", "typeRef": {"name": "string"}}
    ])));
    assert_eq!(renamed["version"], 2);
    assert_eq!(renamed["rewritten"], 2);
    let read = host
        .get("Note", short["id"].as_str().expect("an id"))
        .expect("read");
    assert_eq!(
        read,
        json!({"id": short["id"], "title": "Hi", "size": 1, "_version": 1})
    );

    // A retype the rows satisfy publishes; a tightening they do not is
    // refused, listing each row with what it fails, and changes nothing.
    host.publish(note(json!([
        {"name": "title", "typeRef": {"name": "string"}, "required": true},
        {"name": "size", "typeRef": {"name": "number"}}
    ])));
    host.ok(&Request::Define { document: note(json!([
        {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 5},
        {"name": "size", "typeRef": {"name": "string"}},
        {"name": "rank", "typeRef": {"name": "Generic.Int64"}, "required": true}
    ])) });
    let refused = host.refused(&Request::Publish {
        type_name: "Note".into(),
        discard: false,
    });
    assert_eq!(refused.code, Code::Conflict);
    assert_eq!(refused.class, "StoredRowsError");
    let details = refused.details.as_ref().expect("details");
    assert_eq!(details["version"], 4);
    let rows = details["rows"].as_array().expect("rows");
    assert_eq!(rows.len(), 2);
    let rules = |row: &Value| -> Vec<String> {
        row["issues"]
            .as_array()
            .expect("issues")
            .iter()
            .map(|issue| {
                format!(
                    "{} {}",
                    issue["path"].as_str().unwrap_or_default(),
                    issue["rule"].as_str().unwrap_or_default()
                )
            })
            .collect()
    };
    let of = |id: &Value| rows.iter().find(|row| row["id"] == *id).expect("listed");
    assert_eq!(rules(of(&short["id"])), ["/size type", "/rank required"]);
    assert_eq!(
        rules(of(&long["id"])),
        ["/title maxLength", "/rank required"]
    );
    assert_eq!(
        host.ok(&Request::Describe {
            type_name: "Note".into()
        })["version"],
        3,
        "nothing published"
    );

    // Discarded, they go, in the publish's transaction.
    let discarded = host.ok(&Request::Publish {
        type_name: "Note".into(),
        discard: true,
    });
    assert_eq!(discarded["version"], 4);
    assert_eq!(discarded["discarded"].as_array().map(Vec::len), Some(2));
    assert_eq!(host.list("Note", Value::Null).len(), 0);
    host.create("Note", json!({"title": "Ok", "size": "s", "rank": 1}))
        .expect("the new version serves");
}

#[test]
fn a_publish_that_adds_a_unique_field_fails_the_later_holders() {
    let mut host = Host::new();
    let fields =
        |unique: bool| json!([{"name": "title", "typeRef": {"name": "string"}, "unique": unique}]);
    host.publish(note(fields(false)));
    let a = host
        .create("Note", json!({"title": "same"}))
        .expect("created");
    let b = host
        .create("Note", json!({"title": "same"}))
        .expect("created");
    host.create("Note", json!({"title": "other"}))
        .expect("created");
    host.ok(&Request::Define {
        document: note(fields(true)),
    });
    let refused = host.refused(&Request::Publish {
        type_name: "Note".into(),
        discard: false,
    });
    let rows = refused.details.as_ref().expect("details")["rows"]
        .as_array()
        .cloned()
        .expect("rows");
    assert_eq!(rows.len(), 1);
    let (first, later) = if a["id"].as_str() < b["id"].as_str() {
        (&a, &b)
    } else {
        (&b, &a)
    };
    assert_eq!(rows[0]["id"], later["id"]);
    host.ok(&Request::Publish {
        type_name: "Note".into(),
        discard: true,
    });
    assert!(host
        .get("Note", first["id"].as_str().expect("an id"))
        .is_ok());
    assert_eq!(
        host.get("Note", later["id"].as_str().expect("an id"))
            .unwrap_err()
            .code,
        Code::NotFound
    );
}

fn ticket(behaviors: Value) -> Value {
    json!({"name": "Ticket", "behaviors": behaviors, "fields": [{"name": "title", "typeRef": {"name": "string"}}]})
}

fn ticket_flow() -> Value {
    json!({"name": "Workflow", "field": "status", "config": {
        "states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]
    }})
}

#[test]
fn a_publish_that_adds_a_use_fails_rows_without_its_value_and_removing_one_drops_its_state() {
    let mut host = Host::new();
    host.publish(ticket(json!([])));
    host.create("Ticket", json!({"title": "old"}))
        .expect("created");
    host.ok(&Request::Define {
        document: ticket(json!([ticket_flow()])),
    });
    let refused = host.refused(&Request::Publish {
        type_name: "Ticket".into(),
        discard: false,
    });
    assert!(
        refused.message.contains("status is Workflow's value"),
        "{}",
        refused.message
    );
    host.ok(&Request::Publish {
        type_name: "Ticket".into(),
        discard: true,
    });

    // Timers on the type arms a deadline per row; removing it drops the
    // sub-table rows and the deadline with the use.
    host.publish(ticket(json!([
        ticket_flow(),
        {"name": "Timers", "config": {"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "closed"}}}}}
    ])));
    let created = host
        .create("Ticket", json!({"title": "new"}))
        .expect("created");
    let id = created["id"].as_str().expect("an id").to_owned();
    assert_eq!(host.store.armed("Ticket", &id).len(), 1);
    assert_eq!(
        host.store
            .sub_rows("Ticket", &id, "Timers", "deadlines")
            .len(),
        1
    );
    host.publish(ticket(json!([ticket_flow()])));
    assert!(host.store.armed("Ticket", &id).is_empty());
    assert!(host
        .store
        .sub_rows("Ticket", &id, "Timers", "deadlines")
        .is_empty());
    assert_eq!(host.get("Ticket", &id).expect("kept")["status"], "open");
}

fn invoice(behaviors: Value) -> Value {
    json!({"name": "Invoice", "behaviors": behaviors, "fields": [{"name": "title", "typeRef": {"name": "string"}}]})
}

fn numbered() -> Value {
    json!([{"name": "Numbering", "field": "number", "config": {"format": "INV-{seq:3}"}}])
}

#[test]
fn numbering_counts_in_the_layouts_shared_table_and_a_delete_leaves_a_gap() {
    let mut host = Host::new();
    host.publish(invoice(numbered()));
    let described = host.ok(&Request::Describe {
        type_name: "Invoice".into(),
    });
    assert_eq!(described["uses"][0]["tables"][0]["name"], "counts");
    assert_eq!(described["uses"][0]["tables"][0]["scope"], "schema");
    let number = |host: &mut Host, title: &str| -> (String, Value) {
        let created = host
            .create("Invoice", json!({"title": title}))
            .expect("created");
        (
            created["id"].as_str().expect("an id").to_owned(),
            created["number"].clone(),
        )
    };
    assert_eq!(number(&mut host, "a").1, "INV-001");
    let (second, label) = number(&mut host, "b");
    assert_eq!(label, "INV-002");
    // The count is the type's as a whole: one row of the shared table, its
    // version moved by each write of it.
    let count = host
        .store
        .shared_row("Invoice", "number", "counts", "*")
        .expect("the count is held")
        .clone();
    assert_eq!((count.values["last"].clone(), count.version), (json!(2), 2));
    host.ok(&Request::Delete {
        type_name: "Invoice".into(),
        id: second,
        preconditions: Map::new(),
    });
    assert_eq!(number(&mut host, "c").1, "INV-003", "a delete leaves a gap");

    // A publish that removes the use drops its counts with its state, so a
    // use added again counts from 1.
    host.publish(invoice(json!([])));
    assert!(host
        .store
        .shared_row("Invoice", "number", "counts", "*")
        .is_none());
    host.publish(invoice(numbered()));
    assert_eq!(number(&mut host, "d").1, "INV-001");
}

fn policy() -> Value {
    json!({"name": "Policy",
    "behaviors": [{"name": "Revisions", "config": {"review": {"permission": "policies.review"}}}],
    "fields": [
        {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 10},
        {"name": "body", "typeRef": {"name": "string"}},
        {"name": "code", "typeRef": {"name": "string"}, "unique": true}
    ]})
}

fn revisions(id: &str, operation: &str, params: Value) -> Request {
    Request::Operation {
        type_name: "Policy".into(),
        id: id.into(),
        use_id: "Revisions".into(),
        operation: operation.into(),
        params,
        preconditions: Map::new(),
    }
}

#[test]
fn an_approval_writes_the_rows_own_fields_held_to_its_type() {
    let mut host = Host::new();
    host.publish(policy());
    let created = host
        .create("Policy", json!({"title": "Travel", "body": "Any class."}))
        .expect("created");
    let id = created["id"].as_str().expect("an id").to_owned();
    let reviewer = Caller::new("rita", ["policies"]);
    host.ok(&revisions(
        &id,
        "propose",
        json!({"patch": {"body": "Economy only."}}),
    ));
    let approved = host
        .as_(
            &reviewer,
            &revisions(&id, "approve", json!({"proposal": 1})),
        )
        .expect("approved");
    assert_eq!(approved["state"], "approved");
    // The operation's write of the row's own fields is the row's, and the
    // revision it made is kept beside the first.
    let row = host.get("Policy", &id).expect("read");
    assert_eq!(row["body"], "Economy only.");
    assert_eq!(row["title"], "Travel");
    assert_eq!(row["_version"], 3);
    assert_eq!(
        host.store
            .sub_rows("Policy", &id, "Revisions", "revisions")
            .len(),
        2
    );

    // A patch the core takes but the type refuses (too long a title, a
    // field it lacks, a unique code another row holds) is kept as a
    // proposal, and its approval is refused as a table's columns and its
    // unique index refuse the write, changing nothing.
    host.create("Policy", json!({"title": "Leave", "code": "P2"}))
        .expect("created");
    for (n, (patch, code)) in [
        (json!({"title": "A title past ten"}), Code::InvalidInstance),
        (json!({"subtitle": "none"}), Code::InvalidInstance),
        (json!({"code": "P2"}), Code::Conflict),
    ]
    .into_iter()
    .enumerate()
    {
        host.ok(&revisions(&id, "propose", json!({ "patch": patch })));
        let refused = host
            .as_(
                &reviewer,
                &revisions(&id, "approve", json!({"proposal": n + 2})),
            )
            .unwrap_err();
        assert_eq!(refused.code, code, "{}", refused.message);
    }
    let kept = host.get("Policy", &id).expect("read");
    assert_eq!(kept["title"], "Travel");
    assert!(kept.get("code").is_none());
    assert_eq!(kept["_version"], row["_version"].as_i64().unwrap() + 3);
}

#[test]
fn a_deadline_falls_due_through_the_runner() {
    let mut host = Host::new();
    host.publish(ticket(json!([
        ticket_flow(),
        {"name": "Timers", "config": {"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR, "then": {"transition": "closed"}}}}}
    ])));
    let id = host
        .create("Ticket", json!({"title": "t"}))
        .expect("created")["id"]
        .as_str()
        .expect("an id")
        .to_owned();
    // The create's write is heard (Timers hears its row's writes): the
    // runner runs it as a change command first, which changes nothing.
    let ran = host.run_due();
    assert!(ran.iter().all(|ran| ran.outcome.is_ok()), "{ran:?}");
    assert!(ran
        .iter()
        .all(|ran| matches!(ran.request, Request::Change { .. })));
    assert_eq!(host.get("Ticket", &id).expect("read")["status"], "open");
    host.now += HOUR;
    let ran = host.run_due();
    assert!(
        ran.iter()
            .any(|ran| matches!(ran.request, Request::Deadline { .. }) && ran.outcome.is_ok()),
        "{ran:?}"
    );
    assert_eq!(host.get("Ticket", &id).expect("read")["status"], "closed");
    assert!(host.store.armed("Ticket", &id).is_empty());
    assert!(host.run_due().is_empty(), "nothing is left to run");
}

fn reactions_project() -> Value {
    json!({"name": "Project",
        "behaviors": [{"name": "Workflow", "field": "status", "config": {
            "states": ["planned", "active", "done"],
            "transitions": [{"from": "planned", "to": "active"}, {"from": "active", "to": "done"}]
        }}],
        "fields": [{"name": "id", "typeRef": {"name": "string"}, "key": true}, {"name": "name", "typeRef": {"name": "string"}}]})
}

fn reactions_task() -> Value {
    json!({"name": "Task",
        "behaviors": [
            {"name": "Reactions", "config": {"rules": [{"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}}]}},
            {"name": "Workflow", "field": "status", "config": {
                "states": ["todo", "doing", "done"],
                "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]
            }}
        ],
        "fields": [{"name": "title", "typeRef": {"name": "string"}}, {"name": "project", "typeRef": {"name": "Project"}, "relation": {"type": "Project"}}]})
}

#[test]
fn reactions_hear_a_write_through_the_change_queue() {
    let mut host = Host::new();
    host.publish(reactions_project());
    host.publish(reactions_task());
    host.create("Project", json!({"id": "p1", "name": "Launch"}))
        .expect("created");
    let id = host
        .create("Task", json!({"title": "Write", "projectId": "p1"}))
        .expect("created")["id"]
        .as_str()
        .expect("an id")
        .to_owned();
    host.run_due();
    host.ok(&Request::Operation {
        type_name: "Task".into(),
        id,
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({"to": "doing"}),
        preconditions: Map::new(),
    });
    assert_eq!(
        host.get("Project", "p1").expect("read")["status"],
        "planned",
        "heard after the commit"
    );
    let ran = host.run_due();
    assert!(ran.iter().all(|ran| ran.outcome.is_ok()), "{ran:?}");
    assert_eq!(host.get("Project", "p1").expect("read")["status"], "active");
}

#[test]
fn a_define_is_held_to_the_cores_check_of_its_configs() {
    let mut host = Host::new();
    let refused = host.refused(&Request::Define {
        document: ticket(json!([{"name": "Workflow", "field": "status", "config": {"states": ["open"], "initial": "lost"}}])),
    });
    assert_eq!(refused.code, Code::InvalidSchema);
    let unknown = host.refused(&Request::Define {
        document: ticket(json!([{"name": "Comments"}])),
    });
    assert_eq!(unknown.code, Code::InvalidSchema, "{}", unknown.message);
    let none = host.refused(&Request::Publish {
        type_name: "Ticket".into(),
        discard: false,
    });
    assert_eq!(none.code, Code::NotFound);
}

#[test]
fn a_request_and_its_unit_read_and_write_json() {
    let request: Request = serde_json::from_value(json!({
        "kind": "create", "type": "Order", "data": {"title": "Desk"}
    }))
    .expect("a create reads");
    assert_eq!(
        request,
        Request::Create {
            type_name: "Order".into(),
            data: object(json!({"title": "Desk"})),
            params: Map::new()
        }
    );
    let list: Request = serde_json::from_value(json!({
        "kind": "list", "type": "Order", "filter": {"status": {"eq": "paid"}}, "sort": [{"field": "title", "desc": true}], "limit": 5
    }))
    .expect("a list reads");
    assert!(matches!(list, Request::List { limit: Some(5), .. }));
    let _ = admin();
}
