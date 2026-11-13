//! The runner: `apply`, `status` and `adopt` over one rusqlite connection,
//! as `runtime/migrate/README.md` describes them and as the Go runner's
//! `runner.go` and `sqlite/sqlite.go` do them on a SQLite file.
//!
//! Every write transaction is `BEGIN IMMEDIATE`, which is the lock that
//! serializes runners: the runner checks the state again inside each one.

use std::collections::BTreeSet;
use std::time::{Duration, Instant};

use rusqlite::Connection;

use crate::error::{refuse, BoxError, Error, GaveUp, StepError};
use crate::plan::{Dialect, Model, Phase, Plan, Step};
use crate::state::{self, LoggedStep, State, PLAN_PHASE_EXPAND, PLAN_PHASE_EXPANDED};

/// The pauses before each retry of a step that hit a lock timeout. The step
/// fails after the last retry.
pub const DEFAULT_WAITS: [Duration; 5] = [
    Duration::from_secs(1),
    Duration::from_secs(2),
    Duration::from_secs(4),
    Duration::from_secs(8),
    Duration::from_secs(16),
];

/// How long a transaction waits for another connection's lock before it
/// fails with `SQLITE_BUSY` and the runner retries it.
pub const DEFAULT_BUSY_TIMEOUT: Duration = Duration::from_secs(5);

/// Applies plans to one SQLite file and records them in its state tables
/// (D27, D116). It uses the connection alone until the run ends.
pub struct Runner<'c> {
    conn: &'c Connection,
    log: Option<Log<'c>>,
    waits: Vec<Duration>,
    sleep: Box<dyn FnMut(Duration) + 'c>,
    after_step: Option<AfterStep<'c>>,
}

/// Receives the runner's lines.
type Log<'c> = Box<dyn FnMut(&str) + 'c>;

/// Runs after each step a run finishes.
type AfterStep<'c> = Box<dyn FnMut(&Step) -> Result<(), BoxError> + 'c>;

/// What one [`Runner::apply`] did.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Applied {
    /// True when the file's applied model was the plan's `to` and nothing
    /// ran.
    pub already_applied: bool,
    /// The steps this run ran, by index.
    pub ran: Vec<usize>,
    /// True when every expand step of the plan has finished.
    pub expand_done: bool,
    /// True when the plan has finished: the file's applied model is the
    /// plan's `to`.
    pub finished: bool,
    /// The plan whose pending contract this plan superseded, if it
    /// superseded one.
    pub superseded: Option<String>,
}

/// A service's row of the state table and the log of the plan in progress.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Status {
    /// The DB service the status is of.
    pub service: String,
    /// False when the file has no state for the service.
    pub recorded: bool,
    /// The dialect the row records; empty when there is no row.
    pub dialect: String,
    /// The applied model's hash; empty while no plan has finished and no
    /// model was adopted.
    pub model_hash: String,
    /// The applied model's canonical JSON, when a model is applied.
    pub model: Option<String>,
    /// The plan in progress.
    pub plan_hash: Option<String>,
    /// `None` until the expand steps of the plan in progress have
    /// finished, then [`PLAN_PHASE_EXPANDED`] when `model_hash` is the
    /// plan's expanded model, which a plan from it supersedes, or
    /// [`PLAN_PHASE_EXPAND`] when the plan has none and `model_hash` is
    /// still its `from`.
    pub plan_phase: Option<String>,
    /// When the row last changed, as RFC 3339 UTC text; empty when there
    /// is no row.
    pub updated_at: String,
    /// The log rows of the plan in progress.
    pub steps: Vec<LoggedStep>,
}

impl<'c> Runner<'c> {
    /// Binds a runner to `conn`. It sets the connection's `busy_timeout` to
    /// [`DEFAULT_BUSY_TIMEOUT`] and turns foreign keys on, as every step
    /// the compiler writes expects them.
    pub fn new(conn: &'c Connection) -> Result<Self, Error> {
        conn.busy_timeout(DEFAULT_BUSY_TIMEOUT)
            .map_err(Error::database("sqlite: set busy_timeout"))?;
        conn.execute_batch("PRAGMA foreign_keys = ON")
            .map_err(Error::database("sqlite: PRAGMA foreign_keys = ON"))?;
        Ok(Runner {
            conn,
            log: None,
            waits: DEFAULT_WAITS.to_vec(),
            sleep: Box::new(std::thread::sleep),
            after_step: None,
        })
    }

