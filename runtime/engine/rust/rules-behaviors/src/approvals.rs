//! Approvals, sign-offs on the moves of a Workflow (D56), ported onto the
//! pure core (D103) from the engine's (`../behaviors/src/approvals.rs`),
//! with gates whose sign-offs a change of chosen fields consumes and that
//! pin the version their signer read (D104), and with requirements each
//! instance sets (D200). The config gates states: a transition into a gated
//! state, from a state the gate's `from` lists (every state when it lists
//! none), waits until each of the gate's requirements that applies has
//! `count` distinct signers. A sign-off is one caller's, by its subject,
//! for one state; `signOff` needs the permission of one of the gate's
//! requirements, and a caller who holds none is forbidden. The row's
//! creator, the subject its own field `createdBy` holds (the layout's audit
//! field on a table, D87), cannot sign unless the requirement sets
//! `excludeCreator` to false, and nobody signs for the state the row is in.
//! `withdrawSignOff` takes the caller's own back, and `listSignOffs` reads
//! them a page at a time, oldest first, and writes nothing. Every refusal is
//! a veto with a code the declaration lists, but the missing permission and
//! a bad parameter.
//!
//! Approvals passes D103's test: its state is its `signOffs` sub-table, a
//! row per sign-off; it has rules over how that state changes (a sign-off
//! given, withdrawn, consumed by a change of the fields its gate names or
//! by entering the gate's state) and over the moves it gates; and it
//! recurs, as an expense two approvers sign, a document's review, a
//! release's go-ahead, a change's merge gate.
//!
//! Requirements (D200). A gate is one requirement written in the config
//! (`count`, `permission` and the members below), or reads its requirements
//! from the rows of a type (`requirementsFrom`), which each instance writes
//! and changes at run time while the config stays its build's (D103): the
//! rows of `schema` whose to-one relation `link` points at the row, or with
//! `in`, at one of the keys the row's own list field `in` holds. Each row
//! holds a requirement's members as its own fields, may hold `name`,
//! `resetOn` and `archivedAt`, and an archived row asks for nothing. The
//! rows are read a linked page at a time, as Rollups reads, up to
//! [`MAX_REQUIREMENT_READ`]; past it, or when `in` holds no value yet, the
//! gate does not hold and no sign-off is taken. A requirement's members, and
//! the default of each, which keeps a gate written before D200 as it was:
//!
//! - `count`, 1 on a row; `permission`, which a signer holds;
//! - `callers`: which callers count, and as whom. `person` is a person
//!   calling as themselves, counted by its subject; `acting-for-person` an
//!   agent run acting for a person, counted as that person; `agent` an agent
//!   run, counted as its Agent ([`superschematic_rules::Caller::agent`]).
//!   Absent, every caller counts, as its subject;
//! - `proof`: `none`; `recent-sign-in`, a sign-in at most `proofWithinMs`
//!   before the sign-off; `user-verification`, a passkey with user
//!   verification on the sign-off itself, which counts as a sign-in too. The
//!   host reports the caller's assurance; the core trusts it as it trusts
//!   the subject. Absent, `none`;
//! - `excludeCreator`, true: the subject `createdBy` holds is refused, by
//!   its subject or as whom it would count; `excludeCreatorPerson`, false:
//!   the subject the row's own `createdFor` holds is refused too, matched by
//!   the caller's subject, the person it acts for or its run's Agent;
//! - `excludeSubjectsIn`, none: own fields that list subjects, persons and
//!   Agents, matched as `createdFor` is, at the sign-off and again at each
//!   guard;
//! - `resetOn` (a row's), none: own fields whose change spends a sign-off
//!   for this requirement alone. Each sign-off records the value of every
//!   field the gate's rows reset on when it is made (`values`), and counts
//!   for a requirement only while its fields still hold them; a field the
//!   sign-off recorded no value of spends it, so a row that starts resetting
//!   on a new field asks again. A gate written in the config spends its
//!   sign-offs by consuming them instead (its own `resetOn`, D104);
//! - `recheckPermission`, false: each guard reads each signer's permission
//!   again ([`superschematic_rules::ReadRequest::Permissions`], for whom it
//!   counts as), so a signer who lost it stops counting. Without it a row's
//!   requirement counts the permissions the caller held when it signed
//!   (`held`, every one, so a row whose permission moves later counts a
//!   signer who held the new one), and a gate written in the config does
//!   not read it again;
//! - `when`, none: conditions on the row's own fields, all of which must
//!   hold for the requirement to apply. Each tests a field: `every-matches`
//!   (every value matches one of the glob patterns), `any-matches`,
//!   `contains` (the field holds one of the values) or `at-most` (its number
//!   is at most the one value), and `negate` inverts it. A condition on a
//!   field with no value holds, negated or not, so a fact not yet written
//!   never exempts. A requirement that does not apply asks for nothing; a
//!   gate whose rows ask for nothing holds.
//!
//! A sign-off counts toward every requirement it meets. A row whose members
//! do not parse, or name a field the type lacks, is broken: it applies and
//! is never met, and the veto names why, so a mistake in a row fails closed.
//!
//! The guard reads the state from the `to` of a transition of the Workflow
//! the gates are of, and runs for every transition request, a caller's,
//! another use's `call()` (a Timers fire), another row's `call_on()` (a
//! Reactions move) alike, before any handler: `approvals_missing`, with the
//! state, the count the first unmet requirement needs and the sign-offs it
//! has in its details, and for a gate that reads rows, each requirement it
//! misses (`missing`). It lets a move Workflow's config refuses through for
//! Workflow to refuse, whichever of the two the type lists first.
//!
//! A sign-off counts until it is consumed. The after-change hook consumes
//! them, from the row as the change leaves it, never from which operation
//! ran: a Timers fire or a Reactions move changes the status as a caller's
//! transition does.
//!
//! - An update through the core consumes the sign-offs of each gate whose
//!   `resetOn` names a field it changed (D104), `[]` naming none; a gate
//!   without `resetOn` follows `resetOnChange`, true when absent: any update
//!   consumes its sign-offs, what was signed is what moves. A gate that
//!   reads its requirements from rows names its `resetOn`, so an update it
//!   does not name spends nothing.
//! - A row in a gated state has that gate's consumed: entering it used
//!   them, so a return to it signs again, whatever `resetOn` says.
//! - A sign-off for a state the config no longer gates is consumed at the
//!   row's next write through the core. The engine's afterConfigChange
//!   consumed them at the publish; the core writes no row at a publish (a
//!   build-time project's config change is a build, D103).
//!
//! A consumed sign-off stays in the sub-table, with the time it was
//! consumed, for `listSignOffs`; a read of the row shows the ones that
//! count as the engine's `Approvals.gates` field ([`Behavior::read_fields`]),
//! by gated state, each gate's subjects in the order they signed, and no
//! gate with none. A withdrawal deletes its row. Deleting the row deletes
//! its sign-offs, which the host's delete does. A sign-off's key is its
//! number on the row, one more than the greatest it has held, in ten digits
//! zero-padded, so the key's order is the order they were given in and the
//! engine's cursor over its row id carries over; a withdrawal of the
//! greatest number frees it for the next, as SQLite's row id did.
//!
//! A gate may be pinned (D104): `signOff` then names `version`, the row's
//! version its signer read, the `@optimistic` column every write through
//! the core is held to and moves, a sign-off's own among them. A `version`
//! on any gate is checked, before any guard and before the gate's
//! permission ([`Behavior::settle`]), as Workflow's `from` is: one that is
//! not the row's version is the veto `stale`, details `{ version, current
//! }`, so the signer reads again; a pinned gate's `signOff` without one is
//! a bad parameter. On a gate whose count is above one, each later signer
//! signs against the version the earlier sign-off left. A gate may instead
//! pin named fields (`pinOn`, D200): `signOff` names in `read` the value of
//! each as its signer read it, and one that is not the field's value now is
//! `stale`, details `{ field, read, current }`; a write to another field,
//! such as a fact the gate's conditions read, makes no signer stale.
//!
//! Which Workflow the gates are of is the config's `status`, the id of a
//! Workflow use of the type, as StateRules' is: implied by the type's one,
//! named among several. The engine read the instance's one `Workflow`.
//!
//! A write around the core (a repository's bulk update, raw SQL) consumes
//! no sign-off: Approvals hears no change (D103), the escape hatch's price.
//! A table with Approvals keeps a sub-table, so its repositories have no
//! upserts.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, walked by [`crate::ajv`], each issue
//! at its path in the schema loader's words ([`ConfigError::Schema`]), then
//! each gated and from state held to the Workflow, each field a gate names
//! to the type's own, and a requirement that excludes the creator to a type
//! with a `createdBy` to read it from, in the engine's words
//! ([`ConfigError::Rule`]). The operations' parameters are held to their
//! closed paramsSchemas in the behavior registry's words. Messages name the
//! row `{type} {key}`, where the engine's named `{schema} {id}`. What reads
//! another type (`requirementsFrom`'s type, its relation and its members'
//! fields) is the build's to check and the run's, since a type's define
//! check sees its own spec alone.
//!
//! configChange: gates may be added, removed and changed, and a gate's new
//! count or permission applies to the sign-offs already given. Approvals
//! can be added to a type that has rows, which start with none, and cannot
//! be removed from one: its sign-offs would stay behind with nothing to
//! delete them.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::OnceLock;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    has_any_permission, key_of, Behavior, Caller, Change, ConfigCx, ConfigError, ConfigTarget, Cx,
    Declaration, GuardRequest, Hook, KeyedRow, Refusal, Row, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::ajv;
