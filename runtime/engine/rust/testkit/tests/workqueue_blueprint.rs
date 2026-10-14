//! Blueprint, children created with their parent (D16): the cases of the
//! work queue's `test/blueprint.test.ts` (inline steps stamped at create,
//! each child created with its required parent link, copied links and
//! edges as create parameters, in one event; copied fields and data; when,
//! equals and includes, leaving steps out and passing their edges on; the
//! config checks against this type and the child schema; steps kept in a
//! definition, read from the revision the link pins and stamped when the
//! link is set; the creator's permissions; and one transaction for the
//! parent and every child), on SQLite and, with the variable set, on
//! Postgres. A case that releases a recipe waits for Branches; one whose
//! children are claimed waits for Lease (milestone 12a). The guidance is
//! held to the TypeScript text in the work-queue crate's
//! `tests/guidance_blueprint.rs`.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineOptions, InstanceRecord,
    ListOptions, Policy, Principal, ReadEvents, Result, SchemaTarget, WriteOptions,
};
use superschematic_engine_workqueue::MAX_STEPS;
use support::{alice, issue, issues_of, refusal, targets, veto, RecordingPolicy, Target};

fn nora() -> Principal {
    Principal::new("nora", Vec::<String>::new())
}

fn wren() -> Principal {
    Principal::new("wren", Vec::<String>::new())
}

/// A General schema whose one type, named like it, has the fields and
/// behaviors given.
fn document_of(name: &str, fields: Value, behaviors: Value) -> Value {
    json!({"kind": "General", "name": name, "types": {name: {"name": name, "role": "EmbeddedStruct", "behaviors": behaviors, "fields": fields}}})
}

