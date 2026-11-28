//! Agent orchestration (D158, D183, D193): agents are rows, each run gets a
//! fresh sandbox instance and a token held to its task, and autonomy is set
//! per operation, with budgets and stop switches. The TypeScript runtime's
//! `agents.ts`, with its tables, statements, states and refusals.
//!
//! The tables are the database's classes marked `@agents("<part>")`, whose
//! fields the loader fills: the definitions (a `@versioned` table, so a run
//! records the version it ran), the runs, the dispatcher's request queue,
//! the autonomy policies, the approvals an ask writes, and the controls (one
//! row per agent and one for every agent: the stop switch, and a Budget
//! whose meter each agent's usage counts in, and every agent's through its
//! scope).
//!
//! Triggers. A run request is a message of the request queue: a route
//! naming an agent (the notifier's `{ agent }` recipient, D153), a row
//! assigned to an agent, a schedule, a principal asking, or an inbound
//! message from a party (D157), `{ trigger: "inbound", party, message,
//! grant }`, which names no agent: the definition whose `inbound` is set
//! takes it. A principal of kind agent may not request a run: only the
//! dispatcher starts one.
//!
//! The dispatcher ([`Agents`]) is the queue's worker. It claims a request,
//! refuses it while the agent or every agent is paused or over its budget,
//! writes the run row, holds the run's Lease (D104) while its instance
//! runs, mints the run's token (a grant per target row, D156, held by the
//! run, ended when the run ends), and starts a sandbox instance (D159) with
//! the definition, the token, the endpoint's URL and the credential
//! references, never their values. A failure to start is recorded with
//! Retries (D104), whose backoff sets when the start is tried again. A
//! watch message polls the instance until it ends; the dispatcher then
//! revokes the token, meters the instance's cost, writes the redacted
//! transcript and records the outcome. The outcome, the cost and the
//! transcript go in before the status moves, so a reader that sees a run
//! ended also sees its outcome.
//!
//! The run's principal ([`Agents::principal_of`]) is kind agent, acting for
//! whom the run acts for, holding the agent's role's permissions narrowed
//! to those whom it acts for holds ([`narrow_permissions`]), and reaching
//! the run's targets and the rows the role's paths reach from them. A run
//! acting for a party reaches through only the paths the party's grant of
//! the same target reaches too ([`narrow_reaches`], D183), read on every
//! call.
//!
//! Trust. A run an inbound message started records the message's trust
//! ([`trust_of`]): verified, or entered for a sender entered by hand or a
//! message the dispatcher could not read. An entered sender's run reads as
//! its policy says, and every other operation asks, as a money movement
//! always does ([`effective_autonomy`], D183).
//!
//! Autonomy. Each tool call passes [`Agents::gate`]: the most specific
//! policy row for the agent and the operation or its class says auto, tell
//! or ask ([`best_policy`]), else the tool's mode, else the class's default
//! ([`default_autonomy`]). ask writes an approval holding the exact
//! parameters and their hash ([`params_hash`]), raises
//! `approval.requested` (urgent) on it, and answers 428
//! `approval_required`; the same call runs once an approval of the same
//! hash is approved, and spends it. A call with other parameters
//! supersedes the run's open approvals of the operation and asks again.
//!
//! Budgets. Each turn's usage counts on the agent's control row's Budget
//! meter, which counts in every agent's too. A warning event is raised as
//! usage crosses the row's `warnAt` percent, and at the agent's cap the
//! agent pauses ([`budget_crossing`]); at every agent's cap a scheduled run
//! is refused and a principal's run waits for an approval.
//!
//! Transcripts are redacted as each entry is written ([`redact_text`]: the
//! run's token, bearer tokens, and the app's own patterns), kept on the run
//! row while it runs and in the bucket after. Each turn replays the run's
//! earlier completions as assistant messages with the tool calls they made
//! and the provider's own state of them, which a completion keeps
//! unredacted while the run lives.
//!
//! The parts here are pure and shared with the TypeScript runtime through
//! `runtime/http/testdata/agents_parity.json`. The dispatcher runs over
//! [`Store`](super::store::Store), on Postgres or on a SQLite file (#618),
//! where a run's token is admitted by the SQLite grants' authenticator as
//! by the Postgres one, and its request queue claims on the file.