use crate::declared;
use crate::flow::{self, flow_of, Flow};
use crate::paging::{page, page_request, Page};
use crate::subjects::{canonical_subject, names_subject};
use crate::values::equal;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Approvals";

/// The sub-table that holds a row per sign-off, keyed by its number.
pub const SIGN_OFFS: &str = "signOffs";

/// The operation that signs off on a row for a gated state.
pub const SIGN_OFF: &str = "signOff";

/// The operation that takes a caller's sign-off back.
pub const WITHDRAW_SIGN_OFF: &str = "withdrawSignOff";

/// The operation that lists a row's sign-offs, a page at a time.
pub const LIST_SIGN_OFFS: &str = "listSignOffs";

/// Approvals' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Approvals.behavior.json");

/// The most requirement rows a gate reads (D200): twenty linked pages, as
/// Rollups reads. Past it the gate does not hold.
pub const MAX_REQUIREMENT_READ: usize = 20 * superschematic_rules::LINKED_PAGE;

// The own field a requirement that excludes the creator reads the creator
// from: the layout's audit field on a table (D87, D104).
const CREATED_BY: &str = "createdBy";

// The own field a requirement that excludes the creator's person reads the
// person the creator acted for from (D200).
const CREATED_FOR: &str = "createdFor";

// A requirement row's own fields that name it and archive it.
const ROW_NAME: &str = "name";
const ARCHIVED_AT: &str = "archivedAt";

// The members a gate that reads its requirements from rows leaves to them.
const ROW_MEMBERS: [&str; 10] = [
    "callers",
    "count",
    "excludeCreator",
    "excludeCreatorPerson",
    "excludeSubjectsIn",
    "permission",
    "proof",
    "proofWithinMs",
    "recheckPermission",
    "when",
];

// The width of a sign-off's number in its key.
const NUMBER_WIDTH: usize = 10;

// The declaration file, read once.
fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// Who a caller is to a requirement that lists the callers it counts
/// (D200).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CallerClass {
    /// A person calling as themselves, counted by its subject.
    Person,
    /// An agent run acting for a person, counted as that person.
    ActingForPerson,
    /// An agent run, counted as its Agent.
    Agent,
}

impl CallerClass {
    fn parse(name: &str) -> Option<CallerClass> {
        match name {
            "person" => Some(CallerClass::Person),
            "acting-for-person" => Some(CallerClass::ActingForPerson),
            "agent" => Some(CallerClass::Agent),
            _ => None,
        }
    }

    /// The class's name, as a config and a row name it.
    pub fn name(self) -> &'static str {
        match self {
            CallerClass::Person => "person",
            CallerClass::ActingForPerson => "acting-for-person",
            CallerClass::Agent => "agent",
        }
    }
}

/// What each sign-off of a requirement shows (D200).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Proof {
    /// Nothing beyond the call.
    #[default]
    None,
    /// A sign-in at most `proofWithinMs` before the sign-off.
    RecentSignIn,
    /// A passkey with user verification on the sign-off itself.
    UserVerification,
}

impl Proof {
    fn parse(name: &str) -> Option<Proof> {
        match name {
            "none" => Some(Proof::None),
            "recent-sign-in" => Some(Proof::RecentSignIn),
            "user-verification" => Some(Proof::UserVerification),
            _ => None,
        }
    }
}

/// How a condition tests a field (D200).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConditionTest {
    /// Every value of the field matches one of the glob patterns.
    EveryMatches,
    /// Some value of the field matches one of the glob patterns.
    AnyMatches,
    /// The field holds one of the values.
    Contains,
    /// The field's number is at most the one value.
    AtMost,
}

impl ConditionTest {
    fn parse(name: &str) -> Option<ConditionTest> {
        match name {
            "every-matches" => Some(ConditionTest::EveryMatches),
            "any-matches" => Some(ConditionTest::AnyMatches),
            "contains" => Some(ConditionTest::Contains),
            "at-most" => Some(ConditionTest::AtMost),
            _ => None,
        }
    }
}

/// One condition under which a requirement applies (D200).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Condition {
    pub field: String,
    pub test: ConditionTest,
    pub values: Vec<String>,
    pub negate: bool,
}

/// One requirement of a gate (D200): the config's, or a row's.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Requirement {
    /// How a refusal names it: a row's `name`, else its key; the gated
    /// state for the config's.
    pub name: String,
    /// How many distinct signers it needs.
    pub count: i64,
    /// The permission a signer holds.
    pub permission: String,
    /// The callers it counts; every caller, by its subject, when `None`.
    pub callers: Option<Vec<CallerClass>>,
    pub proof: Proof,
    pub proof_within_ms: Option<i64>,
    pub exclude_creator: bool,
    pub exclude_creator_person: bool,
    pub exclude_subjects_in: Vec<String>,
    /// A row's own fields whose change spends a sign-off for it; none for
    /// the config's, whose gate consumes instead.
    pub reset_on: Vec<String>,
    pub recheck_permission: bool,
    pub when: Vec<Condition>,
}

/// Where a gate reads its requirements from (D200).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RequirementsFrom {
    /// The type whose rows are the requirements.
    pub schema: String,
    /// The to-one relation of its rows the rows are found by.
    pub link: String,
    /// The row's own list field that holds the keys `link` points at; the
    /// row itself when `None`.
    pub within: Option<String>,
}

/// A gate's requirements: one the config writes, or the rows of a type.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Requirements {
    Config(Requirement),
    Rows(RequirementsFrom),
}

/// One gate of an Approvals config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsGate {
    pub requirements: Requirements,
    /// The states a transition from is gated; every state when `None`.
    pub from: Option<Vec<String>>,
    /// The own fields whose change consumes the gate's sign-offs; None
    /// follows resetOnChange (D104).
    pub reset_on: Option<Vec<String>>,
    /// Whether a sign-off names the row's version its signer read (D104).
    pub pinned: bool,
    /// The own fields a sign-off names the values of as its signer read
    /// them (D200).
    pub pin_on: Vec<String>,
}

/// Approvals' config, parsed: the defaults filled in, and the Workflow its
/// gates are of resolved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApprovalsConfig {
    /// By gated state, in the config's order.
    pub gates: Vec<(String, ApprovalsGate)>,
    /// Whether an update consumes the sign-offs of every gate that names no
    /// resetOn.
    pub reset_on_change: bool,
    /// The Workflow use the gates are of, and its transitions.
    pub status: String,
    pub transitions: Vec<(String, String)>,
    /// The IR type of the type's createdBy field, which a requirement that
    /// excludes the creator reads; None when the type has none.
    pub creator_type: Option<String>,
    /// The type's own fields, which a requirement read from rows names.
    pub fields: Vec<String>,
}