fn step_flow() -> Value {
    json!({"states": ["todo", "doing", "done"], "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]})
}

/// A step of a run: its key, a title and the run's topic.
fn step_fields() -> Value {
    json!([
        {"name": "step", "typeRef": {"name": "string"}, "required": true},
        {"name": "title", "typeRef": {"name": "string"}},
        {"name": "topic", "typeRef": {"name": "string"}}
    ])
}

/// What a stamp sets in a step and nothing may change after: its key and
/// the copied topic.
fn step_constants() -> Value {
    json!({"name": "Constants", "config": {"fields": ["step", "topic"]}})
}

fn run_link() -> Value {
    json!({"run": {"schema": "Run", "required": true}})
}

fn step_behaviors(links: Value, extra: Value) -> Value {
    let mut behaviors = vec![
        json!({"name": "Workflow", "config": step_flow()}),
        step_constants(),
    ];
    behaviors.extend(extra.as_array().cloned().unwrap_or_default());
    behaviors.push(json!({"name": "Links", "config": {"links": links}}));
    Value::Array(behaviors)
}

fn dependencies() -> Value {
    json!([{"name": "Dependencies"}])
}

fn run_fields() -> Value {
    json!([
        {"name": "title", "typeRef": {"name": "string"}, "required": true},
        {"name": "topic", "typeRef": {"name": "string"}},
        {"name": "flags", "typeRef": {"name": "string", "isArray": true}}
    ])
}

/// The lifecycle the steps describe: design, then build; audit when the
/// run is flagged auth, docs when its topic is public; review after both,
/// release after review. Left out, audit and docs pass build on to review.
fn lifecycle() -> Value {
    json!({
        "design": {},
        "build": {"after": ["design"], "data": {"title": "Build it"}},
        "audit": {"after": ["build"], "when": {"field": "flags", "includes": "auth"}},
        "docs": {"after": ["build"], "when": {"field": "topic", "equals": "public"}},
        "review": {"after": ["audit", "docs"]},
        "release": {"after": ["review"]}
    })
}

fn inline() -> Value {
    json!({"schema": "Step", "parentLink": "run", "keyField": "step", "steps": lifecycle(), "copyFields": ["topic"]})
}

fn with(base: Value, extra: Value) -> Value {
    let mut out = base;
    if let (Some(out), Value::Object(extra)) = (out.as_object_mut(), extra) {
        out.extend(extra);
    }
    out
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn open(target: &Target) -> Engine {
    target.open_workqueue()
}

// world opens an engine with Step published and Run composing Blueprint
// with the config given, after the other behaviors given.
fn world_with(
    target: &Target,
    config: Value,
    run_behaviors: Value,
    configure: impl FnOnce(&mut EngineOptions),
) -> Engine {
    let engine = target.open_workqueue_with(configure);
    publish(
        &engine,
        &document_of(
            "Step",
            step_fields(),
            step_behaviors(run_link(), dependencies()),
        ),
    );
    let mut behaviors = run_behaviors.as_array().cloned().unwrap_or_default();
    behaviors.push(json!({"name": "Blueprint", "config": config}));
    publish(
        &engine,
        &document_of("Run", run_fields(), Value::Array(behaviors)),
    );
    engine
}

fn world(target: &Target, config: Value) -> Engine {
    world_with(target, config, json!([]), |_| {})
}

fn create(
    engine: &Engine,
    who: &Principal,
    schema: &str,
    data: Value,
    id: Option<&str>,
    behaviors: Option<Value>,
) -> Result<InstanceRecord> {
    engine.instances().create(
        who,
        schema,
        &data,
        CreateOptions {
            id,
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

fn run(engine: &Engine, id: &str, data: Value) -> InstanceRecord {
    create(engine, &nora(), "Run", data, Some(id), None).expect("create the run")
}

fn get(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
}

// children_of reads a run's children, [key, id] each; `None` before it is
// stamped.
fn children_of(engine: &Engine, id: &str) -> Option<Vec<(String, String)>> {
    let record = get(engine, "Run", id)?;
    let children = record.behavior_field("Blueprint", "children")?.as_array()?;
    Some(
        children
            .iter()
            .map(|child| {
                (
                    child["key"].as_str().unwrap_or_default().to_owned(),
                    child["id"].as_str().unwrap_or_default().to_owned(),
                )
            })
            .collect(),
    )
}

fn keys_of(children: &[(String, String)]) -> Vec<&str> {
    children.iter().map(|(key, _)| key.as_str()).collect()
}

fn step_of(engine: &Engine, id: &str) -> InstanceRecord {
    get(engine, "Step", id).expect("the step")
}

fn links_of(engine: &Engine, id: &str) -> Value {
    step_of(engine, id)
        .behavior_field("Links", "targets")
        .cloned()
        .unwrap_or(Value::Null)
}

// edges maps each stamped step's key to the keys of the steps that block
// it, in the order the children were stamped.
fn edges(engine: &Engine, run: &str) -> Map<String, Value> {
    let children = children_of(engine, run).unwrap_or_default();
    let mut out = Map::new();
    for (key, id) in &children {
        let page = engine
            .instances()
            .invoke(
                &alice(),
                "Step",
                id,
                "listBlockers",
                &json!({}),
                WriteOptions::default(),
            )
            .expect("listBlockers");
        let blockers: Vec<Value> = page["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|blocker| {
                let id = blocker["id"].as_str().unwrap_or_default();
                Value::from(
                    children
                        .iter()
                        .find(|(_, child)| child == id)
                        .map_or("", |(key, _)| key.as_str()),
                )
            })
            .collect();
        out.insert(key.clone(), Value::Array(blockers));
    }
    out
}

fn edge_keys(engine: &Engine, run: &str) -> Vec<String> {
    edges(engine, run).keys().cloned().collect()
}

fn all_steps(engine: &Engine) -> usize {
    engine
        .instances()
        .list(&alice(), "Step", ListOptions::default())
        .expect("list")
        .items
        .len()
}

fn message_of<T: std::fmt::Debug>(result: Result<T>) -> String {
    refusal(result).message().to_owned()
}

// Blueprint: inline steps.

#[test]
fn a_create_stamps_a_child_per_step_linked_to_its_parent_and_blocked_after_its_steps() {
    for target in targets("a_create_stamps_a_child_per_step") {
        let engine = world(&target, inline());
        run(
            &engine,
            "r1",
            json!({"title": "First", "topic": "public", "flags": ["auth"]}),
        );
        let children = children_of(&engine, "r1").expect("children");
        assert_eq!(
            keys_of(&children),
            ["design", "build", "audit", "docs", "review", "release"]
        );
        let build = step_of(&engine, &children[1].1);
        assert_eq!(
            json!([
                build.data["step"],
                build.data["title"],
                build.data["topic"],
                build.behavior_field("Workflow", "status")
            ]),
            json!(["build", "Build it", "public", "todo"])
        );
        assert_eq!(
            links_of(&engine, &children[1].1),
            json!({"run": {"schema": "Run", "id": "r1"}})
        );
        assert_eq!(
            Value::Object(edges(&engine, "r1")),
            json!({"design": [], "build": ["design"], "audit": ["build"], "docs": ["build"], "review": ["audit", "docs"], "release": ["review"]})
        );
        // The children are the creator's, as instances().create would make
        // them.
        for (_, id) in &children {
            assert_eq!(step_of(&engine, id).created_by, "nora");
        }
        assert_eq!(
            build.behavior_field("Dependencies", "blocked"),
            Some(&json!(true))
        );
        // Each child holds its link and its edges from its create: one event
        // each.
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Step"),
                    instance_id: Some(&children[1].1),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, "create");
        assert_eq!(
            events[0].change,
            json!({
                "data": {"step": "build", "title": "Build it", "topic": "public"},
                "behaviors": {
                    "Workflow": {"status": "todo", "enteredAt": events[0].at},
                    "Dependencies": {"blocked": true},
                    "Links": {"targets": {"run": {"schema": "Run", "id": "r1"}}}
                }
            })
        );
    }
}

#[test]
fn the_child_schemas_required_parent_link_is_given_at_every_create() {
    for target in targets("the_required_parent_link") {
        let engine = world(&target, inline());
        let refused = refusal(create(
            &engine,
            &nora(),
            "Step",
            json!({"step": "loose"}),
            None,
            None,
        ));
        assert_eq!(
            issues_of(&refused),
            vec![issue(
                "/behaviors/Links",
                "link run is required, so a create of Step gives it"
            )]
        );
        run(&engine, "r1", json!({"title": "First"}));
        let extra = create(
            &engine,
            &nora(),
            "Step",
            json!({"step": "extra"}),
            None,
            Some(json!({"Links": {"run": "r1"}})),
        )
        .expect("create");
        assert_eq!(
            extra.behavior_field("Links", "targets"),
            Some(&json!({"run": {"schema": "Run", "id": "r1"}}))
        );
        // A step created later is not among what was stamped.
        assert!(!children_of(&engine, "r1")
            .expect("children")
            .iter()
            .any(|(_, id)| *id == extra.id));
    }
}

#[test]
fn copy_links_copies_the_links_the_create_gives_to_every_child_of_inline_steps() {
    for target in targets("copy_links_copies") {
        let engine = open(&target);
        publish(
            &engine,
            &document_of(
                "Area",
                json!([{"name": "name", "typeRef": {"name": "string"}}]),
                json!([]),
            ),
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "required": true}, "area": {"schema": "Area"}}),
                    dependencies(),
                ),
            ),
        );
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([
                    {"name": "Links", "config": {"links": {"area": {"schema": "Area"}}}},
                    {"name": "Blueprint", "config": with(inline(), json!({"steps": {"a": {}, "b": {"after": ["a"]}}, "copyLinks": ["area"]}))}
                ]),
            ),
        );
        create(
            &engine,
            &alice(),
            "Area",
            json!({"name": "North"}),
            Some("a1"),
            None,
        )
        .expect("area");
        create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Mapped"}),
            Some("r1"),
            Some(json!({"Links": {"area": "a1"}})),
        )
        .expect("run");
        for (_, id) in children_of(&engine, "r1").expect("children") {
            assert_eq!(
                links_of(&engine, &id),
                json!({"run": {"schema": "Run", "id": "r1"}, "area": {"schema": "Area", "id": "a1"}})
            );
        }
        // A run that holds no area gives its children none.
        run(&engine, "r2", json!({"title": "Bare"}));
        let first = children_of(&engine, "r2").expect("children")[0].1.clone();
        assert_eq!(
            links_of(&engine, &first),
            json!({"run": {"schema": "Run", "id": "r2"}})
        );
    }
}