#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod dispatcher;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod queue;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod routes;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
mod tools;

#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use dispatcher::{
    AgentRunRequest, AgentSandboxTool, AgentTool, AgentToolContext, AgentToolRun, Agents,
    AgentsError, AskDeadline, AskThread, Clock, FollowUp, GateOutcome, OnReturn, PrincipalRef,
    RunAuthenticator, SandboxExtra, SandboxSpecOf, TranscriptEntry, TranscriptStore,
};
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use queue::AgentsQueue;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub use routes::AgentRoutes;

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::auth::covers;
use crate::mcp::canonical_json;

/// The classes of operation an autonomy policy names (D158).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum OperationClass {
    /// A read.
    Read,
    /// A write of the app's own rows.
    InternalWrite,
    /// A message to a party (D157).
    MessageParty,
    /// A movement of money, which is ask whatever a row says.
    MoneyMovement,
}

impl OperationClass {
    /// The class's wire name.
    pub fn as_str(self) -> &'static str {
        match self {
            OperationClass::Read => "read",
            OperationClass::InternalWrite => "internalWrite",
            OperationClass::MessageParty => "messageParty",
            OperationClass::MoneyMovement => "moneyMovement",
        }
    }

    /// The class of a wire name, or none.
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "read" => Some(OperationClass::Read),
            "internalWrite" => Some(OperationClass::InternalWrite),
            "messageParty" => Some(OperationClass::MessageParty),
            "moneyMovement" => Some(OperationClass::MoneyMovement),
            _ => None,
        }
    }
}

/// The autonomy an agent has over an operation.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Autonomy {
    /// The call runs.
    Auto,
    /// The call runs, and an `agent.acted` event tells of it.
    Tell,
    /// The call waits for an approval.
    Ask,
}

impl Autonomy {
    /// The mode's wire name.
    pub fn as_str(self) -> &'static str {
        match self {
            Autonomy::Auto => "auto",
            Autonomy::Tell => "tell",
            Autonomy::Ask => "ask",
        }
    }

    /// The mode of a wire name, or none.
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "auto" => Some(Autonomy::Auto),
            "tell" => Some(Autonomy::Tell),
            "ask" => Some(Autonomy::Ask),
            _ => None,
        }
    }
}

/// The trust of an inbound message's run (D157, D183): verified for a
/// message its row records as from a verified identity, and entered for
/// anything else.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum RunTrust {
    Verified,
    Entered,
}

impl RunTrust {
    /// The trust's wire name.
    pub fn as_str(self) -> &'static str {
        match self {
            RunTrust::Verified => "verified",
            RunTrust::Entered => "entered",
        }
    }
}

/// The permissions of administering agents (D158).
pub mod permissions {
    /// Writing and editing a definition.
    pub const DEFINE: &str = "agents.define";
    /// Writing an autonomy policy.
    pub const AUTONOMY: &str = "agents.autonomy";
    /// Setting a budget, and deciding a run past every agent's.
    pub const BUDGETS: &str = "agents.budgets";
    /// Changing a definition's credential references.
    pub const CREDENTIALS: &str = "agents.credentials";
    /// Pausing and resuming agents.
    pub const PAUSE: &str = "agents.pause";
    /// Deciding an approval, unless its policy names another.
    pub const APPROVE: &str = "agents.approve";
}

/// How long a transcript is kept after its run ends, in days.
pub const DEFAULT_TRANSCRIPT_RETENTION_DAYS: u64 = 30;

