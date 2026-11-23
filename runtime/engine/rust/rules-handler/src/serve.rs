//! The host loop over a [`Store`] that answers at once, which a host in one
//! process runs (the SQLite store, the in-memory one): each request in one
//! transaction, its reads answered, its unit applied whole, run again from
//! fresh state when a fence did not hold, up to [`MAX_ATTEMPTS`] times,
//! then refused as `busy`. The runner runs what the change queue holds and
//! the deadlines due, each a request of its own, as an API's behavior
//! worker runs a table's queues (D103). A host whose store answers later (an
//! asynchronous Postgres client) writes the same loop over its own calls.

use serde_json::Value;
use superschematic_rules::{Caller, Code, Refusal};

use crate::handler::{Context, Done, Handler, HandlerStep, Request};
use crate::store::{Store, StoreAnswers};

/// How many times a request whose rows moved runs before it is refused as
/// busy (D103's bound).
pub const MAX_ATTEMPTS: usize = 8;

/// How many rounds of reads one run of a request makes before it is
/// refused as internal.
pub const MAX_READ_ROUNDS: usize = 256;

/// How many changes and deadlines one call of [`run_due`] runs, the ones
/// the runs queue among them.
pub const MAX_RUNS: usize = 1024;

/// Runs one request to its response over a store, in a transaction of its
/// own: committed when its unit applies (a refused request's unit, a
/// deadline's spend, among them), rolled back otherwise.
pub fn serve<S: Store>(
    handler: &Handler<'_>,
    store: &mut S,
    request: &Request,
    context: &Context,
) -> Result<Value, Refusal> {
    serve_publishing(handler, store, request, context)?.response
}

/// What a request served to its response came to: the response, and the
/// signals its committed unit sent (D77's primitive 2), which the host
/// publishes to its streams; none when it rolled back.
#[derive(Clone, Debug, PartialEq)]
pub struct Published {
    pub response: Result<Value, Refusal>,
    pub signals: Vec<crate::signals::HostSignal>,
}

/// Runs one request as [`serve`] does, and answers the signals its unit
/// sent once the transaction committed: what the host publishes after the
/// commit, the store having kept none of them. A failure of the store
/// itself is the error.
pub fn serve_publishing<S: Store>(
    handler: &Handler<'_>,
    store: &mut S,
    request: &Request,
    context: &Context,
) -> Result<Published, Refusal> {
    for _ in 0..MAX_ATTEMPTS {
        store.begin()?;
        match attempt(handler, store, request, context) {
            Ok(Some((response, signals))) => {
                store.commit()?;
                return Ok(Published { response, signals });
            }
            Ok(None) => store.rollback()?,
            Err(refusal) => {
                store.rollback()?;
                return Err(refusal);
            }
        }
    }
    Err(Refusal::new(
        Code::Busy,
        format!(
            "{}: the rows kept moving while the request ran; it ran {MAX_ATTEMPTS} times and nothing was written, so it may be tried again",
            kind_of(request)
        ),
    ))
}

// attempt runs a request once in the store's transaction: its response and
// the signals its unit sent when its unit applied, `None` when a fence did
// not hold.
#[allow(clippy::type_complexity)]
fn attempt<S: Store>(
    handler: &Handler<'_>,
    store: &mut S,
    request: &Request,
    context: &Context,
) -> Result<Option<(Result<Value, Refusal>, Vec<crate::signals::HostSignal>)>, Refusal> {
    let mut answers = StoreAnswers::new();
    for _ in 0..MAX_READ_ROUNDS {
        match handler.run(request, context, &answers) {
            HandlerStep::Read(reads) => {
                for read in reads {
                    let answer = store.read(&read)?;
                    answers.answer(read, answer);
                }
            }
            HandlerStep::Done(Done { response, unit }) => {
                if unit.is_empty() || store.apply(&unit)? {
                    return Ok(Some((response, unit.signals)));
                }
                return Ok(None);
            }
        }
    }
    Err(Refusal::internal(format!(
        "{}: the request still asked for reads after {MAX_READ_ROUNDS} rounds",
        kind_of(request)
    )))
}

fn kind_of(request: &Request) -> String {
    serde_json::to_value(request)
        .ok()
        .and_then(|value| value.get("kind").and_then(Value::as_str).map(str::to_owned))
        .unwrap_or_default()
}

/// One change or deadline the runner ran, what it came to, and the signals
/// it sent, which the host publishes.
#[derive(Clone, Debug, PartialEq)]
pub struct Ran {
    pub request: Request,
    pub outcome: Result<Value, Refusal>,
    pub signals: Vec<crate::signals::HostSignal>,
}

// served is a runner's request served, its signals beside its outcome.
fn served<S: Store>(
    handler: &Handler<'_>,
    store: &mut S,
    request: Request,
    context: &Context,
) -> Ran {
    let (outcome, signals) = match serve_publishing(handler, store, &request, context) {
        Ok(Published { response, signals }) => (response, signals),
        Err(refusal) => (Err(refusal), Vec::new()),
    };
    Ran {
        request,
        outcome,
        signals,
    }
}

/// Runs what the change queue holds, first in first out, then the deadlines
/// due at `now`, earliest first, then the changes those queued, and on,
/// until neither holds any or [`MAX_RUNS`] have run: each a request of its
/// own as `caller`, at `now`, with a request id and a UUID from `fresh`.
pub fn run_due<S: Store>(
    handler: &Handler<'_>,
    store: &mut S,
    now: i64,
    caller: &Caller,
    fresh: &mut dyn FnMut() -> (String, u128),
) -> Result<Vec<Ran>, Refusal> {
    let mut ran: Vec<Ran> = Vec::new();
    let context = |fresh: &mut dyn FnMut() -> (String, u128)| {
        let (request, id) = fresh();
        Context {
            caller: caller.clone(),
            now,
            request,
            id,
        }
    };
    loop {
        let mut progressed = false;
        loop {
            let queued = store.queued()?;
            let Some(first) = queued.into_iter().next() else {
                break;
            };
            if ran.len() >= MAX_RUNS {
                return Ok(ran);
            }
            let request = Request::Change {
                type_name: first.type_name,
                id: first.id,
                change: first.change,
                seq: Some(first.seq),
            };
            ran.push(served(handler, store, request, &context(fresh)));
            progressed = true;
        }
        for due in store.due(now)? {
            if ran.len() >= MAX_RUNS {
                return Ok(ran);
            }
            let request = Request::Deadline {
                type_name: due.type_name,
                id: due.id,
                use_id: due.use_id,
                name: due.name,
                at: due.at,
                payload: due.payload,
            };
            ran.push(served(handler, store, request, &context(fresh)));
            progressed = true;
        }
        if !progressed || store.queued()?.is_empty() {
            return Ok(ran);
        }
    }
}
