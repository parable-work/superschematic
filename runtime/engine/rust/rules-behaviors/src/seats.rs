//! Seats, named seats on a row (D104), the first behavior only the core
//! implements: no engine registers it, and its declaration is the file
//! `make behaviors` writes for the package `@superschematic/rules`
//! (`rules-behaviors/declarations`), read in place. A table implements it
//! (`implements Seats<Problem, { seats: { ... } }>`), and its config names
//! the row's seats; a seat holds the subjects seated in it.
//!
//! Seats is held to D103's test of a behavior and passes it. Its state is
//! its own: the `occupants` sub-table, a row per subject in a seat, who
//! sits there, since when, and seated by whom. Its rules say who may take a
//! seat, who may seat or unseat another, how many a seat holds and what a
//! take of a full one does, that a field's subject sits there and cannot
//! leave, and which moves and edits a seat lets through (Workflow's and
//! StateRules' `by`). It recurs: an incident's commander and responders, a
//! document's owner and editors, a ticket's assignees, a review's
//! reviewers.
//!
//! The rules:
//!
//! - `takeSeat({ seat, subject? })` seats a subject, the caller's own when
//!   `subject` is absent. A caller seats itself with the seat's `take`
//!   permission when it names one (any caller may when it names none), and
//!   seats another with its `assign` permission (none may when it names
//!   none); a caller lacking it is `forbidden`. A seat with `max` holds at
//!   most that many: a take of a full one is the veto `seat_full` (details
//!   `{ seat, max, seated }`) under `onFull: "refuse"`, the default, and
//!   under `onFull: "displace"` unseats the subjects seated longest (the
//!   least keys, which record the order they were seated in whatever the
//!   clocks of the commands that seated them read) to make room, which its
//!   result lists as `displaced`, longest seated first.
//! - `leaveSeat({ seat, subject? })` unseats a subject: the caller leaves a
//!   seat it holds with no permission, and unseats another with `assign`.
//! - `fromField` seats the subject an own field holds (a `string`,
//!   `Identity.UUID` or `Identity.UserID` field, [`crate::subjects`]). It is
//!   read from the field at each decision, never kept as a row, since the
//!   field is the truth and a row would go stale under a write around the
//!   core: the field's subject counts toward `max`, is never displaced
//!   (`seat_full` when every subject seated is the field's), and its leave
//!   is the veto `held_by_field` (details `{ seat, subject, field }`) while
//!   the field holds it. A new value of the field seats the new subject at
//!   once, and an update that gives it a subject not seated there takes the
//!   seat as a take would, past no permission: into a seat it would put
//!   over its `max` the update is `seat_full` under `refuse` (its reason
//!   naming the field and the subject), and under `displace` its
//!   after-change hook unseats the subjects seated longest down to `max`.
//! - A take of a seat the subject holds, and a leave of one it does not,
//!   answer `unchanged: true` and write nothing, before any guard and any
//!   permission ([`Behavior::settle`]), as Workflow's repeat does: no other
//!   use's gate refuses a call that changes nothing.
//! - `listSeats({ seat? })` reads each seat's subjects, the seats by code
//!   point, each one's fromField subject first and then its occupants in
//!   the order they were seated, by key.
//!
//! The permission checks come before the state's rules, as Workflow's `by`
//! comes before its permission: who may ask, then what the row allows. An
//! unknown seat and a caller with no subject who names none are bad
//! parameters (`invalid_argument`), refused in `settle` before any guard.
//!
//! A subject is kept and compared as its canonical text
//! ([`crate::subjects::canonical_subject`]): a hyphenated UUID in base62,
//! the adapters' wire form and the identity runtime's principal subject,
//! every other text as it is. An occupant's key is its number on the row,
//! one past the greatest among the rows a take reads, in ten digits
//! zero-padded, so the key's order is the order they were seated in, as
//! Approvals keys its sign-offs.
//!
//! A Workflow transition's and a StateRules state's `by` name a seat as
//! `seat:<name>` (D104): a caller seated there, through an occupant row or
//! the seat's fromField, passes it. Their guards read it through Seats' own
//! rule, [`holds_seat`], over the occupants as the run has them, as Budget
//! reads whether a lease is active through Lease's; their config checks
//! resolve the seat through [`seat_of`].
//!
//! Seats' operations are writes of the row, which Lease's guard holds for a
//! caller who does not hold an active lease unless its `exempt` lists them;
//! a seat's operation is no update, so Approvals consumes no sign-off for
//! one. A write around the core (raw SQL of the sub-table, or of a
//! fromField) is held to none of these rules, the escape hatch D103 keeps:
//! a raw write of a fromField may put a seat over its `max`, and the next
//! take of it is `seat_full`, or displaces down to `max - 1`, as after a
//! lowered `max`.
//!
//! The config is checked at define as every ported behavior's is: first as
//! the declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then each seat by code
//! point against the rules ([`ConfigError::Rule`]): an `onFull` needs a
//! `max`, and a `fromField` names an own field that holds a subject. The
//! operations' parameters are held to their closed paramsSchemas in the
//! behavior registry's words. The core runs no JSON Schema, so the schemas'
//! checks are written here, held to the declaration file by this crate's
//! tests.
//!
//! configChange: any change is allowed, and Seats joins and leaves a type
//! with rows: it keeps no owned field. A seat the config drops keeps its
//! occupants, which no rule reads, until the row's next `takeSeat` or
//! `leaveSeat` deletes them; a lowered `max` unseats no one, and the next
//! take of a seat over it is `seat_full`, or displaces down to `max - 1`.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    Refusal, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::subjects::{canonical_subject, holds_subject, names_subject};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Seats";

