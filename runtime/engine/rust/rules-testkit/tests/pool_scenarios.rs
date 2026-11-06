//! The hand-written scenarios of D104's step 3 Pool (`core-pool.json`)
//! replayed on the core: pick, an operation of the table as a whole run as
//! a command without a row, through each tier (the open members prefer
//! keeps, then its near ones, then the other open and near ones, by order
//! and then key), with match, prefer and exclude, a null member of match
//! absent, and with none open its until; markOut with and without until,
//! the later kept and a repeat answered unchanged; reportRoom before and
//! after outUntil, with and without room, near and back; the recheck at
//! outUntil, its event and its arming again every recheckAfterMs, moved by
//! a check without room and cleared by one with room; a member whose Budget
//! meter has no room counted out through Capacity until its period rolls
//! over or a provider's figure lapses; each refusal; and the define
//! refusals, which no engine recorded, since none implements Pool. Every
//! step must answer as the file writes, the events each step's commands
//! wrote among it, and the steps the replayer maps must not drop below the
//! floor.

use serde_json::{json, Map};

use superschematic_rules::{Action, Caller, Command, FieldSpec, TypeSpec, UseSpec};
use superschematic_rules_testkit::{hand_written_path, ported, replay_path, MemoryHost};

/// The steps of `core-pool.json` the replayer maps and compares: all 91 of
/// its 5 scenarios, the 5 `open` steps among them, which count and compare
/// nothing, the 22 picks, each run as a command without a row on Pool's
/// use, and the runDues, which count once the deadlines they ran answered,
/// with the events they wrote.
const FLOOR: usize = 91;

#[test]
fn core_pool_replays_every_step_on_the_reference_host() {
    let report = replay_path(&hand_written_path("core-pool"), &ported)
        .unwrap_or_else(|error| panic!("{error}"));
    println!("{}", report.details());
    assert!(
        report.mismatches().is_empty(),
        "the core answered otherwise than the file writes:\n{}",
        report.details()
    );
    assert!(
        report.checked() >= FLOOR,
        "{} steps checked, under the floor of {FLOOR}:\n{}",
        report.checked(),
        report.details()
    );
}

/// A UUID field's value matches in either written form on the reference
/// host, as each adapter's uuid column binds both: an owner written
/// hyphenated is picked by its base62 wire form, which the core asks the
/// host for, and one written in base62 by its hyphenated form.
#[test]
fn a_pick_matches_a_uuid_written_in_either_form_on_the_reference_host() {
    const BASE62: &str = "TMXp8QPR6yGvKuV2GZQ10";
    const HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
    let alice = Caller::new("alice", Vec::<String>::new());
    let mut host = MemoryHost::new(superschematic_rules_behaviors::core());
    let spec = TypeSpec::new("Account")
        .with(UseSpec::on_type("Pool", json!({"order": "rank"})))
        .with_field(FieldSpec::new("owner", "Identity.UUID"))
        .with_field(FieldSpec::new("rank", "Generic.Int64"));
    host.define(spec, &alice, 0).expect("Account defines");
    host.publish("Account", &alice, 0)
        .expect("Account publishes");
    for (key, owner, rank) in [("a1", HYPHENATED, 1), ("a2", BASE62, 2), ("a3", "x", 0)] {
        let mut own = Map::new();
        own.insert("owner".into(), json!(owner));
        own.insert("rank".into(), json!(rank));
        let create = Action::Create {
            id: Some(key.into()),
            own,
            params: Map::new(),
        };
        host.execute("Account", Command::new(alice.clone(), 0, create))
            .expect("the account is created");
    }
    let mut pick = |owner: &str, exclude: &[&str]| {
        let action = Action::TableOperation {
            use_id: "Pool".into(),
            operation: "pick".into(),
            params: json!({"match": {"owner": owner}, "exclude": exclude}),
        };
        let applied = host
            .execute("Account", Command::new(alice.clone(), 0, action))
            .expect("the pick answers");
        applied.changes.result["member"]["key"].clone()
    };
    assert_eq!(pick(HYPHENATED, &[]), json!("a1"));
    assert_eq!(pick(BASE62, &["a1"]), json!("a2"));
    assert_eq!(pick(HYPHENATED, &["a1", "a2"]), json!(null));
}