#[test]
fn when_leaves_out_the_steps_whose_field_does_not_equal_or_include_the_value() {
    for target in targets("when_leaves_out") {
        let engine = world(&target, inline());
        run(
            &engine,
            "r1",
            json!({"title": "Plain", "topic": "internal", "flags": []}),
        );
        assert_eq!(
            Value::Object(edges(&engine, "r1")),
            json!({"design": [], "build": ["design"], "review": ["build"], "release": ["review"]})
        );
        // A step left out passes on what it comes after, beside the included
        // steps it was with.
        run(
            &engine,
            "r2",
            json!({"title": "Auth", "topic": "internal", "flags": ["ui", "auth"]}),
        );
        assert_eq!(
            Value::Object(edges(&engine, "r2")),
            json!({"design": [], "build": ["design"], "audit": ["build"], "review": ["audit", "build"], "release": ["review"]})
        );
        assert_eq!(edges(&engine, "r2")["review"], json!(["audit", "build"]));
        run(&engine, "r3", json!({"title": "Public", "topic": "public"}));
        assert_eq!(
            Value::Object(edges(&engine, "r3")),
            json!({"design": [], "build": ["design"], "docs": ["build"], "review": ["build", "docs"], "release": ["review"]})
        );
        assert_eq!(edges(&engine, "r3")["review"], json!(["build", "docs"]));
        // A field the run does not hold equals nothing and includes nothing.
        run(&engine, "r4", json!({"title": "Bare"}));
        assert_eq!(
            edge_keys(&engine, "r4"),
            ["design", "build", "review", "release"]
        );
    }
}

#[test]
fn a_chain_of_left_out_steps_passes_on_what_the_first_of_them_comes_after() {
    for target in targets("a_chain_of_left_out_steps") {
        let steps = json!({
            "start": {},
            "one": {"after": ["start"], "when": {"field": "flags", "equals": ["a", "b"]}},
            "two": {"after": ["one"], "when": {"field": "flags", "includes": "c"}},
            "end": {"after": ["two"]}
        });
        let engine = world(
            &target,
            with(inline(), json!({"steps": steps, "copyFields": []})),
        );
        run(
            &engine,
            "r1",
            json!({"title": "Skip both", "flags": ["b", "a"]}),
        );
        assert_eq!(
            Value::Object(edges(&engine, "r1")),
            json!({"start": [], "end": ["start"]})
        );
        run(
            &engine,
            "r2",
            json!({"title": "Keep one", "flags": ["a", "b"]}),
        );
        assert_eq!(
            Value::Object(edges(&engine, "r2")),
            json!({"start": [], "one": ["start"], "end": ["one"]})
        );
    }
}

#[test]
fn an_update_stamps_nothing_again_and_a_delete_of_the_parent_leaves_its_children_to_their_links() {
    for target in targets("an_update_stamps_nothing_again") {
        let engine = world(&target, with(inline(), json!({"steps": {"only": {}}})));
        run(&engine, "r1", json!({"title": "Once"}));
        engine
            .instances()
            .update(
                &nora(),
                "Run",
                "r1",
                &json!({"title": "Twice?"}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(all_steps(&engine), 1);
        // The child's run link is required, so the parent's delete is refused
        // while it stands.
        let kept = veto(
            engine
                .instances()
                .delete(&nora(), "Run", "r1", WriteOptions::default()),
        );
        assert_eq!(
            (kept.0.as_str(), kept.3.as_deref()),
            ("Links", Some("required_target"))
        );
    }
}

// Blueprint: the config.

fn refused(engine: &Engine, config: Value, behaviors: Value) -> String {
    let mut list = behaviors.as_array().cloned().unwrap_or_default();
    list.push(json!({"name": "Blueprint", "config": config}));
    message_of(define(
        engine,
        &document_of("Run", run_fields(), Value::Array(list)),
    ))
}

fn assert_refused(engine: &Engine, config: Value, behaviors: Value, expected: &str) {
    let message = refused(engine, config, behaviors);
    assert!(message.contains(expected), "{message}\nwants: {expected}");
}

#[test]
fn inline_steps_that_form_a_cycle_are_refused_naming_the_cycle() {
    for target in targets("inline_steps_that_form_a_cycle") {
        let engine = world(&target, with(inline(), json!({"steps": {"only": {}}})));
        let cycle = json!({"a": {"after": ["c"]}, "b": {"after": ["a"], "when": {"field": "topic", "equals": "x"}}, "c": {"after": ["b"]}});
        assert_refused(
            &engine,
            with(inline(), json!({"steps": cycle})),
            json!([]),
            "steps: the steps form a cycle: a -> c -> b -> a",
        );
        assert_refused(
            &engine,
            with(inline(), json!({"steps": {"a": {"after": ["z"]}}})),
            json!([]),
            "steps: step a comes after z, which is not a step of the map",
        );
        assert_refused(
            &engine,
            with(inline(), json!({"steps": {"a": {"after": ["a"]}}})),
            json!([]),
            "step a comes after itself",
        );
    }
}

#[test]
fn the_child_schema_must_be_live_link_here_compose_dependencies_for_after_and_not_blueprint() {
    for target in targets("the_child_schema_must_be_live") {
        let engine = open(&target);
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step has no live version; publish it first",
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(json!({"owner": {"schema": "Run"}}), dependencies()),
            ),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step has no link run (its links: owner)",
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"owner": {"schema": "Run"}, "run": {"schema": "Other"}}),
                    dependencies(),
                ),
            ),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step's link run points at Other, not Run",
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "many": true, "max": 2}}),
                    dependencies(),
                ),
            ),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step's link run holds a list of targets, and a child links to its one parent through it",
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                json!([{"name": "Workflow", "config": step_flow()}]),
            ),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step does not compose Links",
        );
    }
    for target in targets("the_child_schema_dependencies") {
        let second = open(&target);
        publish(
            &second,
            &document_of("Step", step_fields(), step_behaviors(run_link(), json!([]))),
        );
        assert_refused(
            &second,
            inline(),
            json!([]),
            "schema Step does not compose Dependencies, so a step's after cannot block its child",
        );
        // Without after, Dependencies is not needed.
        define(
            &second,
            &document_of("Run", run_fields(), json!([{"name": "Blueprint", "config": with(inline(), json!({"steps": {"only": {}}}))}])),
        )
        .expect("define");
        assert_refused(
            &second,
            with(inline(), json!({"schema": "Run", "parentLink": "run"})),
            json!([]),
            "schema Run composes Blueprint: a child cannot stamp children of its own",
        );
    }
}

