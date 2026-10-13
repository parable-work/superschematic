//! Namespaces, the cases of `test/namespaces.test.ts`: independent version
//! lines, the lookup through the shared namespace, a name that lives on one
//! side of that lookup only, and the namespaces a create makes while the
//! engine runs, which an archive freezes; on SQLite and, with the variable
//! set, on Postgres.
//!
//! milestone 4b: what the access policy answers (the TypeScript cases'
//! `managing` policy: who may create, archive and list, the questions it is
//! asked, a refused caller learning nothing), and an archived namespace
//! refusing an instance's create, update, delete, operation and tool while
//! its instances and events read on. milestone 6: the runner, which runs
//! nothing in an archived namespace.

mod support;

use serde_json::json;

use superschematic_engine::{
    Clock, DefineOptions, Engine, EngineErrorCode, NamespaceConfig, NamespaceOptions,
    NamespaceOrigin, NamespaceRecord, NamespaceState, Principal, SchemaSummary, SchemaTarget,
};
use support::{alice, code, order_document, refusal, schema_document, targets, with};

fn note_document() -> serde_json::Value {
    schema_document(
        "Note",
        json!([{"name": "body", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

fn at(namespace: &str) -> SchemaTarget<'_> {
    SchemaTarget {
        namespace: Some(namespace),
    }
}

fn define_in(namespace: &str) -> DefineOptions<'_> {
    DefineOptions {
        namespace: Some(namespace),
        source: None,
    }
}

fn names(list: &[&str]) -> Vec<String> {
    list.iter().map(|name| (*name).to_owned()).collect()
}

fn options(list: &[&str], shared: Option<&str>) -> NamespaceOptions {
    NamespaceOptions {
        names: names(list),
        shared: shared.map(str::to_owned),
    }
}

/// publish defines and publishes a document as alice, in a namespace.
fn publish(engine: &Engine, document: &serde_json::Value, namespace: &str) {
    engine
        .schemas()
        .define(&alice(), document, define_in(namespace))
        .unwrap();
    engine
        .schemas()
        .publish(&alice(), document["name"].as_str().unwrap(), at(namespace))
        .unwrap();
}

#[test]
fn there_is_one_namespace_default_unless_more_are_configured() {
    assert_eq!(NamespaceConfig::default().configured(), ["default"]);
    let config = NamespaceConfig::new(&options(&["east", "west", "east"], None)).unwrap();
    assert_eq!(config.configured(), ["default", "east", "west"]);
    assert_eq!(config.shared(), None);
    assert_eq!(config.lookup("east"), ["east"]);
    for target in targets("there_is_one_namespace_default_unless_more_are_configured") {
        let engine = target.open();
        assert_eq!(engine.namespaces().resolve(None).unwrap(), "default");
        assert_eq!(engine.namespaces().names().unwrap(), ["default"]);
    }
}

#[test]
fn namespace_names_are_checked_and_the_shared_namespace_must_be_one_of_them() {
    for name in ["East", "1east", "east_side", "", &"e".repeat(64)] {
        let refused = NamespaceConfig::new(&options(&[name], None)).unwrap_err();
        assert!(refused.to_string().contains("must match"), "{refused}");
    }
    assert_eq!(
        NamespaceConfig::new(&options(&["east"], Some("common")))
            .unwrap_err()
            .to_string(),
        "the shared namespace \"common\" is not one of the namespaces (default, east)"
    );
    assert_eq!(
        NamespaceConfig::new(&options(&[], Some("default")))
            .unwrap()
            .lookup("default"),
        ["default"]
    );
    // An engine refuses the options before its storage opens.
    for target in
        targets("namespace_names_are_checked_and_the_shared_namespace_must_be_one_of_them")
    {
        let mut engine_options = target.options();
        engine_options.namespaces = options(&["East"], None);
        assert!(matches!(
            Engine::open(engine_options),
            Err(superschematic_engine::Error::Usage(message)) if message.contains("must match")
        ));
    }
}

#[test]
fn a_call_to_a_namespace_that_is_not_configured_is_unknown_namespace() {
    for target in targets("a_call_to_a_namespace_that_is_not_configured_is_unknown_namespace") {
        let engine = target.open();
        let refused = refusal(engine.schemas().define(
            &alice(),
            order_document(),
            define_in("east"),
        ));
        assert_eq!(refused.code(), EngineErrorCode::UnknownNamespace);
        assert_eq!(refused.message(), "unknown namespace \"east\"");
        assert_eq!(
            code(engine.schemas().list(&alice(), at("east"))),
            EngineErrorCode::UnknownNamespace
        );
    }
}

#[test]
fn the_same_name_publishes_in_two_namespaces_without_sharing_versions() {
    for target in targets("the_same_name_publishes_in_two_namespaces_without_sharing_versions") {
        let engine =
            target.open_with(|engine| engine.namespaces = options(&["east", "west"], None));
        for (index, namespace) in ["east", "west", "east"].iter().enumerate() {
            let document = with(
                &order_document(),
                "description",
                json!(format!("release {index}")),
            );
            engine
                .schemas()
                .define(&alice(), &document, define_in(namespace))
                .unwrap();
            engine
                .schemas()
                .publish(&alice(), "Order", at(namespace))
                .unwrap();
        }
        let live = |namespace: Option<&str>| {
            engine
                .schemas()
                .live(&alice(), "Order", SchemaTarget { namespace })
                .unwrap()
        };
        assert_eq!(live(Some("east")).unwrap().version, Some(2));
        assert_eq!(live(Some("west")).unwrap().version, Some(1));
        assert_eq!(live(Some("west")).unwrap().namespace, "west");
        assert_eq!(live(None), None);
        assert_eq!(
            engine.schemas().list(&alice(), at("west")).unwrap(),
            [SchemaSummary {
                namespace: "west".into(),
                name: "Order".into(),
                live_version: Some(1),
                has_draft: false
            }]
        );
    }
}

fn shared_options() -> NamespaceOptions {
    options(&["east", "west", "common"], Some("common"))
}

#[test]
fn a_namespace_reaches_its_own_schemas_then_the_shared_ones() {
    for target in targets("a_namespace_reaches_its_own_schemas_then_the_shared_ones") {
        let engine = target.open_with(|engine| engine.namespaces = shared_options());
        publish(&engine, &note_document(), "common");
        publish(&engine, &order_document(), "east");

        let note = engine
            .schemas()
            .live(&alice(), "Note", at("east"))
            .unwrap()
            .unwrap();
        assert_eq!(note.namespace, "common");
        assert_eq!(note.version, Some(1));
        assert!(engine
            .schemas()
            .validate(
                &alice(),
                "Note",
                &json!({"body": "hello"}),
                superschematic_engine::ValidateOptions {
                    namespace: Some("east"),
                    version: None
                }
            )
            .unwrap()
            .is_empty());
        let summary = |namespace: &str, name: &str| SchemaSummary {
            namespace: namespace.into(),
            name: name.into(),
            live_version: Some(1),
            has_draft: false,
        };
        assert_eq!(
            engine.schemas().list(&alice(), at("east")).unwrap(),
            [summary("common", "Note"), summary("east", "Order")]
        );
        // The shared namespace does not reach the others.
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", at("common"))
                .unwrap(),
            None
        );
        assert_eq!(
            engine
                .schemas()
                .list(&alice(), at("common"))
                .unwrap()
                .into_iter()
                .map(|summary| summary.name)
                .collect::<Vec<_>>(),
            ["Note"]
        );
    }
}

#[test]
fn a_namespace_cannot_define_a_name_the_shared_namespace_holds() {
    for target in targets("a_namespace_cannot_define_a_name_the_shared_namespace_holds") {
        let engine = target.open_with(|engine| engine.namespaces = shared_options());
        engine
            .schemas()
            .define(&alice(), note_document(), define_in("common"))
            .unwrap();
        let refused = refusal(engine.schemas().define(
            &alice(),
            note_document(),
            define_in("east"),
        ));
        assert_eq!(refused.code(), EngineErrorCode::NameTaken);
        assert_eq!(
            refused.message(),
            "schema Note is defined in the shared namespace common, which namespace east looks names up in; use another name"
        );
        assert_eq!(
            engine
                .schemas()
                .draft(&alice(), "Note", at("east"))
                .unwrap()
                .unwrap()
                .namespace,
            "common"
        );
    }
}

#[test]
fn the_shared_namespace_cannot_define_a_name_another_namespace_holds_and_is_not_told_which() {
    for target in targets(
        "the_shared_namespace_cannot_define_a_name_another_namespace_holds_and_is_not_told_which",
    ) {
        let engine = target.open_with(|engine| engine.namespaces = shared_options());
        engine
            .schemas()
            .define(&alice(), note_document(), define_in("west"))
            .unwrap();
        let refused = refusal(engine.schemas().define(
            &alice(),
            note_document(),
            define_in("common"),
        ));
        assert_eq!(refused.code(), EngineErrorCode::NameTaken);
        assert_eq!(
            refused.message(),
            "schema Note is defined in a namespace that looks names up in the shared namespace common; use another name"
        );
        assert!(!refused.message().contains("west"));
    }
}

#[test]
fn the_default_namespace_also_looks_names_up_in_the_shared_one() {
    for target in targets("the_default_namespace_also_looks_names_up_in_the_shared_one") {
        let engine = target.open_with(|engine| engine.namespaces = shared_options());
        publish(&engine, &note_document(), "common");
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Note", SchemaTarget::default())
                .unwrap()
                .unwrap()
                .namespace,
            "common"
        );
    }
}

fn ada() -> Principal {
    Principal::new("ada", ["namespaces"])
}

fn created(name: &str, at: i64, by: &str) -> NamespaceRecord {
    NamespaceRecord {
        name: name.into(),
        origin: NamespaceOrigin::Created,
        shared: false,
        state: NamespaceState::Active,
        created_at: Some(at),
        created_by: Some(by.into()),
        archived_at: None,
        archived_by: None,
    }
}

#[test]
fn create_list_and_get_and_the_next_engine_on_the_storage_has_them() {
    for target in targets("create_list_and_get_and_the_next_engine_on_the_storage_has_them") {
        let configure = |engine: &mut superschematic_engine::EngineOptions| {
            engine.namespaces = options(&["common"], Some("common"));
            engine.clock = Clock::Manual(5_000);
        };
        let engine = target.open_with(configure);
        assert_eq!(
            engine.namespaces().create(&ada(), "acme").unwrap(),
            created("acme", 5_000, "ada")
        );
        engine.namespaces().create(&ada(), "tom").unwrap();
        assert_eq!(
            engine.namespaces().names().unwrap(),
            ["default", "common", "acme", "tom"]
        );
        let listed: Vec<(String, NamespaceOrigin, bool, NamespaceState)> = engine
            .namespaces()
            .list(&ada())
            .unwrap()
            .into_iter()
            .map(|record| (record.name, record.origin, record.shared, record.state))
            .collect();
        assert_eq!(
            listed,
            [
                (
                    "default".into(),
                    NamespaceOrigin::Configured,
                    false,
                    NamespaceState::Active
                ),
                (
                    "common".into(),
                    NamespaceOrigin::Configured,
                    true,
                    NamespaceState::Active
                ),
                (
                    "acme".into(),
                    NamespaceOrigin::Created,
                    false,
                    NamespaceState::Active
                ),
                (
                    "tom".into(),
                    NamespaceOrigin::Created,
                    false,
                    NamespaceState::Active
                ),
            ]
        );
        assert_eq!(engine.namespaces().get(&ada(), "tom").unwrap().name, "tom");
        assert_eq!(
            code(engine.namespaces().get(&ada(), "nowhere")),
            EngineErrorCode::UnknownNamespace
        );

        // A created namespace holds its own schemas and reaches the shared
        // ones.
        publish(&engine, &note_document(), "common");
        publish(&engine, &order_document(), "acme");
        let reached: Vec<(String, String)> = engine
            .schemas()
            .list(&ada(), at("acme"))
            .unwrap()
            .into_iter()
            .map(|summary| (summary.namespace, summary.name))
            .collect();
        assert_eq!(
            reached,
            [
                ("common".into(), "Note".into()),
                ("acme".into(), "Order".into())
            ]
        );
        assert_eq!(
            code(
                engine
                    .schemas()
                    .define(&ada(), note_document(), define_in("acme"))
            ),
            EngineErrorCode::NameTaken
        );
        drop(engine);

        let reopened =
            target.open_with(|engine| engine.namespaces = options(&["common"], Some("common")));
        assert_eq!(
            reopened.namespaces().names().unwrap(),
            ["default", "common", "acme", "tom"]
        );
        assert_eq!(
            reopened
                .namespaces()
                .get(&ada(), "acme")
                .unwrap()
                .created_at,
            Some(5_000)
        );
        assert_eq!(
            reopened
                .schemas()
                .live(&ada(), "Order", at("acme"))
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
    }
}

#[test]
fn a_create_is_refused_for_a_name_that_is_not_one_and_a_name_that_is_a_namespace() {
    for target in
        targets("a_create_is_refused_for_a_name_that_is_not_one_and_a_name_that_is_a_namespace")
    {
        let engine = target.open_with(|engine| engine.namespaces = options(&["east"], None));
        for name in ["East", "1east", "east_side", "", &"e".repeat(64)] {
            let refused = refusal(engine.namespaces().create(&ada(), name));
            assert_eq!(refused.code(), EngineErrorCode::InvalidArgument, "{name}");
            assert_eq!(
                refused.message(),
                format!(
                    "a namespace name is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters, not {}",
                    superschematic_engine::json::quote(name)
                )
            );
        }
        engine.namespaces().create(&ada(), "west").unwrap();
        for name in ["default", "east", "west"] {
            let refused = refusal(engine.namespaces().create(&ada(), name));
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::Conflict,
                    format!("namespace {name} exists").as_str()
                )
            );
        }
    }
}

