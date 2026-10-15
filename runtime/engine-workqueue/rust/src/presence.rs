//! Presence, a heartbeat on an instance that stands for a worker (D16),
//! the port of `typescript/src/presence.ts`. The instance's
//! `principalField` holds the subject of the principal it stands for, and
//! only that principal may beat it: `beat` moves its deadline to `ttlMs`
//! from now. An instance not beaten by its deadline is missed. Its
//! initialize sets the first deadline one `ttlMs` after the create, so a
//! worker that never beats is missed too.
//!
//! `miss` applies a miss, and only once: on an instance past its deadline
//! that is not missed yet, it sets missed, moves the status with
//! `onMissed` when it is one of onMissed's from states, through Workflow's
//! transition, whose guards run, and expires the leases the principal
//! holds on the schemas `releaseLeases` names, through each schema's
//! `expireHolder`, Lease's schema-level operation, so each expiry runs
//! Lease's own rules and appends its own event. It spares a lease renewed
//! after the principal's last beat (`notRenewedAfter`): a worker whose
//! presence beat starved while its lease heartbeats went on is alive. It
//! records the ids it expired, by schema, in its released column, which
//! the released field shows and so the miss's event carries. The leases
//! are the principal's, not the instance's: while another instance of the
//! schema stands for the same principal and is present (not missed, before
//! its deadline), a miss leaves them. A transition a guard vetoes leaves
//! the status as it is and the instance missed all the same; any other
//! refusal, a missing permission say, fails the miss and leaves it as it
//! was. Anyone who may write the instance may call miss; on any other
//! instance it changes nothing. A missed instance stays missed until it
//! beats: the next beat clears missed and sets a new deadline, and
//! `onBeat` moves the status back from its from states.
//!
//! The runner calls miss on every instance past its deadline and not
//! missed, every `sweepMs`, in its schedule `miss`, reading this
//! behavior's columns across the schema through the index on them, as the
//! runner's principal. That principal needs write on this schema and, for
//! releaseLeases, Lease's overridePermission on each schema it names,
//! which expireHolder asks for; a miss that fails fails the run, which the
//! runner retries and its status shows.
//!
//! The guard keeps the instance to its principal: an update that changes
//! `principalField` once it holds a value is refused, whoever asks
//! (`principal_fixed`). beat checks its caller in its handler
//! (`no_principal`, `not_principal`).
//!
//! Every statement is the same on SQLite and Postgres (D58) but the reads
//! of the principal field from the instances' data, JSON access on each
//! (`json_extract`; `->>` on Postgres, whose data is text cast to jsonb),
//! and the sweep's order, by id in byte order on Postgres as on SQLite.
//!
//! configChange: any config may change except `principalField`, which
//! changes only on a schema with no instances. A new ttlMs applies from
//! each instance's next beat. Presence can be added to a schema that has
//! instances, which have no deadline until their first beat, and cannot be
//! removed from one: the deadlines and misses they hold would stay behind.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, Every, InstanceView, OperationCx, Schedule, ScheduleCx,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{number_value, stringify};
use superschematic_engine::storage::{Dialect, Sql, SqlValue};
use superschematic_engine::{Behavior, ConfigTarget, Error, GuardRequest, Hooks, Result};

use crate::moves::{self, StatusMove};

/// How often the runner misses the instances past their deadline when the
/// config gives no sweepMs.
pub const DEFAULT_SWEEP_MS: i64 = 5000;

/// The most instances one run of the sweep misses; what is left waits for
/// the next run.
pub const MAX_SWEEP: i64 = 1000;

/// How many instances the sweep reads at a time.
const SWEEP_BATCH: i64 = 100;

const NAME: &str = "Presence";

/// A move of the status a miss or a beat makes.
pub type PresenceTransition = StatusMove;

/// Presence's config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PresenceConfig {
    pub ttl_ms: i64,
    pub principal_field: String,
    pub on_missed: Option<PresenceTransition>,
    pub on_beat: Option<PresenceTransition>,
    /// The schemas whose leases the principal holds a miss expires.
    pub release_leases: Vec<String>,
    pub sweep_ms: i64,
}

/// The presence's own columns on an instance.
struct State {
    deadline: Option<i64>,
    last_beat_at: Option<i64>,
    missed: bool,
    /// What the last miss released, as JSON text.
    released: Option<String>,
}

