//! Approvals' requirements (D200), through the core's public API over the
//! source in memory the ported behaviors share: which callers a requirement
//! counts and as whom (a person, an agent run counted as the person it acts
//! for, an agent run counted as its Agent), the proof a sign-off shows from
//! the assurance its host reports, the exclusions of the creator, the
//! person the creator acted for and the subjects a field lists, the
//! permission read again at each guard, the conditions under which a
//! requirement applies, and the define checks of each. Then a gate that
//! reads its requirements from the rows of a type: a sign-off counting
//! toward every requirement it meets, a row that changes at run time, each
//! requirement spending a sign-off on its own reset fields, the pin to
//! named fields, a broken row failing closed and the keys a gate reads its
//! rows by not known yet; and a caller as a host's JSON carries it.

mod support;

use serde_json::{json, Map, Value};
use superschematic_rules::{Assurance, Caller, FieldSpec, Outcome, Row, TypeSpec, UseSpec};
use superschematic_rules_behaviors::core;
use support::{caller, changed, define, refused, result, rule, shape, veto_of, Host, START};

// A change is opened, made ready once its gate holds, and landed.
fn flow() -> Value {
    json!({
        "states": ["open", "ready", "landed"],
        "transitions": [
            {"from": "open", "to": "ready"},
            {"from": "ready", "to": "open"},
            {"from": "ready", "to": "landed"}
        ]
    })
}

// A change as a table holds it, its gate on ready: its head and patch, the
// paths it touches and the lines it changes, its flags, who pushed to it,
// the rules whose requirements it reads, and who created it and for whom.
fn change(gate: Value) -> TypeSpec {
    TypeSpec::new("Change")
        .with(UseSpec::on_type(
            "Approvals",
            json!({ "gates": { "ready": gate } }),
        ))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("head", "string"))
        .with_field(FieldSpec::new("patch", "string"))
        .with_field(FieldSpec::new("paths", "string"))
        .with_field(FieldSpec::new("lines", "Generic.Int64"))
        .with_field(FieldSpec::new("flags", "string"))
        .with_field(FieldSpec::new("findings", "string"))
        .with_field(FieldSpec::new("pushers", "Identity.UUID"))
        .with_field(FieldSpec::new("rules", "Identity.UUID"))
        .with_field(FieldSpec::new("createdBy", "string"))
        .with_field(FieldSpec::new("createdFor", "string"))
}

// The type whose rows are a change's requirements: each points at a rule.
fn requirement_type() -> TypeSpec {
    let mut spec = TypeSpec::new("Requirement").with_field(FieldSpec::relation(
        "ruleId",
        "Identity.UUID",
        "rule",
        "Rule",
    ));
    for name in [
        "name",
        "count",
        "permission",
        "callers",
        "proof",
        "proofWithinMs",
        "excludeCreator",
        "excludeCreatorPerson",
        "excludeSubjectsIn",
        "resetOn",
        "recheckPermission",
        "when",
        "archivedAt",
    ] {
        spec = spec.with_field(FieldSpec::new(name, "string"));
    }
    spec
}

// hosting creates c1, opened by dana with the facts given, at head h1.
fn hosting(gate: Value, facts: Value) -> Host {
    let mut host = Host::new(core(), change(gate));
    host.memory
        .specs
        .insert("Requirement".to_owned(), requirement_type());
    let mut own = json!({"title": "Fix", "head": "h1", "patch": "p1", "createdBy": "dana"});
    for (field, value) in facts.as_object().expect("facts") {
        own[field] = value.clone();
    }
    changed(host.create(&caller("dana", &[]), "c1", own));
    host
}

// requirement puts a requirement row in the source, as an instance writes
// one at run time.
fn requirement(host: &mut Host, key: &str, own: Value) {
    host.memory.others.insert(
        ("Requirement".to_owned(), key.to_owned()),
        Row {
            own: own.as_object().cloned().expect("a row"),
            ..Row::default()
        },
    );
}

// edit changes members of a requirement row at run time.
fn edit(host: &mut Host, key: &str, patch: Value) {
    let row = host
        .memory
        .others
        .get_mut(&("Requirement".to_owned(), key.to_owned()))
        .expect("the requirement");
    for (field, value) in patch.as_object().expect("a patch") {
        row.own.insert(field.clone(), value.clone());
    }
}