/// The default autonomy of a class: a read and an internal write run; a
/// message to a party and a money movement ask.
pub fn default_autonomy(class: OperationClass) -> Autonomy {
    match class {
        OperationClass::Read | OperationClass::InternalWrite => Autonomy::Auto,
        OperationClass::MessageParty | OperationClass::MoneyMovement => Autonomy::Ask,
    }
}

/// The run's trust of a message's recorded trust: verified only when the
/// row says so.
pub fn trust_of(recorded: Option<&str>) -> RunTrust {
    if recorded == Some("verified") {
        RunTrust::Verified
    } else {
        RunTrust::Entered
    }
}

/// The autonomy a run has over an operation of `class` (D158, D183): the
/// policy's mode, but a money movement asks whatever it says, and so does
/// every operation but a read of a run whose sender was entered by hand.
pub fn effective_autonomy(
    mode: Autonomy,
    class: OperationClass,
    trust: Option<RunTrust>,
) -> Autonomy {
    if class == OperationClass::MoneyMovement {
        return Autonomy::Ask;
    }
    if trust == Some(RunTrust::Entered) && class != OperationClass::Read {
        return Autonomy::Ask;
    }
    mode
}

/// The relation paths a run reaches from a target it holds, narrowed to
/// those of the party it acts for (D183): the role's paths the party's
/// grants of the same target also reach, by type, each once in the role's
/// order. A target no grant of the party names gives no path: the run
/// reaches the row and nothing through it.
pub fn narrow_reaches(
    role: &BTreeMap<String, Vec<String>>,
    party: &[BTreeMap<String, Vec<String>>],
) -> BTreeMap<String, Vec<String>> {
    let mut out = BTreeMap::new();
    for (ty, paths) in role {
        let mut kept: Vec<String> = Vec::new();
        for path in paths {
            let reached = party
                .iter()
                .any(|reach| reach.get(ty).is_some_and(|p| p.contains(path)));
            if reached && !kept.contains(path) {
                kept.push(path.clone());
            }
        }
        if !kept.is_empty() {
            out.insert(ty.clone(), kept);
        }
    }
    out
}

/// The agent's permissions narrowed to those whom it acts for holds: each
/// of the agent's that one of the actor's covers, then each of the actor's
/// that one of the agent's covers, the narrower of a pair, each once.
pub fn narrow_permissions(agent: &[String], actor: &[String]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut add = |p: &String| {
        if !out.contains(p) {
            out.push(p.clone());
        }
    };
    for p in agent {
        if actor.iter().any(|q| covers(q, p)) {
            add(p);
        }
    }
    for q in actor {
        if agent.iter().any(|p| covers(p, q)) {
            add(q);
        }
    }
    out
}

/// The SHA-256, in hex, of an operation and its parameters as canonical
/// JSON: what an approval is bound to.
pub fn params_hash(operation: &str, params: &Value) -> String {
    let text = canonical_json(&json!({ "operation": operation, "params": params }));
    let digest = ring::digest::digest(&ring::digest::SHA256, text.as_bytes());
    digest.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// An autonomy policy row as [`best_policy`] reads it.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyRow {
    /// The agent it is of; none for every agent.
    #[serde(default)]
    pub agent: Option<String>,
    /// The operation it names; none for a class.
    #[serde(default)]
    pub operation: Option<String>,
    /// The class it names; none for an operation.
    #[serde(default)]
    pub operation_class: Option<String>,
    pub mode: String,
    /// The permission an approver of an ask holds.
    #[serde(default)]
    pub permission: Option<String>,
}

fn policy_score(row: &PolicyRow, operation: &str, class: OperationClass) -> i32 {
    let agent = if row.agent.as_deref().is_some_and(|a| !a.is_empty()) {
        4
    } else {
        0
    };
    let named = match row.operation.as_deref().filter(|o| !o.is_empty()) {
        Some(named) if named == operation => 2,
        Some(_) => -100,
        None if row.operation_class.as_deref() == Some(class.as_str()) => 1,
        None => -100,
    };
    agent + named
}

