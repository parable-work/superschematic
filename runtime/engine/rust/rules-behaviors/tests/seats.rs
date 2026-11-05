//! Seats on the pure core (D104, step 3), through the core's public API
//! over a source in memory that keeps rows and their occupants' sub-table
//! rows: the config held to the declaration file's schemas and to the
//! type's fields; a caller taking a seat for itself with and without its
//! take permission, seating and unseating another with and without assign,
//! a seat with no assign; a full seat refused, and displaced from the
//! longest seated by key, never the field's subject; a repeat take and a
//! leave of a seat not held answered before any guard; a fromField's
//! subject seated, counted once, unable to leave, in either UUID form, and
//! an update of the field held to the seat's max; a hyphenated subject
//! kept in base62; a caller with no subject; listSeats' order; a dropped
//! seat's rows and a lowered max; a take on a leased row; and `by` naming a
//! seat on a Workflow transition, read through Seats' own rule.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Behavior, Caller, ConfigError, ConfigTarget, Declaration, FieldSpec, GuardRequest, Hook,
    Outcome, TableDecl, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{core, seats, Seats};
use support::{caller, changed, define, refused, result, rule, shape, undescribed, veto_of, Host};

// DANA is a UUID in base62, DANA_HYPHENATED the same one hyphenated.
const DANA: &str = "TMXp8QPR6yGvKuV2GZQ10";
const DANA_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";

// A problem as a table holds it: Seats on the type, its title, its agent
// (a string), its owner (a UUID) and a count.
fn problem(config: Value) -> TypeSpec {
    TypeSpec::new("Problem")
        .with(UseSpec::on_type("Seats", config))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("agent", "string"))
        .with_field(FieldSpec::new("owner", "Identity.UUID"))
        .with_field(FieldSpec::new("count", "number"))
}

// The seats the cases share: one accountable subject the next displaces,
// two handles a take of a third refuses, and responsible agents, the
// row's agent among them.
fn seats_config() -> Value {
    json!({"seats": {
        "accountable": {"max": 1, "onFull": "displace", "take": "problems.own", "assign": "problems.assign"},
        "handles": {"max": 2, "assign": "problems.release"},
        "responsible": {"fromField": "agent", "take": "problems.work", "assign": "problems.assign"}
    }})
}

// hosting creates p1 with the own fields given.
fn hosting(config: Value, own: Value) -> Host {
    let mut host = Host::new(core(), problem(config));
    changed(host.create(&alice(), "p1", own));
    host
}

fn alice() -> Caller {
    caller("alice", &["problems.own", "problems.assign"])
}

fn worker(subject: &str) -> Caller {
    caller(subject, &["problems.work"])
}

fn carol() -> Caller {
    caller("carol", &[])
}

fn take(host: &mut Host, who: &Caller, now: i64, params: Value) -> Outcome {
    host.invoke_at(who, now, "p1", "Seats", "takeSeat", params)
}

fn leave(host: &mut Host, who: &Caller, params: Value) -> Outcome {
    host.invoke(who, "p1", "Seats", "leaveSeat", params)
}

fn list(host: &mut Host, params: Value) -> Value {
    result(host.invoke(&carol(), "p1", "Seats", "listSeats", params))["items"].clone()
}

// occupants lists p1's occupant rows as `(key, seat, subject)`.
fn occupants(host: &Host) -> Vec<(String, String, String)> {
    host.sub_rows("p1", "Seats", "occupants")
        .into_iter()
        .map(|row| {
            let text = |field: &str| {
                row.value(field)
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned()
            };
            (row.key.clone(), text("seat"), text("subject"))
        })
        .collect()
}

fn forbidden(message: &str) -> Value {
    json!({"class": "EngineError", "code": "forbidden", "message": message})
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Seats.declaration(),
        Declaration::new("Seats")
            .sub_table(TableDecl::new(
                "occupants",
                "number",
                ["number", "seat", "subject", "seatedAt", "seatedBy"]
            ))
            .operation("takeSeat", true)
            .operation("leaveSeat", true)
            .operation("listSeats", false)
            .veto("seat_full")
            .veto("held_by_field")
    );
    let file: Value = serde_json::from_str(seats::DECLARATION).expect("it parses");
    assert_eq!(file.get("fields"), None);
    assert_eq!(file.get("value"), None);
    assert_eq!(file.get("armsDeadlines"), None);
    assert_eq!(file.get("createParamsSchema"), None);
}

/// The checks Seats writes by hand are the declaration's configSchema,
/// keyword for keyword, so a keyword the file gains fails this test until
/// the checks take it; each comes out in the schema loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(seats::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "seats": {
                    "additionalProperties": {
                        "additionalProperties": false,
                        "properties": {
                            "assign": text,
                            "fromField": text,
                            "max": {"minimum": 1, "type": "integer"},
                            "onFull": {"enum": ["refuse", "displace"]},
                            "take": text
                        },
                        "type": "object"
                    },
                    "minProperties": 1,
                    "propertyNames": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$"},
                    "type": "object"
                }
            },
            "required": ["seats"],
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &problem(config));
    assert_eq!(shaped(json!([])), ["/config: must be object"]);
    assert_eq!(
        shaped(json!({})),
        ["/config: must have required property 'seats'"]
    );
    assert_eq!(
        shaped(json!({"seats": {}, "x": 1})),
        [
            "/config: unknown key \"x\"",
            "/config/seats: must NOT have fewer than 1 properties"
        ]
    );
    assert_eq!(
        shaped(json!({"seats": []})),
        ["/config/seats: must be object"]
    );
    assert_eq!(
        shaped(json!({"seats": {"2nd": {}, "lead": 5}})),
        [
            "/config/seats: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\"",
            "/config/seats: property name must be valid",
            "/config/seats/lead: must be object"
        ]
    );
    assert_eq!(
        shaped(
            json!({"seats": {"lead": {"max": 1.5, "onFull": "evict", "take": "", "assign": 5, "fromField": "", "x": 1}}})
        ),
        [
            "/config/seats/lead: unknown key \"x\"",
            "/config/seats/lead/assign: must be string",
            "/config/seats/lead/fromField: must NOT have fewer than 1 characters",
            "/config/seats/lead/max: must be integer",
            "/config/seats/lead/onFull: must be one of \"refuse\", \"displace\"",
            "/config/seats/lead/take: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(json!({"seats": {"lead": {"max": 0}}})),
        ["/config/seats/lead/max: must be >= 1"]
    );
    let mut wide = serde_json::Map::new();
    wide.insert("a".repeat(65), json!({}));
    assert_eq!(
        shaped(json!({"seats": wide})),
        [
            "/config/seats: must NOT have more than 64 characters",
            "/config/seats: property name must be valid"
        ]
    );
    assert!(define(&core(), &problem(seats_config())).is_ok());
}