    /// Sets how long the connection waits for another connection's lock
    /// before a transaction fails with `SQLITE_BUSY`.
    pub fn busy_timeout(self, timeout: Duration) -> Result<Self, Error> {
        self.conn
            .busy_timeout(timeout)
            .map_err(Error::database("sqlite: set busy_timeout"))?;
        Ok(self)
    }

    /// Sends a line per step and per change of state to `log`. Without it
    /// the lines are discarded.
    pub fn log(mut self, log: impl FnMut(&str) + 'c) -> Self {
        self.log = Some(Box::new(log));
        self
    }

    /// Replaces [`DEFAULT_WAITS`], the pauses before each retry of a step
    /// that hit a lock timeout.
    pub fn waits(mut self, waits: impl Into<Vec<Duration>>) -> Self {
        self.waits = waits.into();
        self
    }

    /// Replaces the pause between retries, which sleeps the thread.
    pub fn sleep(mut self, sleep: impl FnMut(Duration) + 'c) -> Self {
        self.sleep = Box::new(sleep);
        self
    }

    /// Runs `hook` after each step a run finishes. An error it returns
    /// stops the run, as [`Error::Stopped`], with the step applied. Tests
    /// use it to stop a run part-way.
    pub fn after_step(mut self, hook: impl FnMut(&Step) -> Result<(), BoxError> + 'c) -> Self {
        self.after_step = Some(Box::new(hook));
        self
    }

