//! The core a host runs (D103): the behaviors a project composes and the
//! permission matcher its behaviors ask, and [`Core::run`], a pure function
//! of a type, a command and the answers the host has so far. A run returns
//! the reads it needs, the locks it needs, or its outcome; the host
//! answers and runs again until the outcome, which is a refusal or a change
//! set the host applies.
//!
//! [`Core::drive`] is that loop for a host whose reads return at once (an
//! in-memory host, a test): it asks a [`Source`] for each read and lock and
//! runs again until the outcome. A source that cannot answer a read or take
//! a lock refuses the command, with [`Refusal::internal`] for a failure of
//! its own. A host whose reads are asynchronous writes the loop itself, the
//! same way.

use std::sync::Arc;

use serde::{Deserialize, Serialize};

use crate::behavior::{Behavior, Declaration};
use crate::changes::ChangeSet;
use crate::command::Command;
use crate::erased::{Erased, Typed};
use crate::permissions::{DottedPermissions, PermissionMatcher};
use crate::reads::{Answer, Answers, LockKey, ReadRequest};
use crate::refusal::Refusal;
use crate::run::Run;
use crate::spec::TypeSpec;

/// How many times [`Core::drive`] runs a command before it gives up on a
/// run that keeps asking.
pub const MAX_ROUNDS: usize = 256;

/// A behavior the core runs, with its declaration read once.
pub(crate) struct Registered {
    pub declaration: Declaration,
    pub behavior: Box<dyn Erased>,
}

/// The behaviors a project composes, and the matcher their permission
/// checks ask. It holds no state between runs.
pub struct Core {
    behaviors: Vec<Registered>,
    matcher: Arc<dyn PermissionMatcher>,
}

impl Default for Core {
    fn default() -> Self {
        Core {
            behaviors: Vec::new(),
            matcher: Arc::new(DottedPermissions),
        }
    }
}

impl std::fmt::Debug for Core {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Core")
            .field("behaviors", &self.names())
            .finish_non_exhaustive()
    }
}

impl Core {
    /// A core with no behaviors and the default matcher
    /// ([`DottedPermissions`]).
    pub fn new() -> Self {
        Self::default()
    }

    /// The core with a behavior added. A behavior of a declared name the
    /// core runs already replaces it.
    pub fn with<B: Behavior>(mut self, behavior: B) -> Self {
        self.push(behavior);
        self
    }

    /// Adds a behavior, replacing one of the same declared name.
    pub fn push<B: Behavior>(&mut self, behavior: B) {
        let behavior: Box<dyn Erased> = Box::new(Typed(behavior));
        let declaration = behavior.declaration();
        let registered = Registered {
            declaration,
            behavior,
        };
        match self
            .behaviors
            .iter_mut()
            .find(|known| known.declaration.name == registered.declaration.name)
        {
            Some(known) => *known = registered,
            None => self.behaviors.push(registered),
        }
    }

    /// The core with a permission matcher of a deployment's own in place
    /// of the default.
    pub fn with_matcher(mut self, matcher: impl PermissionMatcher + 'static) -> Self {
        self.matcher = Arc::new(matcher);
        self
    }

    /// The matcher a behavior's `can()` asks.
    pub fn matcher(&self) -> &dyn PermissionMatcher {
        self.matcher.as_ref()
    }

    /// The declared name of each behavior, in the order they were added.
    pub fn names(&self) -> Vec<String> {
        self.behaviors
            .iter()
            .map(|known| known.declaration.name.clone())
            .collect()
    }

    /// A behavior's declaration, by its name.
    pub fn declaration(&self, name: &str) -> Option<&Declaration> {
        self.find(name).map(|known| &known.declaration)
    }

    pub(crate) fn find(&self, name: &str) -> Option<&Registered> {
        self.behaviors
            .iter()
            .find(|known| known.declaration.name == name)
    }

    /// Runs a command on a type with the answers the host has so far. The
    /// same inputs give the same step: the host adds what a step asks for
    /// and runs again, until the step is done.
    pub fn run(&self, spec: &TypeSpec, command: &Command, answers: &Answers) -> Step {
        let uses = match Run::bind(self, spec) {
            Ok(uses) => uses,
            Err(refusal) => return Step::Done(Outcome::Refused(refusal)),
        };
        let run = Run::new(self, spec, command, answers, &uses);
        let result = run.execute();
        run.finish(result)
    }

