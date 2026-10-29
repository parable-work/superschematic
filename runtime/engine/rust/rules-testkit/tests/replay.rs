//! The replayer over a recording of its own, with a toy behavior (D103):
//! the steps it maps and compares, the symbols it binds, a mismatch it
//! reports, the steps it skips and why, a write past the host that stops
//! the scenario, and a scenario whose behavior the core does not run.

use serde_json::{json, Value};

use superschematic_engine_testkit::ScenarioFile;
use superschematic_rules::{
    Behavior, ConfigError, ConfigTarget, Core, Cx, Declaration, GuardRequest, Hook, Veto, View,
};
use superschematic_rules_testkit::replay;

// Flag: a switch on the row, which only toggle moves, refused while the
// row's own field locked is true.
struct Flag;

impl Behavior for Flag {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Flag")
            .field("on")
            .field("at")
            .operation("toggle", true)
            .veto("locked")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn initialize(&self, cx: &Cx<'_, ()>, _params: &Value) -> Hook<()> {
        cx.set("on", false)?;
        cx.set("at", cx.now())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let locked = view.own_field("locked") == Some(Value::Bool(true));
        Ok(
            (request.operation_of(view.use_id(), "toggle").is_some() && locked)
                .then(|| Veto::new("it is locked").with_code("locked")),
        )
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, _params: &Value) -> Hook<Value> {
        let on = cx.get("on") != Some(Value::Bool(true));
        cx.set("on", on)?;
        cx.set("at", cx.now())?;
        Ok(json!({ "on": on }))
    }
}

fn recording() -> ScenarioFile {
    let document = json!({
        "kind": "General",
        "name": "Note",
        "types": {"Note": {
            "name": "Note",
            "fields": [{"name": "title", "required": true}, {"name": "locked"}],
            "behaviors": [{"name": "Flag", "config": {}}]
        }}
    });
    let flag = |on: bool, at: i64| json!({"Flag": {"on": on, "at": at}});
    let note = |on: bool, at: i64| json!({"id": "{{id:1}}", "seq": 99, "behaviors": flag(on, at)});
    let alice = "alice";
    ScenarioFile {
        name: "toy".to_owned(),
        scenarios: vec![
            json!({
                "name": "a note's flag",
                "behaviors": ["Flag"],
                "principals": {"alice": {"subject": "alice", "permissions": []}},
                "steps": [
                    {"call": "open", "as": "e1", "args": [{"path": "{{dir:1}}/engine.db", "policy": "allowAll", "clock": "fixed", "ids": "seeded"}], "now": 1000},
                    {"call": "schemas.define", "principal": alice, "args": [document], "expect": {"name": "Note", "definedAt": 1000}},
                    {"call": "schemas.publish", "principal": alice, "args": ["Note"], "expect": {"version": 1, "published": true}},
                    {"call": "instances.create", "principal": alice, "args": ["Note", {"title": "a"}], "expect": note(false, 1000)},
                    {"call": "instances.invoke", "principal": alice, "args": ["Note", "{{id:1}}", "toggle"], "expect": {"on": true}, "now": 2000},
                    {"call": "instances.get", "principal": alice, "args": ["Note", "{{id:1}}"], "expect": note(true, 2000)},
                    // 7: what the recording holds and the host does not.
                    {"call": "instances.get", "principal": alice, "args": ["Note", "{{id:1}}"], "expect": note(false, 2000)},
                    // 8: own-field validation is the store's.
                    {"call": "instances.update", "principal": alice, "args": ["Note", "{{id:1}}", {"nope": 1}], "error": {"class": "InstanceValidationError", "code": "invalid_instance", "message": "Note in namespace default (version 1): nope: Note has no field nope"}},
                    {"call": "instances.update", "principal": alice, "args": ["Note", "{{id:1}}", {"locked": true}], "expect": note(true, 2000), "now": 3000},
                    {"call": "instances.invoke", "principal": alice, "args": ["Note", "{{id:1}}", "toggle"], "error": {"class": "BehaviorVetoError", "code": "vetoed", "message": "behavior Flag vetoes toggle of Note {{id:1}}: it is locked", "details": {"behavior": "Flag", "action": "toggle", "reason": "it is locked", "vetoCode": "locked"}}},
                    // 11: a read outside the host's surface.
                    {"call": "tools.list", "principal": alice, "expect": []},
                    {"call": "events.read", "principal": alice, "args": [{"schema": "Note", "instanceId": "{{id:1}}"}], "expect": {"events": [
                        {"cursor": 3, "kind": "create", "change": {"data": {"title": "a"}, "behaviors": flag(false, 1000)}},
                        {"cursor": 4, "kind": "operation", "change": {"behavior": "Flag", "operation": "toggle", "params": {}, "patch": {"behaviors": flag(true, 2000)}}},
                        {"cursor": 5, "kind": "update", "change": {"patch": {"data": {"locked": true}}}}
                    ], "next": 5, "more": false}},
                    {"call": "instances.invoke", "principal": alice, "args": ["Note", "{{id:1}}", "ring"], "error": {"class": "EngineError", "code": "not_found", "message": "Note has no operation ring"}},
                    // 14: a write past the host stops the scenario.
                    {"call": "storage.run", "args": ["UPDATE engine_instances SET data = '{}'"], "expect": {"changes": 1}},
                    {"call": "instances.get", "principal": alice, "args": ["Note", "{{id:1}}"], "expect": note(true, 2000)}
                ]
            }),
            json!({
                "name": "a behavior the core does not run",
                "behaviors": ["Ghost"],
                "steps": [
                    {"call": "open", "as": "e1", "args": [{"path": "{{dir:1}}/engine.db", "policy": "allowAll"}]},
                    {"call": "schemas.define", "principal": alice, "args": [document]}
                ]
            }),
            json!({
                "name": "a test behavior no fixture ports",
                "testBehaviors": ["test.Unported"],
                "steps": [
                    {"call": "open", "as": "e1", "args": [{"path": "{{dir:1}}/engine.db", "policy": "allowAll"}]}
                ]
            }),
        ],
    }
}