/// Seats' declaration, the file `make behaviors` writes for the core alone
/// (no engine implements it), read in place.
pub const DECLARATION: &str = include_str!("../declarations/Seats.behavior.json");

/// The sub-table of a row's occupants.
pub const OCCUPANTS: &str = "occupants";

/// The operation that seats a subject.
pub const TAKE_SEAT: &str = "takeSeat";

/// The operation that unseats a subject.
pub const LEAVE_SEAT: &str = "leaveSeat";

/// The operation that lists a row's seated subjects.
pub const LIST_SEATS: &str = "listSeats";

/// What a by entry that names a seat starts with (D104).
pub const SEAT_PREFIX: &str = "seat:";

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 1] = ["seats"];
const SEAT_KEYS: [&str; 5] = ["assign", "fromField", "max", "onFull", "take"];

// The values of onFull the configSchema's enum takes, in its order.
const ON_FULL: [&str; 2] = ["refuse", "displace"];

// The width of an occupant's number in its key.
const NUMBER_WIDTH: usize = 10;

/// What a take of a full seat does.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum SeatsOnFull {
    /// The veto seat_full, the default.
    #[default]
    Refuse,
    /// The longest seated occupant the seat's fromField does not name leaves.
    Displace,
}

/// One seat of a Seats config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SeatsSeat {
    pub name: String,
    /// How many subjects it holds at most, a fromField's among them; no cap
    /// when None.
    pub max: Option<i64>,
    pub on_full: SeatsOnFull,
    /// The permission a caller needs to seat itself; any caller may when
    /// None.
    pub take: Option<String>,
    /// The permission a caller needs to seat or unseat another; none may
    /// when None.
    pub assign: Option<String>,
    /// The own field whose subject the seat holds, with its IR type.
    pub from_field: Option<(String, String)>,
}

/// Seats' config: the seats by code point, as the IR's canonical config
/// holds them, so every message and listSeats lists them so.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SeatsConfig {
    pub seats: Vec<SeatsSeat>,
}

impl SeatsConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each seat, by code point, against the rules, a rule's
    /// reason which the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<SeatsConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let empty = Map::new();
        let named = raw
            .get("seats")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let mut names: Vec<&String> = named.keys().collect();
        names.sort();
        let type_name = target.type_name();
        let fields = target.fields();
        let mut seats = Vec::new();
        for name in names {
            let seat = &named[name.as_str()];
            let text = |key: &str| seat.get(key).and_then(Value::as_str).map(str::to_owned);
            let max = seat.get("max").and_then(whole);
            if seat.get("onFull").is_some() && max.is_none() {
                return Err(format!(
                    "seats.{name}.onFull names what a full seat does, and {name} has no max to fill"
                )
                .into());
            }
            let from_field = match text("fromField") {
                None => None,
                Some(field) => {
                    let Some(found) = fields.iter().find(|known| known.name == field) else {
                        let names: Vec<&str> =
                            fields.iter().map(|known| known.name.as_str()).collect();
                        let names = if names.is_empty() {
                            "none".to_owned()
                        } else {
                            names.join(", ")
                        };
                        return Err(format!(
                            "seats.{name}.fromField: {field} is not a field of {type_name} (its fields: {names})"
                        )
                        .into());
                    };
                    if !holds_subject(&found.type_name) {
                        return Err(format!(
                            "seats.{name}.fromField names {field}, a {} field of {type_name}: fromField names a field that holds a subject (string, Identity.UUID or Identity.UserID)",
                            found.type_name
                        )
                        .into());
                    }
                    Some((field, found.type_name.clone()))
                }
            };
            seats.push(SeatsSeat {
                name: name.clone(),
                max,
                on_full: match seat.get("onFull").and_then(Value::as_str) {
                    Some("displace") => SeatsOnFull::Displace,
                    _ => SeatsOnFull::Refuse,
                },
                take: text("take"),
                assign: text("assign"),
                from_field,
            });
        }
        Ok(SeatsConfig { seats })
    }

    /// A seat by its name; `None` when the config has none of the name.
    pub fn seat(&self, name: &str) -> Option<&SeatsSeat> {
        self.seats.iter().find(|seat| seat.name == name)
    }

    /// The seats' names, by code point, joined with ", ": what a refusal
    /// lists.
    pub fn listed(&self) -> String {
        self.seats
            .iter()
            .map(|seat| seat.name.as_str())
            .collect::<Vec<_>>()
            .join(", ")
    }
}