/// Each operation's parameters are held to its closed paramsSchema, in the
/// behavior registry's words, before any guard sees them; and what the
/// handlers and settle answer is each resultSchema's.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(seats::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let schemas: Vec<(String, Value)> = file["operations"]
        .as_array()
        .expect("operations")
        .iter()
        .map(|operation| {
            (
                operation["name"].as_str().expect("a name").to_owned(),
                undescribed(&operation["paramsSchema"]),
            )
        })
        .collect();
    let seat_and_subject = json!({"additionalProperties": false, "properties": {"seat": text, "subject": text}, "required": ["seat"], "type": "object"});
    assert_eq!(
        schemas,
        vec![
            ("takeSeat".to_owned(), seat_and_subject.clone()),
            ("leaveSeat".to_owned(), seat_and_subject),
            (
                "listSeats".to_owned(),
                json!({"additionalProperties": false, "properties": {"seat": text}, "type": "object"})
            ),
        ]
    );
    let results: Vec<Value> = file["operations"]
        .as_array()
        .expect("operations")
        .iter()
        .map(|operation| undescribed(&operation["resultSchema"]))
        .collect();
    assert_eq!(
        results[0],
        json!({"additionalProperties": false, "properties": {
            "displaced": {"items": {"type": "string"}, "type": "array"},
            "seat": {"type": "string"},
            "subject": {"type": "string"},
            "unchanged": {"type": "boolean"}
        }, "required": ["seat", "subject", "displaced"], "type": "object"})
    );
    assert_eq!(
        results[1],
        json!({"additionalProperties": false, "properties": {
            "seat": {"type": "string"},
            "subject": {"type": "string"},
            "unchanged": {"type": "boolean"}
        }, "required": ["seat", "subject"], "type": "object"})
    );
    assert_eq!(
        results[2],
        json!({"additionalProperties": false, "properties": {"items": {"items": {
            "additionalProperties": false,
            "properties": {
                "fromField": {"type": "boolean"},
                "seat": {"type": "string"},
                "seatedAt": {"type": ["integer", "null"]},
                "seatedBy": {"type": ["string", "null"]},
                "subject": {"type": "string"}
            },
            "required": ["seat", "subject", "seatedAt", "seatedBy", "fromField"],
            "type": "object"
        }, "type": "array"}}, "required": ["items"], "type": "object"})
    );
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    let issues = |outcome| refused(outcome)["details"]["issues"].clone();
    assert_eq!(
        refused(take(
            &mut host,
            &alice(),
            1_000_000,
            json!({"subject": "", "note": 1})
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
        "message": "operation takeSeat of behavior Seats: must NOT have additional properties: note; must have required property 'seat'; /subject: must NOT have fewer than 1 characters",
        "details": {"issues": [
            {"path": "", "message": "must NOT have additional properties: note"},
            {"path": "", "message": "must have required property 'seat'"},
            {"path": "/subject", "message": "must NOT have fewer than 1 characters"}
        ]}})
    );
    assert_eq!(
        issues(leave(&mut host, &alice(), json!({"seat": 5}))),
        json!([{"path": "/seat", "message": "must be string"}])
    );
    assert_eq!(
        issues(host.invoke(
            &alice(),
            "p1",
            "Seats",
            "listSeats",
            json!({"seat": "", "subject": "x"})
        )),
        json!([
            {"path": "", "message": "must NOT have additional properties: subject"},
            {"path": "/seat", "message": "must NOT have fewer than 1 characters"}
        ])
    );
    // A seat the config lacks is a bad parameter, its seats listed.
    for operation in ["takeSeat", "leaveSeat", "listSeats"] {
        assert_eq!(
            refused(host.invoke(&alice(), "p1", "Seats", operation, json!({"seat": "owners"})))["message"],
            format!("operation {operation} of behavior Seats: /seat: \"owners\" is not a seat of Problem's Seats (accountable, handles, responsible)")
        );
    }
}

