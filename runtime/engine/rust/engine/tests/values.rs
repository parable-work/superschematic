//! The value store, the cases of `test/values.test.ts` that need no
//! behavior: a top-level member whose JSON is longer than the threshold is
//! stored once by the SHA-256 of its canonical JSON, and the row and the
//! event keep a ref in its place; every read puts the value back; a get or
//! a list that asks for value refs returns the refs; a value is read by its
//! hash only through a schema of the namespace the caller may read that
//! references it; a write that leaves a large field alone neither hashes
//! nor rewrites it; a driver over other storage (D58: a `ValueDriver` the
//! deployment's crate implements) keeps no value a rolled-back write wrote,
//! and the sweep removes what a crash left; on SQLite and, with the
//! variable set, on Postgres.
//!
//! milestone 5: the behaviors' parts (a field reader, an operation's view,
//! Variants' validation, Search's index, before(), a behavior's own rows
//! through values.stow and values.release, Revisions). milestone 6: the
//! route and the MCP tool that read a value by its hash.

mod support;

use std::collections::{BTreeMap, BTreeSet};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::json::{canonical_json, sha256_hex};
use superschematic_engine::{
    AccessPolicy, AccessRequest, CreateOptions, DefineOptions, DriverError, Engine,
    EngineErrorCode, GetOptions, ListOptions, NamespaceOptions, Policy, Principal, ReadEvents,
    SchemaTarget, ValueDriver, ValueOptions, WriteOptions, DEFAULT_VALUE_THRESHOLD,
};
use support::{alice, code, refusal, repository_file, schema_document, targets, texts, Target};

/// The tests' threshold: the least an engine takes.
const THRESHOLD: u64 = 1024;

/// A VerifyResult of n checks: about 30 bytes of JSON each.
fn verify(n: usize, seed: &str) -> Value {
    let checks: Vec<Value> = (0..n)
        .map(|index| json!({"name": format!("{seed}-{index}"), "ok": index % 2 == 0}))
        .collect();
    json!({"passed": true, "checks": checks})
}

fn hash_of(value: &Value) -> String {
    sha256_hex(&canonical_json(value).unwrap())
}

fn ref_of(value: &Value) -> Value {
    json!({"$value": hash_of(value), "bytes": canonical_json(value).unwrap().len()})
}

/// The Variants fixture's Step without its behaviors, with a body field: a
/// result of any JSON, which a check of many items makes long.
fn steps() -> Value {
    let mut document: Value = serde_json::from_str(&repository_file(
        "internal/loader/testdata/services/fixture-variants-json/src/step.schema.json",
    ))
    .unwrap();
    let step = document["types"]["Step"].as_object_mut().unwrap();
    step.remove("behaviors");
    step["fields"]
        .as_array_mut()
        .unwrap()
        .push(json!({"name": "body", "typeRef": {"name": "string"}}));
    document
}

fn open(
    target: &Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Engine {
    target.open_with(|options| {
        options.values = ValueOptions {
            threshold_bytes: Some(THRESHOLD),
            ..ValueOptions::default()
        };
        configure(options);
    })
}

fn publish(engine: &Engine, document: &Value, namespace: Option<&str>) {
    engine
        .schemas()
        .define(
            &alice(),
            document,
            DefineOptions {
                namespace,
                source: None,
            },
        )
        .unwrap();
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().unwrap(),
            SchemaTarget { namespace },
        )
        .unwrap();
}

fn create(
    engine: &Engine,
    data: Value,
    id: &str,
    namespace: Option<&str>,
) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .create(
            &alice(),
            "Step",
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                behaviors: None,
            },
        )
        .unwrap()
}