#[test]
fn the_child_schemas_constants_keeps_key_field_and_every_copied_field() {
    for target in targets("the_child_schemas_constants") {
        let engine = open(&target);
        let links = run_link();
        let without_constants: Vec<Value> = step_behaviors(links.clone(), dependencies())
            .as_array()
            .expect("behaviors")
            .iter()
            .filter(|behavior| behavior["name"] != "Constants")
            .cloned()
            .collect();
        publish(
            &engine,
            &document_of("Step", step_fields(), Value::Array(without_constants)),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step does not compose Constants, so the fields each stamp sets in a child could change after it: compose Constants with fields step, topic",
        );
        let with_constants = |fields: Value| {
            json!([
                {"name": "Workflow", "config": step_flow()},
                {"name": "Constants", "config": {"fields": fields}},
                {"name": "Dependencies"},
                {"name": "Links", "config": {"links": links}}
            ])
        };
        publish(
            &engine,
            &document_of("Step", step_fields(), with_constants(json!(["topic"]))),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step's Constants does not list step, which each stamp sets and nothing may change after",
        );
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                with_constants(json!(["step", "title"])),
            ),
        );
        assert_refused(
            &engine,
            inline(),
            json!([]),
            "schema Step's Constants does not list topic, which each stamp sets",
        );
        // Without copyFields, keyField alone is enough.
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": with(inline(), json!({"copyFields": []}))}]),
            ),
        );

        // A stamped child cannot be turned into another step, by its creator
        // or anyone else.
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(run_link(), dependencies()),
            ),
        );
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": inline()}]),
            ),
        );
        run(&engine, "r1", json!({"title": "First", "topic": "public"}));
        let build = children_of(&engine, "r1").expect("children")[1].1.clone();
        let refused = refusal(engine.instances().update(
            &nora(),
            "Step",
            &build,
            &json!({"step": "release", "topic": "private"}),
            WriteOptions::default(),
        ));
        let rules: Vec<Value> = refused.details().expect("details")["issues"]
            .as_array()
            .expect("issues")
            .iter()
            .map(|issue| json!([issue["path"], issue["rule"]]))
            .collect();
        assert_eq!(
            rules,
            vec![json!(["step", "constant"]), json!(["topic", "constant"])]
        );
        // Its other fields stay open.
        let renamed = engine
            .instances()
            .update(
                &wren(),
                "Step",
                &build,
                &json!({"title": "Build it well"}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(renamed.data["title"], json!("Build it well"));
    }
}

#[test]
fn a_stamp_checks_the_child_schemas_live_constants_again() {
    for target in targets("a_stamp_checks_constants_again") {
        let engine = world(&target, inline());
        let links = run_link();
        // A later version of Step drops topic from Constants, then
        // Constants altogether: Run's published version is not refused for
        // it.
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                json!([
                    {"name": "Workflow", "config": step_flow()},
                    {"name": "Constants", "config": {"fields": ["step"]}},
                    {"name": "Dependencies"},
                    {"name": "Links", "config": {"links": links}}
                ]),
            ),
        );
        let loose = veto(create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Loose", "topic": "public"}),
            Some("r1"),
            None,
        ));
        assert_eq!(
            (loose.0.as_str(), loose.1.as_str(), loose.3.as_deref(), loose.4, loose.2.as_str()),
            (
                "Blueprint",
                "create",
                Some("not_constant"),
                Some(json!({"fields": ["topic"]})),
                "Step's Constants does not list topic, which each stamp sets and nothing may change after"
            )
        );
        let without: Vec<Value> = step_behaviors(links, dependencies())
            .as_array()
            .expect("behaviors")
            .iter()
            .filter(|behavior| behavior["name"] != "Constants")
            .cloned()
            .collect();
        publish(
            &engine,
            &document_of("Step", step_fields(), Value::Array(without)),
        );
        let none = veto(create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "None"}),
            Some("r2"),
            None,
        ));
        assert_eq!(
            (none.3.as_deref(), none.4),
            (
                Some("not_constant"),
                Some(json!({"fields": ["step", "topic"]}))
            )
        );
        assert!(get(&engine, "Run", "r1").is_none());
        assert!(get(&engine, "Run", "r2").is_none());
        assert_eq!(all_steps(&engine), 0);
        // Constants back over both, the stamp goes ahead.
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(run_link(), dependencies()),
            ),
        );
        run(&engine, "r3", json!({"title": "Kept"}));
        assert_eq!(children_of(&engine, "r3").expect("children").len(), 4);
    }
}

#[test]
fn a_pinned_parent_link_needs_revisions_listed_before_blueprint() {
    for target in targets("a_pinned_parent_link") {
        let engine = open(&target);
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "pinned": true}}),
                    dependencies(),
                ),
            ),
        );
        let config = with(inline(), json!({"steps": {"only": {}}}));
        assert_refused(
            &engine,
            config.clone(),
            json!([]),
            "schema Step's link run is pinned, so Run lists Revisions before Blueprint, to have a revision to pin",
        );
        let after = message_of(define(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": config}, {"name": "Revisions"}]),
            ),
        ));
        assert!(after.contains("Revisions before Blueprint"), "{after}");
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Revisions"}, {"name": "Blueprint", "config": config}]),
            ),
        );
        run(&engine, "r1", json!({"title": "Pinned"}));
        let child = children_of(&engine, "r1").expect("children")[0].1.clone();
        assert_eq!(
            links_of(&engine, &child),
            json!({"run": {"schema": "Run", "id": "r1", "revision": 1, "latest": 1, "stale": false}})
        );
    }
}

#[test]
fn a_parent_link_pinned_to_a_release_is_refused() {
    for target in targets("a_parent_link_pinned_to_a_release") {
        let engine = open(&target);
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "pinned": "release"}}),
                    dependencies(),
                ),
            ),
        );
        assert_refused(
            &engine,
            with(inline(), json!({"steps": {"only": {}}})),
            json!([]),
            "schema Step's link run pins a release, which Run has none of when it stamps its children",
        );
    }
}

/// A schema Recipe whose instances are version graphs of steps (Branches).
fn recipes_document() -> Value {
    json!({
        "kind": "General",
        "name": "Recipe",
        "types": {
            "Recipe": {
                "name": "Recipe",
                "role": "EmbeddedStruct",
                "behaviors": [{"name": "Branches", "config": {"kinds": {"step": {"type": "Step", "order": "position"}}}}],
                "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
            },
            "Step": {
                "name": "Step",
                "role": "EmbeddedStruct",
                "fields": [
                    {"name": "instruction", "typeRef": {"name": "string"}, "required": true},
                    {"name": "position", "typeRef": {"name": "Int"}, "required": true}
                ]
            }
        }
    })
}

