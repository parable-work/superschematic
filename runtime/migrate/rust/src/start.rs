//! The chain start (D116, "Migrations a binary carries"): what a binary
//! that carries its plans does to each database file before it serves a
//! request.
//!
//! [`start`] reads the file's applied model. One that is the last plan's
//! `to` needs nothing. One that a carried plan starts from is taken forward
//! plan by plan, each plan's expand and contract steps back to back, since
//! no server of the previous version reads the file. An empty file starts
//! from the first plan. A file with tables and no state is adopted, when
//! the environment names an adoption and its check answers. Any other file
//! stops the start, and nothing in it is changed.

use std::ffi::OsString;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use rusqlite::backup::{Backup, StepResult};
use rusqlite::types::Value;
use rusqlite::{ffi, Connection};

use crate::error::{refuse, Error};
use crate::plan::{read_model, read_plan, Dialect, Model, Phase, Plan};
use crate::runner::{Runner, Status};
use crate::state::{LOG_TABLE, STATE_TABLE};

/// One adoption of a database (D116): a model, and a statement that says
/// whether a file built by hand holds it.
#[derive(Debug, Clone, Copy)]
pub struct Adoption<'a> {
    /// The model document to record as applied. It is a model a carried
    /// plan starts from, or the one the carried plans end at.
    pub model: &'a [u8],
    /// A statement that reads the file and answers one row holding the
    /// integer 1 when the file holds the model: `SELECT user_version = 7
    /// FROM pragma_user_version`, say.
    pub check: &'a str,
}

/// What [`start`] does beyond applying the carried plans.
#[derive(Default)]
pub struct StartOptions<'a> {
    /// The database's adoption, when the environment's settings name one.
    pub adoption: Option<Adoption<'a>>,
    /// Sets the file's journal mode to WAL before anything is applied.
    /// Without it the journal mode stays as it is.
    pub wal: bool,
    /// Receives the runner's lines: one per step and per change of state.
    pub log: Option<&'a mut dyn FnMut(&str)>,
}

/// One plan a [`start`] applied.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppliedPlan {
    /// The plan's hash.
    pub hash: String,
    /// The steps this start ran, by index. A plan an earlier start left in
    /// progress lists only the steps that were left.
    pub steps: Vec<usize>,
}

/// What a [`start`] did.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Started {
    /// The DB service the carried plans migrate.
    pub service: String,
    /// True when this start recorded the adoption's model.
    pub adopted: bool,
    /// The copy of the file this start made before its first step, when
    /// the plans it applied hold a contract step.
    pub backup: Option<PathBuf>,
    /// The plans this start applied, in order. Empty when the file was
    /// already at the last plan's model.
    pub plans: Vec<AppliedPlan>,
    /// The hash of the model the file is at: the last plan's `to`.
    pub model: String,
}