impl ApprovalsConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the Workflow it reads and each gate held to it and to
    /// the type's own fields, a rule's reason in the engine's words, which
    /// the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<ApprovalsConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let flow = flow_of(target, raw.get("status").and_then(Value::as_str))?;
        let empty = Map::new();
        let gates = raw
            .get("gates")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let type_name = target.type_name();
        let own = OwnFields {
            type_name: type_name.to_owned(),
            names: target
                .fields()
                .iter()
                .map(|field| field.name.clone())
                .collect(),
        };
        let creator_type = target
            .fields()
            .iter()
            .find(|field| field.name == CREATED_BY)
            .map(|field| field.type_name.clone());
        let mut parsed = Vec::new();
        for (state, gate) in gates {
            let all = flow.listed();
            if flow.lacks(state) {
                return Err(format!(
                    "gate \"{state}\" is not a state of the type's Workflow ({all})"
                )
                .into());
            }
            let from = gate.get("from").map(|from| strings(Some(from)));
            for source in from.iter().flatten() {
                if source == state {
                    return Err(format!(
                        "gate \"{state}\": from names \"{state}\" itself, and no transition leads from a state to itself"
                    )
                    .into());
                }
                if flow.lacks(source) {
                    return Err(format!(
                        "gate \"{state}\": from state \"{source}\" is not a state of the type's Workflow ({all})"
                    )
                    .into());
                }
            }
            let reset_on = gate.get("resetOn").map(|fields| strings(Some(fields)));
            for field in reset_on.iter().flatten() {
                if !own.has(field) {
                    return Err(format!(
                        "gate \"{state}\": resetOn names \"{field}\", which is not a field of {type_name} (its fields: {})",
                        own.listed()
                    )
                    .into());
                }
            }
            let pin_on = strings(gate.get("pinOn"));
            for field in &pin_on {
                if !own.has(field) {
                    return Err(format!(
                        "gate \"{state}\": pinOn names \"{field}\", which is not a field of {type_name} (its fields: {})",
                        own.listed()
                    )
                    .into());
                }
            }
            let members = gate.as_object().unwrap_or(&empty);
            let requirements = match gate.get("requirementsFrom") {
                Some(rows) => {
                    let schema = text(rows.get("schema"));
                    if let Some(member) = ROW_MEMBERS.iter().find(|member| members.contains_key(**member)) {
                        return Err(format!(
                            "gate \"{state}\" reads its requirements from {schema}'s rows, so it names no {member}: each row holds its own"
                        )
                        .into());
                    }
                    if reset_on.is_none() {
                        return Err(format!(
                            "gate \"{state}\" reads its requirements from {schema}'s rows and names no resetOn, so any update would consume every sign-off, the writes of the facts its rows read among them: name the fields whose change spends every sign-off, [] for none, and give each row its own resetOn"
                        )
                        .into());
                    }
                    let within = rows.get("in").and_then(Value::as_str).map(str::to_owned);
                    if let Some(field) = within.as_deref().filter(|field| !own.has(field)) {
                        return Err(format!(
                            "gate \"{state}\": requirementsFrom names in \"{field}\", which is not a field of {type_name} (its fields: {})",
                            own.listed()
                        )
                        .into());
                    }
                    Requirements::Rows(RequirementsFrom {
                        schema,
                        link: text(rows.get("link")),
                        within,
                    })
                }
                None => {
                    let requirement = requirement_of(
                        state,
                        members,
                        &own,
                        creator_type.is_some(),
                        Source::Config,
                    )
                    .map_err(|why| match why {
                        Broken::Creator => ConfigError::from(format!(
                            "gate \"{state}\" excludes the creator (excludeCreator is true when absent), and {type_name} has no field createdBy to read the creator from: give {type_name} a createdBy field, or set excludeCreator to false"
                        )),
                        Broken::Reason(reason) => {
                            ConfigError::from(format!("gate \"{state}\": {reason}"))
                        }
                    })?;
                    Requirements::Config(requirement)
                }
            };
            parsed.push((
                state.clone(),
                ApprovalsGate {
                    requirements,
                    from,
                    reset_on,
                    pinned: gate.get("pinned") == Some(&Value::Bool(true)),
                    pin_on,
                },
            ));
        }
        let Flow {
            use_id,
            transitions,
            ..
        } = flow;
        Ok(ApprovalsConfig {
            gates: parsed,
            reset_on_change: raw.get("resetOnChange") != Some(&Value::Bool(false)),
            status: use_id,
            transitions,
            creator_type,
            fields: own.names,
        })
    }

    /// The gate on a state; `None` when the config gates it not.
    pub fn gate(&self, state: &str) -> Option<&ApprovalsGate> {
        self.gates
            .iter()
            .find(|(gated, _)| gated == state)
            .map(|(_, gate)| gate)
    }

    // leads says whether a transition of the Workflow leads from one state
    // to another.
    fn leads(&self, from: &str, to: &str) -> bool {
        flow::leads(&self.transitions, from, to)
    }

    // gated lists the gated states, as a refusal names them.
    fn gated(&self) -> String {
        self.gates
            .iter()
            .map(|(state, _)| state.as_str())
            .collect::<Vec<_>>()
            .join(", ")
    }

    // own is the type's own fields, as a requirement read from a row is
    // held to them.
    fn own(&self, type_name: &str) -> OwnFields {
        OwnFields {
            type_name: type_name.to_owned(),
            names: self.fields.clone(),
        }
    }
}

// OwnFields is the type's own fields, by name, which a requirement names.
struct OwnFields {
    type_name: String,
    names: Vec<String>,
}

impl OwnFields {
    fn has(&self, name: &str) -> bool {
        self.names.iter().any(|known| known == name)
    }

    fn listed(&self) -> String {
        if self.names.is_empty() {
            "none".to_owned()
        } else {
            self.names.join(", ")
        }
    }

    // lacks words a member that names a field the type lacks.
    fn lacks(&self, member: &str, field: &str) -> Broken {
        Broken::Reason(format!(
            "{member} names \"{field}\", which is not a field of {} (its fields: {})",
            self.type_name,
            self.listed()
        ))
    }
}

// Source says whose a requirement is: the config's, which the
// configSchema has already held to its shape, or a row's, held here.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Source {
    Config,
    Row,
}

// Broken is why a requirement does not parse: the creator it excludes and
// cannot read, which the config words as before D200, or another reason.
enum Broken {
    Creator,
    Reason(String),
}

impl From<String> for Broken {
    fn from(reason: String) -> Self {
        Broken::Reason(reason)
    }
}

// requirement_of reads a requirement from the config's gate or a row's own
// fields, with each member's default.
fn requirement_of(
    name: &str,
    members: &Map<String, Value>,
    own: &OwnFields,
    has_creator: bool,
    source: Source,
) -> Result<Requirement, Broken> {
    let member = |key: &str| members.get(key).filter(|value| !value.is_null());
    let flag = |key: &str, absent: bool| -> Result<bool, Broken> {
        match member(key) {
            None => Ok(absent),
            Some(Value::Bool(flag)) => Ok(*flag),
            Some(_) => Err(format!("{key} must be true or false").into()),
        }
    };
    let names = |key: &str| -> Result<Vec<String>, Broken> {
        match member(key) {
            None => Ok(Vec::new()),
            Some(Value::Array(items)) => items
                .iter()
                .map(|item| match item {
                    Value::String(name) if !name.is_empty() => Ok(name.clone()),
                    _ => Err(Broken::from(format!("{key} must list field names"))),
                })
                .collect(),
            Some(_) => Err(format!("{key} must list field names").into()),
        }
    };
    let count = match member("count") {
        None => 1,
        Some(value) => whole(value)
            .filter(|count| *count >= 1)
            .ok_or_else(|| Broken::from("count must be a whole number of at least 1".to_owned()))?,
    };
    let permission = match member("permission") {
        Some(Value::String(permission)) if !permission.is_empty() => permission.clone(),
        _ => return Err("it names no permission".to_owned().into()),
    };
    let callers = match member("callers") {
        None => None,
        Some(Value::Array(items)) => {
            let mut classes = Vec::new();
            for item in items {
                let class = item.as_str().and_then(CallerClass::parse).ok_or_else(|| {
                    Broken::from(format!(
                        "callers names {item}, which is not person, acting-for-person or agent"
                    ))
                })?;
                if !classes.contains(&class) {
                    classes.push(class);
                }
            }
            if classes.is_empty() {
                return Err("callers names no caller, so no sign-off counts".to_owned().into());
            }
            Some(classes)
        }
        Some(_) => return Err("callers must list callers".to_owned().into()),
    };
    let proof = match member("proof") {
        None => Proof::None,
        Some(value) => value.as_str().and_then(Proof::parse).ok_or_else(|| {
            Broken::from(format!(
                "proof is {value}, which is not none, recent-sign-in or user-verification"
            ))
        })?,
    };
    let proof_within_ms = match member("proofWithinMs") {
        None => None,
        Some(value) => Some(whole(value).filter(|ms| *ms >= 1).ok_or_else(|| {
            Broken::from("proofWithinMs must be a whole number of at least 1".to_owned())
        })?),
    };
    if proof == Proof::RecentSignIn && proof_within_ms.is_none() {
        return Err(
            "proof recent-sign-in needs proofWithinMs, how recent a sign-in counts"
                .to_owned()
                .into(),
        );
    }
    if source == Source::Config && proof != Proof::RecentSignIn && proof_within_ms.is_some() {
        return Err("proofWithinMs is for proof recent-sign-in".to_owned().into());
    }
    let exclude_creator = flag("excludeCreator", true)?;
    if exclude_creator && !has_creator {
        return Err(match source {
            Source::Config => Broken::Creator,
            Source::Row => Broken::Reason(format!(
                "it excludes the creator (excludeCreator is true when absent), and {} has no field createdBy to read the creator from",
                own.type_name
            )),
        });
    }
    let exclude_creator_person = flag("excludeCreatorPerson", false)?;
    if exclude_creator_person && !own.has(CREATED_FOR) {
        return Err(format!(
            "excludeCreatorPerson reads the person the creator acted for from createdFor, and {} has no field createdFor",
            own.type_name
        )
        .into());
    }
    let exclude_subjects_in = names("excludeSubjectsIn")?;
    if let Some(field) = exclude_subjects_in.iter().find(|field| !own.has(field)) {
        return Err(own.lacks("excludeSubjectsIn", field));
    }
    let reset_on = match source {
        Source::Config => Vec::new(),
        Source::Row => names("resetOn")?,
    };
    if let Some(field) = reset_on.iter().find(|field| !own.has(field)) {
        return Err(own.lacks("resetOn", field));
    }
    let recheck_permission = flag("recheckPermission", false)?;
    let when = conditions_of(member("when"), own)?;
    Ok(Requirement {
        name: name.to_owned(),
        count,
        permission,
        callers,
        proof,
        proof_within_ms,
        exclude_creator,
        exclude_creator_person,
        exclude_subjects_in,
        reset_on,
        recheck_permission,
        when,
    })
}