// release_recipe releases a new tagged commit of a recipe as alice: a step
// saved on a draft, committed and merged into its primary line. It returns
// the recipe's Branches release field, the new release's number.
fn release_recipe(engine: &Engine, id: &str) -> i64 {
    let invoke = |operation: &str, params: Value| {
        engine
            .instances()
            .invoke(
                &alice(),
                "Recipe",
                id,
                operation,
                &params,
                WriteOptions::default(),
            )
            .expect(operation)
    };
    let version = get(engine, "Recipe", id)
        .and_then(|record| {
            record
                .behavior_field("Branches", "release")
                .and_then(Value::as_i64)
        })
        .unwrap_or(0);
    let draft = invoke("branch", json!({"name": format!("release{}", version + 1)}));
    let saved = invoke(
        "save",
        json!({
            "ref": draft["id"],
            "version": draft["version"],
            "edits": {"step": {"upsert": [{"instruction": format!("Step {}", version + 1), "position": version + 1}]}}
        }),
    );
    let committed = invoke(
        "commit",
        json!({"ref": draft["id"], "version": saved["ref"]["version"]}),
    );
    let refs = invoke("refs", json!({}));
    let main = &refs["items"][0];
    let merged = invoke(
        "merge",
        json!({"source": committed["ref"]["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
    );
    invoke(
        "releaseCommit",
        json!({"commit": merged["commit"]["id"], "version": version}),
    )["version"]
        .as_i64()
        .expect("a release")
}

#[test]
#[ignore = "needs Branches (PR #377)"]
fn copy_links_keeps_the_release_a_runs_link_pins_where_the_childs_link_pins_releases_too() {
    for target in targets("copy_links_keeps_the_release") {
        let engine = open(&target);
        publish(&engine, &recipes_document());
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "required": true}, "recipe": {"schema": "Recipe", "pinned": "release"}, "menu": {"schema": "Recipe"}}),
                    dependencies(),
                ),
            ),
        );
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([
                    {"name": "Links", "config": {"links": {"recipe": {"schema": "Recipe", "pinned": "release"}, "menu": {"schema": "Recipe", "pinned": "release"}}}},
                    {"name": "Blueprint", "config": with(inline(), json!({"steps": {"a": {}}, "copyLinks": ["recipe", "menu"]}))}
                ]),
            ),
        );
        create(
            &engine,
            &alice(),
            "Recipe",
            json!({"title": "Soup"}),
            Some("soup"),
            None,
        )
        .expect("recipe");
        release_recipe(&engine, "soup");
        release_recipe(&engine, "soup");
        create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Cook"}),
            Some("r1"),
            Some(json!({"Links": {"recipe": {"id": "soup", "release": 1}, "menu": "soup"}})),
        )
        .expect("run");
        let child = children_of(&engine, "r1").expect("children")[0].1.clone();
        // The child's recipe keeps the run's release 1; its menu link pins
        // nothing, so it takes the id alone.
        assert_eq!(
            links_of(&engine, &child),
            json!({
                "run": {"schema": "Run", "id": "r1"},
                "recipe": {"schema": "Recipe", "id": "soup", "release": 1, "latest": 2, "stale": true},
                "menu": {"schema": "Recipe", "id": "soup"}
            })
        );
    }
}

#[test]
fn key_field_copy_fields_whens_fields_and_data_are_held_to_the_two_types() {
    for target in targets("key_field_copy_fields") {
        let engine = world(&target, with(inline(), json!({"steps": {"only": {}}})));
        for (config, behaviors, expected) in [
            (
                with(inline(), json!({"keyField": "title2"})),
                json!([]),
                "keyField title2 is not a string field of Step",
            ),
            (
                with(inline(), json!({"copyFields": ["flags"]})),
                json!([]),
                "copyFields: flags is not a field of Step",
            ),
            (
                with(inline(), json!({"copyFields": ["nope"]})),
                json!([]),
                "copyFields: nope is not a field of Run",
            ),
            (
                with(
                    inline(),
                    json!({"steps": {"a": {"when": {"field": "nope", "equals": 1}}}}),
                ),
                json!([]),
                "step a: when names nope, which is not a field of Run",
            ),
            (
                with(
                    inline(),
                    json!({"steps": {"a": {"when": {"field": "topic", "includes": "x"}}}}),
                ),
                json!([]),
                "step a: when includes reads topic as a list",
            ),
            (
                with(inline(), json!({"steps": {"a": {"data": {"step": "b"}}}})),
                json!([]),
                "step a: data sets step, which holds the step's key",
            ),
            (
                with(
                    inline(),
                    json!({"steps": {"a": {"data": {"color": "red"}}}}),
                ),
                json!([]),
                "step a: data sets color, which is not a field of Step",
            ),
            (
                with(inline(), json!({"copyLinks": ["area"]})),
                json!([]),
                "copyLinks reads links of the type's Links, which the type does not list",
            ),
            (
                with(inline(), json!({"copyLinks": ["run"]})),
                json!([{"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}}]),
                "copyLinks names run, the link each child points at its parent through",
            ),
            // The configSchema holds the shape: one of steps and from, a
            // step's keys, when's one comparison.
            (
                json!({"schema": "Step", "parentLink": "run", "keyField": "step"}),
                json!([]),
                "config: must match exactly one schema in oneOf",
            ),
            (
                with(
                    inline(),
                    json!({"steps": {"a": {"when": {"field": "topic", "equals": "x", "includes": "y"}}}}),
                ),
                json!([]),
                "when: must match exactly one schema in oneOf",
            ),
            (
                with(inline(), json!({"steps": {"a b": {}}})),
                json!([]),
                "config/steps: property name must be valid",
            ),
        ] {
            assert_refused(&engine, config, behaviors, expected);
        }
        assert_eq!(MAX_STEPS, 500);
    }
}

#[test]
fn blueprint_can_be_added_to_a_schema_with_instances_and_its_config_changed_not_removed() {
    for target in targets("blueprint_can_be_added") {
        let engine = open(&target);
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(run_link(), dependencies()),
            ),
        );
        publish(&engine, &document_of("Run", run_fields(), json!([])));
        create(
            &engine,
            &alice(),
            "Run",
            json!({"title": "Before"}),
            Some("r0"),
            None,
        )
        .expect("run");
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": inline()}]),
            ),
        );
        assert_eq!(children_of(&engine, "r0"), None);
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": with(inline(), json!({"steps": {"only": {}}}))}]),
            ),
        );
        let removed = refusal(define(
            &engine,
            &document_of("Run", run_fields(), json!([])),
        ));
        assert_eq!(removed.code(), EngineErrorCode::IncompatibleChange);
        assert!(
            removed
                .message()
                .contains("the record of what its instances stamped would stay behind"),
            "{}",
            removed.message()
        );
    }
}

