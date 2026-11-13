//! The state tables (`runtime/migrate/README.md`, "State") and the
//! statements that read and write them. The tables, and every value a
//! statement writes, are the Go runner's on SQLite (`state.go`), so either
//! runner goes on from a file the other migrated. The Go runner numbers its
//! parameters `$1`; SQLite reads that as a named parameter, so the same
//! statements number them `?1` here.

use std::collections::BTreeSet;
use std::time::{SystemTime, UNIX_EPOCH};

use rusqlite::{params, Connection, OptionalExtension};

use crate::canonical::sql_hash;
use crate::error::Error;
use crate::plan::{Model, Plan, Step};

/// The table that holds each service's applied model and plan in progress.
pub(crate) const STATE_TABLE: &str = "superschematic_schema_state";

/// The table that holds a row per step a plan has run.
pub(crate) const LOG_TABLE: &str = "superschematic_migrations";

/// Creates the two tables when they are missing. The text is the Go
/// runner's, so `sqlite_master` holds the same SQL whichever runner made
/// them.
pub(crate) const CREATE: [&str; 2] = [
    "CREATE TABLE IF NOT EXISTS superschematic_schema_state (
  service     TEXT PRIMARY KEY,
  dialect     TEXT NOT NULL,
  model_hash  TEXT NOT NULL,
  model       TEXT,
  plan_hash   TEXT,
  plan_phase  TEXT,
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)",
    "CREATE TABLE IF NOT EXISTS superschematic_migrations (
  plan_hash   TEXT NOT NULL,
  step        INTEGER NOT NULL,
  service     TEXT NOT NULL,
  phase       TEXT NOT NULL,
  subject     TEXT NOT NULL,
  sql_hash    TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  PRIMARY KEY (plan_hash, step)
)",
];

/// Counts the state tables: 1 when the state table exists.
pub(crate) const EXISTS: &str =
    "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'superschematic_schema_state'";

/// The value of `plan_phase` once the plan in progress has finished its
/// expand steps and has no expanded model, or a runner that predates the
/// expanded model recorded the phase: the applied model is still the
/// plan's `from`.
pub const PLAN_PHASE_EXPAND: &str = "expand";

/// The value of `plan_phase` once the plan in progress has finished its
/// expand steps and the applied model is the plan's `expandedModel`, which
/// a plan from it supersedes.
pub const PLAN_PHASE_EXPANDED: &str = "expanded";

/// The time a row records, as RFC 3339 UTC text with microseconds
/// (`2026-10-04T16:42:18.146632Z`), which SQLite stores as written.
pub(crate) fn now() -> String {
    let since_epoch = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    timestamp(since_epoch.as_secs(), since_epoch.subsec_micros())
}

/// The RFC 3339 UTC text of a time `seconds` and `micros` after the epoch.
fn timestamp(seconds: u64, micros: u32) -> String {
    let (days, second_of_day) = (seconds / 86_400, seconds % 86_400);
    // The civil date of a day count, by eras of 400 years that start on
    // 1 March, so a leap day is an era's last.
    let shifted = days + 719_468;
    let (era, day_of_era) = (shifted / 146_097, shifted % 146_097);
    let year_of_era =
        (day_of_era - day_of_era / 1_460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let shifted_month = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * shifted_month + 2) / 5 + 1;
    let month = if shifted_month < 10 {
        shifted_month + 3
    } else {
        shifted_month - 9
    };
    let year = year_of_era + era * 400 + u64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{micros:06}Z",
        second_of_day / 3_600,
        second_of_day % 3_600 / 60,
        second_of_day % 60
    )
}

/// A service's row of the state table. A NULL column reads as an empty
/// string.
#[derive(Debug, Default)]
pub(crate) struct State {
    /// False when the service has no row.
    pub(crate) recorded: bool,
    pub(crate) dialect: String,
    pub(crate) model_hash: String,
    pub(crate) model: String,
    pub(crate) plan_hash: String,
    pub(crate) plan_phase: String,
    pub(crate) updated_at: String,
}

