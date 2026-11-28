//! What a reply's verb runs (D153): a reply action the app gives per event
//! kind, and the approve and deny of an approval a route sends to those
//! who hold its permission.

use std::sync::Arc;

use async_trait::async_trait;
use serde_json::{json, Value};

use super::super::store::{PgClient, StoreRepositories, TxRepositories};
use super::super::{rfc3339_millis, FindOptions, Values, WriteOptions};
use crate::auth::{covers, Principal, PrincipalKind};
use crate::BoxError;

/// What a reply action is given.
pub struct ReplyContext<'a, 't> {
    /// The repositories of the transaction it runs in, acting as the
    /// reply's principal: its writes keep to what the principal sees
    /// (D156), and its behaviors' commands run with the reply's
    /// permissions (D103).
    pub repositories: &'a TxRepositories<'a, 't>,
    /// The notice it answers, a row of the inbox.
    pub notice: &'a Values,
    /// The reply's principal, with the permissions the channel's trust
    /// leaves it.
    pub principal: &'a Principal,
    /// The words after the verb and the reference.
    pub args: &'a [String],
    /// The channel kind it came on.
    pub channel: &'a str,
    /// The time the notifier reads.
    pub now: std::time::SystemTime,
}

impl ReplyContext<'_, '_> {
    /// The key of the record the notice is about, as text.
    pub fn subject_key(&self) -> Option<&str> {
        self.notice.get("subjectKey").and_then(Value::as_str)
    }
}

/// What a reply's verb runs (D153): the permission it needs, what it does,
/// and how it is undone. The TypeScript runtime's `ReplyAction`.
#[async_trait]
pub trait ReplyAction: Send + Sync {
    /// The permission the operation's route requires, which the reply's
    /// principal must hold within its channel's trust; none for an action
    /// any principal of the notice may run.
    fn permission(&self) -> Option<&str>;

    /// What the echo says was done: `Marked done`; `Done: <verb>` unless
    /// set.
    fn describe(&self) -> Option<&str> {
        None
    }

    /// Runs the action; what it answers is kept for its undo.
    async fn run(&self, ctx: &ReplyContext<'_, '_>) -> Result<Value, BoxError>;

    /// Whether the action can be undone; one that cannot says so in its
    /// echo.
    fn undoable(&self) -> bool {
        false
    }

    /// Undoes it with what `run` answered.
    async fn undo(&self, ctx: &ReplyContext<'_, '_>, data: &Value) -> Result<(), BoxError> {
        let _ = (ctx, data);
        Err("this reply action cannot be undone".into())
    }
}

/// The verbs `approve` and `deny` of an approval's notice (D153, section
/// 8): each decides the approval the notice is about, a row of `table`,
/// as the replier, who must hold the approval's own permission (its
/// `permission` member) within the channel's trust, and may not be an
/// agent run; a decided approval is refused. It writes the row's `state`
/// (`approved` or `denied`), `decidedBy`, `decidedVia` (the channel) and
/// `decidedAt`, the members of D158's approvals. `permission` is the one a
/// reply needs to run either verb at all (`agents.approve`). Give the pair
/// for the event kind `approval.requested`.
pub fn approval_replies(
    table: impl Into<String>,
    permission: impl Into<String>,
) -> [(&'static str, Arc<dyn ReplyAction>); 2] {
    let table = table.into();
    let permission = permission.into();
    [
        (
            "approve",
            Arc::new(Decide {
                table: table.clone(),
                permission: permission.clone(),
                state: "approved",
                describe: "Approved",
            }),
        ),
        (
            "deny",
            Arc::new(Decide {
                table,
                permission,
                state: "denied",
                describe: "Denied",
            }),
        ),
    ]
}

struct Decide {
    table: String,
    permission: String,
    state: &'static str,
    describe: &'static str,
}

#[async_trait]
impl ReplyAction for Decide {
    fn permission(&self) -> Option<&str> {
        Some(&self.permission)
    }

    fn describe(&self) -> Option<&str> {
        Some(self.describe)
    }

    async fn run(&self, ctx: &ReplyContext<'_, '_>) -> Result<Value, BoxError> {
        let key = ctx
            .subject_key()
            .ok_or("the notice is about no approval")?
            .to_owned();
        // The approval is read and written as the system: the replier's
        // right to decide it is its permission, not its audience.
        let system = ctx
            .repositories
            .db()
            .repositories(ctx.repositories.definition())?
            .as_system();
        let approvals = system.table(&self.table)?;
        let mut by_key = Values::new();
        by_key.insert(approvals_key(&system, &self.table)?, json!({ "eq": key }));
        let approval = approvals
            .find_one(Some(&by_key), &FindOptions::default())
            .await?
            .ok_or("no such approval")?;
        let needs = approval
            .get("permission")
            .and_then(Value::as_str)
            .unwrap_or(&self.permission)
            .to_owned();
        if !ctx.principal.permissions.iter().any(|p| covers(p, &needs)) {
            return Err(format!("approving needs {needs}").into());
        }
        if ctx.principal.kind == PrincipalKind::Agent {
            return Err("an agent run may not decide an approval".into());
        }
        let state = approval
            .get("state")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if state != "pending" {
            return Err(format!("the approval is {state}").into());
        }
        let mut update = Values::new();
        update.insert("state".to_owned(), json!(self.state));
        update.insert("decidedBy".to_owned(), json!(ctx.principal.subject));
        update.insert("decidedVia".to_owned(), json!(ctx.channel));
        update.insert("decidedAt".to_owned(), json!(rfc3339_millis(ctx.now)));
        approvals
            .update_one(
                &Value::String(key.clone()),
                &update,
                &WriteOptions::default(),
            )
            .await?;
        Ok(json!({ "approval": key }))
    }
}

fn approvals_key<C: PgClient>(
    repositories: &StoreRepositories<'_, C>,
    table: &str,
) -> Result<String, BoxError> {
    Ok(repositories.definition().table(table)?.key.clone())
}
