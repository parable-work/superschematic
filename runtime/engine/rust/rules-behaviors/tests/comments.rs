//! Comments on the pure core (D77, D103 step 4), through the core's public
//! API over a source in memory: the declaration's sub-tables, shared table,
//! computed fields and the operations and fields a table's Comments leaves
//! out; the config and the operations' parameters held to the declaration
//! file's schemas; every write beside the row, so the row's version stays
//! and the conversation's shared row holds two writes apart; the edit
//! window and the most edits a message has; and the config held while the
//! table has rows.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{Action, FieldSpec, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{comments, core};
use support::{caller, changed, define, refused, result, rule, shape, undescribed, veto_of, Host};

const USE: &str = "Comments";

fn review(config: Value) -> TypeSpec {
    TypeSpec::new("Review")
        .with(UseSpec::on_type(USE, config))
        .with_field(FieldSpec::new("title", "string"))
}

fn file() -> Value {
    serde_json::from_str(comments::DECLARATION).expect("the declaration parses")
}

fn post(host: &mut Host, subject: &str, params: Value) -> Value {
    result(host.invoke(&caller(subject, &[]), "r1", USE, "post", params))
}

#[test]
fn the_declaration_keeps_its_sub_tables_and_a_shared_one_and_leaves_out_what_waits() {
    let registered = core();
    let declaration = registered.declaration(comments::NAME).expect("registered");
    let tables: Vec<(&str, Option<&str>, bool)> = declaration
        .tables
        .iter()
        .map(|table| (table.name.as_str(), table.key.as_deref(), table.shared))
        .collect();
    assert_eq!(
        tables,
        [
            ("messages", Some("message"), false),
            ("drafts", Some("chunk"), false),
            ("reads", Some("subject"), false),
            ("follows", Some("follow"), false),
            ("deliveries", Some("delivery"), false),
            ("edits", Some("edit"), false),
            ("reactions", Some("reaction"), false),
            ("conversations", Some("conversation"), true),
        ]
    );
    // Its fields are computed at a read, the reader's unread among them.
    assert_eq!(
        declaration.fields,
        [
            "messages",
            "lastMessageAt",
            "threads",
            "composing",
            "unread"
        ]
    );
    assert_eq!(declaration.computed, declaration.fields);
    // It arms a composing message's idle deadline, and writes beside its
    // row alone, so the deadline fires beside it too; it declares the
    // signals D77 gives it.
    assert!(declaration.arms_deadlines && declaration.writes_beside_only());
    let signals: Vec<(&str, &str)> = declaration
        .signals
        .iter()
        .map(|signal| (signal.name.as_str(), signal.access.as_str()))
        .collect();
    assert_eq!(
        signals,
        [
            ("typing", "read"),
            ("viewing", "read"),
            ("draft", "operation")
        ]
    );
    // Every writing operation is beside the row; the left-out ones and the
    // namespace's are none of its.
    let mut beside: Vec<&str> = declaration
        .operations
        .iter()
        .filter(|operation| operation.beside)
        .map(|operation| operation.name.as_str())
        .collect();
    beside.sort_unstable();
    assert_eq!(
        beside,
        [
            "appendDraft",
            "begin",
            "deleteMessage",
            "edit",
            "finish",
            "follow",
            "interrupt",
            "markRead",
            "pin",
            "post",
            "purge",
            "react",
            "reopen",
            "resolve",
            "unpin",
            "unreact"
        ]
    );
    assert_eq!(beside.len(), 16);
    assert!(declaration
        .operations
        .iter()
        .all(|operation| operation.beside == operation.writes));
    for (left_out, _) in comments::LEFT_OUT_OPERATIONS {
        assert!(declaration.find_operation(left_out).is_none(), "{left_out}");
    }
    for name in [
        "inbox",
        "searchMessages",
        "claimDeliveries",
        "settleDeliveries",
    ] {
        assert!(declaration.find_operation(name).is_none(), "{name}");
    }
    assert_eq!(declaration.operation_names(false).len(), 21);
}