pub(crate) fn read_state(conn: &Connection, service: &str) -> Result<State, Error> {
    conn.query_row(
        "SELECT dialect, model_hash, COALESCE(model, ''), COALESCE(plan_hash, ''), COALESCE(plan_phase, ''), updated_at
FROM superschematic_schema_state WHERE service = ?1",
        params![service],
        |row| {
            Ok(State {
                recorded: true,
                dialect: row.get(0)?,
                model_hash: row.get(1)?,
                model: row.get(2)?,
                plan_hash: row.get(3)?,
                plan_phase: row.get(4)?,
                updated_at: row.get(5)?,
            })
        },
    )
    .optional()
    .map(|state| state.unwrap_or_default())
    .map_err(Error::database(format!(
        "read the state of service {service}"
    )))
}

/// Records `plan` as in progress and deletes the log rows an earlier
/// application of the same plan left: a plan is a pure function of its two
/// models, so a database taken from A to B, back to A and to B again runs
/// the same plan twice.
pub(crate) fn start_plan(conn: &Connection, plan: &Plan) -> Result<(), Error> {
    conn.execute(
        "INSERT INTO superschematic_schema_state (service, dialect, model_hash, plan_hash, updated_at)
VALUES (?1, ?2, '', ?3, ?4)
ON CONFLICT (service) DO UPDATE SET plan_hash = excluded.plan_hash, plan_phase = NULL, updated_at = excluded.updated_at",
        params![plan.service, plan.dialect.as_str(), plan.hash, now()],
    )
    .map_err(Error::database(format!(
        "record plan {} as in progress",
        plan.hash
    )))?;
    conn.execute(
        "DELETE FROM superschematic_migrations WHERE plan_hash = ?1",
        params![plan.hash],
    )
    .map_err(Error::database(format!(
        "clear the log of plan {}",
        plan.hash
    )))?;
    Ok(())
}

/// Records `model` as the service's applied model with no plan in progress.
pub(crate) fn set_model(conn: &Connection, model: &Model) -> Result<(), Error> {
    conn.execute(
        "INSERT INTO superschematic_schema_state (service, dialect, model_hash, model, plan_hash, plan_phase, updated_at)
VALUES (?1, ?2, ?3, ?4, NULL, NULL, ?5)
ON CONFLICT (service) DO UPDATE SET dialect = excluded.dialect, model_hash = excluded.model_hash, model = excluded.model,
  plan_hash = NULL, plan_phase = NULL, updated_at = excluded.updated_at",
        params![
            model.service,
            model.dialect.as_str(),
            model.hash,
            model.canonical,
            now()
        ],
    )
    .map_err(Error::database(format!(
        "record model {} for service {}",
        model.hash, model.service
    )))?;
    Ok(())
}

/// Records that the plan in progress has finished its expand steps and,
/// when it has an `expandedModel`, that model as the applied model: the
/// database holds it until the contract runs.
pub(crate) fn set_expand_done(conn: &Connection, plan: &Plan) -> Result<(), Error> {
    let done = match plan.between_phases() {
        Some(between) => conn.execute(
            "UPDATE superschematic_schema_state SET plan_phase = 'expanded', model_hash = ?1, model = ?2, updated_at = ?3
WHERE service = ?4 AND plan_hash = ?5",
            params![between.hash, between.canonical, now(), plan.service, plan.hash],
        ),
        None => conn.execute(
            "UPDATE superschematic_schema_state SET plan_phase = 'expand', updated_at = ?1 WHERE service = ?2 AND plan_hash = ?3",
            params![now(), plan.service, plan.hash],
        ),
    };
    done.map_err(Error::database(format!(
        "record the end of plan {}'s expand phase",
        plan.hash
    )))?;
    Ok(())
}

/// Reports whether any contract step of a plan has a log row: once one has
/// started, the database no longer holds the plan's expanded model.
pub(crate) fn contract_started(conn: &Connection, plan_hash: &str) -> Result<bool, Error> {
    conn.query_row(
        "SELECT count(*) FROM superschematic_migrations WHERE plan_hash = ?1 AND phase = 'contract'",
        params![plan_hash],
        |row| row.get::<_, i64>(0),
    )
    .map(|rows| rows > 0)
    .map_err(Error::database(format!("read the log of plan {plan_hash}")))
}