/// Each seat's rules, by code point, the first refused: an onFull needs a
/// max, and a fromField names an own field that holds a subject.
#[test]
fn the_config_rules_refuse_an_on_full_with_no_max_and_a_from_field_that_is_no_subject() {
    let ruled = |config: Value| rule(&core(), &problem(config), "Seats");
    assert_eq!(
        ruled(json!({"seats": {"handles": {"onFull": "refuse"}}})),
        "seats.handles.onFull names what a full seat does, and handles has no max to fill"
    );
    assert_eq!(
        ruled(json!({"seats": {"responsible": {"fromField": "lead"}}})),
        "seats.responsible.fromField: lead is not a field of Problem (its fields: title, agent, owner, count)"
    );
    assert_eq!(
        ruled(json!({"seats": {"responsible": {"fromField": "count"}}})),
        "seats.responsible.fromField names count, a number field of Problem: fromField names a field that holds a subject (string, Identity.UUID or Identity.UserID)"
    );
    // By code point: b's rule before z's.
    assert_eq!(
        ruled(json!({"seats": {"z": {"onFull": "displace"}, "b": {"fromField": "count"}}})),
        "seats.b.fromField names count, a number field of Problem: fromField names a field that holds a subject (string, Identity.UUID or Identity.UserID)"
    );
    // A type that gives no fields has none for a fromField to name.
    let bare = TypeSpec::new("Problem").with(UseSpec::on_type(
        "Seats",
        json!({"seats": {"responsible": {"fromField": "agent"}}}),
    ));
    assert_eq!(
        rule(&core(), &bare, "Seats"),
        "seats.responsible.fromField: agent is not a field of Problem (its fields: none)"
    );
    // A UUID field holds a subject.
    assert!(define(
        &core(),
        &problem(
            json!({"seats": {"owners": {"fromField": "owner", "max": 3, "onFull": "displace"}}})
        )
    )
    .is_ok());
}

// ---- takes and leaves ----

/// A caller takes a seat for itself with the seat's take permission, and
/// is forbidden without it; a seat with no take lets any caller take it.
#[test]
fn a_caller_takes_a_seat_for_itself_with_take_and_without_it_is_forbidden() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    assert_eq!(
        refused(take(&mut host, &carol(), 1_001_000, json!({"seat": "accountable"}))),
        forbidden("carol may not take a seat in Problem p1's accountable: it needs permission problems.own")
    );
    assert_eq!(
        result(take(
            &mut host,
            &alice(),
            1_001_000,
            json!({"seat": "accountable"})
        )),
        json!({"seat": "accountable", "subject": "alice", "displaced": []})
    );
    assert_eq!(
        result(take(
            &mut host,
            &carol(),
            1_002_000,
            json!({"seat": "handles"})
        )),
        json!({"seat": "handles", "subject": "carol", "displaced": []})
    );
    assert_eq!(
        occupants(&host),
        [
            (
                "0000000001".to_owned(),
                "accountable".to_owned(),
                "alice".to_owned()
            ),
            (
                "0000000002".to_owned(),
                "handles".to_owned(),
                "carol".to_owned()
            )
        ]
    );
    let row = &host.sub_rows("p1", "Seats", "occupants")[0];
    assert_eq!(row.value("seatedAt"), Some(&json!(1_001_000)));
    assert_eq!(row.value("seatedBy"), Some(&json!("alice")));
}

/// Seating and unseating another needs the seat's assign; a seat that
/// names none lets a subject seat itself and leave alone. A subject leaves
/// a seat it holds with no permission.
#[test]
fn seating_and_unseating_another_needs_assign_and_a_seat_with_no_assign_lets_no_one() {
    let mut host = hosting(
        json!({"seats": {"handles": {"assign": "problems.release"}, "watchers": {}}}),
        json!({"title": "Flaky"}),
    );
    let pat = caller("pat", &["problems.release"]);
    assert_eq!(
        refused(take(&mut host, &alice(), 1_001_000, json!({"seat": "handles", "subject": "eli"}))),
        forbidden("alice may not seat eli in Problem p1's handles: seating another needs permission problems.release")
    );
    changed(take(
        &mut host,
        &pat,
        1_001_000,
        json!({"seat": "handles", "subject": "eli"}),
    ));
    assert_eq!(
        host.sub_rows("p1", "Seats", "occupants")[0].value("seatedBy"),
        Some(&json!("pat"))
    );
    assert_eq!(
        refused(leave(&mut host, &carol(), json!({"seat": "handles", "subject": "eli"}))),
        forbidden("carol may not unseat eli from Problem p1's handles: unseating another needs permission problems.release")
    );
    assert_eq!(
        result(leave(
            &mut host,
            &pat,
            json!({"seat": "handles", "subject": "eli"})
        )),
        json!({"seat": "handles", "subject": "eli"})
    );
    // With no assign, no caller seats or unseats another.
    assert_eq!(
        refused(take(&mut host, &pat, 1_002_000, json!({"seat": "watchers", "subject": "eli"}))),
        forbidden("pat may not seat eli in Problem p1's watchers: the seat names no assign permission, so a subject seats itself alone")
    );
    changed(take(
        &mut host,
        &caller("eli", &[]),
        1_002_000,
        json!({"seat": "watchers"}),
    ));
    assert_eq!(
        refused(leave(&mut host, &pat, json!({"seat": "watchers", "subject": "eli"}))),
        forbidden("pat may not unseat eli from Problem p1's watchers: the seat names no assign permission, so a subject leaves it alone")
    );
    // A subject leaves a seat it holds with no permission.
    changed(leave(
        &mut host,
        &caller("eli", &[]),
        json!({"seat": "watchers"}),
    ));
    assert!(occupants(&host).is_empty());
}

/// A full seat under onFull refuse, the default, is seat_full, details its
/// seat, its max and the subjects seated.
#[test]
fn a_full_seat_under_refuse_is_seat_full() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    changed(take(
        &mut host,
        &worker("ada"),
        1_001_000,
        json!({"seat": "handles"}),
    ));
    changed(take(
        &mut host,
        &worker("eli"),
        1_002_000,
        json!({"seat": "handles"}),
    ));
    let error = refused(take(
        &mut host,
        &carol(),
        1_003_000,
        json!({"seat": "handles"}),
    ));
    assert_eq!(
        error,
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": "behavior Seats vetoes takeSeat of Problem p1: Problem p1's handles seats at most 2, and it is full (ada, eli)",
            "details": {"behavior": "Seats", "action": "takeSeat",
                "reason": "Problem p1's handles seats at most 2, and it is full (ada, eli)",
                "vetoCode": "seat_full",
                "vetoDetails": {"seat": "handles", "max": 2, "seated": ["ada", "eli"]}}})
    );
    assert_eq!(occupants(&host).len(), 2);
}