#[test]
fn archive_and_unarchive_only_a_created_namespace_and_again_changes_nothing() {
    for target in
        targets("archive_and_unarchive_only_a_created_namespace_and_again_changes_nothing")
    {
        let engine = target.open_with(|engine| {
            engine.namespaces = options(&["east"], None);
            engine.clock = Clock::Manual(7_000);
        });
        engine.namespaces().create(&ada(), "acme").unwrap();
        engine.clock().set(8_000).unwrap();
        let archived = engine.namespaces().archive(&ada(), "acme").unwrap();
        assert_eq!(
            (
                archived.state,
                archived.archived_at,
                archived.archived_by.as_deref()
            ),
            (NamespaceState::Archived, Some(8_000), Some("ada"))
        );
        assert!(engine.namespaces().archived("acme").unwrap());
        engine.clock().set(9_000).unwrap();
        assert_eq!(
            engine
                .namespaces()
                .archive(&ada(), "acme")
                .unwrap()
                .archived_at,
            Some(8_000),
            "archiving an archived namespace changes nothing"
        );
        for name in ["default", "east"] {
            let refused = refusal(engine.namespaces().archive(&ada(), name));
            assert_eq!(refused.code(), EngineErrorCode::Conflict);
            assert_eq!(
                refused.message(),
                format!("namespace {name} is configured by the engine's options, which alone change it; only a namespace a create made is archived")
            );
        }
        assert_eq!(
            refusal(engine.namespaces().unarchive(&ada(), "east")).message(),
            "namespace east is configured by the engine's options, which alone change it; only a namespace a create made is unarchived"
        );
        assert_eq!(
            code(engine.namespaces().archive(&ada(), "nowhere")),
            EngineErrorCode::UnknownNamespace
        );
        let active = engine.namespaces().unarchive(&ada(), "acme").unwrap();
        assert_eq!(
            (active.state, active.archived_at, active.archived_by),
            (NamespaceState::Active, None, None)
        );
        assert_eq!(
            engine.namespaces().unarchive(&ada(), "acme").unwrap().state,
            NamespaceState::Active
        );
    }
}