fn state(view: &InstanceView<'_, PresenceConfig>) -> Result<State> {
    let columns = view.columns().get()?;
    Ok(State {
        deadline: columns.opt_i64("deadline")?,
        last_beat_at: columns.opt_i64("last_beat_at")?,
        missed: columns.i64("missed")? == 1,
        released: columns.opt_text("released")?.map(str::to_owned),
    })
}

// principal_of reads the subject the instance stands for; `None` when its
// principalField holds none.
fn principal_of(view: &InstanceView<'_, PresenceConfig>) -> Result<Option<String>> {
    Ok(match view.data()?.get(&view.config().principal_field) {
        Some(Value::String(subject)) if !subject.is_empty() => Some(subject.clone()),
        _ => None,
    })
}

fn vetoed(
    view: &InstanceView<'_, PresenceConfig>,
    operation: &str,
    reason: String,
    code: &str,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: None,
        },
    )
}

// move_status moves the status through Workflow's transition when it is
// one of the rule's from states. A guard's veto leaves it as it is.
fn move_status(
    cx: &OperationCx<'_, PresenceConfig>,
    rule: Option<&PresenceTransition>,
) -> Result<()> {
    if rule.is_none() {
        return Ok(());
    }
    let status = moves::status_of(cx)?;
    moves::apply(cx, rule, status.as_deref())
}

// field_is is a test of the principal field's value on the relation's
// row, a member of its JSON data, with its two parameters: the field's
// path and the subject.
fn field_is(dialect: Dialect, field: &str, subject: &str) -> (&'static str, [SqlValue; 2]) {
    match dialect {
        Dialect::Sqlite => (
            "json_extract(data, ?) = ?",
            [
                SqlValue::from(format!("$.\"{field}\"")),
                SqlValue::from(subject),
            ],
        ),
        Dialect::Postgres => (
            "(data::jsonb)->>(?::text) = ?",
            [SqlValue::from(field), SqlValue::from(subject)],
        ),
    }
}

// present_elsewhere reports whether another instance of the schema stands
// for the principal and is present: not missed, and before its deadline.
// The leases are the principal's, so a miss of one of its instances leaves
// them while another still beats.
fn present_elsewhere(cx: &OperationCx<'_, PresenceConfig>, principal: &str) -> Result<bool> {
    let (test, field) = field_is(cx.dialect(), &cx.config().principal_field, principal);
    let mut params = vec![SqlValue::from(cx.id()), SqlValue::Integer(cx.now())];
    params.extend(field);
    Ok(cx
        .sql()
        .get(
            format!(
                "SELECT 1 AS present FROM {} WHERE id <> ? AND missed = 0 AND deadline > ? AND {test} LIMIT 1",
                cx.sql().instances()?
            ),
            &params,
        )?
        .is_some())
}

// last_seen is the principal's last beat on any instance of the schema
// that stands for it, or, for one that never beat, its create: a lease
// renewed after it shows its holder alive, and the miss spares it.
fn last_seen(cx: &OperationCx<'_, PresenceConfig>, principal: &str) -> Result<i64> {
    let (test, params) = field_is(cx.dialect(), &cx.config().principal_field, principal);
    Ok(cx
        .sql()
        .get(
            format!(
                "SELECT MAX(COALESCE(last_beat_at, created_at)) AS seen FROM {} WHERE {test}",
                cx.sql().instances()?
            ),
            &params,
        )?
        .map(|row| row.opt_i64("seen"))
        .transpose()?
        .flatten()
        .unwrap_or(0))
}

// check_leases holds each schema of releaseLeases to a live schema that
// composes Lease, as the caller who defines or publishes may read it.
fn check_leases(target: &ConfigTarget<'_>, schemas: &[String]) -> Result<()> {
    let Some(reached) = target.schemas() else {
        return Ok(());
    };
    for name in schemas {
        let Some(schema) = reached.get(name)? else {
            return Err(Error::config(format!(
                "releaseLeases: schema {name} has no live version; publish it first"
            )));
        };
        if !schema.behaviors.iter().any(|behavior| behavior == "Lease") {
            return Err(Error::config(format!(
                "releaseLeases: {name} does not compose Lease, so it holds no lease to expire"
            )));
        }
    }
    Ok(())
}