/// Under onFull displace the next to take a full seat unseats the longest
/// seated, the least key, never the subject its fromField holds, whatever
/// the seatedAt each take's clock gave; when every subject seated is the
/// field's, the take is seat_full.
#[test]
fn displace_unseats_the_longest_seated_by_key_never_the_fields_subject() {
    let config = json!({"seats": {"crew": {"max": 2, "onFull": "displace", "fromField": "agent"}}});
    let mut host = hosting(config.clone(), json!({"title": "Flaky"}));
    // Two seated at one time: the lesser key is the longer seated.
    changed(take(
        &mut host,
        &caller("ada", &[]),
        1_001_000,
        json!({"seat": "crew"}),
    ));
    changed(take(
        &mut host,
        &caller("eli", &[]),
        1_001_000,
        json!({"seat": "crew"}),
    ));
    assert_eq!(
        result(take(
            &mut host,
            &caller("ivy", &[]),
            1_002_000,
            json!({"seat": "crew"})
        )),
        json!({"seat": "crew", "subject": "ivy", "displaced": ["ada"]})
    );
    // A server whose clock runs slow seats otto after ivy with a seatedAt
    // less than every other's: eli, seated longest, leaves for him, and
    // then ivy, seated before him, leaves for wren.
    assert_eq!(
        result(take(
            &mut host,
            &caller("otto", &[]),
            900_000,
            json!({"seat": "crew"})
        )),
        json!({"seat": "crew", "subject": "otto", "displaced": ["eli"]})
    );
    assert_eq!(
        result(take(
            &mut host,
            &caller("wren", &[]),
            1_004_000,
            json!({"seat": "crew"})
        )),
        json!({"seat": "crew", "subject": "wren", "displaced": ["ivy"]})
    );
    // The field's subject counts and is never displaced.
    let mut host = hosting(config.clone(), json!({"title": "Flaky", "agent": "otto"}));
    changed(take(
        &mut host,
        &caller("ada", &[]),
        1_001_000,
        json!({"seat": "crew"}),
    ));
    assert_eq!(
        result(take(
            &mut host,
            &caller("eli", &[]),
            1_002_000,
            json!({"seat": "crew"})
        )),
        json!({"seat": "crew", "subject": "eli", "displaced": ["ada"]})
    );
    assert_eq!(
        list(&mut host, json!({})),
        json!([
            {"seat": "crew", "subject": "otto", "seatedAt": null, "seatedBy": null, "fromField": true},
            {"seat": "crew", "subject": "eli", "seatedAt": 1_002_000, "seatedBy": "eli", "fromField": false}
        ])
    );
    // Every subject seated the field's: seat_full.
    let mut host = hosting(
        json!({"seats": {"crew": {"max": 1, "onFull": "displace", "fromField": "agent"}}}),
        json!({"title": "Flaky", "agent": "otto"}),
    );
    let (code, details) = veto_of(take(
        &mut host,
        &caller("ada", &[]),
        1_001_000,
        json!({"seat": "crew"}),
    ));
    assert_eq!(code, "seat_full");
    assert_eq!(
        details,
        json!({"seat": "crew", "max": 1, "seated": ["otto"]})
    );
}

// Wall: a test behavior whose guard vetoes every write of its row, so a
// call that reaches the guards is refused.
struct Wall;

impl Behavior for Wall {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Wall").veto("walled")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(match request {
            GuardRequest::Operation { writes: true, .. } => {
                Some(Veto::new("the row is walled").with_code("walled"))
            }
            _ => None,
        })
    }
}

/// A take of a seat the subject holds, and a leave of one it does not,
/// answer unchanged and write nothing before any guard is asked: a guard
/// that vetoes every write does not refuse them, and refuses a real take.
#[test]
fn a_repeat_take_and_a_leave_of_a_seat_not_held_answer_unchanged_before_any_guard() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky", "agent": "ada"}));
    changed(take(
        &mut host,
        &alice(),
        1_001_000,
        json!({"seat": "accountable"}),
    ));
    let walled = TypeSpec {
        uses: [UseSpec::on_type("test.Wall", None)]
            .into_iter()
            .chain(problem(seats_config()).uses)
            .collect(),
        ..problem(seats_config())
    };
    host.core = core().with(Wall);
    host.spec = walled;
    let version = host.row("p1").version;
    let repeat = take(
        &mut host,
        &alice(),
        1_002_000,
        json!({"seat": "accountable"}),
    );
    assert_eq!(
        changed(repeat.clone()).rows,
        Vec::new(),
        "a repeat writes nothing"
    );
    assert_eq!(
        result(repeat),
        json!({"seat": "accountable", "subject": "alice", "displaced": [], "unchanged": true})
    );
    // The field's subject is seated: its take is a repeat too.
    assert_eq!(
        result(take(
            &mut host,
            &worker("ada"),
            1_002_000,
            json!({"seat": "responsible"})
        )),
        json!({"seat": "responsible", "subject": "ada", "displaced": [], "unchanged": true})
    );
    assert_eq!(
        result(leave(&mut host, &carol(), json!({"seat": "handles"}))),
        json!({"seat": "handles", "subject": "carol", "unchanged": true})
    );
    // Before any permission: a caller without take learns it is seated.
    assert_eq!(
        result(take(
            &mut host,
            &carol(),
            1_002_000,
            json!({"seat": "accountable", "subject": "alice"})
        ))["unchanged"],
        json!(true)
    );
    assert_eq!(host.row("p1").version, version);
    assert_eq!(
        veto_of(take(
            &mut host,
            &carol(),
            1_002_000,
            json!({"seat": "handles"})
        ))
        .0,
        "walled"
    );
}