/// A seat a `by` entry names (D104): what Workflow's and StateRules'
/// guards read whether a caller is seated in, through Seats' own rule.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SeatRef {
    /// The type's Seats use, by its id (`Seats`, an implements use's).
    pub use_id: String,
    pub seat: String,
    /// The seat's fromField and its IR type, when it gives one.
    pub from_field: Option<(String, String)>,
}

/// Why a by entry's seat is none of the type's.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum SeatMissing {
    /// The type implements no Seats.
    NoSeats,
    /// Its Seats has no seat of the name; its seats, by code point.
    NoSeat(Vec<String>),
}

/// The seat name a by entry gives after `seat:`; None for a field entry.
pub(crate) fn seat_name(entry: &str) -> Option<&str> {
    entry.strip_prefix(SEAT_PREFIX)
}

/// The seat a `seat:<name>` entry names, read from the type's Seats use's
/// config as the type holds it (its seat's fromField and the field's IR type
/// from `target.fields()`). A Seats config of a shape Seats refuses reads as
/// no seats: Seats refuses it, and the core reports shapes before rules.
pub(crate) fn seat_of(target: &ConfigTarget<'_>, name: &str) -> Result<SeatRef, SeatMissing> {
    let Some(entry) = target.uses().iter().find(|entry| entry.behavior == NAME) else {
        return Err(SeatMissing::NoSeats);
    };
    let raw = entry.config_or_empty();
    let seats: Vec<String> = match raw.get("seats").and_then(Value::as_object) {
        Some(named) if config_issues(&raw).is_empty() => {
            let mut names: Vec<String> = named.keys().cloned().collect();
            names.sort();
            names
        }
        _ => Vec::new(),
    };
    if !seats.iter().any(|seat| seat == name) {
        return Err(SeatMissing::NoSeat(seats));
    }
    let from_field = raw["seats"][name]
        .get("fromField")
        .and_then(Value::as_str)
        .and_then(|field| {
            let found = target.fields().iter().find(|known| known.name == field)?;
            Some((field.to_owned(), found.type_name.clone()))
        });
    Ok(SeatRef {
        use_id: entry.id().to_owned(),
        seat: name.to_owned(),
        from_field,
    })
}

/// A refusal's reason for a by entry whose seat is missing, after `named`,
/// what the caller's config names the entry by:
/// NoSeats: `{named}, a seat of Seats, which {Type} does not implement`;
/// NoSeat: `{named}, which is not a seat of {Type}'s Seats (its seats: a, b)`.
pub(crate) fn missing_reason(named: &str, type_name: &str, missing: &SeatMissing) -> String {
    match missing {
        SeatMissing::NoSeats => {
            format!("{named}, a seat of Seats, which {type_name} does not implement")
        }
        SeatMissing::NoSeat(seats) => {
            let seats = if seats.is_empty() {
                "none".to_owned()
            } else {
                seats.join(", ")
            };
            format!("{named}, which is not a seat of {type_name}'s Seats (its seats: {seats})")
        }
    }
}

/// Whether a subject is seated in a seat of the row's Seats use (D104):
/// an occupant of the seat whose subject is the subject's canonical text,
/// or the subject the seat's fromField holds (`names_subject` on the field's
/// type). Reads the use's occupants as the run has them
/// (`View::sub_rows_of` of the view's own row), none for a row with no key;
/// an empty subject is seated nowhere. The fromField is read first, and
/// the occupants only when it does not hold the subject.
pub(crate) fn holds_seat<C>(view: &View<'_, C>, seat: &SeatRef, subject: &str) -> Hook<bool> {
    if subject.is_empty() {
        return Ok(false);
    }
    let canonical = canonical_subject(subject);
    if let Some(field) = &seat.from_field {
        if field_holds(field, view.own_field(&field.0).as_ref(), &canonical) {
            return Ok(true);
        }
    }
    let Some(key) = view.key() else {
        return Ok(false);
    };
    let rows = view.sub_rows_of(view.type_name(), key, &seat.use_id, OCCUPANTS)?;
    Ok(rows.iter().any(|row| occupies(row, &seat.seat, &canonical)))
}