// conditions_of reads a requirement's `when`: a list, or its JSON text as a
// host may hand a JSON column.
fn conditions_of(value: Option<&Value>, own: &OwnFields) -> Result<Vec<Condition>, Broken> {
    let parsed;
    let value = match value {
        None => return Ok(Vec::new()),
        Some(Value::String(text)) => {
            parsed = serde_json::from_str::<Value>(text)
                .map_err(|_| Broken::from("when is no JSON list of conditions".to_owned()))?;
            &parsed
        }
        Some(value) => value,
    };
    let Some(items) = value.as_array() else {
        return Err("when must list conditions".to_owned().into());
    };
    let mut conditions = Vec::new();
    for (i, item) in items.iter().enumerate() {
        let field = text(item.get("field"));
        if field.is_empty() {
            return Err(format!("when condition {i} names no field").into());
        }
        if !own.has(&field) {
            return Err(own.lacks(&format!("when condition {i}"), &field));
        }
        let test = item
            .get("test")
            .and_then(Value::as_str)
            .and_then(ConditionTest::parse)
            .ok_or_else(|| {
                Broken::from(format!(
                    "when condition {i}'s test is not every-matches, any-matches, contains or at-most"
                ))
            })?;
        let values: Vec<String> = match item.get("values") {
            Some(Value::Array(values)) if !values.is_empty() => values
                .iter()
                .map(|value| {
                    value.as_str().map(str::to_owned).ok_or_else(|| {
                        Broken::from(format!("when condition {i}'s values must be strings"))
                    })
                })
                .collect::<Result<_, _>>()?,
            _ => return Err(format!("when condition {i} names no values").into()),
        };
        if test == ConditionTest::AtMost
            && (values.len() != 1 || number_of_text(&values[0]).is_none())
        {
            return Err(format!("when condition {i}: at-most takes one number").into());
        }
        let negate = match item.get("negate") {
            None | Some(Value::Null) => false,
            Some(Value::Bool(negate)) => *negate,
            Some(_) => return Err(format!("when condition {i}'s negate must be true or false").into()),
        };
        conditions.push(Condition {
            field,
            test,
            values,
            negate,
        });
    }
    Ok(conditions)
}

/// Approvals: sign-offs a move into a gated state waits for.
#[derive(Clone, Copy, Debug, Default)]
pub struct Approvals;

impl Behavior for Approvals {
    type Config = ApprovalsConfig;