/// The subject a fromField holds cannot leave the seat while the field
/// holds it, whether it leaves itself or an assigner unseats it.
#[test]
fn held_by_field_for_the_caller_and_for_an_assigner() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky", "agent": "ada"}));
    let reason = "ada holds Problem p1's responsible through its field agent, and leaves it only when agent names another subject";
    for who in [worker("ada"), alice()] {
        let error = refused(leave(
            &mut host,
            &who,
            json!({"seat": "responsible", "subject": "ada"}),
        ));
        assert_eq!(error["details"]["vetoCode"], "held_by_field");
        assert_eq!(error["details"]["reason"], reason);
        assert_eq!(
            error["details"]["vetoDetails"],
            json!({"seat": "responsible", "subject": "ada", "field": "agent"})
        );
    }
    // An assigner without assign is forbidden first.
    assert_eq!(
        refused(leave(
            &mut host,
            &carol(),
            json!({"seat": "responsible", "subject": "ada"})
        ))["code"],
        "forbidden"
    );
    // Once the field names another, ada holds nothing to leave.
    changed(host.update(&alice(), "p1", json!({"agent": "eli"})));
    assert_eq!(
        result(leave(
            &mut host,
            &worker("ada"),
            json!({"seat": "responsible"})
        )),
        json!({"seat": "responsible", "subject": "ada", "unchanged": true})
    );
}

/// A fromField's subject who also has an occupant row of the seat (taken
/// before the field named it) counts once, is listed once, first, with its
/// row's time, and is never displaced.
#[test]
fn a_fields_subject_with_its_own_row_counts_once() {
    let mut host = hosting(
        json!({"seats": {"crew": {"max": 2, "onFull": "displace", "fromField": "agent"}}}),
        json!({"title": "Flaky"}),
    );
    changed(take(
        &mut host,
        &caller("ada", &[]),
        1_001_000,
        json!({"seat": "crew"}),
    ));
    changed(take(
        &mut host,
        &caller("eli", &[]),
        1_002_000,
        json!({"seat": "crew"}),
    ));
    changed(host.update(&alice(), "p1", json!({"agent": "ada"})));
    assert_eq!(
        list(&mut host, json!({"seat": "crew"})),
        json!([
            {"seat": "crew", "subject": "ada", "seatedAt": 1_001_000, "seatedBy": "ada", "fromField": true},
            {"seat": "crew", "subject": "eli", "seatedAt": 1_002_000, "seatedBy": "eli", "fromField": false}
        ])
    );
    assert_eq!(
        result(take(
            &mut host,
            &caller("ivy", &[]),
            1_003_000,
            json!({"seat": "crew"})
        )),
        json!({"seat": "crew", "subject": "ivy", "displaced": ["eli"]})
    );
    assert_eq!(occupants(&host).len(), 2);
}

/// An update that gives a fromField a subject not seated there takes the
/// seat as a take would, past no permission: into a seat it would put over
/// its max it is seat_full under onFull refuse, its reason naming the field
/// and the subject, and leaves the row as it was; under displace the
/// subjects seated longest that the field does not hold leave, down to max.
/// A subject seated already, a field cleared and a patch that leaves the
/// field alone are not held, and a seat a lowered max put over it is
/// seat_full for any new subject.
#[test]
fn an_update_that_gives_a_from_field_a_new_subject_takes_the_seat_as_a_take_would() {
    let mut host = hosting(
        json!({"seats": {"owners": {"max": 2, "fromField": "agent"}}}),
        json!({"title": "Flaky"}),
    );
    for (who, now) in [("ada", 1_001_000), ("eli", 1_002_000)] {
        changed(take(
            &mut host,
            &caller(who, &[]),
            now,
            json!({"seat": "owners"}),
        ));
    }
    let version = host.row("p1").version;
    let reason =
        "Problem p1's owners seats at most 2, and it is full (ada, eli): agent cannot seat ivy in it";
    assert_eq!(
        refused(host.update(&alice(), "p1", json!({"agent": "ivy"}))),
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": format!("behavior Seats vetoes update of Problem p1: {reason}"),
            "details": {"behavior": "Seats", "action": "update", "reason": reason,
                "vetoCode": "seat_full",
                "vetoDetails": {"seat": "owners", "max": 2, "seated": ["ada", "eli"]}}})
    );
    assert_eq!(host.row("p1").version, version);
    assert_eq!(host.row("p1").own.get("agent"), None);
    // A subject seated already moves into the field, and the field may
    // move to another seated, be cleared, or be left alone.
    for patch in [
        json!({"agent": "eli"}),
        json!({"agent": "ada"}),
        json!({"agent": null}),
        json!({"title": "Still flaky"}),
    ] {
        changed(host.update(&alice(), "p1", patch));
    }
    assert_eq!(occupants(&host).len(), 2);
    // A lowered max: the seat is over it, and any new subject is refused.
    host.spec = problem(json!({"seats": {"owners": {"max": 1, "fromField": "agent"}}}));
    changed(host.update(&alice(), "p1", json!({"agent": "eli"})));
    let (code, details) = veto_of(host.update(&alice(), "p1", json!({"agent": "ivy"})));
    assert_eq!(code, "seat_full");
    assert_eq!(
        details,
        json!({"seat": "owners", "max": 1, "seated": ["eli", "ada"]})
    );

    // Under displace the longest seated leave for the field's new subject.
    let mut host = hosting(
        json!({"seats": {"crew": {"max": 2, "onFull": "displace", "fromField": "agent"}}}),
        json!({"title": "Flaky"}),
    );
    for (who, now) in [("ada", 1_001_000), ("eli", 1_002_000)] {
        changed(take(
            &mut host,
            &caller(who, &[]),
            now,
            json!({"seat": "crew"}),
        ));
    }
    changed(host.update(&alice(), "p1", json!({"agent": "ivy"})));
    assert_eq!(
        list(&mut host, json!({})),
        json!([
            {"seat": "crew", "subject": "ivy", "seatedAt": null, "seatedBy": null, "fromField": true},
            {"seat": "crew", "subject": "eli", "seatedAt": 1_002_000, "seatedBy": "eli", "fromField": false}
        ])
    );
    // eli, seated already, takes the field and displaces no one; then a new
    // subject displaces the longest seated the field does not hold.
    changed(host.update(&alice(), "p1", json!({"agent": "eli"})));
    changed(take(
        &mut host,
        &caller("ada", &[]),
        1_003_000,
        json!({"seat": "crew"}),
    ));
    changed(host.update(&alice(), "p1", json!({"agent": "otto"})));
    assert_eq!(
        list(&mut host, json!({})),
        json!([
            {"seat": "crew", "subject": "otto", "seatedAt": null, "seatedBy": null, "fromField": true},
            {"seat": "crew", "subject": "ada", "seatedAt": 1_003_000, "seatedBy": "ada", "fromField": false}
        ])
    );
}