/// Returns the indexes of the plan's steps whose log row has `finished_at`.
pub(crate) fn finished_steps(conn: &Connection, plan_hash: &str) -> Result<BTreeSet<usize>, Error> {
    let read = || -> rusqlite::Result<BTreeSet<usize>> {
        let mut statement = conn.prepare(
            "SELECT step FROM superschematic_migrations WHERE plan_hash = ?1 AND finished_at IS NOT NULL",
        )?;
        let steps = statement.query_map(params![plan_hash], |row| step_index(row, 0))?;
        steps.collect()
    };
    read().map_err(Error::database(format!("read the log of plan {plan_hash}")))
}

/// Reads a row's step index, which SQLite holds as a signed integer.
fn step_index(row: &rusqlite::Row<'_>, column: usize) -> rusqlite::Result<usize> {
    let step: i64 = row.get(column)?;
    usize::try_from(step).map_err(|_| rusqlite::Error::IntegralValueOutOfRange(column, step))
}

/// One log row of a plan.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LoggedStep {
    /// The step's 1-based index in the plan.
    pub step: usize,
    /// The step's phase, `expand` or `contract`, as the row records it.
    pub phase: String,
    /// The object the step changes.
    pub subject: String,
    /// The SHA-256 of the step's statements joined by `"\n;\n"`.
    pub sql_hash: String,
    /// When the step started, as RFC 3339 UTC text.
    pub started_at: String,
    /// When the step finished; `None` while it has not. A SQLite step's row
    /// commits with the step, so it always has one.
    pub finished_at: Option<String>,
}

pub(crate) fn read_log(conn: &Connection, plan_hash: &str) -> Result<Vec<LoggedStep>, Error> {
    let read = || -> rusqlite::Result<Vec<LoggedStep>> {
        let mut statement = conn.prepare(
            "SELECT step, phase, subject, sql_hash, started_at, finished_at
FROM superschematic_migrations WHERE plan_hash = ?1 ORDER BY step",
        )?;
        let steps = statement.query_map(params![plan_hash], |row| {
            Ok(LoggedStep {
                step: step_index(row, 0)?,
                phase: row.get(1)?,
                subject: row.get(2)?,
                sql_hash: row.get(3)?,
                started_at: row.get(4)?,
                finished_at: row.get(5)?,
            })
        })?;
        steps.collect()
    };
    read().map_err(Error::database(format!("read the log of plan {plan_hash}")))
}

/// Writes a step's log row, with the time it started and the time it
/// finished.
pub(crate) fn insert_log(
    conn: &Connection,
    plan: &Plan,
    step: &Step,
    started_at: &str,
    finished_at: &str,
) -> Result<(), Error> {
    let write = || {
        let index = i64::try_from(step.index)
            .map_err(|err| rusqlite::Error::ToSqlConversionFailure(Box::new(err)))?;
        conn.execute(
            "INSERT INTO superschematic_migrations (plan_hash, step, service, phase, subject, sql_hash, started_at, finished_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            params![
                plan.hash,
                index,
                plan.service,
                step.phase.as_str(),
                step.subject,
                sql_hash(&step.statements),
                started_at,
                finished_at
            ],
        )
    };
    write().map_err(Error::database(format!(
        "write the log row of step {}",
        step.index
    )))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_time_is_rfc3339_utc_with_microseconds() {
        assert_eq!(timestamp(0, 0), "1970-01-01T00:00:00.000000Z");
        assert_eq!(
            timestamp(951_782_399, 999_999),
            "2000-02-28T23:59:59.999999Z"
        );
        assert_eq!(timestamp(951_782_400, 0), "2000-02-29T00:00:00.000000Z");
        assert_eq!(timestamp(951_868_800, 1), "2000-03-01T00:00:00.000001Z");
        assert_eq!(
            timestamp(1_700_000_000, 146_632),
            "2023-11-14T22:13:20.146632Z"
        );
        assert_eq!(timestamp(4_102_444_800, 0), "2100-01-01T00:00:00.000000Z");
        let now = now();
        assert_eq!(now.len(), "2026-10-04T16:42:18.146632Z".len());
        assert!(now.ends_with('Z'));
    }
}