#[test]
fn the_replayer_compares_what_the_core_decides_and_skips_what_it_cannot_map() {
    let report = replay(&recording(), &|| Core::new().with(Flag));
    let scenario = &report.scenarios[0];
    assert_eq!(scenario.steps, 15);
    assert_eq!(scenario.checked, 11, "{}", report.details());

    let mismatches: Vec<(usize, &str)> = scenario
        .mismatches
        .iter()
        .map(|mismatch| (mismatch.step, mismatch.what.as_str()))
        .collect();
    assert_eq!(mismatches, [(7, "behaviors")], "{}", report.details());
    assert_eq!(
        scenario.mismatches[0].expected,
        json!({"Flag": {"on": false, "at": 2000}})
    );
    assert_eq!(
        scenario.mismatches[0].actual,
        json!({"Flag": {"on": true, "at": 2000}})
    );

    let skipped: Vec<(usize, &str)> = scenario
        .skipped
        .iter()
        .map(|skipped| (skipped.step, skipped.reason.as_str()))
        .collect();
    assert_eq!(skipped.len(), 4, "{}", report.details());
    assert_eq!(skipped[0].0, 8);
    assert!(skipped[0]
        .1
        .starts_with("own-field validation is the store's"));
    assert_eq!(skipped[1].0, 11);
    assert!(skipped[1].1.contains("tools.list is outside"));
    assert_eq!(skipped[2].0, 14);
    assert!(skipped[2].1.contains("SQL of its own (storage.run)"));
    assert_eq!(skipped[3].0, 15);
    assert!(skipped[3].1.starts_with("the scenario stopped at step 14"));

    let ghost = &report.scenarios[1];
    assert_eq!((ghost.checked, ghost.skipped.len()), (0, 2));
    assert_eq!(
        ghost.skipped[0].reason,
        "it composes behaviors the core does not run: Ghost"
    );
    let unported = &report.scenarios[2];
    assert_eq!(
        unported.skipped[0].reason,
        "it needs test behaviors written in TypeScript that no fixture ports: test.Unported"
    );

    assert_eq!(report.checked(), 11);
    assert_eq!(report.skipped(), 7);
    assert_eq!(
        report.summary(),
        "toy: 3 scenarios, 11 steps checked, 7 skipped, 1 mismatches"
    );
}

#[test]
fn an_engine_with_a_permission_matcher_of_the_tests_own_is_not_mapped() {
    let mut file = recording();
    file.scenarios.truncate(1);
    let steps = file.scenarios[0]["steps"].as_array_mut().unwrap();
    steps.truncate(3);
    steps[0]["args"][0]["permissionMatcher"] = json!("custom");
    let report = replay(&file, &|| Core::new().with(Flag));
    let scenario = &report.scenarios[0];
    assert_eq!(scenario.checked, 1);
    assert!(scenario.skipped[0].reason.contains("permission matcher"));
}