// Blueprint: steps kept in a definition.

fn from_config() -> Value {
    json!({"schema": "Step", "parentLink": "run", "keyField": "step", "from": {"link": "plan", "field": "steps"}, "copyFields": ["topic"], "copyLinks": ["plan", "area"]})
}

fn run_links() -> Value {
    json!({"plan": {"schema": "Plan", "pinned": true}, "area": {"schema": "Area"}})
}

// definitions opens an engine with Plan (Revisions) and Area, Step linking
// to its run, the plan and an area, and Run with the from config; plan p1
// has two revisions, and area a1 exists.
fn definitions(target: &Target) -> Engine {
    let engine = open(target);
    publish(
        &engine,
        &document_of(
            "Plan",
            json!([
                {"name": "title", "typeRef": {"name": "string"}, "required": true},
                {"name": "steps", "typeRef": {"name": "Generic.JSON"}}
            ]),
            json!([{"name": "Revisions"}]),
        ),
    );
    publish(
        &engine,
        &document_of(
            "Area",
            json!([{"name": "name", "typeRef": {"name": "string"}}]),
            json!([]),
        ),
    );
    publish(
        &engine,
        &document_of(
            "Step",
            step_fields(),
            step_behaviors(
                json!({"run": {"schema": "Run", "required": true}, "plan": {"schema": "Plan", "pinned": true}, "area": {"schema": "Area"}}),
                dependencies(),
            ),
        ),
    );
    publish(
        &engine,
        &document_of(
            "Run",
            run_fields(),
            json!([
                {"name": "Links", "config": {"links": run_links()}},
                {"name": "Blueprint", "config": from_config()}
            ]),
        ),
    );
    create(
        &engine,
        &alice(),
        "Plan",
        json!({"title": "Two steps", "steps": {"first": {}, "second": {"after": ["first"]}}}),
        Some("p1"),
        None,
    )
    .expect("plan");
    engine
        .instances()
        .update(
            &alice(),
            "Plan",
            "p1",
            &json!({"title": "Three steps", "steps": {"first": {}, "second": {"after": ["first"]}, "third": {"after": ["second"]}}}),
            WriteOptions::default(),
        )
        .expect("update");
    create(
        &engine,
        &alice(),
        "Area",
        json!({"name": "North"}),
        Some("a1"),
        None,
    )
    .expect("area");
    engine
}

fn link(
    engine: &Engine,
    who: &Principal,
    run: &str,
    name: &str,
    id: &str,
    revision: Option<i64>,
) -> Result<Value> {
    let mut params = json!({"name": name, "id": id});
    if let Some(revision) = revision {
        params["revision"] = json!(revision);
    }
    engine
        .instances()
        .invoke(who, "Run", run, "link", &params, WriteOptions::default())
}

#[test]
fn a_create_stamps_nothing_and_the_first_link_of_from_stamps_the_map_of_the_revision_it_pins() {
    for target in targets("the_first_link_of_from_stamps") {
        let engine = definitions(&target);
        run(&engine, "r1", json!({"title": "Run", "topic": "public"}));
        assert_eq!(children_of(&engine, "r1"), None);
        link(&engine, &nora(), "r1", "area", "a1", None).expect("link");
        link(&engine, &nora(), "r1", "plan", "p1", None).expect("link");
        assert_eq!(
            Value::Object(edges(&engine, "r1")),
            json!({"first": [], "second": ["first"], "third": ["second"]})
        );
        let third = children_of(&engine, "r1").expect("children")[2].1.clone();
        assert_eq!(step_of(&engine, &third).data["topic"], json!("public"));
        assert_eq!(
            links_of(&engine, &third),
            json!({
                "run": {"schema": "Run", "id": "r1"},
                "plan": {"schema": "Plan", "id": "p1", "revision": 2, "latest": 2, "stale": false},
                "area": {"schema": "Area", "id": "a1"}
            })
        );
        // A link pinned to an earlier revision stamps that revision's map,
        // and children copy the pin.
        run(&engine, "r2", json!({"title": "Old plan"}));
        link(&engine, &nora(), "r2", "plan", "p1", Some(1)).expect("link");
        assert_eq!(
            Value::Object(edges(&engine, "r2")),
            json!({"first": [], "second": ["first"]})
        );
        let second = children_of(&engine, "r2").expect("children")[1].1.clone();
        assert_eq!(
            links_of(&engine, &second),
            json!({"run": {"schema": "Run", "id": "r2"}, "plan": {"schema": "Plan", "id": "p1", "revision": 1, "latest": 2, "stale": true}})
        );
    }
}