    /// Runs the steps of `plan` in `phase` that have not run yet, as
    /// `runtime/migrate/README.md` describes ("`apply`"). A refusal is
    /// [`Error::Refused`]; a failed step is [`Error::Step`], and the next
    /// apply of the same plan resumes at it.
    pub fn apply(&mut self, plan: &Plan, phase: Phase) -> Result<Applied, Error> {
        if plan.dialect != Dialect::Sqlite {
            return Err(refuse!(
                "the plan is for {} and the database is {}",
                plan.dialect,
                Dialect::Sqlite
            ));
        }
        self.create_tables()?;

        let (decision, superseded) = self.write(|conn| {
            let st = state::read_state(conn, &plan.service)?;
            let mut started = false;
            if !st.plan_hash.is_empty()
                && st.plan_hash != plan.hash
                && st.plan_phase == PLAN_PHASE_EXPANDED
            {
                started = state::contract_started(conn, &st.plan_hash)?;
            }
            let decision = decide(&st, plan, started)?;
            if decision == Decision::AlreadyApplied {
                return Ok((decision, None));
            }
            let resume = decision == Decision::Resume;
            if phase == Phase::Contract {
                // Only the plan in progress has a contract to run. A plan
                // that is not, such as one a newer plan superseded, may
                // start from the database's model again when its expand had
                // nothing to do, and running its contract then would drop
                // what the servers that superseded it still use.
                if !resume {
                    return Err(refuse!(
                        "plan {} is not in progress for service {}: --phase contract runs the contract of the plan in progress once its expand phase has run; apply this plan with --phase expand first, or with --phase all",
                        plan.hash,
                        plan.service
                    ));
                }
                expand_finished(conn, plan)?;
            }
            if !resume {
                state::start_plan(conn, plan)?;
            }
            let superseded = (decision == Decision::Supersede).then_some(st.plan_hash);
            Ok((decision, superseded))
        })?;

        let mut result = Applied {
            superseded,
            ..Applied::default()
        };
        if decision == Decision::AlreadyApplied {
            self.logf(format_args!(
                "plan {} is already applied: service {} is at model {}",
                plan.hash, plan.service, plan.to
            ));
            result.already_applied = true;
            result.expand_done = true;
            result.finished = true;
            return Ok(result);
        }
        if let Some(old) = &result.superseded {
            self.logf(format_args!(
                "plan {} supersedes plan {old}, whose contract was pending: service {} is at that plan's expanded model {}, and its contract will not run",
                plan.hash, plan.service, plan.from
            ));
        }
        if decision == Decision::Resume {
            self.logf(format_args!(
                "resuming plan {} for service {}",
                plan.hash, plan.service
            ));
        } else {
            self.logf(format_args!(
                "applying plan {} to service {} ({}): {} steps, from {} to {}",
                plan.hash,
                plan.service,
                plan.dialect,
                plan.steps.len(),
                describe_model(&plan.from),
                plan.to
            ));
        }

        for step in &plan.steps {
            if phase != Phase::All && step.phase != phase {
                continue;
            }
            match self.run_step(plan, step)? {
                StepOutcome::OtherRunnerFinished => {
                    self.logf(format_args!("another runner finished plan {}", plan.hash));
                    result.expand_done = true;
                    result.finished = true;
                    return Ok(result);
                }
                StepOutcome::AlreadyRan => {}
                StepOutcome::Ran => {
                    result.ran.push(step.index);
                    if let Some(hook) = &mut self.after_step {
                        hook(step).map_err(Error::Stopped)?;
                    }
                }
            }
        }

        // A phase with no steps, or a run that resumed after its last step
        // committed, changes the state here.
        let progress = self.write(|conn| {
            let st = state::read_state(conn, &plan.service)?;
            if st.plan_hash != plan.hash {
                if st.plan_hash.is_empty() && st.model_hash == plan.to {
                    return Ok(Progress {
                        expand_done: true,
                        finished: true,
                    });
                }
                return Err(not_in_progress(plan));
            }
            let finished = state::finished_steps(conn, &plan.hash)?;
            advance(conn, plan, &st, &finished)
        })?;
        result.expand_done = progress.expand_done;
        result.finished = progress.finished;
        if result.finished {
            self.logf(format_args!(
                "plan {} applied: service {} is at model {}",
                plan.hash, plan.service, plan.to
            ));
        } else if result.expand_done {
            self.logf(format_args!(
                "plan {}: expand is done; apply --phase contract after the rollout",
                plan.hash
            ));
        }
        Ok(result)
    }

    /// Reads a service's state without changing anything; it creates no
    /// table, and a file the runner never touched has no state.
    pub fn status(&mut self, service: &str) -> Result<Status, Error> {
        transact(self.conn, Transaction::Read, |conn| {
            let mut status = Status {
                service: service.to_string(),
                ..Status::default()
            };
            let tables: i64 = conn
                .query_row(state::EXISTS, [], |row| row.get(0))
                .map_err(Error::database("look for the state table"))?;
            if tables == 0 {
                return Ok(status);
            }
            let st = state::read_state(conn, service)?;
            if !st.recorded {
                return Ok(status);
            }
            if !st.plan_hash.is_empty() {
                status.steps = state::read_log(conn, &st.plan_hash)?;
            }
            status.recorded = true;
            status.dialect = st.dialect;
            status.model_hash = st.model_hash;
            status.model = Some(st.model).filter(|model| !model.is_empty());
            status.plan_hash = Some(st.plan_hash).filter(|hash| !hash.is_empty());
            status.plan_phase = Some(st.plan_phase).filter(|phase| !phase.is_empty());
            status.updated_at = st.updated_at;
            Ok(status)
        })
    }

    /// Records `model` as the applied model of its service without running
    /// anything, and returns the hash it replaces, empty for none. It
    /// refuses while a plan is in progress.
    pub fn adopt(&mut self, model: &Model) -> Result<String, Error> {
        if model.dialect != Dialect::Sqlite {
            return Err(refuse!(
                "the model is for {} and the database is {}",
                model.dialect,
                Dialect::Sqlite
            ));
        }
        self.create_tables()?;
        self.write(|conn| {
            let st = state::read_state(conn, &model.service)?;
            if !st.plan_hash.is_empty() {
                return Err(refuse!(
                    "service {} has plan {} in progress ({}); adopt refuses while a plan is in progress",
                    model.service,
                    st.plan_hash,
                    describe_phase(&st.plan_phase)
                ));
            }
            state::set_model(conn, model)?;
            Ok(st.model_hash)
        })
    }