fn ann() -> Caller {
    caller("ann", &["changes.approve", "changes.lead"])
}

fn ben() -> Caller {
    caller("ben", &["changes.approve"])
}

// run is an agent run of an Agent, acting for a person when one is named.
fn run(subject: &str, agent: &str, acting_for: Option<&str>) -> Caller {
    let run = caller(subject, &["changes.approve", "changes.lead"]).run_of(agent);
    match acting_for {
        Some(person) => run.acting_for(person),
        None => run,
    }
}

fn sign(host: &mut Host, who: &Caller, params: Value) -> Outcome {
    host.invoke(who, "c1", "Approvals", "signOff", params)
}

fn ready(host: &mut Host) -> Outcome {
    host.invoke(
        &caller("alice", &[]),
        "c1",
        "status",
        "transition",
        json!({"to": "ready"}),
    )
}

fn reopen(host: &mut Host) {
    changed(host.invoke(
        &caller("alice", &[]),
        "c1",
        "status",
        "transition",
        json!({"to": "open"}),
    ));
}

fn set(host: &mut Host, patch: Value) {
    changed(host.update(&caller("dana", &[]), "c1", patch));
}

// sign_off_values is c1's sign-off rows' values, by key.
fn sign_off_values(host: &Host) -> Vec<Map<String, Value>> {
    host.sub_rows("c1", "Approvals", "signOffs")
        .into_iter()
        .map(|row| row.values)
        .collect()
}

// ---- callers ----

/// A requirement that lists its callers counts an agent run acting for a
/// person as that person, so two runs for one person are one signer, and
/// an agent run on its own as its Agent; a person calling as themselves
/// counts only where person is listed, and is refused as not_counted
/// before any row is written.
#[test]
fn a_requirement_counts_each_caller_as_whom_it_stands_for() {
    let mut host = hosting(
        json!({"count": 2, "permission": "changes.approve", "callers": ["acting-for-person", "agent"], "resetOn": []}),
        json!({}),
    );
    changed(sign(
        &mut host,
        &run("r1", "reviewer", Some("ann")),
        json!({"state": "ready"}),
    ));
    changed(sign(
        &mut host,
        &run("r2", "reviewer", Some("ann")),
        json!({"state": "ready"}),
    ));
    assert_eq!(
        veto_of(ready(&mut host)),
        (
            "approvals_missing".to_owned(),
            json!({"state": "ready", "required": 2, "count": 1})
        )
    );
    assert_eq!(
        veto_of(sign(&mut host, &ben(), json!({"state": "ready"}))),
        (
            "not_counted".to_owned(),
            json!({"requirements": [{"name": "ready", "reason": "it does not count this kind of caller"}]})
        )
    );
    assert_eq!(sign_off_values(&host).len(), 2);
    changed(sign(
        &mut host,
        &run("r3", "linter", None),
        json!({"state": "ready"}),
    ));
    changed(ready(&mut host));
}

/// Agents alone: two runs of one Agent are one signer.
#[test]
fn a_requirement_of_agents_counts_each_agent_once() {
    let mut host = hosting(
        json!({"count": 2, "permission": "changes.approve", "callers": ["agent"], "excludeCreator": false}),
        json!({}),
    );
    changed(sign(
        &mut host,
        &run("r1", "reviewer", Some("ann")),
        json!({"state": "ready"}),
    ));
    changed(sign(
        &mut host,
        &run("r2", "reviewer", None),
        json!({"state": "ready"}),
    ));
    assert_eq!(veto_of(ready(&mut host)).1["count"], 1);
    changed(sign(
        &mut host,
        &run("r3", "linter", None),
        json!({"state": "ready"}),
    ));
    changed(ready(&mut host));
}

// ---- proof ----