#[test]
fn a_create_that_gives_the_from_link_stamps_in_the_create_and_an_invalid_map_refuses_it() {
    for target in targets("a_create_that_gives_the_from_link") {
        let engine = definitions(&target);
        let created = create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Run", "topic": "public"}),
            Some("r1"),
            Some(json!({"Links": {"plan": "p1", "area": "a1"}})),
        )
        .expect("create");
        let keys: Vec<Value> = created
            .behavior_field("Blueprint", "children")
            .and_then(Value::as_array)
            .expect("children")
            .iter()
            .map(|child| child["key"].clone())
            .collect();
        assert_eq!(keys, vec![json!("first"), json!("second"), json!("third")]);
        assert_eq!(
            Value::Object(edges(&engine, "r1")),
            json!({"first": [], "second": ["first"], "third": ["second"]})
        );
        let first = children_of(&engine, "r1").expect("children")[0].1.clone();
        assert_eq!(
            links_of(&engine, &first),
            json!({
                "run": {"schema": "Run", "id": "r1"},
                "plan": {"schema": "Plan", "id": "p1", "revision": 2, "latest": 2, "stale": false},
                "area": {"schema": "Area", "id": "a1"}
            })
        );
        // A create pinned to an earlier revision stamps that revision's map.
        create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Old plan"}),
            Some("r2"),
            Some(json!({"Links": {"plan": {"id": "p1", "revision": 1}}})),
        )
        .expect("create");
        assert_eq!(
            Value::Object(edges(&engine, "r2")),
            json!({"first": [], "second": ["first"]})
        );
        // Stamped at its create, the run's from link cannot move.
        let moved = veto(link(&engine, &nora(), "r1", "plan", "p1", Some(1)));
        assert_eq!(
            (moved.0.as_str(), moved.3.as_deref()),
            ("Blueprint", Some("stamped"))
        );

        create(
            &engine,
            &alice(),
            "Plan",
            json!({"title": "Cyclic", "steps": {"a": {"after": ["b"]}, "b": {"after": ["a"]}}}),
            Some("bad"),
            None,
        )
        .expect("plan");
        let before = all_steps(&engine);
        let refused = veto(create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Doomed"}),
            Some("r3"),
            Some(json!({"Links": {"plan": "bad"}})),
        ));
        assert_eq!(
            (
                refused.0.as_str(),
                refused.1.as_str(),
                refused.2.as_str(),
                refused.3.as_deref()
            ),
            (
                "Blueprint",
                "create",
                "the steps of Plan bad revision 1 are invalid: the steps form a cycle: a -> b -> a",
                Some("invalid_steps")
            )
        );
        assert!(get(&engine, "Run", "r3").is_none());
        assert_eq!(all_steps(&engine), before);
    }
}

#[test]
fn a_later_revision_of_the_definition_changes_what_new_runs_stamp_not_a_stamped_runs_children() {
    for target in targets("a_later_revision_of_the_definition") {
        let engine = definitions(&target);
        run(&engine, "r1", json!({"title": "Run"}));
        link(&engine, &nora(), "r1", "plan", "p1", None).expect("link");
        engine
            .instances()
            .update(
                &alice(),
                "Plan",
                "p1",
                &json!({"steps": {"first": null, "second": null, "third": null, "only": {}}}),
                WriteOptions::default(),
            )
            .expect("update");
        run(&engine, "r2", json!({"title": "Later"}));
        link(&engine, &nora(), "r2", "plan", "p1", None).expect("link");
        assert_eq!(edge_keys(&engine, "r1"), ["first", "second", "third"]);
        assert_eq!(edge_keys(&engine, "r2"), ["only"]);
        let refused = veto(link(&engine, &nora(), "r1", "plan", "p1", None));
        assert_eq!(
            (refused.2.as_str(), refused.3.as_deref()),
            (
                "its children were stamped from the revision its link plan pins, so the link cannot move",
                Some("stamped")
            )
        );
        assert_eq!(all_steps(&engine), 4);
    }
}

#[test]
fn an_invalid_map_refuses_the_link_and_leaves_nothing_behind() {
    for target in targets("an_invalid_map_refuses_the_link") {
        let engine = definitions(&target);
        for (id, data) in [
            (
                "bad",
                json!({"title": "Cyclic", "steps": {"a": {"after": ["b"]}, "b": {"after": ["a"]}}}),
            ),
            (
                "odd",
                json!({"title": "Unknown field", "steps": {"a": {"when": {"field": "size", "equals": 1}}}}),
            ),
            ("none", json!({"title": "No steps"})),
            ("list", json!({"title": "A list", "steps": ["a"]})),
        ] {
            create(&engine, &alice(), "Plan", data, Some(id), None).expect("plan");
        }
        run(&engine, "r1", json!({"title": "Run"}));
        let vetoes: Vec<_> = ["bad", "odd", "none", "list"]
            .iter()
            .map(|id| veto(link(&engine, &nora(), "r1", "plan", id, None)))
            .collect();
        assert!(vetoes
            .iter()
            .all(|veto| veto.3.as_deref() == Some("invalid_steps")));
        let reasons: Vec<&str> = vetoes.iter().map(|veto| veto.2.as_str()).collect();
        assert_eq!(
            reasons,
            [
                "the steps of Plan bad revision 1 are invalid: the steps form a cycle: a -> b -> a",
                "the steps of Plan odd revision 1 are invalid: step a: when names size, which is not a field of Run (its fields: title, topic, flags)",
                "the steps of Plan none revision 1 are invalid: it has no steps",
                "the steps of Plan list revision 1 are invalid: a map of steps is a JSON object of steps by key",
            ]
        );
        let found = get(&engine, "Run", "r1").expect("the run");
        assert_eq!(
            json!([
                found.behaviors.get("Links"),
                found.behaviors.get("Blueprint")
            ]),
            json!([{}, {}])
        );
        assert_eq!(all_steps(&engine), 0);
    }
}