    /// Creates the state tables when they are missing.
    fn create_tables(&mut self) -> Result<(), Error> {
        self.write(|conn| {
            for statement in state::CREATE {
                conn.execute_batch(statement)
                    .map_err(Error::database("create the state tables"))?;
            }
            Ok(())
        })
    }

    /// Runs one write transaction on the state tables, retrying it after a
    /// lock timeout as a step is.
    fn write<T>(
        &mut self,
        mut work: impl FnMut(&Connection) -> Result<T, Error>,
    ) -> Result<T, Error> {
        let mut attempt = 0;
        loop {
            match transact(self.conn, Transaction::Write, &mut work) {
                Err(err) if err.is_busy() && attempt < self.waits.len() => {
                    let wait = self.waits[attempt];
                    self.logf(format_args!(
                        "lock timeout on the state tables; retrying in {}",
                        go_duration(wait)
                    ));
                    (self.sleep)(wait);
                    attempt += 1;
                }
                done => return done,
            }
        }
    }

    /// Runs one step, retrying it after a lock timeout.
    fn run_step(&mut self, plan: &Plan, step: &Step) -> Result<StepOutcome, Error> {
        let label = format!(
            "step {}/{} {} {}",
            step.index,
            plan.steps.len(),
            step.phase,
            step.subject
        );
        let mut attempt = 0;
        loop {
            let began = Instant::now();
            let err = match transactional_step(self.conn, plan, step) {
                Ok(outcome) => {
                    match outcome {
                        StepOutcome::Ran => {
                            // To the millisecond, as the Go runner rounds it.
                            let millis = (began.elapsed().as_micros() + 500) / 1_000;
                            let elapsed =
                                Duration::from_millis(u64::try_from(millis).unwrap_or(u64::MAX));
                            self.logf(format_args!("{label}: done in {}", go_duration(elapsed)));
                        }
                        StepOutcome::AlreadyRan => self.logf(format_args!("{label}: already done")),
                        StepOutcome::OtherRunnerFinished => {}
                    }
                    return Ok(outcome);
                }
                Err(err) => err,
            };
            if !err.is_busy() {
                return Err(Error::Step(err));
            }
            if attempt == self.waits.len() {
                return Err(Error::Step(StepError {
                    cause: Box::new(GaveUp {
                        cause: err.cause,
                        retries: self.waits.len(),
                    }),
                    ..err
                }));
            }
            let wait = self.waits[attempt];
            self.logf(format_args!(
                "{label}: lock timeout; retrying in {}",
                go_duration(wait)
            ));
            (self.sleep)(wait);
            attempt += 1;
        }
    }

    /// Sends one line to the log.
    pub(crate) fn logf(&mut self, line: std::fmt::Arguments<'_>) {
        if let Some(log) = &mut self.log {
            log(&line.to_string());
        }
    }
}

/// What step 4 of apply decides to do with a plan.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Decision {
    /// No plan is in progress and the database is at the plan's `from`.
    Start,
    /// The plan is in progress.
    Resume,
    /// The database is at the plan's `to`.
    AlreadyApplied,
    /// Another plan is in progress with only its contract left, none of it
    /// started, and the database is at that plan's expanded model, the
    /// plan's `from`.
    Supersede,
}