/// Takes the SQLite file at `file` to the model the last of `plans` ends
/// at, before any request is served (D116). `plans` are the plan documents
/// one database's binary carries, in order: each starts from the model the
/// one before it ends at. A file that does not exist is created.
///
/// It opens a connection of its own with foreign keys on and a busy timeout
/// of five seconds, and decides from the file's state:
///
/// - at the last plan's `to`: nothing to do;
/// - at a model a carried plan starts from, or with a carried plan in
///   progress: every plan from that one on is applied, each step in a
///   transaction, as [`Runner::apply`] runs [`Phase::All`];
/// - no state and no tables: the same, from the first plan;
/// - no state, tables, and an adoption whose check answers one row holding
///   1: the adoption's model is recorded as applied, once, and the chain
///   runs from it;
/// - anything else: [`Error::UnknownModel`], or a refusal for a file with
///   tables that nothing adopts. The file is not changed.
///
/// Before the first step of plans that hold a contract step, it copies the
/// file through SQLite's backup to `<file>.before-<hash>`, where `<hash>`
/// is the applied model the plans start from, and removes every older
/// `<file>.before-*`. It makes no copy of a file with no applied model,
/// which holds nothing to lose, nor when it resumes a plan whose contract
/// has already started, since the copy an earlier start made is the one
/// from before that contract.
///
/// A step that fails stops the start with [`Error::ChainStep`]. The steps
/// before it stay applied, and the next start goes on from it.
pub fn start<P: AsRef<[u8]>>(
    file: impl AsRef<Path>,
    plans: &[P],
    options: StartOptions<'_>,
) -> Result<Started, Error> {
    let file = file.as_ref();
    let chain = read_chain(plans)?;
    let Some(last) = chain.last() else {
        return Err(refuse!(
            "start takes the plans a database's binary carries, and was given none"
        ));
    };
    let adoption = options
        .adoption
        .map(|adoption| read_adoption(adoption, &chain))
        .transpose()?;

    let conn =
        Connection::open(file).map_err(Error::database(format!("open {}", file.display())))?;
    let mut runner = Runner::new(&conn)?;
    if let Some(log) = options.log {
        runner = runner.log(log);
    }
    let status = runner.status(&last.service)?;
    let decision = decide(file, &conn, &chain, last, adoption.as_ref(), &status)?;

    // Every refusal is behind: from here the file changes.
    if options.wal {
        set_wal(&conn, file)?;
    }
    let mut started = Started {
        service: last.service.clone(),
        adopted: false,
        backup: None,
        plans: Vec::new(),
        model: last.to.clone(),
    };
    let mut applied = status.model_hash;
    if let Some(model) = decision.adopt {
        runner.adopt(model)?;
        runner.logf(format_args!(
            "adopted model {} for service {}: the file was built by hand and the adoption's check answered",
            model.hash, model.service
        ));
        started.adopted = true;
        applied.clone_from(&model.hash);
    }
    let Forward::From(begin) = decision.forward else {
        return Ok(started);
    };
    let rest = &chain[begin..];
    if decision.copy && !applied.is_empty() && rest.iter().any(Plan::contracts) {
        let made = backup(&conn, file, &applied)?;
        runner.logf(format_args!(
            "copied {} to {} before a contract step",
            file.display(),
            made.display()
        ));
        started.backup = Some(made);
    }
    for plan in rest {
        let result = runner.apply(plan, Phase::All).map_err(|err| match err {
            Error::Step(step) => Error::ChainStep {
                file: file.to_path_buf(),
                plan: plan.hash.clone(),
                index: step.index,
                op: step
                    .index
                    .checked_sub(1)
                    .and_then(|at| plan.steps.get(at))
                    .map(|failed| failed.op.clone())
                    .unwrap_or_default(),
                subject: step.subject.clone(),
                source: Box::new(step),
            },
            other => other,
        })?;
        if !result.already_applied {
            started.plans.push(AppliedPlan {
                hash: plan.hash.clone(),
                steps: result.ran,
            });
        }
    }
    Ok(started)
}

/// Where a file goes from the model it is at.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Forward {
    /// Nowhere: the file is at the model the carried plans end at.
    Nothing,
    /// Through every carried plan from this one on.
    From(usize),
}

/// The way forward for a file at `model`, or `None` when no carried plan
/// starts from it. A chain may pass a model twice; the last plan that
/// starts from it is the shortest way forward.
fn forward(chain: &[Plan], model: &str) -> Option<Forward> {
    if chain.last().is_some_and(|last| last.to == model) {
        return Some(Forward::Nothing);
    }
    chain
        .iter()
        .rposition(|plan| plan.from == model)
        .map(Forward::From)
}

/// An adoption [`start`] read and held to the chain.
struct Adopting<'a> {
    model: Model,
    check: &'a str,
    /// The way forward from the model.
    forward: Forward,
}

/// What a start does to a file.
struct Decision<'a> {
    /// The model to record as applied first.
    adopt: Option<&'a Model>,
    forward: Forward,
    /// False when the copy an earlier start made is the one to keep.
    copy: bool,
}