    /// Runs a command to its outcome, asking a source for every read and
    /// lock: the host loop for reads that return at once. When a step asks
    /// for locks it takes them, and the core asks again for every read
    /// answered before them ([`Answers::get`]), so every read a rule across
    /// rows decides on is made under its locks. A read or a lock the source
    /// refuses refuses the command; a run that is still asking after
    /// [`MAX_ROUNDS`] is refused as `internal`.
    pub fn drive<S: Source + ?Sized>(
        &self,
        spec: &TypeSpec,
        command: &Command,
        source: &mut S,
    ) -> Outcome {
        let mut answers = Answers::new();
        for _ in 0..MAX_ROUNDS {
            match self.run(spec, command, &answers) {
                Step::Done(outcome) => return outcome,
                Step::Read(requests) => {
                    for request in requests {
                        match source.answer(&request) {
                            Ok(answer) => answers.answer(request, answer),
                            Err(refusal) => return Outcome::Refused(refusal),
                        }
                    }
                }
                Step::Lock(keys) => {
                    for key in keys {
                        if let Err(refusal) = source.lock(&key) {
                            return Outcome::Refused(refusal);
                        }
                        answers.hold(key);
                    }
                }
            }
        }
        Outcome::Refused(Refusal::internal(format!(
            "{} of {}: the run still asked for reads or locks after {MAX_ROUNDS} rounds",
            command.action.name(),
            spec.name
        )))
    }
}

/// What answers the core's reads and takes its locks, for [`Core::drive`].
pub trait Source {
    /// The answer to a request, of the variant it asks for, or the refusal
    /// of a request it cannot answer: [`Refusal::internal`] for a kind of
    /// read it does not take (the enum is open) or a read that failed.
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal>;

    /// Takes a lock, held until the command's change set is applied or
    /// dropped, or refuses the command when it cannot (`busy` for a lock
    /// it waited too long on). A host whose one writer holds every lock
    /// takes nothing.
    fn lock(&mut self, key: &LockKey) -> Result<(), Refusal> {
        let _ = key;
        Ok(())
    }
}

/// What one run returns.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Step {
    /// Reads the host answers before it runs the command again.
    Read(Vec<ReadRequest>),
    /// Locks the host takes before it runs the command again, before the
    /// reads they serialize.
    Lock(Vec<LockKey>),
    /// The command's outcome.
    Done(Outcome),
}

impl Step {
    /// The outcome, when the run is done.
    pub fn outcome(&self) -> Option<&Outcome> {
        match self {
            Step::Done(outcome) => Some(outcome),
            _ => None,
        }
    }

    /// The reads the run asks for; empty when it asks for none.
    pub fn reads(&self) -> &[ReadRequest] {
        match self {
            Step::Read(requests) => requests,
            _ => &[],
        }
    }

    /// The locks the run asks for; empty when it asks for none.
    pub fn locks(&self) -> &[LockKey] {
        match self {
            Step::Lock(keys) => keys,
            _ => &[],
        }
    }
}

/// A command's outcome: a refusal, or a change set to apply (boxed, so a
/// step stays small).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Outcome {
    Refused(Refusal),
    Changed(Box<ChangeSet>),
}

impl Outcome {
    pub fn refusal(&self) -> Option<&Refusal> {
        match self {
            Outcome::Refused(refusal) => Some(refusal),
            Outcome::Changed(_) => None,
        }
    }

    pub fn changes(&self) -> Option<&ChangeSet> {
        match self {
            Outcome::Changed(changes) => Some(changes),
            Outcome::Refused(_) => None,
        }
    }

    /// The outcome as a result: the change set, or the refusal.
    pub fn into_result(self) -> Result<ChangeSet, Refusal> {
        match self {
            Outcome::Changed(changes) => Ok(*changes),
            Outcome::Refused(refusal) => Err(refusal),
        }
    }
}