/// The row of a step as stored: its data, parsed, its value_refs and the
/// length of its data.
fn row_of(engine: &Engine, id: &str) -> (Value, Option<Value>, usize) {
    let data = texts(engine.db(), &format!("SELECT data FROM engine_instances WHERE schema = 'Step' AND namespace = 'default' AND id = '{id}'"));
    let refs = texts(engine.db(), &format!("SELECT COALESCE(value_refs, 'null') FROM engine_instances WHERE schema = 'Step' AND namespace = 'default' AND id = '{id}'"));
    let refs: Value = serde_json::from_str(&refs[0]).unwrap();
    (
        serde_json::from_str(&data[0]).unwrap(),
        (!refs.is_null()).then_some(refs),
        data[0].len(),
    )
}

/// The values engine_payloads holds, by hash.
fn payloads(engine: &Engine) -> Vec<String> {
    let mut hashes = texts(engine.db(), "SELECT hash FROM engine_payloads");
    hashes.sort();
    hashes
}

/// Who holds a value: namespace, schema, holder, id and key, sorted.
fn holders(engine: &Engine, value: &Value) -> Vec<String> {
    let mut held = texts(
        engine.db(),
        &format!(
            "SELECT namespace || ' ' || schema || ' ' || holder || ' ' || id || ' ' || key FROM engine_payload_holders WHERE hash = '{}'",
            hash_of(value)
        ),
    );
    held.sort();
    held
}

fn step_events(engine: &Engine, id: &str) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Step"),
                instance_id: Some(id),
                ..ReadEvents::default()
            },
        )
        .unwrap()
        .events
}

/// A value driver over a map that counts its calls, outside the engine's
/// transactions, as a driver over other storage is; it lists its hashes
/// when `lists` is set.
#[derive(Default)]
struct MapDriver {
    values: Mutex<BTreeMap<String, String>>,
    reads: AtomicUsize,
    writes: AtomicUsize,
    removes: AtomicUsize,
    lists: bool,
}

impl MapDriver {
    fn keys(&self) -> Vec<String> {
        self.values.lock().unwrap().keys().cloned().collect()
    }

    fn counts(&self) -> (usize, usize, usize) {
        (
            self.reads.load(Ordering::SeqCst),
            self.writes.load(Ordering::SeqCst),
            self.removes.load(Ordering::SeqCst),
        )
    }
}

impl ValueDriver for MapDriver {
    fn read(&self, hash: &str) -> Result<Option<String>, DriverError> {
        self.reads.fetch_add(1, Ordering::SeqCst);
        Ok(self.values.lock().unwrap().get(hash).cloned())
    }

    fn write(&self, hash: &str, json: &str) -> Result<(), DriverError> {
        self.writes.fetch_add(1, Ordering::SeqCst);
        self.values
            .lock()
            .unwrap()
            .entry(hash.to_owned())
            .or_insert_with(|| json.to_owned());
        Ok(())
    }

    fn remove(&self, hash: &str) -> Result<(), DriverError> {
        self.removes.fetch_add(1, Ordering::SeqCst);
        self.values.lock().unwrap().remove(hash);
        Ok(())
    }

    fn list(&self, after: &str, limit: usize) -> Option<Result<Vec<String>, DriverError>> {
        self.lists.then(|| {
            Ok(self
                .values
                .lock()
                .unwrap()
                .keys()
                .filter(|hash| hash.as_str() > after)
                .take(limit)
                .cloned()
                .collect())
        })
    }
}

fn with_driver(
    driver: &Arc<MapDriver>,
    cache_bytes: Option<u64>,
) -> impl FnOnce(&mut superschematic_engine::EngineOptions) + '_ {
    move |options| {
        options.values = ValueOptions {
            threshold_bytes: Some(THRESHOLD),
            driver: Some(Arc::clone(driver) as Arc<dyn ValueDriver>),
            cache_bytes,
            ..ValueOptions::default()
        };
    }
}