/// Step 4 of apply: resume the plan, report it applied, start it, start it
/// in place of a pending contract, or refuse. `contract_started` is whether
/// a contract step of the plan in progress has started; `decide` reads it
/// only when that plan recorded its expanded model.
fn decide(st: &State, plan: &Plan, contract_started: bool) -> Result<Decision, Error> {
    let in_progress = !st.plan_hash.is_empty();
    let pending = in_progress && st.plan_phase == PLAN_PHASE_EXPANDED && st.model_hash == plan.from;
    if st.plan_hash == plan.hash {
        return Ok(Decision::Resume);
    }
    if !in_progress && st.model_hash == plan.to && plan.to != plan.from {
        return Ok(Decision::AlreadyApplied);
    }
    if pending && contract_started {
        return Err(refuse!(
            "service {} has plan {} in progress, and its contract has started; finish it before applying plan {}",
            plan.service,
            st.plan_hash,
            plan.hash
        ));
    }
    if in_progress && !pending {
        return Err(refuse!(
            "service {} has plan {} in progress ({}); finish it before applying plan {}",
            plan.service,
            st.plan_hash,
            describe_phase(&st.plan_phase),
            plan.hash
        ));
    }
    if st.model_hash != plan.from && st.model_hash.is_empty() {
        // status has no model to print: a database built from create.sql or
        // by hand records the model it holds with adopt.
        return Err(refuse!(
            "the plan starts from {}, but service {} has no applied model; if the database already holds a schema (built from create.sql or by hand), record the model it matches with superschematic-migrate adopt (superschematic migrate plan --print-model prints it), and if it is empty, plan from an empty database",
            describe_model(&plan.from),
            plan.service
        ));
    }
    if st.model_hash != plan.from {
        return Err(refuse!(
            "the plan starts from {}, but service {} is at model {}; plan again from the applied model (superschematic-migrate status --model)",
            describe_model(&plan.from),
            plan.service,
            st.model_hash
        ));
    }
    if st.recorded && st.dialect != plan.dialect.as_str() {
        return Err(refuse!(
            "service {}'s state records dialect {}; the plan is for {}",
            plan.service,
            st.dialect,
            plan.dialect
        ));
    }
    if pending {
        return Ok(Decision::Supersede);
    }
    Ok(Decision::Start)
}

/// Step 5 of apply: the contract runs only once every expand step of the
/// plan in progress has finished.
fn expand_finished(conn: &Connection, plan: &Plan) -> Result<(), Error> {
    let finished = state::finished_steps(conn, &plan.hash)?;
    match plan
        .steps
        .iter()
        .find(|step| step.phase == Phase::Expand && !finished.contains(&step.index))
    {
        Some(step) => Err(refuse!(
            "plan {}'s expand step {} has not run; apply --phase expand first",
            plan.hash,
            step.index
        )),
        None => Ok(()),
    }
}

/// How far a plan has come.
#[derive(Debug, Clone, Copy)]
struct Progress {
    expand_done: bool,
    finished: bool,
}

/// Step 8 of apply: when every expand step has finished and the plan has
/// contract steps, `plan_phase` records it, with the plan's expanded model
/// as the applied model when it has one; when every step has finished, the
/// plan's model becomes the applied model.
fn advance(
    conn: &Connection,
    plan: &Plan,
    st: &State,
    finished: &BTreeSet<usize>,
) -> Result<Progress, Error> {
    let pending = |phase: Option<Phase>| {
        plan.steps.iter().any(|step| {
            !finished.contains(&step.index) && phase.is_none_or(|phase| step.phase == phase)
        })
    };
    let progress = Progress {
        expand_done: !pending(Some(Phase::Expand)),
        finished: !pending(None),
    };
    if progress.finished {
        state::set_model(conn, plan.model())?;
    } else if progress.expand_done && st.plan_phase.is_empty() {
        state::set_expand_done(conn, plan)?;
    }
    Ok(progress)
}

/// What one step did.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum StepOutcome {
    Ran,
    /// The step's log row has `finished_at`.
    AlreadyRan,
    /// Another runner finished the plan: runners take turns per step.
    OtherRunnerFinished,
}

/// Why a step's transaction failed: one of its statements, or anything
/// else in it.
enum StepFailure<'s> {
    Statement(&'s str, rusqlite::Error),
    Other(Error),
}

impl From<Error> for StepFailure<'_> {
    fn from(err: Error) -> Self {
        StepFailure::Other(err)
    }
}