/// Who a by names, as a refusal says it: its field entries as
/// `the subject {a, b or c} holds`, its seat entries as
/// `a subject seated in {x or y}`, the two joined with ` or `, fields first,
/// each in by's order. A by of fields alone reads as before.
pub(crate) fn by_named(by: &[String]) -> String {
    let fields: Vec<String> = by
        .iter()
        .filter(|entry| seat_name(entry).is_none())
        .cloned()
        .collect();
    let seats: Vec<String> = by
        .iter()
        .filter_map(|entry| seat_name(entry))
        .map(str::to_owned)
        .collect();
    let mut named = Vec::new();
    if !fields.is_empty() {
        named.push(format!("the subject {} holds", either(&fields)));
    }
    if !seats.is_empty() {
        named.push(format!("a subject seated in {}", either(&seats)));
    }
    named.join(" or ")
}

// either lists names as a refusal says them: `a`, `a or b`, `a, b or c`.
fn either(names: &[String]) -> String {
    match names {
        [] => String::new(),
        [one] => one.clone(),
        [rest @ .., last] => format!("{} or {last}", rest.join(", ")),
    }
}

// field_holds says whether a fromField's value holds a subject, given in
// its canonical text: the value names it on the field's type, or is the
// same subject kept canonically (a hyphenated UUID a string field holds).
fn field_holds(field: &(String, String), value: Option<&Value>, subject: &str) -> bool {
    names_subject(&field.1, value, subject)
        || matches!(value, Some(Value::String(held)) if !held.is_empty() && canonical_subject(held) == subject)
}

// occupies says whether an occupant row seats a subject, given in its
// canonical text, in a seat.
fn occupies(row: &SubRow, seat: &str, subject: &str) -> bool {
    row.value("seat").and_then(Value::as_str) == Some(seat)
        && row
            .value("subject")
            .and_then(Value::as_str)
            .is_some_and(|held| canonical_subject(held) == subject)
}

/// Seats: named seats on a row, each holding the subjects seated in it.
#[derive(Clone, Copy, Debug, Default)]
pub struct Seats;

impl Behavior for Seats {
    type Config = SeatsConfig;