#[test]
fn a_field_whose_json_is_longer_than_the_threshold_is_stored_once_by_hash_and_the_row_keeps_a_ref_in_its_place(
) {
    for target in targets("a_field_whose_json_is_longer_than_the_threshold") {
        let engine = open(&target, |_| {});
        publish(&engine, &steps(), None);
        let result = verify(60, "check");
        assert!(canonical_json(&result).unwrap().len() as u64 > THRESHOLD);
        let created = create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": result}),
            "s1",
            None,
        );
        assert_eq!(created.data["result"], result);
        assert_eq!(created.value_refs, None);
        let (data, refs, length) = row_of(&engine, "s1");
        assert_eq!(
            data,
            json!({"title": "Check", "kind": "verify", "result": ref_of(&result)})
        );
        assert_eq!(refs, Some(json!(["/result"])));
        assert!(length < 200, "the row holds {length} characters");
        assert_eq!(payloads(&engine), [hash_of(&result)]);
        assert_eq!(
            texts(engine.db(), "SELECT value FROM engine_payloads"),
            [canonical_json(&result).unwrap()]
        );
    }
}

#[test]
fn the_threshold_counts_utf8_bytes_a_field_at_it_stays_in_the_row_one_byte_past_it_goes_to_the_store(
) {
    for target in targets("the_threshold_counts_utf8_bytes") {
        let engine = open(&target, |_| {});
        publish(&engine, &steps(), None);
        let half = (THRESHOLD as usize - 2) / 2;
        let cases = [
            ("at", "a".repeat(THRESHOLD as usize - 2), false),
            ("past", "a".repeat(THRESHOLD as usize - 1), true),
            ("at-wide", "\u{e9}".repeat(half), false),
            ("past-wide", format!("{}a", "\u{e9}".repeat(half)), true),
        ];
        for (id, body, stored) in cases {
            create(
                &engine,
                json!({"title": id, "kind": "note", "body": body}),
                id,
                None,
            );
            assert_eq!(
                row_of(&engine, id).1,
                stored.then(|| json!(["/body"])),
                "{id}"
            );
            let got = engine
                .instances()
                .get(&alice(), "Step", id, GetOptions::default())
                .unwrap()
                .unwrap();
            assert_eq!(got.data["body"], body);
        }
    }
}

#[test]
fn the_default_threshold_is_64_kib_and_an_engine_refuses_a_threshold_under_1_kib() {
    for target in targets("the_default_threshold_is_64_kib") {
        let engine = target.open();
        assert_eq!(engine.values().threshold_bytes(), DEFAULT_VALUE_THRESHOLD);
        publish(&engine, &steps(), None);
        let under = verify(1900, "check");
        let over = verify(2300, "check");
        assert!(
            canonical_json(&under).unwrap().len() < 64 * 1024
                && canonical_json(&over).unwrap().len() > 64 * 1024
        );
        create(
            &engine,
            json!({"title": "Under", "kind": "verify", "result": under}),
            "under",
            None,
        );
        create(
            &engine,
            json!({"title": "Over", "kind": "verify", "result": over}),
            "over",
            None,
        );
        assert_eq!(row_of(&engine, "under").1, None);
        assert_eq!(row_of(&engine, "over").1, Some(json!(["/result"])));
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Step", "over", GetOptions::default())
                .unwrap()
                .unwrap()
                .data["result"],
            over
        );
        for threshold in [1023, 0] {
            let fresh = target.fresh();
            let refused = Engine::open({
                let mut options = fresh.options();
                options.values = ValueOptions {
                    threshold_bytes: Some(threshold),
                    ..ValueOptions::default()
                };
                options
            });
            assert!(matches!(
                refused,
                Err(superschematic_engine::Error::Usage(message)) if message == format!("values.thresholdBytes is an integer of at least 1024, got {threshold}")
            ));
        }
    }
}