#[test]
fn the_config_is_the_declaration_files_schema_and_a_member_that_waits_is_refused() {
    let schema = undescribed(&file()["configSchema"]);
    let members: Vec<&String> = schema["properties"]
        .as_object()
        .expect("properties")
        .keys()
        .collect();
    assert_eq!(
        members,
        [
            "anchorPermission",
            "attachments",
            "blocks",
            "composeTimeoutMs",
            "deliverPermission",
            "deliveryRetentionMs",
            "editWindowMs",
            "events",
            "maxAttempts",
            "maxBodyLength",
            "maxDrafts",
            "moderatePermission",
            "pinPermission",
            "resolvePermission",
            "retryBackoffMs"
        ]
    );
    assert_eq!(
        schema["properties"]["maxBodyLength"],
        json!({"maximum": 40000, "minimum": 1, "type": "integer"})
    );
    assert_eq!(
        schema["properties"]["editWindowMs"],
        json!({"minimum": 1000, "type": "integer"})
    );
    assert_eq!(
        schema["properties"]["blocks"],
        json!({"items": {"enum": ["ref", "action", "code", "attachment"]}, "type": "array", "uniqueItems": true})
    );
    assert!(define(&core(), &review(json!({}))).is_ok());
    assert!(define(
        &core(),
        &review(json!({"blocks": ["ref", "code"], "maxBodyLength": 10, "moderatePermission": "m"}))
    )
    .is_ok());
    assert_eq!(
        shape(
            &core(),
            &review(
                json!({"maxBodyLength": 0, "blocks": ["ref", "ref", "file"], "keep": 1, "pinPermission": ""})
            )
        ),
        [
            "/config: unknown key \"keep\"",
            "/config/blocks: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/blocks/2: must be one of \"ref\", \"action\", \"code\", \"attachment\"",
            "/config/maxBodyLength: must be >= 1",
            "/config/pinPermission: must NOT have fewer than 1 characters",
        ]
    );
    // Streamed replies' and deliveries' members are a table's now; the
    // deliveries a message makes are recorded, and their claims wait.
    assert!(define(
        &core(),
        &review(json!({"composeTimeoutMs": 5000, "maxDrafts": 2, "deliverPermission": "notify", "maxAttempts": 3}))
    )
    .is_ok());
    let reason = rule(
        &core(),
        &review(json!({"anchorPermission": "anchor"})),
        comments::NAME,
    );
    assert!(
        reason.starts_with("anchorPermission: ") && reason.contains("waits for"),
        "{reason}"
    );
    // Attachment blocks and the config's attachments are a table's since
    // D154.
    assert!(define(
        &core(),
        &review(json!({"blocks": ["attachment"], "attachments": {"maxBytes": 10, "schemes": ["attachment"]}}))
    )
    .is_ok());
}

#[test]
fn an_attachment_block_is_held_to_the_configs_attachments_in_the_contracts_order() {
    let mut host = Host::new(
        core(),
        review(
            json!({"attachments": {"maxBytes": 1000, "mediaTypes": ["image/*", "application/pdf"], "schemes": ["attachment", "https"]}}),
        ),
    );
    let alice = caller("alice", &[]);
    changed(host.create(&alice, "r1", json!({"title": "Plan"})));
    let block = |uri: &str, media_type: &str, bytes: i64| json!({"kind": "attachment", "uri": uri, "name": "plan.pdf", "mediaType": media_type, "bytes": bytes, "sha256": "0".repeat(64)});
    // A file the config allows is posted, its block stored as given.
    let posted = post(
        &mut host,
        "alice",
        json!({"body": "The plan.", "blocks": [block("attachment:7c1e", "application/pdf", 900)]}),
    );
    assert_eq!(posted["blocks"][0]["uri"], json!("attachment:7c1e"));
    // The scheme first, then the media type, then the size, each the veto
    // with the member it fails.
    for (given, member, value, allowed) in [
        (
            block("bucket:7c1e", "text/plain", 2000),
            "uri",
            json!("bucket"),
            json!(["attachment", "https"]),
        ),
        (
            block("attachment:7c1e", "text/plain", 2000),
            "mediaType",
            json!("text/plain"),
            json!(["image/*", "application/pdf"]),
        ),
        (
            block("attachment:7c1e", "image/png", 2000),
            "bytes",
            json!(2000),
            json!(1000),
        ),
    ] {
        let (code, details) = veto_of(host.invoke(
            &alice,
            "r1",
            USE,
            "post",
            json!({"body": "x", "blocks": [given]}),
        ));
        assert_eq!(code, "attachment_not_allowed");
        assert_eq!(
            details,
            json!({"block": 0, "member": member, "value": value, "allowed": allowed})
        );
    }
    // An https uri names a host.
    let error = refused(host.invoke(
        &alice,
        "r1",
        USE,
        "post",
        json!({"body": "x", "blocks": [block("https:///plan.pdf", "image/png", 10)]}),
    ));
    assert_eq!(
        error["details"]["issues"][0]["path"],
        json!("/blocks/0/uri")
    );
    // An edit that keeps a block is not held to the config again.
    let edited = result(host.invoke(
        &alice,
        "r1",
        USE,
        "edit",
        json!({"message": 1, "body": "The plan, again.", "blocks": [block("attachment:7c1e", "application/pdf", 900)]}),
    ));
    assert_eq!(edited["body"], json!("The plan, again."));
}