#[test]
fn from_must_be_a_link_pinned_to_a_revision_to_a_schema_with_revisions_and_the_field() {
    for target in targets("from_must_be_a_pinned_link") {
        let engine = definitions(&target);
        let refused = |config: Value, links: Value| {
            message_of(define(
                &engine,
                &document_of(
                    "Run",
                    run_fields(),
                    json!([{"name": "Links", "config": {"links": links}}, {"name": "Blueprint", "config": config}]),
                ),
            ))
        };
        for (config, links, expected) in [
            (with(from_config(), json!({"from": {"link": "spec", "field": "steps"}})), run_links(), "from: the type's Links has no link spec"),
            (from_config(), json!({"plan": {"schema": "Plan"}, "area": {"schema": "Area"}}), "from: link plan is not pinned"),
            (
                from_config(),
                json!({"plan": {"schema": "Plan", "pinned": "release"}, "area": {"schema": "Area"}}),
                "from: link plan pins a release, not the revision of the fields its steps are read from",
            ),
            (with(from_config(), json!({"from": {"link": "plan", "field": "title"}})), run_links(), "from: Plan's title holds string, not a map of steps"),
            (with(from_config(), json!({"from": {"link": "plan", "field": "body"}})), run_links(), "from: Plan has no field body"),
            (with(from_config(), json!({"copyLinks": ["run"]})), run_links(), "copyLinks names run, the link each child points at its parent through"),
            (with(from_config(), json!({"copyLinks": ["owner"]})), run_links(), "copyLinks: the type's Links has no link owner"),
            (
                with(from_config(), json!({"copyLinks": ["area"]})),
                json!({"plan": {"schema": "Plan", "pinned": true}, "area": {"schema": "Plan"}}),
                "copyLinks: schema Step's link area points at Area, and this type's at Plan",
            ),
            // Each link it reads holds one target.
            (
                from_config(),
                json!({"plan": {"schema": "Plan", "pinned": true, "many": true, "max": 2}, "area": {"schema": "Area"}}),
                "from: link plan holds a list of targets, and the steps are read from one",
            ),
            (
                from_config(),
                json!({"plan": {"schema": "Plan", "pinned": true}, "area": {"schema": "Area", "many": true, "max": 2}}),
                "copyLinks: link area holds a list of targets, and a stamp copies one",
            ),
        ] {
            let message = refused(config, links);
            assert!(message.contains(expected), "{message}\nwants: {expected}");
        }
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                step_behaviors(
                    json!({"run": {"schema": "Run", "required": true}, "plan": {"schema": "Plan", "pinned": true}, "area": {"schema": "Area", "many": true, "max": 2}}),
                    dependencies(),
                ),
            ),
        );
        let message = refused(from_config(), run_links());
        assert!(
            message.contains("copyLinks: schema Step's link area holds a list of targets, and a stamp copies one"),
            "{message}"
        );
    }
}

// Blueprint: children that are claimable work.

#[test]
#[ignore = "needs Lease (12a)"]
fn stamped_children_compose_queue_and_claim_next_claims_them_in_the_order_their_edges_give() {
    for target in targets("stamped_children_compose_queue") {
        let engine = open(&target);
        publish(
            &engine,
            &document_of(
                "Step",
                step_fields(),
                json!([
                    {"name": "Workflow", "config": step_flow()},
                    {"name": "Constants", "config": {"fields": ["step"]}},
                    {"name": "Dependencies"},
                    {"name": "Lease"},
                    {"name": "Queue", "config": {"claim": {"from": ["todo"], "to": "doing"}}},
                    {"name": "Links", "config": {"links": {"run": {"schema": "Run", "required": true}}}}
                ]),
            ),
        );
        let steps = json!({"a": {}, "b": {"after": ["a"]}, "c": {"after": ["a"]}, "d": {"after": ["b", "c"]}});
        publish(
            &engine,
            &document_of(
                "Run",
                run_fields(),
                json!([{"name": "Blueprint", "config": {"schema": "Step", "parentLink": "run", "keyField": "step", "steps": steps}}]),
            ),
        );
        run(&engine, "r1", json!({"title": "Work"}));
        let children = children_of(&engine, "r1").expect("children");
        let key_of = |id: &str| {
            children
                .iter()
                .find(|(_, child)| child == id)
                .map(|(key, _)| key.clone())
        };
        // claim claims the next step wren can, and says which, by key.
        let claim = || {
            let claimed = engine
                .instances()
                .invoke_schema(&wren(), "Step", "claimNext", &json!({}), None)
                .expect("claimNext")["claimed"]
                .clone();
            claimed["id"]
                .as_str()
                .map(|id| (key_of(id).unwrap_or_default(), id.to_owned()))
        };
        let finish = |step: &Option<(String, String)>| {
            let (_, id) = step.as_ref().expect("a step");
            engine
                .instances()
                .invoke(
                    &wren(),
                    "Step",
                    id,
                    "transition",
                    &json!({"to": "done"}),
                    WriteOptions::default(),
                )
                .expect("transition");
        };
        let a = claim();
        assert_eq!(a.as_ref().map(|(key, _)| key.as_str()), Some("a"));
        assert_eq!(claim(), None);
        finish(&a);
        // A finished blocker refreshes Queue's copies on the steps it held
        // up. b and c were created in one transaction, at one time, so Queue
        // orders them by id.
        let first = claim();
        let second = claim();
        let mut both = vec![
            first
                .as_ref()
                .map(|(key, _)| key.clone())
                .unwrap_or_default(),
            second
                .as_ref()
                .map(|(key, _)| key.clone())
                .unwrap_or_default(),
        ];
        both.sort();
        assert_eq!(both, ["b", "c"]);
        assert_eq!(claim(), None);
        finish(&first);
        assert_eq!(claim(), None);
        finish(&second);
        assert_eq!(claim().map(|(key, _)| key), Some("d".to_owned()));
    }
}

// Blueprint: one transaction, as the creator.

#[test]
fn a_creator_who_may_not_write_the_child_schema_is_refused_and_nothing_is_left() {
    for target in targets("a_creator_who_may_not_write") {
        let policy = RecordingPolicy::new(|request| {
            !(request.principal().subject == "nora"
                && request.action() == "write"
                && request.schema() == Some("Step"))
        });
        let engine = world_with(&target, inline(), json!([]), move |options| {
            options.policy = Some(Policy::custom(policy));
        });
        let refused = refusal(create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Mine"}),
            Some("r1"),
            None,
        ));
        assert_eq!(refused.code(), EngineErrorCode::Forbidden);
        assert!(get(&engine, "Run", "r1").is_none());
        assert_eq!(all_steps(&engine), 0);
        create(
            &engine,
            &alice(),
            "Run",
            json!({"title": "Theirs"}),
            Some("r2"),
            None,
        )
        .expect("run");
        assert_eq!(
            children_of(&engine, "r2").map(|children| children.len()),
            Some(4)
        );
    }
}

#[test]
fn children_are_created_in_the_parent_create_and_a_failure_at_the_last_rolls_back_every_one() {
    for target in targets("a_failure_at_the_last_child") {
        let steps =
            json!({"a": {}, "b": {"after": ["a"]}, "c": {"after": ["b"], "data": {"title": 42}}});
        let engine = world(&target, with(inline(), json!({"steps": steps})));
        let refused = refusal(create(
            &engine,
            &nora(),
            "Run",
            json!({"title": "Doomed"}),
            Some("r1"),
            None,
        ));
        assert_eq!(refused.code(), EngineErrorCode::InvalidInstance);
        assert!(get(&engine, "Run", "r1").is_none());
        assert_eq!(all_steps(&engine), 0);
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert!(events.iter().all(|event| event.instance_id.is_none()));
    }
}