/// Presence: a heartbeat on each instance, which stands for a worker.
#[derive(Clone, Copy, Debug, Default)]
pub struct Presence;

impl Behavior for Presence {
    type Config = PresenceConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Presence.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its fields, its Workflow's states, and the schemas it releases leases
    // on.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<PresenceConfig> {
        let principal_field = raw
            .get("principalField")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        if !target.fields().contains(&principal_field) {
            return Err(Error::config(format!(
                "principalField \"{principal_field}\" is not a field of {} (its fields: {})",
                target.type_name(),
                target.fields().join(", ")
            )));
        }
        if !moves::only_types(target, &principal_field, "string") {
            return Err(Error::config(format!(
                "principalField \"{principal_field}\" is not a string field of {}",
                target.type_name()
            )));
        }
        let on_missed = raw.get("onMissed").map(StatusMove::parse);
        let on_beat = raw.get("onBeat").map(StatusMove::parse);
        moves::check_moves(
            target,
            &[
                ("onMissed", on_missed.as_ref()),
                ("onBeat", on_beat.as_ref()),
            ],
        )?;
        let release_leases = moves::strings(raw.get("releaseLeases"));
        check_leases(target, &release_leases)?;
        let integer = |name: &str| match raw.get(name) {
            Some(Value::Number(ms)) => Some(number_value(ms) as i64),
            _ => None,
        };
        Ok(PresenceConfig {
            ttl_ms: integer("ttlMs").unwrap_or_default(),
            principal_field,
            on_missed,
            on_beat,
            release_leases,
            sweep_ms: integer("sweepMs").unwrap_or(DEFAULT_SWEEP_MS),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, change| {
                Ok(match (before, after) {
                    (Some(_), None) => Some(
                        "the deadlines and misses its instances hold would stay behind and come back if it were added again"
                            .to_owned(),
                    ),
                    (Some(before), Some(after))
                        if before.principal_field != after.principal_field
                            && change.instances()? =>
                    {
                        Some(format!(
                            "principalField is where every instance holds its principal: it stays {}",
                            before.principal_field
                        ))
                    }
                    _ => None,
                })
            }),
            guidance: Some(|_, config, target| crate::guidance::presence::guidance(config, target)),
            migrations: vec![
                BehaviorMigration::new(1, "presence")
                    .column("deadline", ColumnSpec::integer())
                    .column("last_beat_at", ColumnSpec::integer())
                    .column("missed", ColumnSpec::integer().not_null().default(0))
                    // The sweep reads the instances not missed, oldest
                    // deadline first.
                    .index("due", &["missed", "deadline"]),
                // What the last miss expired, as JSON, which the field and so
                // the miss's event show.
                BehaviorMigration::new(2, "released").column("released", ColumnSpec::text()),
            ],
            initialize: Some(|_, cx, _| {
                cx.columns().set(&[
                    ("deadline", SqlValue::Integer(cx.now() + cx.config().ttl_ms)),
                    ("missed", SqlValue::Integer(0)),
                ])
            }),
            guard: Some(guard),
            operations: vec![("beat", beat), ("miss", miss)],
            // Each field reads the presence's columns: an instance never
            // beaten has no lastBeatAt, and one never missed no released.
            fields: vec![
                ("deadline", |_, view| {
                    Ok(state(view)?.deadline.map(Value::from))
                }),
                ("lastBeatAt", |_, view| {
                    Ok(state(view)?.last_beat_at.map(Value::from))
                }),
                ("missed", |_, view| {
                    Ok(Some(Value::Bool(state(view)?.missed)))
                }),
                ("released", |_, view| {
                    state(view)?
                        .released
                        .map(|text| {
                            serde_json::from_str(&text).map_err(|error| {
                                Error::behavior(NAME, format!("released is no JSON: {error}"))
                            })
                        })
                        .transpose()
                }),
            ],
            schedules: vec![(
                "miss",
                Schedule {
                    every: Every::Config(|_, config: &PresenceConfig| Ok(Some(config.sweep_ms))),
                    run: sweep,
                },
            )],
            ..Hooks::default()
        }
    }
}