    // The declaration file names its occupants sub-table, its three
    // operations and its two vetoes.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<SeatsConfig, ConfigError> {
        SeatsConfig::parse(raw, target)
    }

    // Any change, an added and a removed use included: Seats keeps no owned
    // field, a dropped seat's occupants are inert until the row's next take
    // or leave, and a lowered max unseats no one.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&SeatsConfig>,
        _after: Option<&SeatsConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &SeatsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // settle answers its own operations before any use's guard is asked
    // (D104): a seat the config lacks and a caller with no subject who
    // names none are bad parameters, and a take of a seat the subject holds
    // or a leave of one it does not answers unchanged, so no other use's
    // gate refuses a call that changes nothing.
    fn settle(
        &self,
        view: &View<'_, SeatsConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        if ![TAKE_SEAT, LEAVE_SEAT, LIST_SEATS].contains(&operation) {
            return Ok(None);
        }
        let config = view.config();
        let named = params.get("seat").and_then(Value::as_str);
        if let Some(name) = named.filter(|name| config.seat(name).is_none()) {
            return Err(bad_param(
                view,
                operation,
                "/seat",
                format!(
                    "\"{name}\" is not a seat of {}'s Seats ({})",
                    view.type_name(),
                    config.listed()
                ),
            ));
        }
        let Some(seat) = named.and_then(|name| config.seat(name)) else {
            return Ok(None);
        };
        if operation == LIST_SEATS {
            return Ok(None);
        }
        let subject = subject_of(view, params);
        if subject.is_empty() {
            let reason = if operation == TAKE_SEAT {
                "a caller with no subject takes no seat itself: name the subject to seat"
            } else {
                "a caller with no subject holds no seat: name the subject to unseat"
            };
            return Err(bad_param(view, operation, "/subject", reason.to_owned()));
        }
        let field_holds_it = seat
            .from_field
            .as_ref()
            .is_some_and(|field| field_holds(field, field_value(view, seat).as_ref(), &subject));
        let seated = field_holds_it
            || view
                .sub_rows(OCCUPANTS)?
                .iter()
                .any(|row| occupies(row, &seat.name, &subject));
        Ok(match (operation, seated) {
            (TAKE_SEAT, true) => Some(json!({
                "seat": seat.name, "subject": subject, "displaced": [], "unchanged": true
            })),
            (LEAVE_SEAT, false) => Some(json!({
                "seat": seat.name, "subject": subject, "unchanged": true
            })),
            _ => None,
        })
    }

    // guard holds its own takeSeat and leaveSeat, whoever calls them, to
    // the permissions the seat names, then to the seat's state: a full
    // seat, and the subject a fromField holds leaving; and an update that
    // gives a fromField a subject not seated to the seat's max.
    fn guard(
        &self,
        view: &View<'_, SeatsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        if let GuardRequest::Update { patch, after } = request {
            return update_guard(view, patch, after);
        }
        let (operation, params) = match (
            request.operation_of(view.use_id(), TAKE_SEAT),
            request.operation_of(view.use_id(), LEAVE_SEAT),
        ) {
            (Some(params), _) => (TAKE_SEAT, params),
            (_, Some(params)) => (LEAVE_SEAT, params),
            _ => return Ok(None),
        };
        let Some(seat) = view.config().seat(seat_param(params)) else {
            return Ok(None);
        };
        let subject = subject_of(view, params);
        let caller = view.caller().subject.as_str();
        let own = !caller.is_empty() && canonical_subject(caller) == subject;
        let who = if caller.is_empty() {
            "a caller with no subject"
        } else {
            caller
        };
        let target = format!("{}'s {}", row(view), seat.name);
        if operation == TAKE_SEAT {
            if own {
                if let Some(take) = &seat.take {
                    if !view.can(take)? {
                        return Err(view.forbidden(format!(
                            "{who} may not take a seat in {target}: it needs permission {take}"
                        )));
                    }
                }
            } else {
                match &seat.assign {
                    None => {
                        return Err(view.forbidden(format!(
                            "{who} may not seat {subject} in {target}: the seat names no assign permission, so a subject seats itself alone"
                        )))
                    }
                    Some(assign) if !view.can(assign)? => {
                        return Err(view.forbidden(format!(
                            "{who} may not seat {subject} in {target}: seating another needs permission {assign}"
                        )))
                    }
                    Some(_) => {}
                }
            }
            let Some(max) = seat.max else {
                return Ok(None);
            };
            let seated = seated(
                seat,
                field_value(view, seat).as_ref(),
                &view.sub_rows(OCCUPANTS)?,
            );
            let count = seated.len() as i64;
            let displaceable = seated.iter().filter(|seated| !seated.from_field).count() as i64;
            let full = count >= max
                && (seat.on_full == SeatsOnFull::Refuse || displaceable < count + 1 - max);
            if !full {
                return Ok(None);
            }
            return Ok(Some(seat_full(&target, seat, max, &seated, None)));
        }
        if !own {
            match &seat.assign {
                None => {
                    return Err(view.forbidden(format!(
                        "{who} may not unseat {subject} from {target}: the seat names no assign permission, so a subject leaves it alone"
                    )))
                }
                Some(assign) if !view.can(assign)? => {
                    return Err(view.forbidden(format!(
                        "{who} may not unseat {subject} from {target}: unseating another needs permission {assign}"
                    )))
                }
                Some(_) => {}
            }
        }
        let Some(field) = &seat.from_field else {
            return Ok(None);
        };
        if !field_holds(field, view.own_field(&field.0).as_ref(), &subject) {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert("seat".into(), Value::from(seat.name.as_str()));
        details.insert("subject".into(), Value::from(subject.as_str()));
        details.insert("field".into(), Value::from(field.0.as_str()));
        Ok(Some(
            Veto::new(format!(
                "{subject} holds {target} through its field {}, and leaves it only when {} names another subject",
                field.0, field.0
            ))
            .with_code("held_by_field")
            .with_details(details),
        ))
    }

    fn operation(&self, cx: &Cx<'_, SeatsConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            TAKE_SEAT => take_seat(cx, params),
            LEAVE_SEAT => leave_seat(cx, params),
            LIST_SEATS => list_seats(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // after_change makes room in a seat under onFull displace that an
    // update put over its max by giving its fromField a subject not seated
    // there, as a take of it would: the subjects seated longest that the
    // field does not hold leave, down to max. The guard refused such an
    // update of a seat under refuse.
    fn after_change(&self, cx: &Cx<'_, SeatsConfig>, change: &Change<'_>) -> Hook<()> {
        let Change::Update { patch, before } = change else {
            return Ok(());
        };
        let after = cx.own();
        for seat in &cx.config().seats {
            let (Some(max), SeatsOnFull::Displace) = (seat.max, seat.on_full) else {
                continue;
            };
            let Some(subject) = field_seats(seat, patch, &after) else {
                continue;
            };
            let rows = cx.sub_rows(OCCUPANTS)?;
            if is_seated(seat, field_of(seat, before).as_ref(), &rows, &subject) {
                continue;
            }
            let seated = seated(seat, field_of(seat, &after).as_ref(), &rows);
            displace(cx, seat, &seated, &rows, seated.len() as i64 - max)?;
        }
        Ok(())
    }
}

// update_guard holds an update that gives a seat's fromField a subject not
// seated there to the seat's max, as a take of the seat would be, past no
// permission: under onFull refuse a seat the new subject would put over
// its max is seat_full, its seated as they are before the update; under
// displace the after-change hook makes room.
fn update_guard(
    view: &View<'_, SeatsConfig>,
    patch: &Map<String, Value>,
    after: &Map<String, Value>,
) -> Hook<Option<Veto>> {
    for seat in &view.config().seats {
        let (Some(max), SeatsOnFull::Refuse) = (seat.max, seat.on_full) else {
            continue;
        };
        let Some(subject) = field_seats(seat, patch, after) else {
            continue;
        };
        let rows = view.sub_rows(OCCUPANTS)?;
        let before = field_value(view, seat);
        if is_seated(seat, before.as_ref(), &rows, &subject) {
            continue;
        }
        if seated(seat, field_of(seat, after).as_ref(), &rows).len() as i64 <= max {
            continue;
        }
        let field = seat
            .from_field
            .as_ref()
            .map(|(field, _)| field.as_str())
            .unwrap_or_default();
        return Ok(Some(seat_full(
            &format!("{}'s {}", row(view), seat.name),
            seat,
            max,
            &seated(seat, before.as_ref(), &rows),
            Some((field, &subject)),
        )));
    }
    Ok(None)
}

// seat_full is the veto of a take, or of an update that gives the seat's
// fromField a new subject (`by`, the field and the subject), of a full
// seat: its reason names the seat, its max and the subjects seated, and
// its details give them.
fn seat_full(
    target: &str,
    seat: &SeatsSeat,
    max: i64,
    seated: &[Seated],
    by: Option<(&str, &str)>,
) -> Veto {
    let names: Vec<&str> = seated
        .iter()
        .map(|seated| seated.subject.as_str())
        .collect();
    let mut details = Map::new();
    details.insert("seat".into(), Value::from(seat.name.as_str()));
    details.insert("max".into(), Value::from(max));
    details.insert(
        "seated".into(),
        Value::Array(names.iter().map(|name| Value::from(*name)).collect()),
    );
    let mut reason = format!(
        "{target} seats at most {max}, and it is full ({})",
        names.join(", ")
    );
    if let Some((field, subject)) = by {
        reason.push_str(&format!(": {field} cannot seat {subject} in it"));
    }
    Veto::new(reason)
        .with_code("seat_full")
        .with_details(details)
}

// take_seat seats the subject: past a full seat the guard let through
// (onFull displace), the longest seated occupants the fromField does not
// name leave first; the rows of seats the config dropped go too.
fn take_seat(cx: &Cx<'_, SeatsConfig>, params: &Value) -> Hook<Value> {
    let seat = seat_of_params(cx, params)?;
    let subject = subject_of(cx, params);
    let rows = cx.sub_rows(OCCUPANTS)?;
    let next = rows.iter().filter_map(number_of).max().unwrap_or(0) + 1;
    drop_dropped(cx, &rows)?;
    let displaced = match seat.max {
        Some(max) => {
            let seated = seated(seat, field_value(cx, seat).as_ref(), &rows);
            displace(cx, seat, &seated, &rows, seated.len() as i64 + 1 - max)?
        }
        None => Vec::new(),
    };
    let by = canonical_subject(&cx.caller().subject);
    let mut values = Map::new();
    values.insert("seat".into(), Value::from(seat.name.as_str()));
    values.insert("subject".into(), Value::from(subject.as_str()));
    values.insert("seatedAt".into(), Value::from(cx.now()));
    values.insert(
        "seatedBy".into(),
        if by.is_empty() {
            Value::Null
        } else {
            Value::from(by)
        },
    );
    cx.insert_row(OCCUPANTS, &key_of(next), values)?;
    Ok(json!({ "seat": seat.name, "subject": subject, "displaced": displaced }))
}

// leave_seat unseats the subject: every occupant row of the seat that
// seats it, and the rows of seats the config dropped.
fn leave_seat(cx: &Cx<'_, SeatsConfig>, params: &Value) -> Hook<Value> {
    let seat = seat_of_params(cx, params)?;
    let subject = subject_of(cx, params);
    let rows = cx.sub_rows(OCCUPANTS)?;
    drop_dropped(cx, &rows)?;
    for row in rows
        .iter()
        .filter(|row| occupies(row, &seat.name, &subject))
    {
        cx.delete_row(OCCUPANTS, &row.key)?;
    }
    Ok(json!({ "seat": seat.name, "subject": subject }))
}

// list_seats reads each seat's subjects, the seats by code point (the one
// seat named when given), as Seated orders them.
fn list_seats(cx: &Cx<'_, SeatsConfig>, params: &Value) -> Hook<Value> {
    let only = params.get("seat").and_then(Value::as_str);
    let rows = cx.sub_rows(OCCUPANTS)?;
    let mut items = Vec::new();
    for seat in &cx.config().seats {
        if only.is_some_and(|name| name != seat.name) {
            continue;
        }
        for seated in seated(seat, field_value(cx, seat).as_ref(), &rows) {
            items.push(json!({
                "seat": seat.name,
                "subject": seated.subject,
                "seatedAt": seated.at,
                "seatedBy": seated.by,
                "fromField": seated.from_field,
            }));
        }
    }
    Ok(json!({ "items": items }))
}

// displace unseats the first `over` of a seat's subjects, as Seated orders
// them, that its fromField does not hold, deleting every occupant row of
// the seat that seats each, and returns them, longest seated first; none
// when `over` is not positive.
fn displace(
    cx: &Cx<'_, SeatsConfig>,
    seat: &SeatsSeat,
    seated: &[Seated],
    rows: &[SubRow],
    over: i64,
) -> Hook<Vec<String>> {
    let over = usize::try_from(over).unwrap_or(0);
    let mut displaced = Vec::new();
    for leaving in seated.iter().filter(|seated| !seated.from_field).take(over) {
        let canonical = canonical_subject(&leaving.subject);
        for row in rows
            .iter()
            .filter(|row| occupies(row, &seat.name, &canonical))
        {
            cx.delete_row(OCCUPANTS, &row.key)?;
        }
        displaced.push(leaving.subject.clone());
    }
    Ok(displaced)
}

// field_seats is the subject, in its canonical text, that an update's
// patch gives a seat's fromField: None for a seat with no fromField, a
// patch that leaves the field alone, and a field it clears.
fn field_seats(
    seat: &SeatsSeat,
    patch: &Map<String, Value>,
    after: &Map<String, Value>,
) -> Option<String> {
    let (field, _) = seat.from_field.as_ref()?;
    if !patch.contains_key(field) {
        return None;
    }
    match after.get(field) {
        Some(Value::String(held)) if !held.is_empty() => Some(canonical_subject(held)),
        _ => None,
    }
}

// is_seated says whether a subject, in its canonical text, is seated in a
// seat: its fromField's value holds it, or an occupant row seats it.
fn is_seated(seat: &SeatsSeat, value: Option<&Value>, rows: &[SubRow], subject: &str) -> bool {
    seat.from_field
        .as_ref()
        .is_some_and(|field| field_holds(field, value, subject))
        || rows.iter().any(|row| occupies(row, &seat.name, subject))
}

// field_of is the value a seat's fromField holds in a row's own fields;
// None for a seat that names none, and for no value.
fn field_of(seat: &SeatsSeat, own: &Map<String, Value>) -> Option<Value> {
    let (field, _) = seat.from_field.as_ref()?;
    own.get(field).filter(|value| !value.is_null()).cloned()
}

// drop_dropped deletes the occupant rows of seats the config no longer
// lists, which no rule reads.
fn drop_dropped(cx: &Cx<'_, SeatsConfig>, rows: &[SubRow]) -> Hook<()> {
    let config = cx.config();
    for row in rows {
        let seat = row
            .value("seat")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if config.seat(seat).is_none() {
            cx.delete_row(OCCUPANTS, &row.key)?;
        }
    }
    Ok(())
}

/// One subject seated in a seat, as listSeats lists it.
struct Seated {
    subject: String,
    at: Value,
    by: Value,
    from_field: bool,
}

// seated is a seat's subjects: the one its fromField holds first, when the
// field holds a non-empty string (with its occupant row's seatedAt and
// seatedBy when one names it, else null), then its other occupants by key,
// each subject once. A take's key is one past every key it read, so the
// keys' order is the order the occupants were seated in, which seatedAt,
// the command's clock, need not be when two servers' clocks disagree.
fn seated(seat: &SeatsSeat, value: Option<&Value>, rows: &[SubRow]) -> Vec<Seated> {
    let mut mine: Vec<&SubRow> = rows
        .iter()
        .filter(|row| row.value("seat").and_then(Value::as_str) == Some(seat.name.as_str()))
        .collect();
    mine.sort_by(|a, b| a.key.cmp(&b.key));
    let mut out: Vec<Seated> = Vec::new();
    let held = match (&seat.from_field, value) {
        (Some(field), Some(Value::String(held))) if !held.is_empty() => Some((field, held)),
        _ => None,
    };
    if let Some((field, held)) = &held {
        let own = mine.iter().find(|row| {
            row.value("subject")
                .and_then(Value::as_str)
                .is_some_and(|subject| field_holds(field, value, &canonical_subject(subject)))
        });
        out.push(Seated {
            subject: (*held).clone(),
            at: own
                .and_then(|row| row.value("seatedAt"))
                .cloned()
                .unwrap_or(Value::Null),
            by: own
                .and_then(|row| row.value("seatedBy"))
                .cloned()
                .unwrap_or(Value::Null),
            from_field: true,
        });
    }
    for row in mine {
        let Some(subject) = row.value("subject").and_then(Value::as_str) else {
            continue;
        };
        let canonical = canonical_subject(subject);
        if held.is_some_and(|(field, _)| field_holds(field, value, &canonical)) {
            continue;
        }
        if out
            .iter()
            .any(|seated| !seated.from_field && canonical_subject(&seated.subject) == canonical)
        {
            continue;
        }
        out.push(Seated {
            subject: subject.to_owned(),
            at: row.value("seatedAt").cloned().unwrap_or(Value::Null),
            by: row.value("seatedBy").cloned().unwrap_or(Value::Null),
            from_field: false,
        });
    }
    out
}

// field_value is the value a seat's fromField holds on the row as the run
// has it; None for a seat that names none.
fn field_value<C>(view: &View<'_, C>, seat: &SeatsSeat) -> Option<Value> {
    seat.from_field
        .as_ref()
        .and_then(|(field, _)| view.own_field(field))
}

// subject_of is the subject an operation names, in its canonical text: its
// subject, else the caller's.
fn subject_of<C>(view: &View<'_, C>, params: &Value) -> String {
    match params.get("subject").and_then(Value::as_str) {
        Some(subject) => canonical_subject(subject),
        None => canonical_subject(&view.caller().subject),
    }
}

// seat_of_params is the config's seat an operation names, which settle
// held to be one.
fn seat_of_params<'c>(cx: &Cx<'c, SeatsConfig>, params: &Value) -> Hook<&'c SeatsSeat> {
    let name = seat_param(params);
    cx.config()
        .seat(name)
        .ok_or_else(|| cx.defect(format!("{name} reached its handler without its settle")))
}