#[test]
fn the_parameters_are_the_declaration_files_schemas() {
    let file = file();
    let operations = file["operations"].as_array().expect("operations");
    let schema_of = |name: &str| {
        undescribed(
            &operations
                .iter()
                .find(|operation| operation["name"] == name)
                .expect("declared")["paramsSchema"],
        )
    };
    // Each operation's members and required members, as the checks take
    // them.
    for (name, members, required) in [
        (
            "post",
            vec!["blocks", "body", "clientId", "mentions", "quotes", "thread"],
            vec!["body"],
        ),
        (
            "edit",
            vec!["blocks", "body", "mentions", "message"],
            vec!["message"],
        ),
        ("deleteMessage", vec!["message"], vec!["message"]),
        ("purge", vec!["message"], vec!["message"]),
        ("react", vec!["emoji", "message"], vec!["message", "emoji"]),
        (
            "unreact",
            vec!["emoji", "message"],
            vec!["message", "emoji"],
        ),
        ("pin", vec!["message"], vec!["message"]),
        ("unpin", vec!["message"], vec!["message"]),
        ("resolve", vec!["thread"], vec!["thread"]),
        ("reopen", vec!["thread"], vec!["thread"]),
        (
            "listMessages",
            vec!["after", "around", "before", "limit", "thread"],
            vec![],
        ),
        ("getMessage", vec!["message"], vec!["message"]),
        (
            "listEdits",
            vec!["cursor", "limit", "message"],
            vec!["message"],
        ),
        (
            "listReactions",
            vec!["cursor", "emoji", "limit", "message"],
            vec!["message", "emoji"],
        ),
        ("listPinned", vec!["cursor", "limit"], vec![]),
    ] {
        let schema = schema_of(name);
        assert_eq!(schema["additionalProperties"], json!(false), "{name}");
        let held: Vec<&String> = schema["properties"]
            .as_object()
            .expect(name)
            .keys()
            .collect();
        assert_eq!(held, members, "{name}");
        let wanted: Vec<Value> = required.iter().map(|member| json!(member)).collect();
        assert_eq!(
            schema.get("required").cloned().unwrap_or(json!([])),
            Value::Array(wanted),
            "{name}"
        );
    }
    let post = schema_of("post");
    assert_eq!(
        post["properties"]["body"],
        json!({"maxLength": 40000, "minLength": 1, "pattern": "\\S", "type": "string"})
    );
    assert_eq!(
        post["properties"]["mentions"],
        json!({"items": {"maxLength": 256, "minLength": 1, "pattern": "^\\S+$", "type": "string"}, "maxItems": 50, "type": "array", "uniqueItems": true})
    );
    let kinds: Vec<&Value> = post["properties"]["blocks"]["items"]["oneOf"]
        .as_array()
        .expect("oneOf")
        .iter()
        .map(|kind| &kind["properties"]["kind"]["const"])
        .collect();
    assert_eq!(
        kinds,
        [
            &json!("ref"),
            &json!("action"),
            &json!("code"),
            &json!("attachment")
        ]
    );
    assert_eq!(post["properties"]["blocks"]["maxItems"], json!(20));
    assert_eq!(
        schema_of("listEdits")["properties"]["limit"],
        json!({"maximum": 500, "minimum": 1, "type": "integer"})
    );
    assert_eq!(
        schema_of("listMessages")["properties"]["limit"],
        json!({"maximum": 100, "minimum": 1, "type": "integer"})
    );
    // A malformed block meets every kind's schema and the oneOf's.
    let mut host = Host::new(core(), review(json!({})));
    changed(host.create(&caller("alice", &[]), "r1", json!({"title": "Plan"})));
    let error = refused(host.invoke(
        &caller("alice", &[]),
        "r1",
        USE,
        "post",
        json!({"body": "x", "blocks": [{"kind": "ref", "schema": "Review"}]}),
    ));
    let issues: Vec<String> = error["details"]["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            format!(
                "{}: {}",
                issue["path"].as_str().unwrap(),
                issue["message"].as_str().unwrap()
            )
        })
        .collect();
    assert!(
        issues.contains(&"/blocks/0: must have required property 'id'".to_owned()),
        "{issues:?}"
    );
    assert!(
        issues.contains(&"/blocks/0/kind: must be equal to constant".to_owned()),
        "{issues:?}"
    );
    assert!(
        issues.contains(&"/blocks/0: must match exactly one schema in oneOf".to_owned()),
        "{issues:?}"
    );
}