    // The declaration file names its signOffs sub-table and its computed
    // gates field (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<ApprovalsConfig, ConfigError> {
        ApprovalsConfig::parse(raw, target)
    }

    // Gates may change; Approvals joins a type with rows and does not leave
    // one, whose sign-offs would stay behind.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&ApprovalsConfig>,
        after: Option<&ApprovalsConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none()).then(|| {
            "the sign-offs its instances hold would stay behind with nothing to delete them"
                .to_owned()
        }))
    }

    fn check_params(
        &self,
        config: &ApprovalsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        let issues = ajv::params_issues(file(), operation, params);
        if !issues.is_empty() || operation != SIGN_OFF {
            return issues;
        }
        let state = state_param(params);
        let Some(gate) = config.gate(state) else {
            return Vec::new();
        };
        // A pinned gate's sign-off names the version its signer read
        // (D104).
        if gate.pinned && params.get("version").is_none() {
            return vec![SchemaIssue::new(
                "/version",
                format!(
                    "is required: the gate on {state} is pinned, so a sign-off names the row's version its signer read"
                ),
            )];
        }
        // A gate that pins fields takes the value of each, and no other
        // (D200).
        let read = params.get("read").and_then(Value::as_object);
        match read {
            None if !gate.pin_on.is_empty() => vec![SchemaIssue::new(
                "/read",
                format!(
                    "is required: the gate on {state} pins {}, so a sign-off names the value its signer read of each",
                    gate.pin_on.join(", ")
                ),
            )],
            None => Vec::new(),
            Some(read) => {
                let mut issues = Vec::new();
                for field in gate.pin_on.iter().filter(|field| !read.contains_key(*field)) {
                    issues.push(SchemaIssue::new(
                        "/read",
                        format!("must have required property '{field}'"),
                    ));
                }
                for field in read.keys().filter(|field| !gate.pin_on.contains(field)) {
                    issues.push(SchemaIssue::new(
                        "/read",
                        format!("names {field}, which the gate on {state} does not pin"),
                    ));
                }
                issues
            }
        }
    }

    // settle holds a sign-off that names a version or the values of pinned
    // fields to the row's, before any guard and before the gate's
    // permission, as Workflow's from is (D104, D200): a stale read is
    // answered to any caller. Only for a state the config gates; any other
    // goes on to no_gate.
    fn settle(
        &self,
        view: &View<'_, ApprovalsConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        if operation != SIGN_OFF {
            return Ok(None);
        }
        let Some(gate) = view.config().gate(state_param(params)) else {
            return Ok(None);
        };
        if let (Some(named), Some(current)) =
            (params.get("version").and_then(whole), view.version())
        {
            if named != current {
                let mut details = Map::new();
                details.insert("version".into(), Value::from(named));
                details.insert("current".into(), Value::from(current));
                return Err(view.veto(
                    Veto::new(format!(
                        "{} is at version {current}, not {named}",
                        row(view)
                    ))
                    .with_code("stale")
                    .with_details(details),
                ));
            }
        }
        let Some(read) = params.get("read").and_then(Value::as_object) else {
            return Ok(None);
        };
        for field in &gate.pin_on {
            let named = read.get(field);
            let current = view.own_field(field);
            if !equal(named, current.as_ref()) {
                let mut details = Map::new();
                details.insert("field".into(), Value::from(field.as_str()));
                details.insert("read".into(), named.cloned().unwrap_or(Value::Null));
                details.insert("current".into(), current.clone().unwrap_or(Value::Null));
                return Err(view.veto(
                    Veto::new(format!(
                        "{}'s {field} is {}, not {}",
                        row(view),
                        current.unwrap_or(Value::Null),
                        named.cloned().unwrap_or(Value::Null)
                    ))
                    .with_code("stale")
                    .with_details(details),
                ));
            }
        }
        Ok(None)
    }

    // guard is the gate: a transition of the Workflow the gates are of into
    // a gated state, from a state the gate allows, waits until each of its
    // requirements that applies is met. A move Workflow's config refuses is
    // Workflow's to refuse.
    fn guard(
        &self,
        view: &View<'_, ApprovalsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        let Some(params) = request.operation_of(&config.status, workflow::TRANSITION) else {
            return Ok(None);
        };
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let Some(gate) = config.gate(to) else {
            return Ok(None);
        };
        let Some(from) = status_of(view) else {
            return Ok(None);
        };
        if !config.leads(&from, to) {
            return Ok(None);
        }
        if gate
            .from
            .as_ref()
            .is_some_and(|states| !states.contains(&from))
        {
            return Ok(None);
        }
        let read = match requirements_of(view, gate)? {
            Ok(read) => read,
            Err(reason) => {
                let mut details = Map::new();
                details.insert("state".into(), Value::from(to));
                details.insert("reason".into(), Value::from(reason.as_str()));
                return Ok(Some(
                    Veto::new(format!("{} cannot move to {to}: {reason}", row(view)))
                        .with_code("approvals_missing")
                        .with_details(details),
                ));
            }
        };
        let signers: Vec<Signer> = view
            .sub_rows(SIGN_OFFS)?
            .iter()
            .filter(|row| is_live(row) && row.value("state").and_then(Value::as_str) == Some(to))
            .filter_map(Signer::of_row)
            .collect();
        let own = view.own();
        let applying: Vec<&ReadRequirement> = read
            .iter()
            .filter(|found| match &found.requirement {
                Ok(requirement) => applies(requirement, &own),
                Err(_) => true,
            })
            .collect();
        let held_now = rechecked(view, &applying, &signers)?;
        let rows = matches!(gate.requirements, Requirements::Rows(_));
        let mut missing: Vec<(&ReadRequirement, i64)> = Vec::new();
        for found in &applying {
            let counted = match &found.requirement {
                Ok(requirement) => {
                    let met = met_by(view, requirement, rows, &signers, &own, held_now);
                    if met >= requirement.count {
                        continue;
                    }
                    met
                }
                Err(_) => 0,
            };
            missing.push((found, counted));
        }
        let Some((first, counted)) = missing.first() else {
            return Ok(None);
        };
        let required = first.requirement.as_ref().map_or(1, |requirement| requirement.count);
        let mut details = Map::new();
        details.insert("state".into(), Value::from(to));
        details.insert("required".into(), Value::from(required));
        details.insert("count".into(), Value::from(*counted));
        if !rows {
            return Ok(Some(
                Veto::new(format!(
                    "{} cannot move to {to} with {counted} of the {required} sign-offs it needs",
                    row(view)
                ))
                .with_code("approvals_missing")
                .with_details(details),
            ));
        }
        let named: Vec<String> = missing
            .iter()
            .map(|(found, counted)| match &found.requirement {
                Ok(requirement) => format!(
                    "{} ({counted} of {})",
                    requirement.name, requirement.count
                ),
                Err(reason) => format!("{} (broken: {reason})", found.name),
            })
            .collect();
        details.insert(
            "missing".into(),
            Value::Array(
                missing
                    .iter()
                    .map(|(found, counted)| missing_of(found, *counted))
                    .collect(),
            ),
        );
        Ok(Some(
            Veto::new(format!(
                "{} cannot move to {to}: it still needs {}",
                row(view),
                named.join(", ")
            ))
            .with_code("approvals_missing")
            .with_details(details),
        ))
    }

    fn operation(&self, cx: &Cx<'_, ApprovalsConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            SIGN_OFF => sign_off(cx, params),
            WITHDRAW_SIGN_OFF => withdraw_sign_off(cx, params),
            LIST_SIGN_OFFS => list_sign_offs(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // after_change consumes what no longer counts, from the row as the
    // change left it: the sign-offs of a gate the config no longer has, of
    // each gate an update's change of its fields resets (D104), and of the
    // gate of the state the row is in. A delete takes the row's sub-table
    // rows with it.
    fn after_change(&self, cx: &Cx<'_, ApprovalsConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Delete) {
            return Ok(());
        }
        let rows = cx.sub_rows(SIGN_OFFS)?;
        let live: Vec<&SubRow> = rows.iter().filter(|row| is_live(row)).collect();
        if live.is_empty() {
            return Ok(());
        }
        let config = cx.config();
        let now = cx.own();
        let status = status_of(cx);
        for row in live {
            let state = row
                .value("state")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let consumed = match config.gate(state) {
                // A gate a later build removed.
                None => true,
                Some(gate) => {
                    // A change of the own fields, an update's or an
                    // operation's update() (Revisions' approve) alike; one
                    // that leaves them as they were changes nothing, as the
                    // engine answered it with no write.
                    let before = match change {
                        Change::Update { before, .. } => Some(*before),
                        Change::Operation {
                            before: Some(before),
                            ..
                        } => Some(*before),
                        _ => None,
                    };
                    let reset = before.is_some_and(|before| match &gate.reset_on {
                        Some(fields) => fields
                            .iter()
                            .any(|field| !equal(before.get(field), now.get(field))),
                        None => config.reset_on_change && *before != now,
                    });
                    // Entering the gate's state used its sign-offs.
                    reset || status.as_deref() == Some(state)
                }
            };
            if consumed {
                let mut values = Map::new();
                values.insert("consumedAt".into(), Value::from(cx.now()));
                cx.update_row(SIGN_OFFS, &row.key, values)?;
            }
        }
        Ok(())
    }

    // A read of the row shows the sign-offs that count, as the engine's
    // Approvals.gates field.
    fn read_fields(
        &self,
        config: &ApprovalsConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let rows = tables.get(SIGN_OFFS).map_or(&[][..], Vec::as_slice);
        let mut gates = Map::new();
        for row in rows.iter().filter(|row| is_live(row)) {
            let (Some(state), Some(subject)) = (
                row.value("state").and_then(Value::as_str),
                row.value("subject").and_then(Value::as_str),
            ) else {
                continue;
            };
            if config.gate(state).is_none() {
                continue;
            }
            let entry = gates
                .entry(state.to_owned())
                .or_insert_with(|| json!({ "signedBy": [] }));
            if let Some(Value::Array(subjects)) = entry.get_mut("signedBy") {
                subjects.push(Value::from(subject));
            }
        }
        let mut fields = Map::new();
        fields.insert("gates".into(), Value::Object(gates));
        fields
    }
}

// ---- requirements ----

// ReadRequirement is one requirement as a gate read it: its row's key and
// name, and the requirement, or why its row is broken.
struct ReadRequirement {
    key: Option<String>,
    name: String,
    requirement: Result<Requirement, String>,
}

// requirements_of reads a gate's requirements: the config's one, or its
// type's rows, every linked page of each key, the archived left out; the
// reason the gate cannot read them when it cannot (D200).
fn requirements_of(
    view: &View<'_, ApprovalsConfig>,
    gate: &ApprovalsGate,
) -> Hook<Result<Vec<ReadRequirement>, String>> {
    let from = match &gate.requirements {
        Requirements::Config(requirement) => {
            return Ok(Ok(vec![ReadRequirement {
                key: None,
                name: requirement.name.clone(),
                requirement: Ok(requirement.clone()),
            }]))
        }
        Requirements::Rows(from) => from,
    };
    let type_name = view.type_name();
    let Some(linking) = view.read_spec(&from.schema)? else {
        return Ok(Err(format!(
            "its requirements' type {} does not exist",
            from.schema
        )));
    };
    let Some(field) = linking.relation(&from.link) else {
        return Ok(Err(format!(
            "its requirements' type {} has no to-one relation {}",
            from.schema, from.link
        )));
    };
    let field = field.clone();
    let keys: Vec<String> = match &from.within {
        None => {
            if field
                .relation
                .as_ref()
                .is_none_or(|relation| relation.type_name != type_name)
            {
                return Ok(Err(format!(
                    "{}.{} does not point at {type_name}",
                    from.schema, from.link
                )));
            }
            vec![view.key().unwrap_or_default().to_owned()]
        }
        Some(list) => match view.own_field(list) {
            None => {
                return Ok(Err(format!(
                    "its {list} holds no keys yet, so its requirements are not known"
                )))
            }
            Some(Value::Array(items)) => {
                let mut keys: Vec<String> = Vec::new();
                for item in &items {
                    let Some(key) = key_of(item) else {
                        return Ok(Err(format!("its {list} holds {item}, which is no key")));
                    };
                    if !keys.contains(&key) {
                        keys.push(key);
                    }
                }
                keys
            }
            Some(other) => {
                return Ok(Err(format!("its {list} holds {other}, which is no list of keys")))
            }
        },
    };
    let mut rows: Vec<KeyedRow> = Vec::new();
    let mut seen: BTreeSet<String> = BTreeSet::new();
    for key in keys {
        let mut after: Option<String> = None;
        loop {
            let page = view.read_linked(&from.schema, &field.name, &key, after.as_deref())?;
            for found in &page.rows {
                if seen.insert(found.key.clone()) {
                    rows.push(found.clone());
                }
            }
            if rows.len() > MAX_REQUIREMENT_READ {
                return Ok(Err(format!(
                    "it reads more than {MAX_REQUIREMENT_READ} requirements of {}",
                    from.schema
                )));
            }
            match (page.more, page.rows.last()) {
                (true, Some(last)) => after = Some(last.key.clone()),
                _ => break,
            }
        }
    }
    let config = view.config();
    let own = config.own(type_name);
    let has_creator = config.creator_type.is_some();
    Ok(Ok(rows
        .iter()
        .filter(|found| {
            found
                .row
                .own
                .get(ARCHIVED_AT)
                .is_none_or(Value::is_null)
        })
        .map(|found| {
            let name = match found.row.own.get(ROW_NAME) {
                Some(Value::String(name)) if !name.is_empty() => name.clone(),
                _ => found.key.clone(),
            };
            let requirement =
                requirement_of(&name, &found.row.own, &own, has_creator, Source::Row).map_err(
                    |why| match why {
                        Broken::Reason(reason) => reason,
                        Broken::Creator => "it excludes the creator, and there is no createdBy".to_owned(),
                    },
                );
            ReadRequirement {
                key: Some(found.key.clone()),
                name,
                requirement,
            }
        })
        .collect()))
}