/// Decides what a start does from the file's state, and refuses a file the
/// carried plans cannot take forward. It only reads the file.
fn decide<'a>(
    file: &Path,
    conn: &Connection,
    chain: &[Plan],
    last: &Plan,
    adoption: Option<&'a Adopting<'_>>,
    status: &Status,
) -> Result<Decision<'a>, Error> {
    let service = &last.service;
    let unknown = || Error::UnknownModel {
        file: file.to_path_buf(),
        service: service.clone(),
        applied: status.model_hash.clone(),
        in_progress: status.plan_hash.clone(),
        expected: last.to.clone(),
    };
    let from = |forward| Decision {
        adopt: None,
        forward,
        copy: true,
    };

    if let Some(in_progress) = &status.plan_hash {
        // A plan an earlier start left in progress goes on from its step.
        // The last of two equal plans is the shortest way forward. Once a
        // contract step of it has run, the file is no longer what a copy
        // is for.
        let at = chain
            .iter()
            .rposition(|plan| plan.hash == *in_progress)
            .ok_or_else(unknown)?;
        return Ok(Decision {
            copy: !contract_started(status),
            ..from(Forward::From(at))
        });
    }
    if !status.model_hash.is_empty() {
        let forward = forward(chain, &status.model_hash).ok_or_else(unknown)?;
        if forward != Forward::Nothing && status.dialect != Dialect::Sqlite.as_str() {
            return Err(refuse!(
                "service {service}'s state records dialect {}; the plan is for {}",
                status.dialect,
                Dialect::Sqlite
            ));
        }
        return Ok(from(forward));
    }
    // No state. An empty file is an empty database.
    if !holds_objects(conn)? {
        return Ok(from(forward(chain, "").ok_or_else(unknown)?));
    }
    let by_hand = format!(
        "{} holds tables and records no model for service {service}",
        file.display()
    );
    let Some(adoption) = adoption else {
        return Err(refuse!(
            "{by_hand}, and the environment names no adoption for it; the carried plans end at model {}. Nothing was changed",
            last.to
        ));
    };
    if !check_answers(conn, adoption.check)? {
        return Err(refuse!(
            "{by_hand}, and the adoption's check did not answer one row holding 1; the carried plans end at model {}. Nothing was changed",
            last.to
        ));
    }
    Ok(Decision {
        adopt: Some(&adoption.model),
        ..from(adoption.forward)
    })
}

/// Reads the carried plans and refuses a chain the start cannot follow,
/// before the file is opened: a plan that does not read, a plan of another
/// dialect or service, and a plan that does not start from the model the
/// one before it ends at.
fn read_chain<P: AsRef<[u8]>>(plans: &[P]) -> Result<Vec<Plan>, Error> {
    let mut chain: Vec<Plan> = Vec::with_capacity(plans.len());
    for (i, doc) in plans.iter().enumerate() {
        let number = i + 1;
        let plan =
            read_plan(doc.as_ref()).map_err(|err| err.within(&format!("carried plan {number}")))?;
        if plan.dialect != Dialect::Sqlite {
            return Err(refuse!(
                "carried plan {number}: the plan is for {} and the database is {}",
                plan.dialect,
                Dialect::Sqlite
            ));
        }
        if let Some(previous) = chain.last() {
            if plan.service != previous.service {
                return Err(refuse!(
                    "carried plan {number} is for service {}; the plans before it are for service {}",
                    plan.service,
                    previous.service
                ));
            }
            if plan.from != previous.to {
                return Err(refuse!(
                    "carried plan {number} starts from model {}, but the plan before it ends at model {}; each carried plan starts where the one before it ends",
                    plan.from,
                    previous.to
                ));
            }
        }
        chain.push(plan);
    }
    Ok(chain)
}

/// Reads an adoption and refuses one the chain cannot go on from, before
/// the file is opened.
fn read_adoption<'a>(adoption: Adoption<'a>, chain: &[Plan]) -> Result<Adopting<'a>, Error> {
    let model = read_model(adoption.model).map_err(|err| err.within("the adoption's model"))?;
    if let Some(last) = chain.last() {
        if model.dialect != Dialect::Sqlite || model.service != last.service {
            return Err(refuse!(
                "the adoption's model is of {} service {}; the carried plans are for {} service {}",
                model.dialect,
                model.service,
                Dialect::Sqlite,
                last.service
            ));
        }
    }
    let Some(forward) = forward(chain, &model.hash) else {
        return Err(refuse!(
            "the adoption's model {} is no model a carried plan starts from, nor the model the carried plans end at",
            model.hash
        ));
    };
    Ok(Adopting {
        model,
        check: adoption.check,
        forward,
    })
}

/// Reports whether the file holds anything but the runner's state tables.
fn holds_objects(conn: &Connection) -> Result<bool, Error> {
    conn.query_row(
        r"SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name NOT IN (?1, ?2)",
        [STATE_TABLE, LOG_TABLE],
        |row| row.get::<_, i64>(0),
    )
    .map(|objects| objects > 0)
    .map_err(Error::database("read the file's tables"))
}