/// The proof a requirement asks for is the assurance the host reports with
/// the call: a passkey with user verification on the sign-off itself, or a
/// sign-in no longer ago than proofWithinMs. A caller whose host reports
/// none proves nothing.
#[test]
fn a_requirement_asks_for_user_verification_or_a_recent_sign_in() {
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "proof": "user-verification", "excludeCreator": false}),
        json!({}),
    );
    assert_eq!(
        veto_of(sign(&mut host, &ann(), json!({"state": "ready"}))).1,
        json!({"requirements": [{"name": "ready", "reason": "the caller's proof falls short"}]})
    );
    let verified = ann().with_assurance(Assurance {
        user_verified: true,
        ..Assurance::default()
    });
    changed(sign(&mut host, &verified, json!({"state": "ready"})));
    changed(ready(&mut host));

    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "proof": "recent-sign-in", "proofWithinMs": 60000, "excludeCreator": false}),
        json!({}),
    );
    let signed_in = |at: i64| {
        ann().with_assurance(Assurance {
            signed_in_at: Some(at),
            ..Assurance::default()
        })
    };
    assert_eq!(
        veto_of(sign(
            &mut host,
            &signed_in(START - 120_000),
            json!({"state": "ready"})
        ))
        .0,
        "not_counted"
    );
    changed(sign(
        &mut host,
        &signed_in(START - 30_000),
        json!({"state": "ready"}),
    ));
    assert_eq!(sign_off_values(&host)[0]["signedInAt"], START - 30_000);
    changed(ready(&mut host));
}

// ---- exclusions ----

/// The creator is excluded by its subject; the person it acted for by
/// createdFor, matched by a caller's subject, the person it acts for or its
/// run's Agent; and so is whoever a listed field names. A list is read
/// again at each guard, so a signer a later write lists stops counting.
#[test]
fn exclusions_match_the_subject_the_person_and_the_agent() {
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "excludeCreatorPerson": true, "excludeSubjectsIn": ["pushers"], "resetOn": []}),
        json!({"createdBy": "r0", "createdFor": "dana", "pushers": ["ben", "coder"]}),
    );
    let reason = |outcome: Outcome| veto_of(outcome).1["requirements"][0]["reason"].clone();
    assert_eq!(
        veto_of(sign(
            &mut host,
            &run("r0", "coder", Some("dana")),
            json!({"state": "ready"})
        ))
        .0,
        "creator_cannot_sign"
    );
    let dana = caller("dana", &["changes.approve"]);
    assert_eq!(
        reason(sign(&mut host, &dana, json!({"state": "ready"}))),
        "it excludes the person the creator acted for"
    );
    assert_eq!(
        reason(sign(
            &mut host,
            &run("r1", "helper", Some("dana")),
            json!({"state": "ready"})
        )),
        "it excludes the person the creator acted for"
    );
    assert_eq!(
        reason(sign(&mut host, &ben(), json!({"state": "ready"}))),
        "a field it excludes by lists the caller"
    );
    assert_eq!(
        reason(sign(
            &mut host,
            &run("r2", "coder", Some("ann")),
            json!({"state": "ready"})
        )),
        "a field it excludes by lists the caller"
    );
    changed(sign(&mut host, &ann(), json!({"state": "ready"})));
    set(&mut host, json!({"pushers": ["ben", "ann"]}));
    assert_eq!(veto_of(ready(&mut host)).1["count"], 0);
    set(&mut host, json!({"pushers": ["ben"]}));
    changed(ready(&mut host));
}

/// Without excludeCreatorPerson, the person an agent run created a change
/// for may approve it, as a lead approves what they asked an agent to make.
#[test]
fn the_person_the_creator_acted_for_may_sign_unless_excluded() {
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "resetOn": []}),
        json!({"createdBy": "r0", "createdFor": "ann"}),
    );
    changed(sign(&mut host, &ann(), json!({"state": "ready"})));
    changed(ready(&mut host));
}

// ---- the permission read again ----

/// recheckPermission reads each signer's permission at each guard, for
/// whom it counts as: a host that cannot answer for a subject, or answers a
/// permission that does not cover the requirement's, leaves it uncounted.
#[test]
fn a_requirement_reads_each_signers_permission_again() {
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "callers": ["acting-for-person"], "recheckPermission": true, "excludeCreator": false}),
        json!({}),
    );
    changed(sign(
        &mut host,
        &run("r1", "reviewer", Some("ann")),
        json!({"state": "ready"}),
    ));
    assert_eq!(veto_of(ready(&mut host)).1["count"], 0);
    host.memory
        .permissions
        .insert("ann".to_owned(), vec!["changes.read".to_owned()]);
    assert_eq!(veto_of(ready(&mut host)).1["count"], 0);
    host.memory
        .permissions
        .insert("ann".to_owned(), vec!["changes".to_owned()]);
    changed(ready(&mut host));
}