#[test]
fn an_archived_namespace_is_read_as_it_was_and_refuses_every_write() {
    for target in targets("an_archived_namespace_is_read_as_it_was_and_refuses_every_write") {
        let engine = target.open();
        engine.namespaces().create(&alice(), "acme").unwrap();
        publish(&engine, &order_document(), "acme");
        engine
            .schemas()
            .define(
                &alice(),
                with(&order_document(), "description", json!("a draft")),
                define_in("acme"),
            )
            .unwrap();
        engine.namespaces().archive(&alice(), "acme").unwrap();

        let refusals = [
            (
                "define",
                refusal(
                    engine
                        .schemas()
                        .define(&alice(), note_document(), define_in("acme")),
                ),
            ),
            (
                "publish",
                refusal(engine.schemas().publish(&alice(), "Order", at("acme"))),
            ),
        ];
        for (what, refused) in refusals {
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::NamespaceArchived,
                    "namespace acme is archived: it is read as it was and refuses every write until it is unarchived"
                ),
                "{what}"
            );
        }
        // Reads go on.
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", at("acme"))
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
        assert_eq!(
            engine
                .schemas()
                .draft(&alice(), "Order", at("acme"))
                .unwrap()
                .unwrap()
                .document["description"],
            "a draft"
        );
        // Its capabilities answer every write false.
        let capabilities = engine.schemas().capabilities(&alice(), at("acme")).unwrap();
        assert_eq!(
            capabilities
                .iter()
                .map(|entry| (entry.read, entry.write, entry.define, entry.publish))
                .collect::<Vec<_>>(),
            [(true, false, false, false)]
        );

        // Unarchived, it writes again.
        engine.namespaces().unarchive(&alice(), "acme").unwrap();
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "Order", at("acme"))
                .unwrap()
                .version,
            2
        );
    }
}