#[test]
fn reads_put_the_value_back_and_value_refs_returns_the_refs() {
    for target in targets("reads_put_the_value_back") {
        let engine = open(&target, |_| {});
        publish(&engine, &steps(), None);
        let result = verify(60, "check");
        create(
            &engine,
            json!({"title": "Big", "kind": "verify", "result": result}),
            "s1",
            None,
        );
        create(
            &engine,
            json!({"title": "Small", "kind": "verify", "result": verify(2, "check")}),
            "s2",
            None,
        );
        let got = engine
            .instances()
            .get(&alice(), "Step", "s1", GetOptions::default())
            .unwrap()
            .unwrap();
        assert_eq!(got.data["result"], result);
        let page = engine
            .instances()
            .list(&alice(), "Step", ListOptions::default())
            .unwrap();
        assert_eq!(page.items[0].data["result"], result);
        assert_eq!(page.items[0].value_refs, None);

        let refs = engine
            .instances()
            .list(
                &alice(),
                "Step",
                ListOptions {
                    value_refs: true,
                    ..ListOptions::default()
                },
            )
            .unwrap();
        assert_eq!(refs.items[0].data["result"], ref_of(&result));
        assert_eq!(
            refs.items[0].value_refs.as_deref(),
            Some(&["/result".to_owned()][..])
        );
        assert_eq!(refs.items[1].data["result"], verify(2, "check"));
        assert_eq!(refs.items[1].value_refs, None);
        let one = engine
            .instances()
            .get(
                &alice(),
                "Step",
                "s1",
                GetOptions {
                    value_refs: true,
                    ..GetOptions::default()
                },
            )
            .unwrap()
            .unwrap();
        assert_eq!(one.data["result"], ref_of(&result));
        assert_eq!(
            serde_json::to_value(&one).unwrap()["valueRefs"],
            json!(["/result"])
        );
        // An update of another field validates the whole instance, its
        // value back in place, and keeps the value.
        let renamed = engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"title": "Renamed"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            (renamed.data["title"].as_str(), &renamed.data["result"]),
            (Some("Renamed"), &result)
        );
    }
}

#[test]
fn the_log_keeps_refs_a_creates_instance_and_an_updates_patch() {
    for target in targets("the_log_keeps_refs") {
        let engine = open(&target, |_| {});
        publish(&engine, &steps(), None);
        let (first, second) = (verify(60, "a"), verify(60, "b"));
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": first}),
            "s1",
            None,
        );
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"result": second}),
                WriteOptions::default(),
            )
            .unwrap();
        // Each ref is a pointer into the change: its own fields under data.
        let events = step_events(&engine, "s1");
        assert_eq!(
            events[0].change,
            json!({"data": {"title": "Check", "kind": "verify", "result": ref_of(&first)}, "behaviors": {}})
        );
        assert_eq!(
            events[0].value_refs.as_deref(),
            Some(&["/data/result".to_owned()][..])
        );
        assert_eq!(
            events[1].change,
            json!({"data": {"result": ref_of(&second)}})
        );
        assert_eq!(
            events[1].value_refs.as_deref(),
            Some(&["/data/result".to_owned()][..])
        );
        let longest = texts(
            engine.db(),
            "SELECT change FROM engine_events WHERE schema = 'Step' AND instance_id IS NOT NULL",
        )
        .iter()
        .map(String::len)
        .max()
        .unwrap();
        assert!(
            longest < 400,
            "the longest change holds {longest} characters"
        );
        let mut wanted = vec![hash_of(&first), hash_of(&second)];
        wanted.sort();
        assert_eq!(payloads(&engine), wanted);
        // The create's event holds the first value with the row, by its
        // cursor.
        assert_eq!(
            holders(&engine, &first),
            [format!("default Step event s1 {}", events[0].cursor)]
        );
    }
}