/// agentRole counts a sign-off that counts as its run's Agent through the
/// permissions its Agent's agent role grants now (#625), rechecked or not:
/// the host answers the Agent from its agents table, and an Agent whose
/// role does not cover the requirement's permission, or that the host
/// cannot answer for, counts for nothing. Without agentRole a requirement
/// that rechecks reads no Agent, so the Agent's sign-off counts for
/// nothing, and one that does not counts what the run held when it signed.
#[test]
fn an_agents_sign_off_counts_through_its_agent_role_where_the_requirement_says() {
    for recheck in [false, true] {
        let mut host = hosting(
            json!({"count": 1, "permission": "changes.approve", "callers": ["agent"], "agentRole": true, "recheckPermission": recheck, "excludeCreator": false}),
            json!({}),
        );
        changed(sign(
            &mut host,
            &run("r1", "reviewer", None),
            json!({"state": "ready"}),
        ));
        assert_eq!(
            veto_of(ready(&mut host)).1["count"],
            0,
            "an Agent the host cannot answer for"
        );
        // A person's permissions under the Agent's key do not count: the
        // read asks for it as an Agent.
        host.memory
            .permissions
            .insert("reviewer".to_owned(), vec!["changes".to_owned()]);
        assert_eq!(veto_of(ready(&mut host)).1["count"], 0);
        host.memory
            .agent_permissions
            .insert("reviewer".to_owned(), vec!["changes.read".to_owned()]);
        assert_eq!(
            veto_of(ready(&mut host)).1["count"],
            0,
            "a role that does not cover it"
        );
        host.memory
            .agent_permissions
            .insert("reviewer".to_owned(), vec!["changes".to_owned()]);
        changed(ready(&mut host));
    }

    // Without agentRole, rechecked: the Agent is read for nothing.
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "callers": ["agent"], "recheckPermission": true, "excludeCreator": false}),
        json!({}),
    );
    changed(sign(
        &mut host,
        &run("r1", "reviewer", None),
        json!({"state": "ready"}),
    ));
    host.memory
        .agent_permissions
        .insert("reviewer".to_owned(), vec!["changes".to_owned()]);
    host.memory
        .permissions
        .insert("reviewer".to_owned(), vec!["changes".to_owned()]);
    assert_eq!(veto_of(ready(&mut host)).1["count"], 0);

    // A requirement that counts the run as the person it acts for reads the
    // person, not the Agent, agentRole or not.
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "callers": ["acting-for-person", "agent"], "agentRole": true, "recheckPermission": true, "excludeCreator": false}),
        json!({}),
    );
    changed(sign(
        &mut host,
        &run("r1", "reviewer", Some("ann")),
        json!({"state": "ready"}),
    ));
    host.memory
        .agent_permissions
        .insert("reviewer".to_owned(), vec!["changes".to_owned()]);
    assert_eq!(veto_of(ready(&mut host)).1["count"], 0);
    host.memory
        .permissions
        .insert("ann".to_owned(), vec!["changes".to_owned()]);
    changed(ready(&mut host));
}

// ---- conditions ----