/// A hyphenated subject is kept in base62, the adapters' wire form, so it
/// matches its caller in base62 and leaves the seat as that caller.
#[test]
fn a_hyphenated_subject_is_kept_in_base62_and_matches_its_base62_caller() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    let pat = caller("pat", &["problems.release"]);
    assert_eq!(
        result(take(
            &mut host,
            &pat,
            1_001_000,
            json!({"seat": "handles", "subject": DANA_HYPHENATED})
        )),
        json!({"seat": "handles", "subject": DANA, "displaced": []})
    );
    assert_eq!(occupants(&host)[0].2, DANA);
    // Her take in base62 is a repeat, and so is one hyphenated.
    for subject in [DANA, DANA_HYPHENATED] {
        assert_eq!(
            result(take(
                &mut host,
                &caller(subject, &[]),
                1_002_000,
                json!({"seat": "handles"})
            ))["unchanged"],
            json!(true)
        );
    }
    // A caller hyphenated takes a seat as itself and is kept in base62.
    changed(take(
        &mut host,
        &caller(DANA_HYPHENATED, &["problems.own"]),
        1_003_000,
        json!({"seat": "accountable"}),
    ));
    let rows = host.sub_rows("p1", "Seats", "occupants");
    assert_eq!(rows[1].value("subject"), Some(&json!(DANA)));
    assert_eq!(rows[1].value("seatedBy"), Some(&json!(DANA)));
    assert_eq!(
        result(leave(
            &mut host,
            &caller(DANA, &[]),
            json!({"seat": "handles"})
        )),
        json!({"seat": "handles", "subject": DANA})
    );
    // 7 and 07 are two subjects.
    changed(take(
        &mut host,
        &pat,
        1_004_000,
        json!({"seat": "handles", "subject": "7"}),
    ));
    assert_eq!(
        result(take(
            &mut host,
            &pat,
            1_005_000,
            json!({"seat": "handles", "subject": "07"})
        )),
        json!({"seat": "handles", "subject": "07", "displaced": []})
    );
}

/// A UUID fromField names its subject in either form: its value
/// hyphenated, the caller in base62, and the other way around.
#[test]
fn a_uuid_from_field_names_its_subject_in_either_form() {
    let config = json!({"seats": {"owners": {"fromField": "owner"}}});
    for (held, subject) in [(DANA_HYPHENATED, DANA), (DANA, DANA_HYPHENATED)] {
        let mut host = hosting(config.clone(), json!({"title": "Flaky", "owner": held}));
        assert_eq!(
            result(take(
                &mut host,
                &caller(subject, &[]),
                1_001_000,
                json!({"seat": "owners"})
            ))["unchanged"],
            json!(true)
        );
        assert_eq!(
            veto_of(leave(
                &mut host,
                &caller(subject, &[]),
                json!({"seat": "owners"})
            ))
            .0,
            "held_by_field"
        );
        assert_eq!(
            list(&mut host, json!({})),
            json!([{"seat": "owners", "subject": held, "seatedAt": null, "seatedBy": null, "fromField": true}])
        );
    }
}

/// A caller with no subject takes no seat itself and holds none to leave,
/// both bad parameters; with assign, it seats another.
#[test]
fn a_caller_with_no_subject_takes_and_holds_no_seat_itself() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    let nobody = caller("", &["problems.release"]);
    assert_eq!(
        refused(take(
            &mut host,
            &nobody,
            1_001_000,
            json!({"seat": "handles"})
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
            "message": "operation takeSeat of behavior Seats: /subject: a caller with no subject takes no seat itself: name the subject to seat",
            "details": {"issues": [{"path": "/subject", "message": "a caller with no subject takes no seat itself: name the subject to seat"}]}})
    );
    assert_eq!(
        refused(leave(&mut host, &nobody, json!({"seat": "handles"})))["message"],
        "operation leaveSeat of behavior Seats: /subject: a caller with no subject holds no seat: name the subject to unseat"
    );
    changed(take(
        &mut host,
        &nobody,
        1_001_000,
        json!({"seat": "handles", "subject": "eli"}),
    ));
    let row = &host.sub_rows("p1", "Seats", "occupants")[0];
    assert_eq!(row.value("seatedBy"), None);
    assert_eq!(
        refused(take(&mut host, &caller("", &[]), 1_002_000, json!({"seat": "handles", "subject": "ivy"}))),
        forbidden("a caller with no subject may not seat ivy in Problem p1's handles: seating another needs permission problems.release")
    );
}