#[test]
fn one_value_written_twice_to_two_instances_and_in_two_namespaces_is_stored_once() {
    for target in targets("one_value_written_twice_is_stored_once") {
        let engine = open(&target, |options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into()],
                shared: None,
            }
        });
        let result = verify(60, "check");
        for namespace in ["east", "west"] {
            publish(&engine, &steps(), Some(namespace));
            create(
                &engine,
                json!({"title": "One", "kind": "verify", "result": result}),
                "s1",
                Some(namespace),
            );
            // The same value with its members in another order: one
            // canonical JSON.
            create(
                &engine,
                json!({"title": "Two", "kind": "verify", "result": {"checks": result["checks"], "passed": result["passed"]}}),
                "s2",
                Some(namespace),
            );
        }
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"result": result}),
                WriteOptions {
                    namespace: Some("east"),
                    ..WriteOptions::default()
                },
            )
            .unwrap();
        assert_eq!(payloads(&engine), [hash_of(&result)]);
        assert_eq!(
            holders(&engine, &result)
                .into_iter()
                .filter(|holder| holder.contains(" instance "))
                .collect::<Vec<_>>(),
            [
                "east Step instance s1 ",
                "east Step instance s2 ",
                "west Step instance s1 ",
                "west Step instance s2 ",
            ]
        );
    }
}

#[test]
fn a_write_that_leaves_the_large_field_alone_neither_hashes_nor_rewrites_it() {
    for target in targets("a_write_that_leaves_the_large_field_alone") {
        let driver = Arc::new(MapDriver::default());
        let engine = open(&target, with_driver(&driver, Some(0)));
        publish(&engine, &steps(), None);
        let result = verify(60, "check");
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": result}),
            "s1",
            None,
        );
        assert_eq!(driver.counts(), (0, 1, 0));
        // An update of another field reuses the ref: nothing is hashed or
        // written again.
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"title": "Renamed"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(row_of(&engine, "s1").0["result"], ref_of(&result));
        assert_eq!(driver.counts().1, 1);
        let got = engine
            .instances()
            .get(&alice(), "Step", "s1", GetOptions::default())
            .unwrap()
            .unwrap();
        assert_eq!(got.data["result"], result);
    }
}

#[test]
fn the_engine_keeps_the_values_it_read_last_in_memory_up_to_cache_bytes() {
    for target in targets("the_engine_keeps_the_values_it_read_last_in_memory") {
        let driver = Arc::new(MapDriver::default());
        let engine = open(&target, with_driver(&driver, None));
        publish(&engine, &steps(), None);
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": verify(60, "check")}),
            "s1",
            None,
        );
        engine
            .instances()
            .get(&alice(), "Step", "s1", GetOptions::default())
            .unwrap();
        engine
            .instances()
            .get(&alice(), "Step", "s1", GetOptions::default())
            .unwrap();
        engine
            .instances()
            .list(&alice(), "Step", ListOptions::default())
            .unwrap();
        assert_eq!(driver.counts().0, 1);
    }
}

// alice may do everything; reader may read Step in default; stranger may
// do anything with Other.
struct ValueReaders;

impl AccessPolicy for ValueReaders {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let subject = request.principal().subject.as_str();
        subject == "alice"
            || (subject == "reader"
                && request.action() == "read"
                && request.namespace() == "default"
                && request.schema() == Some("Step"))
            || (subject == "stranger" && request.schema() == Some("Other"))
    }
}