/// A requirement applies when each of its conditions holds; one that does
/// not apply asks for nothing. A condition on a field with no value holds,
/// negated or not, so a fact not yet written never exempts.
#[test]
fn conditions_decide_whether_a_requirement_applies() {
    let lead = |when: Value| json!({"count": 1, "permission": "changes.lead", "excludeCreator": false, "resetOn": [], "when": when});
    // contains: the lead's flag.
    let mut host = hosting(
        lead(json!([{"field": "flags", "test": "contains", "values": ["needs-lead"]}])),
        json!({}),
    );
    assert_eq!(veto_of(ready(&mut host)).0, "approvals_missing");
    set(&mut host, json!({"flags": []}));
    changed(ready(&mut host));
    reopen(&mut host);
    set(&mut host, json!({"flags": ["hold", "needs-lead"]}));
    assert_eq!(veto_of(ready(&mut host)).0, "approvals_missing");

    // every-matches, negated: a change of tests alone needs no lead.
    let mut host = hosting(
        lead(
            json!([{"field": "paths", "test": "every-matches", "values": ["**/*_test.go", "testdata/**"], "negate": true}]),
        ),
        json!({"paths": ["pkg/a_test.go", "testdata/golden/x.json"]}),
    );
    changed(ready(&mut host));
    reopen(&mut host);
    set(&mut host, json!({"paths": ["pkg/a.go", "pkg/a_test.go"]}));
    assert_eq!(veto_of(ready(&mut host)).0, "approvals_missing");

    // any-matches: one path in a lead's area.
    let mut host = hosting(
        lead(json!([{"field": "paths", "test": "any-matches", "values": ["schemas/**"]}])),
        json!({"paths": ["src/a.rs"]}),
    );
    changed(ready(&mut host));
    reopen(&mut host);
    set(&mut host, json!({"paths": ["src/a.rs", "schemas/x.ts"]}));
    assert_eq!(veto_of(ready(&mut host)).0, "approvals_missing");

    // at-most, negated: a large change needs a lead.
    let mut host = hosting(
        lead(json!([{"field": "lines", "test": "at-most", "values": ["400"], "negate": true}])),
        json!({"lines": 120}),
    );
    changed(ready(&mut host));
    reopen(&mut host);
    set(&mut host, json!({"lines": 900}));
    assert_eq!(veto_of(ready(&mut host)).0, "approvals_missing");
    changed(sign(&mut host, &ann(), json!({"state": "ready"})));
    changed(ready(&mut host));
}

// ---- the define checks ----

/// Define holds each new member to the type and to the others: a gate that
/// reads rows names no requirement member and names its resetOn; each field
/// a member names is the type's own; recent-sign-in needs proofWithinMs and
/// nothing else takes it; excludeCreatorPerson needs createdFor.
#[test]
fn define_refuses_what_a_gate_cannot_hold() {
    let refused_rule = |gate: Value| rule(&core(), &change(gate), "Approvals");
    let rows = json!({"schema": "Requirement", "link": "rule", "in": "rules"});
    assert_eq!(
        refused_rule(json!({"count": 1, "resetOn": [], "requirementsFrom": rows})),
        "gate \"ready\" reads its requirements from Requirement's rows, so it names no count: each row holds its own"
    );
    assert!(refused_rule(json!({"requirementsFrom": rows})).starts_with(
        "gate \"ready\" reads its requirements from Requirement's rows and names no resetOn"
    ));
    assert!(refused_rule(json!({"resetOn": [], "requirementsFrom": {"schema": "Requirement", "link": "rule", "in": "ruleList"}}))
        .starts_with("gate \"ready\": requirementsFrom names in \"ruleList\", which is not a field of Change"));
    assert!(
        refused_rule(json!({"resetOn": [], "pinOn": ["sha"], "requirementsFrom": rows}))
            .starts_with("gate \"ready\": pinOn names \"sha\", which is not a field of Change")
    );
    let gate = |extra: Value| {
        let mut gate = json!({"count": 1, "permission": "p", "excludeCreator": false});
        for (key, value) in extra.as_object().expect("members") {
            gate[key] = value.clone();
        }
        gate
    };
    assert!(refused_rule(gate(
        json!({"when": [{"field": "size", "test": "at-most", "values": ["1"]}]})
    ))
    .starts_with(
        "gate \"ready\": when condition 0 names \"size\", which is not a field of Change"
    ));
    assert_eq!(
        refused_rule(gate(
            json!({"when": [{"field": "lines", "test": "at-most", "values": ["many"]}]})
        )),
        "gate \"ready\": when condition 0: at-most takes one number"
    );
    assert!(
        refused_rule(gate(json!({"excludeSubjectsIn": ["reviewers"]}))).starts_with(
            "gate \"ready\": excludeSubjectsIn names \"reviewers\", which is not a field of Change"
        )
    );
    assert_eq!(
        refused_rule(gate(json!({"proof": "recent-sign-in"}))),
        "gate \"ready\": proof recent-sign-in needs proofWithinMs, how recent a sign-in counts"
    );
    assert_eq!(
        refused_rule(gate(json!({"proofWithinMs": 1000}))),
        "gate \"ready\": proofWithinMs is for proof recent-sign-in"
    );
    let without_created_for = TypeSpec::new("Change")
        .with(UseSpec::on_type(
            "Approvals",
            json!({"gates": {"ready": gate(json!({"excludeCreatorPerson": true}))}}),
        ))
        .with(UseSpec::on_field("status", "Workflow", flow()));
    assert_eq!(
        rule(&core(), &without_created_for, "Approvals"),
        "gate \"ready\": excludeCreatorPerson reads the person the creator acted for from createdFor, and Change has no field createdFor"
    );
    // The shapes, in the schema loader's words.
    assert_eq!(
        shape(&core(), &change(json!({"count": 1, "permission": "p", "callers": ["robot"], "requirementsFrom": {"schema": "Requirement"}}))),
        [
            "/config/gates/ready/callers/0: must be one of \"person\", \"acting-for-person\", \"agent\"",
            "/config/gates/ready/requirementsFrom: must have required property 'link'"
        ]
    );
    // A gate that reads rows needs no count or permission.
    assert!(define(
        &core(),
        &change(json!({"resetOn": [], "pinOn": ["head"], "requirementsFrom": rows}))
    )
    .is_ok());
}