#[test]
fn every_write_is_beside_the_row_and_its_conversations_row_holds_two_apart() {
    let mut host = Host::new(
        core(),
        review(json!({"moderatePermission": "reviews.moderate"})),
    );
    let alice = caller("alice", &[]);
    changed(host.create(&alice, "r1", json!({"title": "Plan"})));
    let version = host.row("r1").version;
    let posted = changed(host.invoke(&alice, "r1", USE, "post", json!({"body": "First."})));
    // No row is written: its version and its own fields stay.
    assert!(posted.rows.is_empty(), "{:?}", posted.rows);
    assert_eq!(host.row("r1").version, version);
    // The message is a row of the use's sub-table, keyed by its id.
    assert_eq!(
        serde_json::to_value(&posted.sub_rows).unwrap()[0]["key"],
        json!("0000000001")
    );
    // The conversation's shared row is inserted, then each write moves it,
    // held to the version read.
    assert_eq!(
        serde_json::to_value(&posted.shared_rows).unwrap(),
        json!([{"type": "Review", "useId": "Comments", "table": "conversations", "key": "r1", "change": {"kind": "insert", "values": {"lastMessage": 1, "lastReaction": 0, "lastStand": 1}}}])
    );
    let pinned = changed(host.invoke(&alice, "r1", USE, "pin", json!({"message": 1})));
    assert_eq!(
        serde_json::to_value(&pinned.shared_rows).unwrap()[0]["change"],
        json!({"kind": "update", "expectedVersion": 1, "values": {"lastMessage": 1, "lastReaction": 0, "lastStand": 1}})
    );
    assert!(pinned.rows.is_empty());
    // A read writes nothing.
    let listed = changed(host.invoke(&alice, "r1", USE, "listPinned", json!({})));
    assert!(listed.sub_rows.is_empty() && listed.shared_rows.is_empty());
    assert_eq!(listed.result["items"][0]["pinned"], json!(true));
    // The fields count from the messages.
    assert_eq!(
        host.read_fields("r1", USE, comments::MESSAGES),
        serde_json::from_value::<serde_json::Map<String, Value>>(json!({
            "messages": 1, "lastMessageAt": support::START, "threads": {"open": 1, "resolved": 0},
            "composing": []
        }))
        .unwrap()
    );
}

#[test]
fn an_author_edits_within_the_window_and_a_message_takes_at_most_500_edits() {
    let mut host = Host::new(core(), review(json!({"editWindowMs": 1000})));
    let alice = caller("alice", &[]);
    changed(host.create(&alice, "r1", json!({"title": "Plan"})));
    post(&mut host, "alice", json!({"body": "v0"}));
    let start = support::START;
    // Within the window, from postedAt.
    let edited = result(host.invoke_at(
        &alice,
        start + 1000,
        "r1",
        USE,
        "edit",
        json!({"message": 1, "body": "v1"}),
    ));
    assert_eq!(
        [&edited["edits"], &edited["editedAt"]],
        [&json!(1), &json!(start + 1000)]
    );
    let (code, details) = veto_of(host.invoke_at(
        &alice,
        start + 1001,
        "r1",
        USE,
        "edit",
        json!({"message": 1, "body": "v2"}),
    ));
    assert_eq!(code, "edit_window_passed");
    assert_eq!(
        details,
        json!({"message": 1, "postedAt": start, "editWindowMs": 1000})
    );

    // With no window, the 501st edit is refused.
    let mut host = Host::new(core(), review(json!({})));
    changed(host.create(&alice, "r1", json!({"title": "Plan"})));
    post(&mut host, "alice", json!({"body": "v0"}));
    for edit in 1..=500 {
        changed(host.invoke(
            &alice,
            "r1",
            USE,
            "edit",
            json!({"message": 1, "body": format!("v{edit}")}),
        ));
    }
    let (code, details) = veto_of(host.invoke(
        &alice,
        "r1",
        USE,
        "edit",
        json!({"message": 1, "body": "one too many"}),
    ));
    assert_eq!(code, "too_many_edits");
    assert_eq!(details, json!({"message": 1, "max": 500}));
    // listEdits pages what each edit replaced, by number.
    let page = result(host.invoke(
        &alice,
        "r1",
        USE,
        "listEdits",
        json!({"message": 1, "limit": 2}),
    ));
    assert_eq!(page["items"][0]["body"], json!("v0"));
    assert_eq!(page["items"][1]["edit"], json!(2));
    assert!(page["next"].is_string());
}

#[test]
fn comments_joins_a_table_with_rows_and_does_not_leave_one() {
    let mut host = Host::new(
        core(),
        TypeSpec::new("Review").with_field(FieldSpec::new("title", "string")),
    );
    changed(host.create(&caller("alice", &[]), "r1", json!({"title": "Plan"})));
    let without = host.spec.clone();
    let with = review(json!({}));
    let publish = |host: &mut Host, from: TypeSpec, to: TypeSpec| {
        host.spec = to;
        host.run(
            &caller("alice", &[]),
            support::START,
            Action::Publish { from: Some(from) },
        )
    };
    changed(publish(&mut host, without.clone(), with.clone()));
    let refusal = refused(publish(&mut host, with, without));
    assert!(
        refusal["message"].as_str().is_some_and(
            |message| message.contains("the messages its instances have would stay behind")
        ),
        "{refusal}"
    );
}