// missing_of is a requirement a veto names as missing.
fn missing_of(found: &ReadRequirement, counted: i64) -> Value {
    let mut out = Map::new();
    if let Some(key) = &found.key {
        out.insert("requirement".into(), Value::from(key.as_str()));
    }
    out.insert("name".into(), Value::from(found.name.as_str()));
    match &found.requirement {
        Ok(requirement) => {
            out.insert("required".into(), Value::from(requirement.count));
            out.insert("count".into(), Value::from(counted));
            out.insert(
                "permission".into(),
                Value::from(requirement.permission.as_str()),
            );
            if let Some(callers) = &requirement.callers {
                out.insert(
                    "callers".into(),
                    Value::Array(callers.iter().map(|class| Value::from(class.name())).collect()),
                );
            }
        }
        Err(reason) => {
            out.insert("broken".into(), Value::from(reason.as_str()));
        }
    }
    Value::Object(out)
}

// applies says whether each of a requirement's conditions holds on the
// row's own fields.
fn applies(requirement: &Requirement, own: &Map<String, Value>) -> bool {
    requirement
        .when
        .iter()
        .all(|condition| condition_holds(condition, own))
}

// condition_holds tests one condition. A field with no value holds, negated
// or not, so a fact not yet written never exempts.
fn condition_holds(condition: &Condition, own: &Map<String, Value>) -> bool {
    let Some(value) = own.get(&condition.field).filter(|value| !value.is_null()) else {
        return true;
    };
    let texts: Vec<String> = match value {
        Value::Array(items) => items.iter().filter_map(text_of).collect(),
        other => text_of(other).into_iter().collect(),
    };
    let tested = match condition.test {
        ConditionTest::EveryMatches => texts
            .iter()
            .all(|text| condition.values.iter().any(|pattern| glob(pattern, text))),
        ConditionTest::AnyMatches => texts
            .iter()
            .any(|text| condition.values.iter().any(|pattern| glob(pattern, text))),
        ConditionTest::Contains => texts.iter().any(|text| condition.values.contains(text)),
        ConditionTest::AtMost => {
            let (Some(number), Some(most)) = (
                value.as_f64().or_else(|| value.as_str().and_then(number_of_text)),
                number_of_text(&condition.values[0]),
            ) else {
                return true;
            };
            number <= most
        }
    };
    tested != condition.negate
}

/// Whether a text matches a glob pattern (D200): `*` matches any run of
/// characters within a path segment, `**` any run across segments, and
/// `**/` no directory too; `?` matches one character other than `/`; any
/// other character itself.
pub fn glob(pattern: &str, text: &str) -> bool {
    let pattern: Vec<char> = pattern.chars().collect();
    let text: Vec<char> = text.chars().collect();
    glob_from(&pattern, &text)
}

fn glob_from(pattern: &[char], text: &[char]) -> bool {
    match pattern.first() {
        None => text.is_empty(),
        Some('*') if pattern.get(1) == Some(&'*') => {
            let rest = &pattern[2..];
            if rest.first() == Some(&'/') && glob_from(&rest[1..], text) {
                return true;
            }
            (0..=text.len()).any(|at| glob_from(rest, &text[at..]))
        }
        Some('*') => {
            let rest = &pattern[1..];
            for at in 0..=text.len() {
                if glob_from(rest, &text[at..]) {
                    return true;
                }
                if text.get(at) == Some(&'/') {
                    break;
                }
            }
            false
        }
        Some('?') => {
            text.first().is_some_and(|c| *c != '/') && glob_from(&pattern[1..], &text[1..])
        }
        Some(c) => text.first() == Some(c) && glob_from(&pattern[1..], &text[1..]),
    }
}

// ---- signers ----

// Signer is a caller as a sign-off records it: its subject, kind, whom it
// acted for, its run's Agent, its assurance, the permissions it held and
// the values of the reset fields when it signed.
struct Signer {
    subject: String,
    kind: Option<String>,
    acting_for: Option<String>,
    acting_for_kind: Option<String>,
    agent: Option<String>,
    signed_in_at: Option<i64>,
    user_verified: bool,
    held: Vec<String>,
    values: Option<Map<String, Value>>,
    at: i64,
}

impl Signer {
    fn of_caller(caller: &Caller, now: i64) -> Signer {
        let assurance = caller.assurance.clone().unwrap_or_default();
        Signer {
            subject: caller.subject.clone(),
            kind: caller.kind.clone(),
            acting_for: caller.acting_for.clone(),
            acting_for_kind: caller.acting_for_kind.clone(),
            agent: caller.agent.clone(),
            signed_in_at: assurance.signed_in_at,
            user_verified: assurance.user_verified,
            held: Vec::new(),
            values: None,
            at: now,
        }
    }

    fn of_row(row: &SubRow) -> Option<Signer> {
        let text = |field: &str| row.value(field).and_then(Value::as_str).map(str::to_owned);
        Some(Signer {
            subject: text("subject")?,
            kind: text("kind"),
            acting_for: text("actingFor"),
            acting_for_kind: text("actingForKind"),
            agent: text("agent"),
            signed_in_at: row.value("signedInAt").and_then(whole),
            user_verified: row.value("userVerified") == Some(&Value::Bool(true)),
            held: json_of(row.value("held"))
                .as_ref()
                .and_then(Value::as_array)
                .map(|items| {
                    items
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            values: json_of(row.value("values")).and_then(|values| match values {
                Value::Object(values) => Some(values),
                _ => None,
            }),
            at: row.value("at").and_then(whole).unwrap_or_default(),
        })
    }

    fn is_agent_run(&self) -> bool {
        self.agent.is_some() || self.kind.as_deref() == Some("agent")
    }

    fn acts_for_person(&self) -> bool {
        self.acting_for.is_some() && matches!(self.acting_for_kind.as_deref(), None | Some("user"))
    }

    fn is_person(&self) -> bool {
        matches!(self.kind.as_deref(), None | Some("user"))
            && self.agent.is_none()
            && self.acting_for.is_none()
    }

    // identities is the caller's subject, the person it acts for and its
    // run's Agent, each canonical: what an exclusion matches.
    fn identities(&self) -> Vec<String> {
        [Some(&self.subject), self.acting_for.as_ref(), self.agent.as_ref()]
            .into_iter()
            .flatten()
            .map(|subject| canonical_subject(subject))
            .collect()
    }

    // counted_as is whom the sign-off counts as toward a requirement: its
    // subject when the requirement lists no callers; else the person an
    // agent run acts for, the run's Agent, or the person, in that order of
    // the classes it lists; None when it lists none the caller is.
    fn counted_as(&self, requirement: &Requirement) -> Option<String> {
        let Some(classes) = &requirement.callers else {
            return Some(canonical_subject(&self.subject));
        };
        if classes.contains(&CallerClass::ActingForPerson) && self.acts_for_person() {
            return self.acting_for.as_deref().map(canonical_subject);
        }
        if classes.contains(&CallerClass::Agent) && self.is_agent_run() {
            return Some(canonical_subject(self.agent.as_deref().unwrap_or(&self.subject)));
        }
        if classes.contains(&CallerClass::Person) && self.is_person() {
            return Some(canonical_subject(&self.subject));
        }
        None
    }

    // proves says whether the sign-off shows the requirement's proof.
    fn proves(&self, requirement: &Requirement) -> bool {
        match requirement.proof {
            Proof::None => true,
            Proof::UserVerification => self.user_verified,
            Proof::RecentSignIn => {
                self.user_verified
                    || self.signed_in_at.is_some_and(|signed_in| {
                        requirement
                            .proof_within_ms
                            .is_some_and(|within| self.at.saturating_sub(signed_in) <= within)
                    })
            }
        }
    }
}

// Shortfall is why a sign-off does not count toward a requirement.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Shortfall {
    Caller,
    Creator,
    CreatorPerson,
    Listed,
    Proof,
}

impl Shortfall {
    fn reason(self) -> &'static str {
        match self {
            Shortfall::Caller => "it does not count this kind of caller",
            Shortfall::Creator => "it excludes the creator",
            Shortfall::CreatorPerson => "it excludes the person the creator acted for",
            Shortfall::Listed => "a field it excludes by lists the caller",
            Shortfall::Proof => "the caller's proof falls short",
        }
    }
}