/// listSeats lists each seat by code point, its fromField's subject first,
/// then its occupants by key, the order they were seated in, whatever
/// their seatedAt says (eli's server's clock ran ahead of ada's); with
/// seat, that seat's.
#[test]
fn list_seats_lists_by_seat_the_fields_subject_first_then_by_key() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky", "agent": "otto"}));
    let pat = caller("pat", &["problems.release"]);
    changed(take(
        &mut host,
        &pat,
        1_003_000,
        json!({"seat": "handles", "subject": "eli"}),
    ));
    changed(take(
        &mut host,
        &pat,
        1_001_000,
        json!({"seat": "handles", "subject": "ada"}),
    ));
    changed(take(
        &mut host,
        &worker("ivy"),
        1_002_000,
        json!({"seat": "responsible"}),
    ));
    changed(take(
        &mut host,
        &alice(),
        1_002_000,
        json!({"seat": "accountable"}),
    ));
    assert_eq!(
        list(&mut host, json!({})),
        json!([
            {"seat": "accountable", "subject": "alice", "seatedAt": 1_002_000, "seatedBy": "alice", "fromField": false},
            {"seat": "handles", "subject": "eli", "seatedAt": 1_003_000, "seatedBy": "pat", "fromField": false},
            {"seat": "handles", "subject": "ada", "seatedAt": 1_001_000, "seatedBy": "pat", "fromField": false},
            {"seat": "responsible", "subject": "otto", "seatedAt": null, "seatedBy": null, "fromField": true},
            {"seat": "responsible", "subject": "ivy", "seatedAt": 1_002_000, "seatedBy": "ivy", "fromField": false}
        ])
    );
    assert_eq!(
        list(&mut host, json!({"seat": "handles"})),
        json!([
            {"seat": "handles", "subject": "eli", "seatedAt": 1_003_000, "seatedBy": "pat", "fromField": false},
            {"seat": "handles", "subject": "ada", "seatedAt": 1_001_000, "seatedBy": "pat", "fromField": false}
        ])
    );
    // A read writes nothing.
    let version = host.row("p1").version;
    assert_eq!(
        changed(host.invoke(&carol(), "p1", "Seats", "listSeats", json!({}))).rows,
        Vec::new()
    );
    assert_eq!(host.row("p1").version, version);
}

/// A seat the config drops keeps its occupants, which no rule reads and
/// listSeats does not list, until the row's next take or leave deletes
/// them; a lowered max unseats no one, and the next take of the seat over
/// it is seat_full, or displaces down to max - 1.
#[test]
fn a_dropped_seats_rows_go_at_the_next_take_and_a_lowered_max_unseats_no_one() {
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    changed(take(
        &mut host,
        &alice(),
        1_001_000,
        json!({"seat": "accountable"}),
    ));
    changed(take(
        &mut host,
        &worker("ada"),
        1_002_000,
        json!({"seat": "handles"}),
    ));
    changed(take(
        &mut host,
        &worker("eli"),
        1_003_000,
        json!({"seat": "handles"}),
    ));
    // A new version drops accountable and lowers handles' max to 1.
    host.spec = problem(json!({"seats": {
        "handles": {"max": 1, "assign": "problems.release"},
        "crew": {"max": 1, "onFull": "displace"}
    }}));
    assert_eq!(occupants(&host).len(), 3);
    assert_eq!(
        list(&mut host, json!({})),
        json!([
            {"seat": "handles", "subject": "ada", "seatedAt": 1_002_000, "seatedBy": "ada", "fromField": false},
            {"seat": "handles", "subject": "eli", "seatedAt": 1_003_000, "seatedBy": "eli", "fromField": false}
        ])
    );
    let (code, details) = veto_of(take(
        &mut host,
        &carol(),
        1_004_000,
        json!({"seat": "handles"}),
    ));
    assert_eq!(code, "seat_full");
    assert_eq!(details["seated"], json!(["ada", "eli"]));
    // The next take deletes the dropped seat's row, and takes its number
    // past every row it read.
    changed(take(
        &mut host,
        &carol(),
        1_004_000,
        json!({"seat": "crew"}),
    ));
    assert_eq!(
        occupants(&host),
        [
            (
                "0000000002".to_owned(),
                "handles".to_owned(),
                "ada".to_owned()
            ),
            (
                "0000000003".to_owned(),
                "handles".to_owned(),
                "eli".to_owned()
            ),
            (
                "0000000004".to_owned(),
                "crew".to_owned(),
                "carol".to_owned()
            )
        ]
    );
    // A displacing seat over its max displaces down to max - 1.
    host.spec = problem(json!({"seats": {
        "handles": {"max": 1, "onFull": "displace", "assign": "problems.release"},
        "crew": {"max": 1, "onFull": "displace"}
    }}));
    assert_eq!(
        result(take(
            &mut host,
            &carol(),
            1_005_000,
            json!({"seat": "handles"})
        )),
        json!({"seat": "handles", "subject": "carol", "displaced": ["ada", "eli"]})
    );
}