// guard keeps the instance to its principal: a create sets
// principalField, an update only while it holds none.
fn guard(
    _: &Presence,
    view: &InstanceView<'_, PresenceConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Update { after, .. } = request else {
        return Ok(None);
    };
    let field = &view.config().principal_field;
    let data = view.data()?;
    let before = data.get(field);
    if before.is_none_or(Value::is_null) || before == after.get(field) {
        return Ok(None);
    }
    Ok(Some(Veto {
        reason: format!(
            "{field} holds the principal the instance stands for, so it cannot change once set"
        ),
        code: Some("principal_fixed".to_owned()),
        details: None,
    }))
}

fn beat(_: &Presence, cx: &OperationCx<'_, PresenceConfig>, _: &Value) -> Result<Value> {
    let config = cx.config();
    let Some(principal) = principal_of(cx)? else {
        return Err(vetoed(
            cx,
            "beat",
            format!(
                "its {} holds no principal, so no one may beat it",
                config.principal_field
            ),
            "no_principal",
        ));
    };
    if principal != cx.principal().subject {
        return Err(vetoed(
            cx,
            "beat",
            "it stands for another principal, who alone may beat it".to_owned(),
            "not_principal",
        ));
    }
    let deadline = cx.now() + config.ttl_ms;
    cx.columns().set(&[
        ("deadline", SqlValue::Integer(deadline)),
        ("last_beat_at", SqlValue::Integer(cx.now())),
        ("missed", SqlValue::Integer(0)),
    ])?;
    move_status(cx, config.on_beat.as_ref())?;
    Ok(json!({ "deadline": deadline }))
}

fn miss(_: &Presence, cx: &OperationCx<'_, PresenceConfig>, _: &Value) -> Result<Value> {
    let config = cx.config();
    let current = state(cx)?;
    // Missed already, or not due: nothing changes.
    if current.missed || current.deadline.is_none_or(|deadline| cx.now() < deadline) {
        cx.unchanged();
        return Ok(json!({ "missed": false }));
    }
    cx.columns().set(&[("missed", SqlValue::Integer(1))])?;
    move_status(cx, config.on_missed.as_ref())?;
    let mut released = Map::new();
    if let Some(principal) = principal_of(cx)? {
        if !present_elsewhere(cx, &principal)? {
            let not_renewed_after = last_seen(cx, &principal)?;
            for schema in &config.release_leases {
                let result = cx.instances().invoke_schema(
                    schema,
                    "expireHolder",
                    &json!({ "holder": principal, "notRenewedAfter": not_renewed_after }),
                )?;
                released.insert(
                    schema.clone(),
                    result.get("ids").cloned().unwrap_or_else(|| json!([])),
                );
            }
        }
    }
    let released = Value::Object(released);
    cx.columns()
        .set(&[("released", SqlValue::from(stringify(&released)))])?;
    Ok(json!({ "missed": true, "released": released }))
}

// sweep runs miss on every instance past its deadline and not missed,
// oldest deadline first, through the index on the two columns, at most
// MAX_SWEEP a run. Each miss sets missed, so the next batch starts after
// it; one that changes nothing ends the run, so a run never reads the same
// instance twice.
fn sweep(_: &Presence, cx: &ScheduleCx<'_, PresenceConfig>) -> Result<()> {
    let relation = cx.sql().instances()?;
    let order = match cx.dialect() {
        Dialect::Sqlite => "deadline, id",
        Dialect::Postgres => "deadline, id COLLATE \"C\"",
    };
    let statement = format!(
        "SELECT id FROM {relation} WHERE missed = 0 AND deadline <= ? ORDER BY {order} LIMIT ?"
    );
    let mut swept = 0;
    while swept < MAX_SWEEP {
        let due = cx.sql().all(
            Sql::same(statement.as_str()),
            &[
                SqlValue::Integer(cx.now()),
                SqlValue::Integer(SWEEP_BATCH.min(MAX_SWEEP - swept)),
            ],
        )?;
        for row in &due {
            let result =
                cx.instances()
                    .invoke(cx.schema(), row.text("id")?, "miss", &json!({}), None)?;
            if result.get("missed") != Some(&Value::Bool(true)) {
                return Ok(());
            }
        }
        swept += due.len() as i64;
        if (due.len() as i64) < SWEEP_BATCH {
            return Ok(());
        }
    }
    Ok(())
}