// ---- requirements from rows ----

fn rows_gate() -> Value {
    json!({"resetOn": [], "pinOn": ["head"], "requirementsFrom": {"schema": "Requirement", "link": "rule", "in": "rules"}})
}

// review and lead: two rules' requirements, each resetting on its own field.
fn two_rules(host: &mut Host) {
    requirement(
        host,
        "q1",
        json!({"ruleId": "R1", "name": "review", "count": 1, "permission": "changes.approve", "callers": ["person", "acting-for-person", "agent"], "resetOn": ["patch"]}),
    );
    requirement(
        host,
        "q2",
        json!({"ruleId": "R2", "name": "lead", "count": 1, "permission": "changes.lead", "callers": ["person", "acting-for-person"], "resetOn": ["head"]}),
    );
}

fn read_head(head: &str) -> Value {
    json!({"state": "ready", "read": {"head": head}})
}

/// A gate reads its requirements from the rows of every rule the change
/// names, and a sign-off counts toward each requirement it meets; the veto
/// names each requirement still missing.
#[test]
fn a_gate_reads_its_requirements_from_rows() {
    let mut host = hosting(rows_gate(), json!({"rules": ["R1", "R2"]}));
    two_rules(&mut host);
    assert_eq!(
        result(sign(&mut host, &ben(), read_head("h1"))),
        json!({"state": "ready", "subject": "ben", "at": START, "signedBy": ["ben"], "count": 1, "countsToward": ["review"]})
    );
    assert_eq!(
        veto_of(ready(&mut host)),
        (
            "approvals_missing".to_owned(),
            json!({"state": "ready", "required": 1, "count": 0, "missing": [
                {"requirement": "q2", "name": "lead", "required": 1, "count": 0, "permission": "changes.lead", "callers": ["person", "acting-for-person"]}
            ]})
        )
    );
    assert_eq!(
        result(sign(&mut host, &ann(), read_head("h1")))["countsToward"],
        json!(["review", "lead"])
    );
    assert_eq!(
        sign_off_values(&host)[1]["held"],
        json!(["changes.approve", "changes.lead"])
    );
    assert_eq!(
        sign_off_values(&host)[0]["held"],
        json!(["changes.approve"])
    );
    assert_eq!(
        sign_off_values(&host)[1]["values"],
        json!({"head": "h1", "patch": "p1"})
    );
    changed(ready(&mut host));
}