/// The most specific of `rows`, the policies of an agent and of every
/// agent, for `operation` of `class`: one of the agent's over one of every
/// agent's, one naming the operation over one naming its class, the first
/// of equals in the order given; none when no row names either.
pub fn best_policy<'r>(
    rows: &'r [PolicyRow],
    operation: &str,
    class: OperationClass,
) -> Option<&'r PolicyRow> {
    let mut best: Option<(&PolicyRow, i32)> = None;
    for row in rows {
        let score = policy_score(row, operation, class);
        if score < 0 {
            continue;
        }
        if best.is_none_or(|(_, held)| score > held) {
            best = Some((row, score));
        }
    }
    best.map(|(row, _)| row)
}

/// What a use of a budget crossed (D158): whether it crossed the warning
/// threshold, `warn_at` percent of `limit`, and whether it reached the cap.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct BudgetCrossing {
    pub warns: bool,
    pub capped: bool,
}

/// What a use that took the meter from `before` to `after` crossed of a
/// budget of `limit` (none for no limit) that warns at `warn_at` percent:
/// a warning as it crosses the threshold, once, and the cap at the limit.
pub fn budget_crossing(
    before: i64,
    after: i64,
    limit: Option<i64>,
    warn_at: i64,
) -> BudgetCrossing {
    let Some(limit) = limit else {
        return BudgetCrossing::default();
    };
    let (before, after, limit, warn_at) = (
        i128::from(before),
        i128::from(after),
        i128::from(limit),
        i128::from(warn_at),
    );
    BudgetCrossing {
        warns: limit > 0 && before * 100 < warn_at * limit && after * 100 >= warn_at * limit,
        capped: after >= limit,
    }
}

/// What a redaction writes in place of what it takes out.
pub const REDACTED: &str = "[redacted]";

fn token_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '_' | '-')
}

fn bearer_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '~' | '+' | '/' | '=' | '-')
}

/// `text` with every grant's token (`grant_` and at least 16 of its
/// characters) taken out.
fn redact_grants(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    while let Some(at) = rest.find("grant_") {
        let after = &rest[at + "grant_".len()..];
        let run: usize = after
            .chars()
            .take_while(|c| token_char(*c))
            .map(char::len_utf8)
            .sum();
        out.push_str(&rest[..at]);
        if after[..run].chars().count() >= 16 {
            out.push_str(REDACTED);
            rest = &after[run..];
        } else {
            out.push_str("grant_");
            rest = after;
        }
    }
    out.push_str(rest);
    out
}

/// `text` with every bearer credential (`Bearer`, white space, and a
/// token) taken out.
fn redact_bearers(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    while let Some(at) = rest.find("Bearer") {
        let after = &rest[at + "Bearer".len()..];
        let space: usize = after
            .chars()
            .take_while(|c| c.is_whitespace())
            .map(char::len_utf8)
            .sum();
        let token: usize = after[space..]
            .chars()
            .take_while(|c| bearer_char(*c))
            .map(char::len_utf8)
            .sum();
        out.push_str(&rest[..at]);
        if space > 0 && token > 0 {
            out.push_str(REDACTED);
            rest = &after[space + token..];
        } else {
            out.push_str("Bearer");
            rest = after;
        }
    }
    out.push_str(rest);
    out
}

