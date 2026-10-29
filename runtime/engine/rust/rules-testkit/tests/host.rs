//! The reference host's loop with a toy behavior (D103): a read of another
//! type's row answered from memory under the lock it asked for, a row that
//! moved run again from fresh state, one that keeps moving refused as
//! busy, a refusal that applies nothing, a change set applied whole, one
//! that writes two rows held to both versions, events written whatever
//! rows a change set writes, the request ids keys are made from, a
//! deadline that falls due and one cleared or moved before it does, and
//! the define and publish checks.

use serde_json::{json, Map, Value};

use superschematic_rules::{
    Action, Answer, Answers, Behavior, Caller, Change, ChangeSet, Code, Command, ConfigError,
    ConfigTarget, Core, Cx, Declaration, GuardRequest, Hook, LockKey, OutboxEvent, ReadRequest,
    Refusal, RowRef, SchemaIssue, Step, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_testkit::{ApplyError, MemoryHost, Store, MAX_ATTEMPTS};

const NOW: i64 = 1_000_000;

// Tally: a count that moves only through add, held under a cap another
// type's row keeps when the config names that type. Each add writes an
// entry, emits an event and arms a deadline that resets the count. note
// writes an entry under a key it makes; addTo adds to another row's Tally;
// later arms a second deadline, and a reset clears it.
struct Tally;

struct TallyConfig {
    /// The type whose row `cap` holds the cap in its own field `value`.
    cap_from: Option<String>,
}

impl Behavior for Tally {
    type Config = TallyConfig;

    fn declaration(&self) -> Declaration {
        Declaration::new("Tally")
            .field("count")
            .table("entries")
            .operation("add", true)
            .operation("peek", false)
            .operation("note", true)
            .operation("addTo", true)
            .operation("later", true)
            .veto("over_cap")
    }

    // Its config schema: an object whose one member is capFrom.
    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<TallyConfig, ConfigError> {
        let shape = |message: String| ConfigError::Schema(vec![SchemaIssue::new("", message)]);
        let members = raw
            .as_object()
            .ok_or_else(|| shape("must be object".to_owned()))?;
        if let Some(key) = members.keys().find(|key| *key != "capFrom") {
            return Err(shape(format!("unknown key \"{key}\"")));
        }
        Ok(TallyConfig {
            cap_from: members
                .get("capFrom")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn check_params(
        &self,
        _config: &TallyConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if ["add", "addTo"].contains(&operation)
            && params.get("by").and_then(Value::as_i64).is_none()
        {
            return vec![SchemaIssue::new("/by", "must be an integer")];
        }
        Vec::new()
    }

    fn initialize(&self, cx: &Cx<'_, TallyConfig>, _params: &Value) -> Hook<()> {
        cx.set("count", 0)
    }

    fn guard(
        &self,
        view: &View<'_, TallyConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let Some(params) = request.operation_of(view.use_id(), "add") else {
            return Ok(None);
        };
        let Some(cap_from) = &view.config().cap_from else {
            return Ok(None);
        };
        view.lock(format!("Tally/{cap_from}"))?;
        let cap = view
            .read_row(cap_from, "cap")?
            .and_then(|row| row.own.get("value").and_then(Value::as_i64))
            .unwrap_or(i64::MAX);
        let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
        let count = view.get("count").and_then(|v| v.as_i64()).unwrap_or(0);
        Ok((count + by > cap).then(|| {
            Veto::new(format!("{} more would pass the cap of {cap}", by)).with_code("over_cap")
        }))
    }

    fn operation(&self, cx: &Cx<'_, TallyConfig>, name: &str, params: &Value) -> Hook<Value> {
        let count = cx.get("count").and_then(|v| v.as_i64()).unwrap_or(0);
        let text = |key: &str| params.get(key).and_then(Value::as_str).unwrap_or_default();
        match name {
            "peek" => return Ok(json!({ "count": count })),
            "note" => {
                let key = cx.new_key("note")?;
                cx.insert_row("entries", &key, Map::new())?;
                return Ok(Value::from(key));
            }
            "addTo" => {
                let by = json!({ "by": params.get("by") });
                return cx.call_on(text("type"), text("key"), "Tally", "add", &by);
            }
            "later" => {
                cx.arm("later", cx.now() + 2_000, json!({ "later": true }))?;
                return Ok(Value::Null);
            }
            _ => {}
        }
        let by = params.get("by").and_then(Value::as_i64).unwrap_or(0);
        let next = count + by;
        cx.set("count", next)?;
        let mut entry = Map::new();
        entry.insert("by".into(), Value::from(by));
        cx.insert_row("entries", &format!("e{next}"), entry)?;
        cx.emit("added", json!({ "by": by, "count": next }))?;
        cx.arm("reset", cx.now() + 1_000, json!({ "from": next }))?;
        Ok(json!({ "count": next }))
    }

    // A reset clears the count and the later deadline; the later one
    // counts one up.
    fn deadline(&self, cx: &Cx<'_, TallyConfig>, name: &str, _payload: &Value) -> Hook<()> {
        let count = cx.get("count").and_then(|v| v.as_i64()).unwrap_or(0);
        if name == "later" {
            return cx.set("count", count + 1);
        }
        cx.clear("later")?;
        cx.set("count", 0)
    }

    fn after_change(&self, cx: &Cx<'_, TallyConfig>, change: &Change<'_>) -> Hook<()> {
        if let Change::Deadline { .. } = change {
            cx.emit("reset", Value::Null)?;
        }
        Ok(())
    }
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

fn command(action: Action) -> Command {
    Command::new(alice(), NOW, action)
}

fn create(key: &str, own: Value) -> Command {
    command(Action::Create {
        id: Some(key.to_owned()),
        own: object(own),
        params: Map::new(),
    })
}

fn add(key: &str, by: i64) -> Command {
    command(Action::Operation {
        key: key.to_owned(),
        use_id: "Tally".to_owned(),
        operation: "add".to_owned(),
        params: json!({ "by": by }),
    })
}

fn count(host: &MemoryHost, key: &str) -> Option<Value> {
    host.store()
        .row("Counter", key)
        .and_then(|row| row.owned.get("Tally"))
        .and_then(|fields| fields.get("count"))
        .cloned()
}

// host is a host with a Cap type and a Counter type whose Tally reads its
// cap from Cap, a cap of 5, and a counter c1 at 0.
fn host() -> MemoryHost {
    let mut host = MemoryHost::new(Core::new().with(Tally));
    for spec in [
        TypeSpec::new("Cap"),
        TypeSpec::new("Counter").with(UseSpec::on_type("Tally", json!({ "capFrom": "Cap" }))),
    ] {
        let name = spec.name.clone();
        host.define(spec, &alice(), NOW).unwrap();
        assert_eq!(host.publish(&name, &alice(), NOW).unwrap(), Some(1));
    }
    host.execute("Cap", create("cap", json!({ "value": 5 })))
        .unwrap();
    host.execute("Counter", create("c1", json!({ "title": "Desk" })))
        .unwrap();
    host
}

#[test]
fn a_read_of_another_row_is_answered_from_memory_under_its_lock() {
    let mut host = host();
    let applied = host.execute("Counter", add("c1", 3)).unwrap();
    assert_eq!(applied.result(), &json!({ "count": 3 }));
    assert_eq!(applied.locks, vec!["Tally/Cap".into()]);
    assert_eq!(applied.attempts, 1);
    assert_eq!(applied.version, Some(2));
    assert_eq!(count(&host, "c1"), Some(json!(3)));

    // The cap the host read refuses the next add.
    let refused = host.execute("Counter", add("c1", 3)).unwrap_err();
    assert_eq!(refused.code, Code::Vetoed);
    assert_eq!(
        refused.message,
        "behavior Tally vetoes add of Counter c1: 3 more would pass the cap of 5"
    );
}

#[test]
fn a_row_that_moved_runs_again_from_fresh_state() {
    let mut host = host();
    let applied = host
        .execute_racing("Counter", add("c1", 2), |store: &mut Store, attempt| {
            if attempt == 0 {
                // Another writer commits between the run and its apply.
                store.write_row("Counter", "c1", object(json!({ "title": "Oak desk" })));
            }
        })
        .unwrap();
    assert_eq!(applied.attempts, 2);
    // The second run read the row at the version the other writer left.
    let row = host.store().row("Counter", "c1").unwrap();
    assert_eq!(row.version, 3);
    assert_eq!(row.own, object(json!({ "title": "Oak desk" })));
    assert_eq!(count(&host, "c1"), Some(json!(2)));
    let kinds: Vec<&str> = host
        .store()
        .log_of("Counter", "c1")
        .iter()
        .map(|logged| logged.kind())
        .collect();
    assert_eq!(kinds, ["create", "operation"]);
}

#[test]
fn a_row_that_keeps_moving_is_refused_as_busy_and_nothing_applies() {
    for (mut host, bound) in [(host(), MAX_ATTEMPTS), (host().with_attempts(2), 2)] {
        let mut raced = 0;
        let refused = host
            .execute_racing("Counter", add("c1", 2), |store: &mut Store, _| {
                raced += 1;
                store.write_row("Counter", "c1", object(json!({ "title": "Desk" })));
            })
            .unwrap_err();
        assert_eq!(raced, bound);
        assert_eq!(refused.code, Code::Busy);
        assert_eq!(refused.status, 503);
        assert_eq!(count(&host, "c1"), Some(json!(0)));
        assert!(host.store().outbox().is_empty());
        assert!(host.store().deadlines().is_empty());
    }
}

#[test]
fn a_refusal_applies_nothing() {
    let mut host = host();
    let before = host.store().clone();

    // The core refuses: a veto, refused parameters, a row that is not there.
    assert_eq!(
        host.execute("Counter", add("c1", 9)).unwrap_err().code,
        Code::Vetoed
    );
    assert_eq!(
        host.execute(
            "Counter",
            command(Action::Operation {
                key: "c1".into(),
                use_id: "Tally".into(),
                operation: "add".into(),
                params: json!({ "by": "two" }),
            })
        )
        .unwrap_err()
        .class,
        "OperationParamsError"
    );
    assert_eq!(
        host.execute("Counter", add("c9", 1)).unwrap_err().code,
        Code::NotFound
    );
    // The host refuses: a create of a key that exists.
    assert_eq!(
        host.execute("Counter", create("c1", json!({})))
            .unwrap_err()
            .code,
        Code::Conflict
    );
    assert_eq!(host.store(), &before);
}

#[test]
fn a_change_set_applies_whole_all_or_nothing() {
    let mut host = host();
    host.execute("Counter", add("c1", 2)).unwrap();
    let store = host.store();
    let entries: Vec<(&str, Value)> = store
        .sub_rows("Counter", "c1")
        .into_iter()
        .map(|(at, values)| (at.sub_key.as_str(), Value::Object(values.clone())))
        .collect();
    assert_eq!(entries, [("e2", json!({ "by": 2 }))]);
    assert_eq!(store.outbox().len(), 1);
    assert_eq!(store.outbox()[0].event.kind, "added");
    assert_eq!(
        store.outbox()[0].event.payload,
        json!({ "by": 2, "count": 2 })
    );
    assert_eq!(store.due(NOW + 1_000).len(), 1);
    assert!(store.due(NOW + 999).is_empty());

    // A change set whose sub-table insert collides applies none of it, not
    // even the row's write before it: an add of 0 writes entry e2 again.
    // The host holds the lock before it answers, so the core decides on
    // the answers.
    let mut answers = Answers::new();
    answers.hold(LockKey::from("Tally/Cap"));
    let answers = answers
        .with(
            ReadRequest::row("Counter", "c1"),
            Answer::Row(store.row("Counter", "c1").map(|row| row.as_row())),
        )
        .with(
            ReadRequest::row("Cap", "cap"),
            Answer::Row(store.row("Cap", "cap").map(|row| row.as_row())),
        );
    let step = host
        .core()
        .run(store.live("Counter").unwrap(), &add("c1", 0), &answers);
    let Step::Done(outcome) = step else {
        panic!("the run asks for nothing more: {step:?}");
    };
    let changes = outcome.into_result().unwrap();
    let before = host.store().clone();
    let mut store = host.store().clone();
    let applied = store.apply("Counter", &add("c1", 0), &changes);
    assert!(
        matches!(applied, Err(ApplyError::Refused(ref refusal)) if refusal.code == Code::Conflict)
    );
    assert_eq!(store, before);
}

#[test]
fn a_deadline_that_falls_due_runs_as_a_command() {
    let mut host = host();
    host.execute("Counter", add("c1", 4)).unwrap();
    let ran = host.run_due(NOW + 1_000, &alice());
    assert_eq!(ran.len(), 1);
    let (at, outcome) = &ran[0];
    assert_eq!((at.use_id.as_str(), at.name.as_str()), ("Tally", "reset"));
    assert!(outcome.is_ok());
    assert_eq!(count(&host, "c1"), Some(json!(0)));
    // It is spent, and its handler armed nothing.
    assert!(host.store().deadlines().is_empty());
    let last = host.store().log().last().unwrap();
    assert_eq!(last.kind(), "deadline");
    assert_eq!(host.store().outbox().last().unwrap().event.kind, "reset");
}

#[test]
fn a_delete_takes_the_rows_sub_table_rows_and_deadlines() {
    let mut host = host();
    host.execute("Counter", add("c1", 1)).unwrap();
    host.execute("Counter", command(Action::Delete { key: "c1".into() }))
        .unwrap();
    assert!(host.store().row("Counter", "c1").is_none());
    assert!(host.store().sub_rows("Counter", "c1").is_empty());
    assert!(host.store().deadlines().is_empty());
}

#[test]
fn a_read_only_operation_writes_nothing() {
    let mut host = host();
    let before = host.store().clone();
    let applied = host
        .execute(
            "Counter",
            command(Action::Operation {
                key: "c1".into(),
                use_id: "Tally".into(),
                operation: "peek".into(),
                params: json!({}),
            }),
        )
        .unwrap();
    assert_eq!(applied.result(), &json!({ "count": 0 }));
    assert_eq!(applied.version, None);
    assert_eq!(host.store(), &before);
}

#[test]
fn define_and_publish_hold_a_type_to_the_cores_checks() {
    let mut host = host();
    // A config the behavior refuses defines nothing.
    let refused = host
        .define(
            TypeSpec::new("Other").with(UseSpec::on_type("Tally", json!({ "cap": 1 }))),
            &alice(),
            NOW,
        )
        .unwrap_err();
    assert_eq!(refused.class, "SchemaDocumentError");
    assert_eq!(
        refused.message,
        "type Other: behavior Tally config: unknown key \"cap\""
    );
    assert_eq!(
        refused.details.as_deref().unwrap()["issues"][0]["path"],
        json!("/config")
    );
    assert!(host.store().versions("Other").is_none());

    // Without rows a behavior joins a type; with rows, the default allows
    // it not, and allows no change of config either.
    host.define(
        TypeSpec::new("Cap").with(UseSpec::on_type("Tally", json!({}))),
        &alice(),
        NOW,
    )
    .unwrap_err();
    host.define(TypeSpec::new("Fresh"), &alice(), NOW).unwrap();
    host.publish("Fresh", &alice(), NOW).unwrap();
    host.define(
        TypeSpec::new("Fresh").with(UseSpec::on_type("Tally", json!({}))),
        &alice(),
        NOW,
    )
    .unwrap();
    assert_eq!(host.publish("Fresh", &alice(), NOW).unwrap(), Some(2));
    let changed = host
        .define(
            TypeSpec::new("Counter").with(UseSpec::on_type("Tally", json!({}))),
            &alice(),
            NOW,
        )
        .unwrap_err();
    assert_eq!(changed.code, Code::IncompatibleChange);
    assert_eq!(
        changed.message,
        r#"type Counter: behavior Tally on type Counter cannot change its config from {"capFrom":"Cap"} to {}: it allows no config change"#
    );
    // Nothing waits to be published once a define is refused.
    assert_eq!(host.publish("Counter", &alice(), NOW).unwrap(), None);
    assert_eq!(host.store().live_version("Counter"), 1);
}

#[test]
fn a_command_on_a_type_never_published_is_not_found() {
    let mut host = MemoryHost::new(Core::new());
    let refused: Refusal = host.execute("Ghost", create("g1", json!({}))).unwrap_err();
    assert_eq!(refused.code, Code::NotFound);
}

#[test]
fn a_create_without_an_id_gets_one_from_the_host() {
    let mut host = host();
    host.queue_ids(["queued"]);
    let make = || {
        command(Action::Create {
            id: None,
            own: Map::new(),
            params: Map::new(),
        })
    };
    assert_eq!(host.execute("Counter", make()).unwrap().key, "queued");
    assert_eq!(host.execute("Counter", make()).unwrap().key, "id-1");
    let keys: Vec<&str> = host
        .store()
        .rows("Counter")
        .into_iter()
        .map(|(key, _)| key)
        .collect();
    assert_eq!(keys, ["c1", "queued", "id-1"]);
}

#[test]
fn a_change_set_that_writes_two_rows_is_held_to_both_versions() {
    let mut host = host();
    host.execute("Counter", create("c2", json!({}))).unwrap();
    let add_to = |key: &str, by: i64| {
        command(Action::Operation {
            key: "c1".into(),
            use_id: "Tally".into(),
            operation: "addTo".into(),
            params: json!({ "type": "Counter", "key": key, "by": by }),
        })
    };
    let applied = host.execute("Counter", add_to("c2", 2)).unwrap();
    assert_eq!(applied.result(), &json!({ "count": 2 }));
    assert_eq!(applied.version, Some(2));
    assert_eq!(count(&host, "c2"), Some(json!(2)));
    assert_eq!(host.store().row("Counter", "c2").unwrap().version, 2);
    // Each row written is logged, and the events and entries are c2's.
    let logged: Vec<(&str, &str)> = host
        .store()
        .log()
        .iter()
        .rev()
        .take(2)
        .map(|logged| (logged.key.as_str(), logged.kind()))
        .collect();
    assert_eq!(logged, [("c2", "operation"), ("c1", "operation")]);
    assert_eq!(host.store().outbox().last().unwrap().key, "c2");
    assert_eq!(host.store().sub_rows("Counter", "c2").len(), 1);

    // When the row reached through the call moved, the command runs again.
    let applied = host
        .execute_racing("Counter", add_to("c2", 1), |store: &mut Store, attempt| {
            if attempt == 0 {
                store.write_row("Counter", "c2", object(json!({ "title": "moved" })));
            }
        })
        .unwrap();
    assert_eq!(applied.attempts, 2);
    assert_eq!(count(&host, "c2"), Some(json!(3)));

    // A row of another type is bound by its live version, read from memory.
    host.define(
        TypeSpec::new("Gauge").with(UseSpec::on_type("Tally", json!({}))),
        &alice(),
        NOW,
    )
    .unwrap();
    host.publish("Gauge", &alice(), NOW).unwrap();
    host.execute("Gauge", create("g1", json!({}))).unwrap();
    let applied = host
        .execute(
            "Counter",
            command(Action::Operation {
                key: "c1".into(),
                use_id: "Tally".into(),
                operation: "addTo".into(),
                params: json!({ "type": "Gauge", "key": "g1", "by": 4 }),
            }),
        )
        .unwrap();
    assert_eq!(applied.result(), &json!({ "count": 4 }));
    assert_eq!(applied.changes.rows.len(), 2);
    assert_eq!(
        applied.changes.rows[1].row(),
        RowRef::new("Gauge", Some("g1"))
    );
}

#[test]
fn every_event_is_written_whatever_rows_the_change_set_writes() {
    let host = host();
    let mut store = host.store().clone();
    let changes = ChangeSet {
        events: vec![OutboxEvent {
            row: RowRef::new("Counter", Some("c1")),
            kind: "noticed".into(),
            use_id: "Tally".into(),
            payload: json!({}),
        }],
        ..ChangeSet::default()
    };
    assert_eq!(store.apply("Counter", &add("c1", 0), &changes), Ok(None));
    assert_eq!(store.outbox().last().unwrap().event.kind, "noticed");
    assert_eq!(store.log().len(), host.store().log().len());
}

#[test]
fn the_host_gives_each_command_a_request_id_kept_through_its_runs() {
    let mut host = host();
    let note = || {
        command(Action::Operation {
            key: "c1".into(),
            use_id: "Tally".into(),
            operation: "note".into(),
            params: json!({}),
        })
    };
    // host() ran two commands: this is the third, made again after the row
    // moved, with the same key.
    let applied = host
        .execute_racing("Counter", note(), |store: &mut Store, attempt| {
            if attempt == 0 {
                store.write_row("Counter", "c1", object(json!({ "title": "Oak" })));
            }
        })
        .unwrap();
    assert_eq!(applied.attempts, 2);
    assert_eq!(applied.result(), &json!("request-3:Tally:note:1"));
    // A request id the caller gives is the one it keeps.
    let given = host
        .execute("Counter", note().with_request("call-9"))
        .unwrap();
    assert_eq!(given.result(), &json!("call-9:Tally:note:1"));
}

#[test]
fn a_deadline_cleared_or_moved_before_it_falls_due_does_not() {
    let mut host = host();
    host.execute("Counter", add("c1", 4)).unwrap();
    let later = || {
        command(Action::Operation {
            key: "c1".into(),
            use_id: "Tally".into(),
            operation: "later".into(),
            params: json!({}),
        })
    };
    host.execute("Counter", later()).unwrap();
    assert_eq!(host.store().due(NOW + 2_000).len(), 2);
    // reset falls due first and clears later, which then does not run.
    let ran = host.run_due(NOW + 2_000, &alice());
    let names: Vec<&str> = ran.iter().map(|(at, _)| at.name.as_str()).collect();
    assert_eq!(names, ["reset"]);
    assert_eq!(count(&host, "c1"), Some(json!(0)));
    assert!(host.store().deadlines().is_empty());

    // A deadline command for an arming that is gone, or moved, is refused,
    // and applies nothing.
    host.execute("Counter", later()).unwrap();
    let due = |at: i64| {
        Command::new(
            alice(),
            NOW + 5_000,
            Action::Deadline {
                key: "c1".into(),
                use_id: "Tally".into(),
                name: "later".into(),
                at,
                payload: json!({ "later": true }),
            },
        )
    };
    let before = host.store().clone();
    let moved = host.execute("Counter", due(NOW + 1)).unwrap_err();
    assert_eq!(moved.code, Code::NotFound);
    assert_eq!(
        moved.message,
        format!(
            "Counter c1 has no deadline Tally.later armed at {}",
            NOW + 1
        )
    );
    assert_eq!(host.store(), &before);
    // One cleared between the run and its apply runs again, and is refused.
    let cleared = host
        .execute_racing("Counter", due(NOW + 2_000), |store: &mut Store, _| {
            let mut emptied = store.clone();
            emptied
                .apply(
                    "Counter",
                    &add("c1", 0),
                    &ChangeSet {
                        deadlines: vec![superschematic_rules::DeadlineChange::Clear {
                            row: RowRef::new("Counter", Some("c1")),
                            use_id: "Tally".into(),
                            name: "later".into(),
                        }],
                        ..ChangeSet::default()
                    },
                )
                .unwrap();
            *store = emptied;
        })
        .unwrap_err();
    assert_eq!(cleared.code, Code::NotFound);
    // Armed as it fell due, it runs.
    host.execute("Counter", later()).unwrap();
    let applied = host.execute("Counter", due(NOW + 2_000)).unwrap();
    assert_eq!(applied.attempts, 1);
    assert_eq!(count(&host, "c1"), Some(json!(1)));
}