/// Runs a step and its log row in one transaction. The transaction is the
/// lock, so it checks the state again first.
fn transactional_step(
    conn: &Connection,
    plan: &Plan,
    step: &Step,
) -> Result<StepOutcome, StepError> {
    let transaction = if step.foreign_keys_off {
        Transaction::ForeignKeysOff
    } else {
        Transaction::Write
    };
    transact(conn, transaction, |conn| {
        let st = state::read_state(conn, &plan.service)?;
        if st.plan_hash != plan.hash {
            if st.plan_hash.is_empty() && st.model_hash == plan.to {
                return Ok(StepOutcome::OtherRunnerFinished);
            }
            return Err(not_in_progress(plan).into());
        }
        let mut finished = state::finished_steps(conn, &plan.hash)?;
        if finished.contains(&step.index) {
            return Ok(StepOutcome::AlreadyRan);
        }
        let started_at = state::now();
        for statement in &step.statements {
            // One call runs the statement, and every statement of a text
            // that holds several.
            conn.execute_batch(statement)
                .map_err(|err| StepFailure::Statement(statement, err))?;
        }
        state::insert_log(conn, plan, step, &started_at, &state::now())?;
        finished.insert(step.index);
        advance(conn, plan, &st, &finished)?;
        Ok(StepOutcome::Ran)
    })
    .map_err(|failure| {
        let (statement, cause): (_, BoxError) = match failure {
            StepFailure::Statement(statement, err) => (Some(statement.to_string()), Box::new(err)),
            StepFailure::Other(err) => (None, Box::new(err)),
        };
        StepError {
            index: step.index,
            subject: step.subject.clone(),
            statement,
            cause,
        }
    })
}

fn not_in_progress(plan: &Plan) -> Error {
    Error::NotInProgress {
        plan: plan.hash.clone(),
        service: plan.service.clone(),
    }
}

/// How a transaction begins and ends.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Transaction {
    /// A deferred transaction, which takes no write lock.
    Read,
    /// `BEGIN IMMEDIATE`.
    Write,
    /// `BEGIN IMMEDIATE` with foreign key enforcement off around the
    /// transaction, which fails before its commit if any foreign key is
    /// violated.
    ForeignKeysOff,
}

/// Ends what a transaction left open when it did not commit: it rolls the
/// transaction back and turns foreign keys on again.
struct Restore<'c> {
    conn: &'c Connection,
    foreign_keys: bool,
}

impl Drop for Restore<'_> {
    fn drop(&mut self) {
        // Some errors end the transaction on their own, and then there is
        // none to roll back.
        if !self.conn.is_autocommit() {
            let _ = self.conn.execute_batch("ROLLBACK");
        }
        if self.foreign_keys {
            let _ = self.conn.execute_batch("PRAGMA foreign_keys = ON");
        }
    }
}

/// Runs `work` in one transaction. It commits when `work` returns `Ok` and
/// rolls back otherwise, returning `work`'s error.
///
/// For [`Transaction::ForeignKeysOff`] it turns foreign keys off before the
/// transaction, which SQLite allows only outside one, runs `PRAGMA
/// foreign_key_check` before the commit and fails on any row it returns,
/// and turns foreign keys on again after.
fn transact<T, E: From<Error>>(
    conn: &Connection,
    transaction: Transaction,
    work: impl FnOnce(&Connection) -> Result<T, E>,
) -> Result<T, E> {
    let foreign_keys_off = transaction == Transaction::ForeignKeysOff;
    if foreign_keys_off {
        conn.execute_batch("PRAGMA foreign_keys = OFF")
            .map_err(Error::database("sqlite: turn foreign keys off"))?;
    }
    let mut restore = Restore {
        conn,
        foreign_keys: foreign_keys_off,
    };
    let begin = match transaction {
        Transaction::Read => "BEGIN",
        Transaction::Write | Transaction::ForeignKeysOff => "BEGIN IMMEDIATE",
    };
    conn.execute_batch(begin)
        .map_err(Error::database(format!("sqlite: {begin}")))?;
    let value = work(conn)?;
    if foreign_keys_off {
        foreign_key_check(conn)?;
    }
    conn.execute_batch("COMMIT")
        .map_err(Error::database("sqlite: commit"))?;
    if foreign_keys_off {
        restore.foreign_keys = false;
        conn.execute_batch("PRAGMA foreign_keys = ON")
            .map_err(Error::database("sqlite: turn foreign keys on"))?;
    }
    Ok(value)
}