fn seat_param(params: &Value) -> &str {
    params
        .get("seat")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

// bad_param refuses a parameter at its pointer, as an invalid argument.
fn bad_param<C>(view: &View<'_, C>, operation: &str, at: &str, message: String) -> Stop {
    Refusal::operation_params(
        view.behavior(),
        view.use_id(),
        operation,
        vec![SchemaIssue::new(at, message)],
    )
    .into()
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// number_of is an occupant's number, from its key.
fn number_of(row: &SubRow) -> Option<i64> {
    row.key.parse().ok()
}

// key_of is an occupant's key: its number in ten digits, zero-padded.
fn key_of(number: i64) -> String {
    format!("{number:0NUMBER_WIDTH$}")
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

// ---- the configSchema and the paramsSchemas, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    match config.get("seats") {
        None => issues.push("", "required", "must have required property 'seats'"),
        Some(seats) => seats_issues(seats, &mut issues),
    }
    issues
}

fn seats_issues(seats: &Value, issues: &mut Issues) {
    let at = "/seats";
    let Some(named) = seats.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (name, seat) in named {
        // A key that is no seat's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = schema::name_issues(name);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push(at, keyword, message);
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        seat_issues(&schema::member(at, name), seat, issues);
    }
}

fn seat_issues(at: &str, seat: &Value, issues: &mut Issues) {
    let Some(members) = seat.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &SEAT_KEYS, issues);
    if let Some(max) = members.get("max") {
        schema::integer_issues(&schema::member(at, "max"), max, Some(1), None, issues);
    }
    if members
        .get("onFull")
        .is_some_and(|on_full| !on_full.as_str().is_some_and(|text| ON_FULL.contains(&text)))
    {
        issues.push(
            schema::member(at, "onFull"),
            "enum",
            format!("must be one of {}", ON_FULL.map(schema::quoted).join(", ")),
        );
    }
    for key in ["take", "assign", "fromField"] {
        if let Some(value) = members.get(key) {
            schema::text_issues(&schema::member(at, key), value, issues);
        }
    }
}

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
fn unknown_keys(at: &str, members: &Map<String, Value>, known: &[&str], issues: &mut Issues) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words: takeSeat and leaveSeat
// name their seat, and a subject they may; listSeats a seat it may.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let keys: &[&str] = match operation {
        TAKE_SEAT | LEAVE_SEAT => &["seat", "subject"],
        LIST_SEATS => &["seat"],
        _ => return issues,
    };
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    if operation != LIST_SEATS && !given.contains_key("seat") {
        issues.push("", "required", "must have required property 'seat'");
    }
    for key in keys {
        if let Some(value) = given.get(*key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_occupants_key_is_its_number_in_ten_digits() {
        assert_eq!(key_of(1), "0000000001");
        assert_eq!(number_of(&SubRow::new("0000000042", Map::new())), Some(42));
        assert_eq!(number_of(&SubRow::new("x", Map::new())), None);
    }

    #[test]
    fn a_by_names_its_fields_then_its_seats() {
        let by = |entries: &[&str]| -> String {
            by_named(&entries.iter().map(|e| (*e).to_owned()).collect::<Vec<_>>())
        };
        assert_eq!(by(&["askedOf"]), "the subject askedOf holds");
        assert_eq!(by(&["a", "b", "c"]), "the subject a, b or c holds");
        assert_eq!(by(&["seat:owner"]), "a subject seated in owner");
        assert_eq!(
            by(&["seat:x", "agent", "seat:y"]),
            "the subject agent holds or a subject seated in x or y"
        );
        assert_eq!(seat_name("seat:owner"), Some("owner"));
        assert_eq!(seat_name("owner"), None);
    }
}