/// Seats' operations are writes of the row, which Lease's guard holds for a
/// caller who does not hold an active lease, unless its exempt lists them;
/// listSeats reads, which Lease lets through.
#[test]
fn a_take_on_a_leased_row_is_held_by_leases_guard() {
    for (exempt, held) in [(json!([]), true), (json!(["Seats.takeSeat"]), false)] {
        let mut spec = problem(seats_config());
        spec.uses
            .insert(0, UseSpec::on_type("Lease", json!({"exempt": exempt})));
        let mut host = Host::new(core(), spec);
        changed(host.create(&alice(), "p1", json!({"title": "Flaky"})));
        changed(host.invoke(&caller("wren", &[]), "p1", "Lease", "acquire", json!({})));
        let outcome = take(&mut host, &carol(), 1_001_000, json!({"seat": "handles"}));
        if held {
            assert_eq!(veto_of(outcome).0, "held_by_another");
        } else {
            changed(outcome);
        }
        changed(host.invoke(&carol(), "p1", "Seats", "listSeats", json!({})));
        // The holder takes a seat whatever exempt says.
        changed(take(
            &mut host,
            &caller("wren", &[]),
            1_002_000,
            json!({"seat": "handles"}),
        ));
    }
}

/// Seats allows any config change, and joins and leaves a type with rows.
#[test]
fn any_config_change_is_allowed() {
    let cx_spec = problem(seats_config());
    let config = superschematic_rules_behaviors::SeatsConfig::parse(
        &seats_config(),
        &ConfigTarget::new(&cx_spec, 0),
    )
    .expect("a config");
    assert_eq!(config.listed(), "accountable, handles, responsible");
    assert_eq!(
        config.seat("handles").map(|seat| (seat.max, seat.on_full)),
        Some((Some(2), superschematic_rules_behaviors::SeatsOnFull::Refuse))
    );
    assert_eq!(
        config
            .seat("responsible")
            .and_then(|seat| seat.from_field.clone()),
        Some(("agent".to_owned(), "string".to_owned()))
    );
    let mut host = hosting(seats_config(), json!({"title": "Flaky"}));
    changed(take(
        &mut host,
        &alice(),
        1_001_000,
        json!({"seat": "accountable"}),
    ));
    let publish = |before: Option<TypeSpec>, after: TypeSpec, host: &mut Host| {
        let command = superschematic_rules::Command::new(
            alice(),
            1_002_000,
            superschematic_rules::Action::Publish { from: before },
        );
        host.core
            .drive(&after, &command, &mut host.memory)
            .into_result()
            .map(|_| ())
    };
    let bare = TypeSpec {
        uses: Vec::new(),
        ..problem(seats_config())
    };
    let changed_config = problem(json!({"seats": {"lead": {"max": 3}}}));
    assert!(publish(Some(problem(seats_config())), changed_config, &mut host).is_ok());
    assert!(publish(Some(problem(seats_config())), bare.clone(), &mut host).is_ok());
    assert!(publish(Some(bare), problem(seats_config()), &mut host).is_ok());
}

// ---- by naming a seat ----

// A problem whose Workflow's transitions are made by the seats' subjects.
fn worked() -> Host {
    let mut spec = problem(json!({"seats": {
        "accountable": {"max": 1, "onFull": "displace", "take": "problems.own"},
        "responsible": {"fromField": "owner", "take": "problems.work"}
    }}));
    spec.uses.push(UseSpec::on_field(
        "status",
        "Workflow",
        json!({
            "states": ["open", "working", "done"],
            "transitions": [
                {"from": "open", "to": "working", "by": ["agent", "seat:responsible"]},
                {"from": "working", "to": "done", "by": ["seat:accountable"], "permission": "problems.close"}
            ],
            "overridePermission": "problems.override"
        }),
    ));
    let mut host = Host::new(core(), spec);
    changed(host.create(
        &alice(),
        "p1",
        json!({"title": "Flaky", "agent": "otto", "owner": DANA_HYPHENATED}),
    ));
    host
}

fn transition(host: &mut Host, who: &Caller, to: &str) -> Outcome {
    host.invoke(who, "p1", "status", "transition", json!({ "to": to }))
}

/// A transition whose by names a seat is made by a subject seated there:
/// through the seat's fromField, a UUID in either form, or an occupant
/// row; a field entry still names its subject; another caller is
/// not_principal naming the fields and then the seats; overridePermission
/// moves past it; and the transition's permission is still needed.
#[test]
fn a_transitions_by_seat_is_held_through_seats_rule() {
    let mut host = worked();
    let error = refused(transition(&mut host, &carol(), "working"));
    assert_eq!(
        error["details"]["reason"],
        "carol may not move Problem p1's status from open to working: the transition is made by the subject agent holds or a subject seated in responsible"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"from": "open", "to": "working", "by": ["agent", "seat:responsible"]})
    );
    // dana, seated through owner, which holds her UUID hyphenated.
    changed(transition(&mut host, &caller(DANA, &[]), "working"));
    // Seated in accountable through a row; done needs its permission too.
    let ivy = caller("ivy", &["problems.own"]);
    changed(take(
        &mut host,
        &ivy,
        1_001_000,
        json!({"seat": "accountable"}),
    ));
    assert_eq!(
        refused(transition(&mut host, &ivy, "done"))["code"],
        "forbidden"
    );
    let closer = caller("ivy", &["problems.close"]);
    changed(transition(&mut host, &closer, "done"));
    // A field entry names its subject, and override passes every by.
    let mut host = worked();
    changed(transition(&mut host, &caller("otto", &[]), "working"));
    let mut host = worked();
    changed(transition(
        &mut host,
        &caller("mod", &["problems.override"]),
        "working",
    ));
    // A subject who left the seat makes no transition it names.
    let mut host = worked();
    changed(transition(&mut host, &caller("otto", &[]), "working"));
    changed(take(
        &mut host,
        &ivy,
        1_001_000,
        json!({"seat": "accountable"}),
    ));
    changed(leave(&mut host, &ivy, json!({"seat": "accountable"})));
    let (code, details) = veto_of(transition(&mut host, &closer, "done"));
    assert_eq!(code, "not_principal");
    assert_eq!(details["by"], json!(["seat:accountable"]));
}