/// `text` redacted as a transcript entry is (D158): each of `secrets`
/// taken out where it appears, then every grant's token (`grant_` and at
/// least 16 of `[A-Za-z0-9_-]`), then every bearer credential (`Bearer`,
/// white space and `[A-Za-z0-9._~+/=-]+`), each replaced by
/// [`REDACTED`].
pub fn redact_text(text: &str, secrets: &[String]) -> String {
    let mut out = text.to_owned();
    for secret in secrets.iter().filter(|s| !s.is_empty()) {
        out = out.replace(secret.as_str(), REDACTED);
    }
    redact_bearers(&redact_grants(&out))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(value: &Value) -> Vec<String> {
        value
            .as_array()
            .map(|items| {
                items
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default()
    }

    fn reaches(value: &Value) -> BTreeMap<String, Vec<String>> {
        value
            .as_object()
            .map(|members| {
                members
                    .iter()
                    .map(|(ty, paths)| (ty.clone(), strings(paths)))
                    .collect()
            })
            .unwrap_or_default()
    }

    /// The vectors the TypeScript runtime's agents-parity test writes
    /// (`AGENTS_PARITY_WRITE=1 bun test src/agents-parity.test.ts`).
    #[test]
    fn the_parity_vectors_hold() {
        let text = include_str!("../../../../testdata/agents_parity.json");
        let vectors: Value = serde_json::from_str(text).unwrap();
        for case in vectors["defaultAutonomy"].as_array().unwrap() {
            let class = OperationClass::parse(case["class"].as_str().unwrap()).unwrap();
            assert_eq!(
                default_autonomy(class).as_str(),
                case["want"].as_str().unwrap(),
                "default {case}"
            );
        }
        for case in vectors["trustOf"].as_array().unwrap() {
            assert_eq!(
                trust_of(case["recorded"].as_str()).as_str(),
                case["want"].as_str().unwrap(),
                "trust {case}"
            );
        }
        for case in vectors["effectiveAutonomy"].as_array().unwrap() {
            let mode = Autonomy::parse(case["mode"].as_str().unwrap()).unwrap();
            let class = OperationClass::parse(case["class"].as_str().unwrap()).unwrap();
            let trust = case["trust"].as_str().map(|t| trust_of(Some(t)));
            assert_eq!(
                effective_autonomy(mode, class, trust).as_str(),
                case["want"].as_str().unwrap(),
                "autonomy {case}"
            );
        }
        for case in vectors["narrowReaches"].as_array().unwrap() {
            let party: Vec<_> = case["party"]
                .as_array()
                .unwrap()
                .iter()
                .map(reaches)
                .collect();
            assert_eq!(
                narrow_reaches(&reaches(&case["role"]), &party),
                reaches(&case["want"]),
                "reaches {case}"
            );
        }
        for case in vectors["narrowPermissions"].as_array().unwrap() {
            assert_eq!(
                narrow_permissions(&strings(&case["agent"]), &strings(&case["actor"])),
                strings(&case["want"]),
                "permissions {case}"
            );
        }
        for case in vectors["paramsHash"].as_array().unwrap() {
            assert_eq!(
                params_hash(case["operation"].as_str().unwrap(), &case["params"]),
                case["want"].as_str().unwrap(),
                "hash {case}"
            );
        }
        for case in vectors["bestPolicy"].as_array().unwrap() {
            let rows: Vec<PolicyRow> = serde_json::from_value(case["rows"].clone()).unwrap();
            let class = OperationClass::parse(case["class"].as_str().unwrap()).unwrap();
            let best = best_policy(&rows, case["operation"].as_str().unwrap(), class)
                .map(|row| rows.iter().position(|r| std::ptr::eq(r, row)).unwrap());
            assert_eq!(
                best.map(|i| i as u64),
                case["want"].as_u64(),
                "policy {case}"
            );
        }
        for case in vectors["budgetCrossing"].as_array().unwrap() {
            let got = budget_crossing(
                case["before"].as_i64().unwrap(),
                case["after"].as_i64().unwrap(),
                case["limit"].as_i64(),
                case["warnAt"].as_i64().unwrap(),
            );
            assert_eq!(got.warns, case["warns"].as_bool().unwrap(), "budget {case}");
            assert_eq!(
                got.capped,
                case["capped"].as_bool().unwrap(),
                "budget {case}"
            );
        }
        for case in vectors["redactText"].as_array().unwrap() {
            assert_eq!(
                redact_text(case["text"].as_str().unwrap(), &strings(&case["secrets"])),
                case["want"].as_str().unwrap(),
                "redact {case}"
            );
        }
    }
}