#[test]
fn a_namespace_the_options_configure_is_configured_whatever_the_storage_holds_for_it() {
    for target in
        targets("a_namespace_the_options_configure_is_configured_whatever_the_storage_holds_for_it")
    {
        let first = target.open();
        first.namespaces().create(&alice(), "acme").unwrap();
        first.namespaces().archive(&alice(), "acme").unwrap();
        drop(first);
        let engine = target.open_with(|engine| engine.namespaces = options(&["acme"], None));
        assert_eq!(
            engine.namespaces().get(&alice(), "acme").unwrap(),
            NamespaceRecord {
                name: "acme".into(),
                origin: NamespaceOrigin::Configured,
                shared: false,
                state: NamespaceState::Active,
                created_at: None,
                created_by: None,
                archived_at: None,
                archived_by: None,
            }
        );
        engine
            .schemas()
            .define(&alice(), order_document(), define_in("acme"))
            .unwrap();
    }
}

#[test]
fn a_namespace_another_engine_creates_or_archives_is_seen_at_once() {
    // Two engines on one storage: two processes on Postgres, two
    // connections on SQLite. Neither keeps the created namespaces in
    // memory, so what one commits the other's next call reads.
    for target in targets("a_namespace_another_engine_creates_or_archives_is_seen_at_once") {
        let first = target.open();
        let second = target.open();
        first.namespaces().create(&alice(), "acme").unwrap();
        assert_eq!(second.namespaces().names().unwrap(), ["default", "acme"]);
        second
            .schemas()
            .define(&alice(), order_document(), define_in("acme"))
            .unwrap();
        first.namespaces().archive(&alice(), "acme").unwrap();
        assert_eq!(
            code(second.schemas().publish(&alice(), "Order", at("acme"))),
            EngineErrorCode::NamespaceArchived
        );
        assert_eq!(
            code(second.namespaces().create(&alice(), "acme")),
            EngineErrorCode::Conflict
        );
    }
}