#[test]
fn a_value_is_read_by_its_hash_only_through_a_schema_of_the_namespace_the_caller_may_read_that_references_it(
) {
    for target in targets("a_value_is_read_by_its_hash_only_through_a_schema") {
        let engine = open(&target, |options| {
            options.policy = Some(Policy::custom(ValueReaders));
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            };
        });
        let reader = Principal::new("reader", Vec::<String>::new());
        let stranger = Principal::new("stranger", Vec::<String>::new());
        publish(&engine, &steps(), None);
        publish(
            &engine,
            &schema_document(
                "Other",
                json!([{"name": "title", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        publish(&engine, &steps(), Some("east"));
        let (held, moved, eastern) = (verify(60, "held"), verify(60, "moved"), verify(60, "east"));
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": held}),
            "s1",
            None,
        );
        create(
            &engine,
            json!({"title": "East", "kind": "verify", "result": eastern}),
            "e1",
            Some("east"),
        );

        let values = engine.values();
        let got = values.get(&alice(), &hash_of(&held), None).unwrap();
        assert_eq!(
            serde_json::to_value(&got).unwrap(),
            json!({"hash": hash_of(&held), "bytes": ref_of(&held)["bytes"], "value": held})
        );
        assert_eq!(
            values.get(&reader, &hash_of(&held), None).unwrap().value,
            held
        );
        let unknown = sha256_hex("nothing");
        for (principal, hash, namespace) in [
            // Knowing the hash grants nothing: a schema the caller may not
            // read references it, and the refusal is a missing value's.
            (&stranger, hash_of(&held), "default"),
            (&alice(), unknown.clone(), "default"),
            // A value another namespace holds is not this one's, whoever
            // asks.
            (&alice(), hash_of(&eastern), "default"),
            (&alice(), hash_of(&held), "east"),
            (&reader, hash_of(&eastern), "east"),
        ] {
            let refused = refusal(values.get(principal, &hash, Some(namespace)));
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::NotFound,
                    format!(
                        "namespace {namespace} holds no value {hash} that {} may read",
                        principal.subject
                    )
                    .as_str()
                )
            );
        }
        assert_eq!(
            values
                .get(&alice(), &hash_of(&eastern), Some("east"))
                .unwrap()
                .value,
            eastern
        );
        let refused = refusal(values.get(&alice(), "ABC", None));
        assert_eq!(
            (refused.code(), refused.message()),
            (EngineErrorCode::InvalidArgument, "a value's hash is the SHA-256 of its canonical JSON, 64 lowercase hex digits, not \"ABC\"")
        );
        // A value only the log holds now is still read through its schema.
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"result": moved}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            values.get(&reader, &hash_of(&held), None).unwrap().value,
            held
        );
        engine
            .instances()
            .delete(&alice(), "Step", "s1", WriteOptions::default())
            .unwrap();
        assert_eq!(
            values.get(&reader, &hash_of(&moved), None).unwrap().value,
            moved
        );
    }
}

#[test]
fn an_instances_old_value_stays_while_an_event_holds_it() {
    for target in targets("an_instances_old_value_stays_while_an_event_holds_it") {
        let engine = open(&target, |_| {});
        publish(&engine, &steps(), None);
        let (x, z) = (verify(60, "x"), verify(60, "z"));
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": x}),
            "s1",
            None,
        );
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({"result": z}),
                WriteOptions::default(),
            )
            .unwrap();
        let mut both = vec![hash_of(&x), hash_of(&z)];
        both.sort();
        assert_eq!(payloads(&engine), both);
        let kinds = |value: &Value| -> BTreeSet<String> {
            holders(&engine, value)
                .iter()
                .map(|holder| holder.split(' ').nth(2).unwrap().to_owned())
                .collect()
        };
        assert_eq!(kinds(&x), BTreeSet::from(["event".to_owned()]));
        engine
            .instances()
            .delete(&alice(), "Step", "s1", WriteOptions::default())
            .unwrap();
        assert_eq!(kinds(&z), BTreeSet::from(["event".to_owned()]));
        assert_eq!(payloads(&engine), both);
    }
}

#[test]
fn a_driver_outside_the_transaction_keeps_no_value_a_rolled_back_write_wrote() {
    for target in targets("a_driver_outside_the_transaction_keeps_no_value") {
        let driver = Arc::new(MapDriver::default());
        let engine = open(&target, with_driver(&driver, None));
        let mut document = steps();
        document["types"]["Step"]["fields"][0]["unique"] = json!(true);
        publish(&engine, &document, None);
        let (x, y) = (verify(60, "x"), verify(60, "y"));
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": x}),
            "s1",
            None,
        );
        // The second create stows y through the driver, then the unique
        // title refuses it and the write rolls back: y, which nothing
        // holds, goes once the transaction has ended.
        let refused = engine.instances().create(
            &alice(),
            "Step",
            &json!({"title": "Check", "kind": "verify", "result": y}),
            CreateOptions {
                id: Some("s2"),
                ..CreateOptions::default()
            },
        );
        assert_eq!(code(refused), EngineErrorCode::Conflict);
        assert_eq!(driver.keys(), [hash_of(&x)]);
        assert_eq!(driver.counts().2, 1);
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Step", "s1", GetOptions::default())
                .unwrap()
                .unwrap()
                .data["result"],
            x
        );
        // A committed write's values stay.
        create(
            &engine,
            json!({"title": "Other", "kind": "verify", "result": y}),
            "s3",
            None,
        );
        let mut both = vec![hash_of(&x), hash_of(&y)];
        both.sort();
        assert_eq!(driver.keys(), both);
    }
}