// shortfall says why a signer does not count toward a requirement on the
// row as it is, its permission and reset aside; None when it counts, as
// whom it counts.
fn shortfall(
    view: &View<'_, ApprovalsConfig>,
    requirement: &Requirement,
    signer: &Signer,
    own: &Map<String, Value>,
) -> Result<String, Shortfall> {
    let Some(counted) = signer.counted_as(requirement) else {
        return Err(Shortfall::Caller);
    };
    if requirement.exclude_creator {
        let type_name = view.config().creator_type.as_deref().unwrap_or("string");
        let creator = own.get(CREATED_BY);
        if names_subject(type_name, creator, &signer.subject)
            || names_subject(type_name, creator, &counted)
            || creator
                .and_then(Value::as_str)
                .is_some_and(|creator| canonical_subject(creator) == counted)
        {
            return Err(Shortfall::Creator);
        }
    }
    let identities = signer.identities();
    if requirement.exclude_creator_person
        && listed(own.get(CREATED_FOR)).iter().any(|held| identities.contains(held))
    {
        return Err(Shortfall::CreatorPerson);
    }
    if requirement
        .exclude_subjects_in
        .iter()
        .any(|field| listed(own.get(field)).iter().any(|held| identities.contains(held)))
    {
        return Err(Shortfall::Listed);
    }
    if !signer.proves(requirement) {
        return Err(Shortfall::Proof);
    }
    Ok(counted)
}

// listed is the canonical subjects a field's value names: a list's
// strings, or the one string.
fn listed(value: Option<&Value>) -> Vec<String> {
    match value {
        Some(Value::Array(items)) => items
            .iter()
            .filter_map(Value::as_str)
            .map(canonical_subject)
            .collect(),
        Some(Value::String(subject)) if !subject.is_empty() => vec![canonical_subject(subject)],
        _ => Vec::new(),
    }
}

// rechecked reads, once, the permissions every signer a requirement that
// reads them again counts as holds now (D200); None when none does.
fn rechecked<'r>(
    view: &View<'r, ApprovalsConfig>,
    applying: &[&ReadRequirement],
    signers: &[Signer],
) -> Hook<Option<&'r BTreeMap<String, Vec<String>>>> {
    let mut subjects: BTreeSet<String> = BTreeSet::new();
    for found in applying {
        let Ok(requirement) = &found.requirement else {
            continue;
        };
        if !requirement.recheck_permission {
            continue;
        }
        subjects.extend(signers.iter().filter_map(|signer| signer.counted_as(requirement)));
    }
    if subjects.is_empty() {
        return Ok(None);
    }
    let subjects: Vec<String> = subjects.into_iter().collect();
    Ok(Some(view.permissions_of(&subjects)?))
}

// met_by counts the distinct signers whose sign-off counts toward a
// requirement on the row as it is.
fn met_by(
    view: &View<'_, ApprovalsConfig>,
    requirement: &Requirement,
    rows: bool,
    signers: &[Signer],
    own: &Map<String, Value>,
    held_now: Option<&BTreeMap<String, Vec<String>>>,
) -> i64 {
    let wanted = [requirement.permission.clone()];
    let mut counted: BTreeSet<String> = BTreeSet::new();
    for signer in signers {
        let Ok(whom) = shortfall(view, requirement, signer, own) else {
            continue;
        };
        let permitted = if requirement.recheck_permission {
            held_now
                .and_then(|held| held.get(&whom))
                .is_some_and(|held| has_any_permission(held, &wanted))
        } else {
            // A row's requirement counts what the signer held then; the
            // config's, whose permission signOff checked, does not read it
            // again, as before D200.
            !rows || has_any_permission(&signer.held, &wanted)
        };
        if !permitted {
            continue;
        }
        let unchanged = requirement.reset_on.iter().all(|field| {
            signer
                .values
                .as_ref()
                .is_some_and(|values| values.contains_key(field) && equal(values.get(field), own.get(field)))
        });
        if unchanged {
            counted.insert(whom);
        }
    }
    counted.len() as i64
}

// ---- the operations ----

fn sign_off(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let state = state_param(params);
    let gate = gate_of(cx, state)?;
    let subject = cx.caller().subject.as_str();
    let rows = matches!(gate.requirements, Requirements::Rows(_));
    let read = match requirements_of(cx, gate)? {
        Ok(read) => read,
        Err(reason) => {
            return Err(refuse(
                cx,
                format!("{subject}'s sign-off on {} for {state} cannot count: {reason}", row(cx)),
                "not_counted",
            ))
        }
    };
    let valid: Vec<&Requirement> = read
        .iter()
        .filter_map(|found| found.requirement.as_ref().ok())
        .collect();
    let mut permitted: Vec<&Requirement> = Vec::new();
    for requirement in &valid {
        if cx.can(&requirement.permission)? {
            permitted.push(requirement);
        }
    }
    if permitted.is_empty() {
        let mut permissions: Vec<&str> = valid
            .iter()
            .map(|requirement| requirement.permission.as_str())
            .collect();
        permissions.dedup();
        return Err(match permissions.as_slice() {
            [] => refuse(
                cx,
                format!(
                    "{subject}'s sign-off on {} for {state} cannot count: the gate asks for no requirement it could count toward",
                    row(cx)
                ),
                "not_counted",
            ),
            [one] => cx.forbidden(format!(
                "{subject} may not sign off on {} for {state}: it needs permission {one}",
                row(cx)
            )),
            many => cx.forbidden(format!(
                "{subject} may not sign off on {} for {state}: it needs one of the permissions {}",
                row(cx),
                many.join(", ")
            )),
        });
    }
    if status_of(cx).as_deref() == Some(state) {
        return Err(refuse(
            cx,
            format!(
                "{} is {state} already; a sign-off for it waits until it has left",
                row(cx)
            ),
            "in_state",
        ));
    }
    let own = cx.own();
    let signer = Signer::of_caller(cx.caller(), cx.now());
    let mut counts_toward: Vec<&Requirement> = Vec::new();
    let mut reasons: Vec<(&Requirement, Shortfall)> = Vec::new();
    for requirement in &permitted {
        match shortfall(cx, requirement, &signer, &own) {
            Ok(_) => counts_toward.push(requirement),
            Err(why) => reasons.push((requirement, why)),
        }
    }
    if counts_toward.is_empty() {
        if reasons.iter().all(|(_, why)| *why == Shortfall::Creator) {
            return Err(refuse(
                cx,
                format!(
                    "{subject} created {}, and its gate on {state} takes no sign-off from its creator",
                    row(cx)
                ),
                "creator_cannot_sign",
            ));
        }
        let mut details = Map::new();
        details.insert(
            "requirements".into(),
            Value::Array(
                reasons
                    .iter()
                    .map(|(requirement, why)| {
                        json!({ "name": requirement.name, "reason": why.reason() })
                    })
                    .collect(),
            ),
        );
        let named: Vec<String> = reasons
            .iter()
            .map(|(requirement, why)| format!("{}: {}", requirement.name, why.reason()))
            .collect();
        return Err(cx.veto(
            Veto::new(format!(
                "{subject}'s sign-off on {} for {state} would count toward none of its gate's requirements ({})",
                row(cx),
                named.join("; ")
            ))
            .with_code("not_counted")
            .with_details(details),
        ));
    }
    let rows_of_sign_offs = cx.sub_rows(SIGN_OFFS)?;
    if live_of(&rows_of_sign_offs, state, subject).is_some() {
        return Err(refuse(
            cx,
            format!(
                "{subject} has signed off on {} for {state} already",
                row(cx)
            ),
            "already_signed",
        ));
    }
    let next = rows_of_sign_offs.iter().filter_map(number_of).max().unwrap_or(0) + 1;
    let note = params.get("note").and_then(Value::as_str);
    let caller = cx.caller();
    let mut values = Map::new();
    values.insert("state".into(), Value::from(state));
    values.insert("subject".into(), Value::from(subject));
    for (field, value) in [
        ("kind", &caller.kind),
        ("actingFor", &caller.acting_for),
        ("actingForKind", &caller.acting_for_kind),
        ("agent", &caller.agent),
    ] {
        if let Some(value) = value {
            values.insert(field.into(), Value::from(value.as_str()));
        }
    }
    if let Some(signed_in) = signer.signed_in_at {
        values.insert("signedInAt".into(), Value::from(signed_in));
    }
    if signer.user_verified {
        values.insert("userVerified".into(), Value::Bool(true));
    }
    if rows {
        // The permissions the caller held, which a row's requirement counts
        // later, whatever permission the row names by then, and the value
        // of each field one of the rows resets on (D200).
        let mut held: Vec<&str> = caller.permissions.iter().map(String::as_str).collect();
        held.sort_unstable();
        held.dedup();
        values.insert(
            "held".into(),
            Value::Array(held.into_iter().map(Value::from).collect()),
        );
        let fields: BTreeSet<&str> = valid
            .iter()
            .flat_map(|requirement| requirement.reset_on.iter().map(String::as_str))
            .collect();
        if !fields.is_empty() {
            values.insert(
                "values".into(),
                Value::Object(
                    fields
                        .into_iter()
                        .map(|field| {
                            (
                                field.to_owned(),
                                own.get(field).cloned().unwrap_or(Value::Null),
                            )
                        })
                        .collect(),
                ),
            );
        }
    }
    if let Some(note) = note {
        values.insert("note".into(), Value::from(note));
    }
    values.insert("at".into(), Value::from(cx.now()));
    cx.insert_row(SIGN_OFFS, &key_of_number(next), values)?;
    let mut out = Map::new();
    out.insert("state".into(), Value::from(state));
    out.insert("subject".into(), Value::from(subject));
    if let Some(note) = note {
        out.insert("note".into(), Value::from(note));
    }
    out.insert("at".into(), Value::from(cx.now()));
    out.insert(
        "signedBy".into(),
        Value::Array(
            signed_by(&cx.sub_rows(SIGN_OFFS)?, state)
                .into_iter()
                .map(Value::from)
                .collect(),
        ),
    );
    out.insert("count".into(), Value::from(counts_toward[0].count));
    if rows {
        out.insert(
            "countsToward".into(),
            Value::Array(
                counts_toward
                    .iter()
                    .map(|requirement| Value::from(requirement.name.as_str()))
                    .collect(),
            ),
        );
    }
    Ok(Value::Object(out))
}