/// Fails when `PRAGMA foreign_key_check` returns a row, naming the first
/// five.
fn foreign_key_check(conn: &Connection) -> Result<(), Error> {
    let read = || -> rusqlite::Result<Vec<String>> {
        let mut statement = conn.prepare("PRAGMA foreign_key_check")?;
        let mut rows = statement.query([])?;
        let mut violations = Vec::new();
        while let Some(row) = rows.next()? {
            if violations.len() == 5 {
                continue;
            }
            let table: String = row.get(0)?;
            let rowid: Option<i64> = row.get(1)?;
            let parent: String = row.get(2)?;
            let which = rowid.map_or_else(|| "without rowid".to_string(), |id| id.to_string());
            violations.push(format!(
                "{table} row {which} references a missing row of {parent}"
            ));
        }
        Ok(violations)
    };
    let violations = read().map_err(Error::database("sqlite: foreign_key_check"))?;
    if violations.is_empty() {
        Ok(())
    } else {
        Err(Error::ForeignKeys(violations.join("; ")))
    }
}

fn describe_model(hash: &str) -> String {
    if hash.is_empty() {
        "an empty database".to_string()
    } else {
        format!("model {hash}")
    }
}

fn describe_phase(phase: &str) -> &'static str {
    if phase == PLAN_PHASE_EXPAND || phase == PLAN_PHASE_EXPANDED {
        "its expand steps are done"
    } else {
        "its expand steps are not all done"
    }
}

/// A duration as Go prints one (`1s`, `10ms`, `1m30s`), which is how the
/// Go runner's log lines give a wait and a step's time.
fn go_duration(duration: Duration) -> String {
    let nanos = duration.as_nanos();
    if nanos == 0 {
        return "0s".to_string();
    }
    if nanos < 1_000_000_000 {
        let (unit, scale) = match nanos {
            0..1_000 => ("ns", 1),
            1_000..1_000_000 => ("\u{b5}s", 1_000),
            _ => ("ms", 1_000_000),
        };
        return format!("{}{unit}", decimal(nanos, scale));
    }
    let seconds = duration.as_secs();
    let (hours, minutes) = (seconds / 3_600, seconds % 3_600 / 60);
    let rest = decimal(nanos % 60_000_000_000, 1_000_000_000);
    match (hours, minutes) {
        (0, 0) => format!("{rest}s"),
        (0, _) => format!("{minutes}m{rest}s"),
        _ => format!("{hours}h{minutes}m{rest}s"),
    }
}

/// `value / scale` in decimal, without trailing zeros.
fn decimal(value: u128, scale: u128) -> String {
    let (whole, fraction) = (value / scale, value % scale);
    if fraction == 0 {
        return whole.to_string();
    }
    let width = scale.ilog10() as usize;
    let digits = format!("{fraction:0width$}");
    format!("{whole}.{}", digits.trim_end_matches('0'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_duration_prints_as_go_prints_it() {
        for (duration, want) in [
            (Duration::ZERO, "0s"),
            (Duration::from_nanos(15), "15ns"),
            (Duration::from_micros(1_500), "1.5ms"),
            (Duration::from_micros(250), "250\u{b5}s"),
            (Duration::from_millis(10), "10ms"),
            (Duration::from_secs(1), "1s"),
            (Duration::from_millis(1_234), "1.234s"),
            (Duration::from_secs(16), "16s"),
            (Duration::from_secs(90), "1m30s"),
            (Duration::from_millis(61_250), "1m1.25s"),
            (Duration::from_secs(3_600), "1h0m0s"),
        ] {
            assert_eq!(go_duration(duration), want);
        }
    }
}