#[test]
fn the_sweep_removes_what_a_crash_left_in_a_driver_that_lists_its_hashes_and_refuses_a_driver_that_does_not(
) {
    for target in targets("the_sweep_removes_what_a_crash_left") {
        let driver = Arc::new(MapDriver {
            lists: true,
            ..MapDriver::default()
        });
        let engine = open(&target, with_driver(&driver, None));
        publish(&engine, &steps(), None);
        let x = verify(60, "x");
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": x}),
            "s1",
            None,
        );
        // A value a crash left between the driver's write and the end of
        // its transaction: stored, held by nothing.
        driver
            .values
            .lock()
            .unwrap()
            .insert("0".repeat(64), "\"left behind\"".into());
        assert_eq!(engine.values().sweep().unwrap().removed, 1);
        assert_eq!(driver.keys(), [hash_of(&x)]);
        assert_eq!(engine.values().sweep().unwrap().removed, 0);
        // engine_payloads lists its hashes too, and leaves none behind.
        let (first, second) = (target.fresh(), target.fresh());
        let plain = open(&first, |_| {});
        assert_eq!(plain.values().sweep().unwrap().removed, 0);
        let unlisted = open(&second, with_driver(&Arc::new(MapDriver::default()), None));
        assert!(matches!(
            unlisted.values().sweep(),
            Err(superschematic_engine::Error::Usage(message)) if message == "values.sweep needs a driver that lists its hashes (ValueDriver.list)"
        ));
    }
}

#[test]
fn a_value_longer_than_max_bytes_is_refused_with_value_too_large_where_it_would_be_stored() {
    for target in targets("a_value_longer_than_max_bytes_is_refused") {
        let fresh = target.fresh();
        let refused = Engine::open({
            let mut options = fresh.options();
            options.values = ValueOptions {
                threshold_bytes: Some(2048),
                max_bytes: Some(1024),
                ..ValueOptions::default()
            };
            options
        });
        assert!(matches!(
            refused,
            Err(superschematic_engine::Error::Usage(message)) if message == "values.maxBytes is an integer of at least values.thresholdBytes (2048), got 1024"
        ));
        let engine = open(&target, |options| options.values.max_bytes = Some(4096));
        assert_eq!(engine.values().max_bytes(), 4096);
        publish(&engine, &steps(), None);
        let big = verify(500, "x");
        let refused = refusal(engine.instances().create(
            &alice(),
            "Step",
            &json!({"title": "big", "kind": "verify", "result": big}),
            CreateOptions::default(),
        ));
        assert_eq!(
            (refused.code(), refused.name(), refused.status()),
            (EngineErrorCode::ValueTooLarge, "ValueTooLargeError", 413)
        );
        let details = refused.details().unwrap();
        assert_eq!(
            (details["path"].as_str(), details["maxBytes"].as_u64()),
            (Some("/result"), Some(4096))
        );
        assert!(details["bytes"].as_u64().unwrap() > 4096);
        assert!(engine
            .instances()
            .list(&alice(), "Step", ListOptions::default())
            .unwrap()
            .items
            .is_empty());
        // One under the most is stored, by hash.
        create(
            &engine,
            json!({"title": "fits", "kind": "verify", "result": verify(60, "x")}),
            "fits",
            None,
        );
        assert_eq!(row_of(&engine, "fits").1, Some(json!(["/result"])));
    }
}