fn withdraw_sign_off(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let state = state_param(params);
    gate_of(cx, state)?;
    let subject = cx.caller().subject.as_str();
    let rows = cx.sub_rows(SIGN_OFFS)?;
    let Some(found) = live_of(&rows, state, subject) else {
        return Err(refuse(
            cx,
            format!(
                "{subject} has no sign-off on {} for {state} to withdraw",
                row(cx)
            ),
            "not_signed",
        ));
    };
    cx.delete_row(SIGN_OFFS, &found.key)?;
    Ok(sign_off_of(found))
}

fn list_sign_offs(cx: &Cx<'_, ApprovalsConfig>, params: &Value) -> Hook<Value> {
    let request = page_request(params).map_err(|issue| -> Stop {
        Refusal::operation_params(cx.behavior(), cx.use_id(), LIST_SIGN_OFFS, vec![issue]).into()
    })?;
    let only = params.get("state").and_then(Value::as_str);
    let rows: Vec<SubRow> = cx
        .sub_rows(SIGN_OFFS)?
        .into_iter()
        .filter(|row| number_of(row).is_some_and(|number| number > request.after))
        .filter(|row| {
            only.is_none_or(|state| row.value("state").and_then(Value::as_str) == Some(state))
        })
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(rows, request.limit, |row| {
        number_of(row).unwrap_or_default()
    });
    Ok(Page {
        items: found.items.iter().map(sign_off_of).collect(),
        next: found.next,
    }
    .into_json())
}

// gate_of is the config's gate on a state an operation names, which must
// be one.
fn gate_of<'c>(view: &View<'c, ApprovalsConfig>, state: &str) -> Hook<&'c ApprovalsGate> {
    let config = view.config();
    config.gate(state).ok_or_else(|| {
        refuse(
            view,
            format!(
                "{state} is not a state {}'s Approvals gates (it gates {})",
                view.type_name(),
                config.gated()
            ),
            "no_gate",
        )
    })
}

// refuse is a veto of one of the behavior's operations, with no details.
fn refuse<C>(view: &View<'_, C>, reason: String, code: &str) -> Stop {
    view.veto(Veto::new(reason).with_code(code))
}

// sign_off_of reads a sign-off row as withdrawSignOff and listSignOffs
// return it: a member with no value left out.
fn sign_off_of(row: &SubRow) -> Value {
    let mut out = Map::new();
    for field in [
        "state",
        "subject",
        "actingFor",
        "agent",
        "note",
        "at",
        "consumedAt",
    ] {
        if let Some(value) = row.value(field) {
            out.insert(field.into(), value.clone());
        }
    }
    Value::Object(out)
}

// is_live says whether a sign-off counts: it has not been consumed.
fn is_live(row: &SubRow) -> bool {
    row.value("consumedAt").is_none()
}

// signed_by is the subjects whose sign-off for a state counts, in the
// order they signed.
fn signed_by<'r>(rows: &'r [SubRow], state: &str) -> Vec<&'r str> {
    rows.iter()
        .filter(|row| is_live(row) && row.value("state").and_then(Value::as_str) == Some(state))
        .filter_map(|row| row.value("subject").and_then(Value::as_str))
        .collect()
}

// live_of is a subject's sign-off for a state that counts.
fn live_of<'r>(rows: &'r [SubRow], state: &str, subject: &str) -> Option<&'r SubRow> {
    rows.iter().find(|row| {
        is_live(row)
            && row.value("state").and_then(Value::as_str) == Some(state)
            && row.value("subject").and_then(Value::as_str) == Some(subject)
    })
}

// number_of is a sign-off's number, from its key.
fn number_of(row: &SubRow) -> Option<i64> {
    row.key.parse().ok()
}

// key_of_number is a sign-off's key: its number in ten digits,
// zero-padded.
fn key_of_number(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
}

// status_of is the status of the Workflow the gates are of, as the run has
// it; `None` when the row has none.
fn status_of(view: &View<'_, ApprovalsConfig>) -> Option<String> {
    view.get_of(&view.config().status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn state_param(params: &Value) -> &str {
    params
        .get("state")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

// whole reads a whole number a schema let through: 2 or 2.0.
fn whole(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    number.as_i64().or_else(|| {
        number
            .as_f64()
            .filter(|n| n.is_finite() && n.fract() == 0.0)
            .map(|n| n as i64)
    })
}

// number_of_text reads a number written as text: "400".
fn number_of_text(text: &str) -> Option<f64> {
    text.trim().parse::<f64>().ok().filter(|n| n.is_finite())
}

// text_of is a value a condition tests as text: a string itself, a number
// or a boolean written out.
fn text_of(value: &Value) -> Option<String> {
    match value {
        Value::String(text) => Some(text.clone()),
        Value::Number(number) => Some(number.to_string()),
        Value::Bool(flag) => Some(flag.to_string()),
        _ => None,
    }
}

// text reads a string member a schema let through; empty when absent.
fn text(value: Option<&Value>) -> String {
    value
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

// json_of reads a JSON column's value, which a host may hand as its text.
fn json_of(value: Option<&Value>) -> Option<Value> {
    match value? {
        Value::String(text) => serde_json::from_str(text).ok(),
        other => Some(other.clone()),
    }
}

// strings reads a list of strings the configSchema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_sign_offs_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of_number(1), "0000000001");
        assert_eq!(key_of_number(1_234_567_890), "1234567890");
        assert_eq!(number_of(&SubRow::new("0000000042", Map::new())), Some(42));
        assert_eq!(number_of(&SubRow::new("x", Map::new())), None);
    }

    #[test]
    fn a_glob_matches_within_and_across_path_segments() {
        assert!(glob("docs/*.md", "docs/a.md"));
        assert!(!glob("docs/*.md", "docs/sub/a.md"));
        assert!(glob("docs/**", "docs/sub/a.md"));
        assert!(glob("**/*_test.go", "a_test.go"));
        assert!(glob("**/*_test.go", "pkg/x/a_test.go"));
        assert!(!glob("**/*_test.go", "pkg/x/a.go"));
        assert!(glob("scripts/ci/?.sh", "scripts/ci/a.sh"));
        assert!(!glob("scripts/ci/?.sh", "scripts/ci/ab.sh"));
        assert!(glob("Makefile", "Makefile"));
        assert!(!glob("Makefile", "makefile"));
    }
}
