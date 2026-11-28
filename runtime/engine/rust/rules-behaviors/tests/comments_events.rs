//! A table's Comments' events (#713) and a message's via (#606) on the
//! pure core, over a source in memory that keeps the events a command
//! emits: with the config's events, a message that comes to stand emits
//! mentioned and replied about the row, beside it, naming the subjects who
//! should hear of it; without them it emits none.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{FieldSpec, TypeSpec, UseSpec};
use superschematic_rules_behaviors::core;
use support::{caller, changed, result, Host};

const USE: &str = "Comments";

fn review(config: Value) -> TypeSpec {
    TypeSpec::new("Review")
        .with(UseSpec::on_type(USE, config))
        .with_field(FieldSpec::new("title", "string"))
}

fn post(host: &mut Host, subject: &str, params: Value) -> Value {
    result(host.invoke(&caller(subject, &[]), "r1", USE, "post", params))
}

/// With the config's events, a message that comes to stand emits mentioned
/// and replied about the row, beside it: the mentioned, as written, and the
/// thread's followers no mention reached, never the author or the subject
/// it was written for; an edit that adds a mention emits mentioned for the
/// added. Without them it emits nothing. A message records the channel its
/// caller came over as via.
#[test]
fn a_message_emits_its_mentions_and_replies_with_the_configs_events_and_records_its_via() {
    let mut host = Host::new(core(), review(json!({"events": true})));
    let alice = caller("alice", &[]);
    changed(host.create(&alice, "r1", json!({"title": "Plan"})));
    let version = host.row("r1").version;
    let top = post(
        &mut host,
        "alice",
        json!({"body": "Look, bob and carol.", "mentions": ["bob", "carol", "alice"]}),
    );
    assert_eq!(
        host.row("r1").version,
        version,
        "an emit beside the row moves no version of it"
    );
    let events = std::mem::take(&mut host.memory.events);
    assert_eq!(events.len(), 1);
    assert_eq!(
        (events[0].kind.as_str(), events[0].use_id.as_str()),
        ("mentioned", USE)
    );
    assert_eq!(
        events[0].payload,
        json!({"message": top["id"], "author": "alice", "excerpt": "Look, bob and carol.", "subjects": ["bob", "carol"]})
    );
    // bob replies, mentioning carol: alice follows the thread and hears of
    // it as replied, carol as mentioned, once.
    post(
        &mut host,
        "bob",
        json!({"body": "Seen.", "thread": top["id"], "mentions": ["carol"]}),
    );
    let events = std::mem::take(&mut host.memory.events);
    let kinds: Vec<(&str, Value)> = events
        .iter()
        .map(|event| (event.kind.as_str(), event.payload["subjects"].clone()))
        .collect();
    assert_eq!(
        kinds,
        [
            ("mentioned", json!(["carol"])),
            ("replied", json!(["alice"]))
        ]
    );
    // An edit that adds a mention emits it for the added alone.
    result(host.invoke(
        &alice,
        "r1",
        USE,
        "edit",
        json!({"message": 1, "mentions": ["bob", "carol", "dan"]}),
    ));
    let events = std::mem::take(&mut host.memory.events);
    assert_eq!(events.len(), 1);
    assert_eq!(events[0].payload["subjects"], json!(["dan"]));
    // An agent writing for dan over SMS: dan hears nothing of it, and the
    // message records its for and its via.
    let agent = caller("run-1", &[]).acting_for("dan").over("sms");
    let posted = result(host.invoke(
        &agent,
        "r1",
        USE,
        "post",
        json!({"body": "On my way.", "mentions": ["dan", "carol"]}),
    ));
    assert_eq!(
        (posted["for"].clone(), posted["via"].clone()),
        (json!("dan"), json!("sms"))
    );
    let events = std::mem::take(&mut host.memory.events);
    assert_eq!(events[0].payload["subjects"], json!(["carol"]));
    assert_eq!(
        (
            events[0].payload["via"].clone(),
            events[0].payload["forSubject"].clone()
        ),
        (json!("sms"), json!("dan"))
    );
    // Without the config's events, nothing is emitted.
    let mut quiet = Host::new(core(), review(json!({})));
    changed(quiet.create(&alice, "r1", json!({"title": "Plan"})));
    post(
        &mut quiet,
        "alice",
        json!({"body": "Look, bob.", "mentions": ["bob"]}),
    );
    assert!(quiet.memory.events.is_empty());
}