/// The rows are read at each guard: a row whose count rises asks for more,
/// a new row asks for its own, an archived row asks for nothing, and a row
/// whose permission moves stops counting the sign-offs of signers who did
/// not hold the new one.
#[test]
fn a_requirement_row_that_changes_at_run_time_applies_at_the_next_guard() {
    let mut host = hosting(rows_gate(), json!({"rules": ["R1"]}));
    requirement(
        &mut host,
        "q1",
        json!({"ruleId": "R1", "name": "review", "count": 1, "permission": "changes.approve"}),
    );
    changed(sign(&mut host, &ben(), read_head("h1")));
    edit(&mut host, "q1", json!({"count": 2}));
    assert_eq!(veto_of(ready(&mut host)).1["missing"][0]["count"], 1);
    changed(sign(&mut host, &ann(), read_head("h1")));
    // ann held changes.lead when she signed; ben did not.
    edit(&mut host, "q1", json!({"permission": "changes.lead"}));
    assert_eq!(veto_of(ready(&mut host)).1["missing"][0]["count"], 1);
    edit(&mut host, "q1", json!({"count": 1}));
    requirement(
        &mut host,
        "q2",
        json!({"ruleId": "R1", "name": "security", "count": 1, "permission": "changes.security"}),
    );
    assert_eq!(
        veto_of(ready(&mut host)).1["missing"][0]["name"],
        "security"
    );
    edit(&mut host, "q2", json!({"archivedAt": START}));
    changed(ready(&mut host));
}

/// Each requirement spends a sign-off on its own reset fields: the gate's
/// resetOn is [], so no update consumes a sign-off, and a sign-off counts
/// for a requirement only while the fields it resets on hold the values the
/// sign-off recorded. A rebase that keeps the patch keeps the review and
/// spends the lead's; a row that starts resetting on a field the sign-off
/// recorded no value of asks again.
#[test]
fn each_requirement_spends_a_sign_off_on_its_own_reset_fields() {
    let mut host = hosting(rows_gate(), json!({"rules": ["R1", "R2"]}));
    two_rules(&mut host);
    changed(sign(&mut host, &ann(), read_head("h1")));
    set(&mut host, json!({"head": "h2", "findings": ["lint"]}));
    assert_eq!(sign_off_values(&host)[0].get("consumedAt"), None);
    let missing = |host: &mut Host| -> Vec<Value> {
        veto_of(ready(host)).1["missing"]
            .as_array()
            .expect("missing")
            .iter()
            .map(|missing| missing["name"].clone())
            .collect()
    };
    assert_eq!(missing(&mut host), [json!("lead")]);
    set(&mut host, json!({"patch": "p2"}));
    assert_eq!(missing(&mut host), [json!("review"), json!("lead")]);
    set(&mut host, json!({"head": "h1", "patch": "p1"}));
    edit(&mut host, "q1", json!({"resetOn": ["patch", "lines"]}));
    assert_eq!(missing(&mut host), [json!("review")]);
    edit(&mut host, "q1", json!({"resetOn": ["patch"]}));
    changed(ready(&mut host));
}

/// pinOn pins a sign-off to the fields it names: signOff names in read the
/// value of each as its signer read it, one that has moved is stale before
/// any other check, and a write to another field, such as a fact a
/// condition reads, makes no signer stale.
#[test]
fn pin_on_refuses_a_sign_off_on_a_head_the_signer_did_not_read() {
    let mut host = hosting(rows_gate(), json!({"rules": ["R1", "R2"]}));
    two_rules(&mut host);
    assert_eq!(
        refused(sign(&mut host, &ann(), json!({"state": "ready"})))["details"]["issues"],
        json!([{"path": "/read", "message": "is required: the gate on ready pins head, so a sign-off names the value its signer read of each"}])
    );
    assert_eq!(
        refused(sign(
            &mut host,
            &ann(),
            json!({"state": "ready", "read": {"head": "h1", "title": "Fix"}})
        ))["details"]["issues"],
        json!([{"path": "/read", "message": "names title, which the gate on ready does not pin"}])
    );
    assert_eq!(
        veto_of(sign(&mut host, &caller("eve", &[]), read_head("h0"))),
        (
            "stale".to_owned(),
            json!({"field": "head", "read": "h0", "current": "h1"})
        )
    );
    set(&mut host, json!({"findings": ["lint"], "lines": 3}));
    changed(sign(&mut host, &ann(), read_head("h1")));
}