/// Reports whether a contract step of the plan in progress has a log row.
fn contract_started(status: &Status) -> bool {
    status
        .steps
        .iter()
        .any(|step| step.phase == Phase::Contract.as_str())
}

/// Runs an adoption's check and reports whether it answers exactly one row
/// that holds the integer 1 and nothing else. A check that writes is
/// refused, so a refused start leaves the file as it was, and a check
/// SQLite cannot run on the file is refused with SQLite's reason.
fn check_answers(conn: &Connection, check: &str) -> Result<bool, Error> {
    let refused = |err: rusqlite::Error| refuse!("the adoption's check did not run: {err}");
    let mut statement = conn.prepare(check).map_err(refused)?;
    if !statement.readonly() {
        return Err(refuse!(
            "the adoption's check writes to the file; a check only reads"
        ));
    }
    let columns = statement.column_count();
    let mut rows = statement.query([]).map_err(refused)?;
    let Some(row) = rows.next().map_err(refused)? else {
        return Ok(false);
    };
    let one = columns == 1 && row.get::<_, Value>(0).map_err(refused)? == Value::Integer(1);
    Ok(one && rows.next().map_err(refused)?.is_none())
}

/// Sets the file's journal mode to WAL.
fn set_wal(conn: &Connection, file: &Path) -> Result<(), Error> {
    let mode: String = conn
        .query_row("PRAGMA journal_mode = WAL", [], |row| row.get(0))
        .map_err(Error::database(format!(
            "set the journal mode of {}",
            file.display()
        )))?;
    if mode != "wal" {
        return Err(refuse!(
            "{} keeps journal mode {mode}; SQLite did not set WAL",
            file.display()
        ));
    }
    Ok(())
}

/// Copies the file through SQLite's backup to `<file>.before-<hash>` and
/// removes every other `<file>.before-*`, so the newest copy is the only
/// one. The copy is written beside its name and renamed, so a start that
/// dies while it copies leaves no copy that looks whole.
fn backup(conn: &Connection, file: &Path, hash: &str) -> Result<PathBuf, Error> {
    let name = file
        .file_name()
        .ok_or_else(|| refuse!("{} names no file to copy", file.display()))?;
    let sibling = |suffix: &str| {
        let mut sibling = OsString::from(name);
        sibling.push(suffix);
        file.with_file_name(sibling)
    };
    let copy = sibling(&format!(".before-{hash}"));
    let partial = sibling(&format!(".before-{hash}.partial"));
    let io_error = |doing: String| move |source: io::Error| Error::Io { doing, source };

    match fs::remove_file(&partial) {
        Err(err) if err.kind() != io::ErrorKind::NotFound => {
            return Err(io_error(format!("remove {}", partial.display()))(err));
        }
        _ => {}
    }
    let copying = || format!("copy {} to {}", file.display(), partial.display());
    {
        let mut destination = Connection::open(&partial).map_err(Error::database(copying()))?;
        let pages = Backup::new(conn, &mut destination).map_err(Error::database(copying()))?;
        // One step of every page: the connection's busy timeout waits for
        // a writer, and a lock that outlasts it stops the start.
        match pages.step(-1).map_err(Error::database(copying()))? {
            StepResult::Done => {}
            _ => {
                return Err(Error::database(copying())(rusqlite::Error::SqliteFailure(
                    ffi::Error::new(ffi::SQLITE_BUSY),
                    None,
                )));
            }
        }
    }
    fs::rename(&partial, &copy).map_err(io_error(format!(
        "rename {} to {}",
        partial.display(),
        copy.display()
    )))?;

    let directory = match file.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent,
        _ => Path::new("."),
    };
    let mut prefix = OsString::from(name);
    prefix.push(".before-");
    let listing = || format!("list {}", directory.display());
    for entry in fs::read_dir(directory).map_err(io_error(listing()))? {
        let entry = entry.map_err(io_error(listing()))?;
        let older = entry.file_name();
        if older
            .as_encoded_bytes()
            .starts_with(prefix.as_encoded_bytes())
            && Some(older.as_os_str()) != copy.file_name()
        {
            fs::remove_file(entry.path())
                .map_err(io_error(format!("remove {}", entry.path().display())))?;
        }
    }
    Ok(copy)
}