/// The keys a list holds reach the core as the column holds them: a list
/// of UUIDs hyphenated, where a relation's key comes in base62, its wire
/// form. A key in either form finds its rows, so the gate never reads no
/// requirement because of the form a key came in.
#[test]
fn a_listed_key_in_either_form_finds_its_rows() {
    const RULE: &str = "TMXp8QPR6yGvKuV2GZQ10";
    const RULE_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
    let mut host = hosting(rows_gate(), json!({"rules": [RULE_HYPHENATED]}));
    requirement(
        &mut host,
        "q1",
        json!({"ruleId": RULE, "name": "review", "permission": "changes.approve"}),
    );
    assert_eq!(veto_of(ready(&mut host)).1["missing"][0]["name"], "review");
    changed(sign(&mut host, &ben(), read_head("h1")));
    changed(ready(&mut host));
}

/// A row whose members do not parse is broken: it applies, is never met,
/// and the veto says why, while the others still count. A gate whose list
/// of keys holds no value yet does not hold and takes no sign-off; one
/// whose list is empty asks for nothing.
#[test]
fn a_broken_row_and_unknown_requirements_fail_closed() {
    let mut host = hosting(rows_gate(), json!({"rules": ["R1"]}));
    requirement(
        &mut host,
        "q1",
        json!({"ruleId": "R1", "name": "review", "permission": "changes.approve", "callers": ["robot"]}),
    );
    requirement(
        &mut host,
        "q2",
        json!({"ruleId": "R1", "name": "plain", "permission": "changes.approve", "when": [{"field": "size", "test": "contains", "values": ["x"]}]}),
    );
    requirement(
        &mut host,
        "q3",
        json!({"ruleId": "R1", "name": "second", "permission": "changes.approve"}),
    );
    assert_eq!(
        result(sign(&mut host, &ben(), read_head("h1")))["countsToward"],
        json!(["second"])
    );
    assert_eq!(
        veto_of(ready(&mut host)).1["missing"],
        json!([
            {"requirement": "q1", "name": "review", "broken": "callers names \"robot\", which is not person, acting-for-person or agent"},
            {"requirement": "q2", "name": "plain", "broken": "when condition 0 names \"size\", which is not a field of Change (its fields: title, head, patch, paths, lines, flags, findings, pushers, rules, createdBy, createdFor)"}
        ])
    );

    let mut host = hosting(rows_gate(), json!({}));
    two_rules(&mut host);
    assert_eq!(
        veto_of(ready(&mut host)).1,
        json!({"state": "ready", "reason": "its rules holds no keys yet, so its requirements are not known"})
    );
    assert_eq!(
        veto_of(sign(&mut host, &ann(), read_head("h1"))).0,
        "not_counted"
    );
    set(&mut host, json!({"rules": []}));
    changed(ready(&mut host));
}

// ---- the caller on the wire ----

/// A host hands the caller as JSON: its kind, whom it acts for and that
/// one's kind, its run's Agent and its assurance; a sign-off records each,
/// and listSignOffs shows whom it acted for and its Agent.
#[test]
fn a_caller_from_the_wire_carries_its_agent_and_assurance() {
    let mut host = hosting(
        json!({"count": 1, "permission": "changes.approve", "proof": "user-verification", "callers": ["acting-for-person"], "resetOn": []}),
        json!({}),
    );
    let from_wire: Caller = serde_json::from_value(json!({
        "subject": "r1",
        "permissions": ["changes.approve"],
        "kind": "agent",
        "actingFor": "ann",
        "actingForKind": "user",
        "agent": "reviewer",
        "assurance": {"signedInAt": START - 5, "userVerified": true}
    }))
    .expect("a caller");
    changed(sign(&mut host, &from_wire, json!({"state": "ready"})));
    let mut recorded = sign_off_values(&host)[0].clone();
    recorded.remove("at");
    assert_eq!(
        Value::Object(recorded),
        json!({"state": "ready", "subject": "r1", "kind": "agent", "actingFor": "ann", "actingForKind": "user", "agent": "reviewer", "signedInAt": START - 5, "userVerified": true})
    );
    assert_eq!(
        result(host.invoke(&ann(), "c1", "Approvals", "listSignOffs", json!({})))["items"],
        json!([{"state": "ready", "subject": "r1", "actingFor": "ann", "agent": "reviewer", "at": START}])
    );
    changed(ready(&mut host));
}
