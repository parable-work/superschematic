//! The handler of a generated router's active grants (#735): the route the
//! loader adds for an API's `@accessGrantsOperations` set,
//! `GET grants/active`, which lists the grants active now through
//! [`active_grants`](crate::access::active_grants), with no method of an
//! implementation, as the TypeScript runtime's `accessGrantsOperation` and
//! the Go server's `accessgrants.go` serve it.
//!
//! The handler decides which of three listers the route's caller is, and
//! lists exactly the grants that one may see:
//!
//! - a caller who holds the declaration's permission (a grant of one that
//!   covers it too) lists every active grant the query keeps: its holder,
//!   target type and target id, each optional;
//! - the owner of the row the query names by its target type and id (the
//!   member that holds its table's owner, its `@audience` owner else its
//!   `createdBy`, read as the system actor) lists the active grants on
//!   that row, the query's holder keeping them further;
//! - a holder lists its own: a query whose holder is the caller's own
//!   subject, its target keeping them further.
//!
//! Any other request is refused 403 before a grant is read, and a request
//! with no caller 401. A grant ended by its date, by its target's state or
//! by a revocation is never listed, and each is answered without its
//! token's hash or its revocation.

use serde::Serialize;
use serde_json::{Map, Value};

use super::ActiveGrantsFilter;
use crate::auth::{covers, Principal};
use crate::error::ApiError;
use crate::orm::{Definition, FindOptions, Values};
use crate::uuid62::parse_uuid;

/// The active grants' route as a generated router carries it: the
/// permission whose holder lists every active grant, and the member that
/// holds the owner of each table's rows, by the table's type.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AccessGrantsOperation {
    pub permission: &'static str,
    pub owners: &'static [(&'static str, &'static str)],
}

impl AccessGrantsOperation {
    /// The route whose administrator holds `permission`, over the tables
    /// whose rows' owners `owners` names.
    pub const fn new(
        permission: &'static str,
        owners: &'static [(&'static str, &'static str)],
    ) -> Self {
        Self { permission, owners }
    }

    /// The member that holds the owner of `target_type`'s rows.
    fn owner_of(&self, target_type: &str) -> Option<&'static str> {
        self.owners
            .iter()
            .find(|(table, _)| *table == target_type)
            .map(|(_, member)| *member)
    }
}

/// What the route decoded of its query: the holder, the target's table
/// type and the target's key, each in its wire form.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AccessGrantsQuery {
    pub holder: Option<String>,
    pub target_type: Option<String>,
    pub target_id: Option<String>,
}

impl AccessGrantsQuery {
    /// The query of the route's arguments, each as the API's types hold
    /// it.
    pub fn new(
        holder: Option<&impl Serialize>,
        target_type: Option<&impl Serialize>,
        target_id: Option<&impl Serialize>,
    ) -> Result<Self, ApiError> {
        Ok(Self {
            holder: text_of(holder)?,
            target_type: text_of(target_type)?,
            target_id: text_of(target_id)?,
        })
    }

    fn filter(&self) -> ActiveGrantsFilter {
        ActiveGrantsFilter {
            holder: self.holder.clone(),
            target_type: self.target_type.clone(),
            target_id: self.target_id.clone(),
            now: None,
        }
    }
}

/// An argument in its wire form: a string its text, any other value its
/// JSON; none for an absent or empty one.
fn text_of(value: Option<&impl Serialize>) -> Result<Option<String>, ApiError> {
    let Some(value) = value else {
        return Ok(None);
    };
    let text = match serde_json::to_value(value) {
        Ok(Value::String(text)) => text,
        Ok(Value::Null) => return Ok(None),
        Ok(other) => other.to_string(),
        Err(err) => {
            return Err(ApiError::internal(format!(
                "the active grants: an argument: {err}"
            )))
        }
    };
    Ok((!text.is_empty()).then_some(text))
}

/// Whether two ids name one principal or row: equal, or one UUID in either
/// of its forms.
fn same_id(a: &str, b: &str) -> bool {
    a == b || matches!((parse_uuid(a), parse_uuid(b)), (Some(x), Some(y)) if x == y)
}

/// The caller, or the 401 of a request with none.
fn caller(principal: Option<&Principal>) -> Result<&Principal, ApiError> {
    match principal {
        Some(principal) if !principal.subject.is_empty() => Ok(principal),
        _ => Err(ApiError::unauthorized("the active grants take a caller")),
    }
}

/// The 403 of a caller who is none of the three listers.
fn refused(operation: &AccessGrantsOperation) -> ApiError {
    ApiError::forbidden(format!(
        "only a holder of {}, the owner of the target a query names, or a holder asking for its own grants lists the active grants",
        operation.permission
    ))
}

/// The owner's member and the read of the target `query` names, when its
/// table names an owner: the table's name and the filter of the row.
fn owned_target(
    operation: &AccessGrantsOperation,
    definition: &Definition,
    query: &AccessGrantsQuery,
) -> Option<(&'static str, String, Values)> {
    let (Some(target_type), Some(target_id)) = (&query.target_type, &query.target_id) else {
        return None;
    };
    let member = operation.owner_of(target_type)?;
    let table = definition.table(target_type).ok()?;
    let mut eq = Map::new();
    eq.insert("eq".to_owned(), Value::String(target_id.clone()));
    let mut filter = Values::new();
    filter.insert(table.key.clone(), Value::Object(eq));
    Some((member, table.name.clone(), filter))
}

/// Whether the target's row, read as `rows`, names `subject` its owner in
/// `member`.
fn owns(rows: &[Values], member: &str, subject: &str) -> bool {
    rows.first()
        .and_then(|row| row.get(member))
        .and_then(Value::as_str)
        .is_some_and(|owner| same_id(owner, subject))
}

/// The options of the target's read: one row, uncounted.
fn one() -> FindOptions {
    FindOptions {
        limit: Some(1),
        skip_count: true,
        ..FindOptions::default()
    }
}

/// The members of a grant the route answers: its AccessGrant view.
const VIEW: [&str; 10] = [
    "id",
    "holder",
    "holderKind",
    "role",
    "targetType",
    "targetId",
    "startsAt",
    "endsAt",
    "endsWhenField",
    "endsWhenState",
];

/// The grants as the route answers them: each its view.
fn views(rows: Vec<Values>) -> Value {
    Value::Array(
        rows.into_iter()
            .map(|row| {
                Value::Object(
                    VIEW.iter()
                        .map(|member| {
                            (
                                (*member).to_owned(),
                                row.get(*member).cloned().unwrap_or(Value::Null),
                            )
                        })
                        .collect(),
                )
            })
            .collect(),
    )
}

/// Whether the caller holds the route's permission.
fn administers(principal: &Principal, operation: &AccessGrantsOperation) -> bool {
    principal
        .permissions
        .iter()
        .any(|granted| covers(granted, operation.permission))
}

/// Whether the query's holder is the caller.
fn holds(principal: &Principal, query: &AccessGrantsQuery) -> bool {
    query
        .holder
        .as_deref()
        .is_some_and(|holder| same_id(holder, &principal.subject))
}

#[cfg(feature = "postgres")]
mod pg {
    use serde_json::Value;

    use super::{
        administers, caller, holds, one, owned_target, owns, refused, views, AccessGrantsOperation,
        AccessGrantsQuery,
    };
    use crate::auth::Principal;
    use crate::error::ApiError;
    use crate::orm::postgres::{Repositories, Tables};

    impl Tables {
        /// Lists the active grants `principal` may see of those `query`
        /// keeps (#735), as the route's AccessGrant views, as JSON.
        pub async fn active_grants_route(
            &self,
            operation: AccessGrantsOperation,
            principal: Option<Principal>,
            query: AccessGrantsQuery,
        ) -> Result<Value, ApiError> {
            let principal = caller(principal.as_ref())?;
            let client = self.pool().get().await.map_err(|err| {
                ApiError::service_unavailable(format!("the database is unreachable: {err}"))
            })?;
            let definition = self.definition()?;
            if !administers(principal, &operation) {
                let mut allowed = holds(principal, &query);
                if !allowed {
                    if let Some((member, table, filter)) =
                        owned_target(&operation, definition, &query)
                    {
                        let system = Repositories::new(&client, definition)?.as_system();
                        let found = system
                            .table(&table)?
                            .find_many(Some(&filter), &one())
                            .await?;
                        allowed = owns(&found.rows, member, &principal.subject);
                    }
                }
                if !allowed {
                    return Err(refused(&operation));
                }
            }
            let rows = crate::access::active_grants(&client, definition, &query.filter()).await?;
            Ok(views(rows))
        }
    }

    // A route of a multi-threaded runtime awaits the handler, so its
    // future is Send.
    #[cfg(test)]
    #[allow(dead_code)]
    fn sends(tables: &Tables, operation: AccessGrantsOperation) {
        fn send<T: Send>(_: T) {}
        send(tables.active_grants_route(operation, None, AccessGrantsQuery::default()));
    }
}

#[cfg(feature = "sqlite")]
mod sqlite {
    use serde_json::Value;

    use super::{
        administers, caller, holds, one, owned_target, owns, refused, views, AccessGrantsOperation,
        AccessGrantsQuery,
    };
    use crate::auth::Principal;
    use crate::error::ApiError;
    use crate::orm::sqlite::{Repositories, Tables};

    impl Tables {
        /// Lists the active grants `principal` may see of those `query`
        /// keeps (#735), as the route's AccessGrant views, as JSON, on a
        /// reading connection of the file.
        pub async fn active_grants_route(
            &self,
            operation: AccessGrantsOperation,
            principal: Option<Principal>,
            query: AccessGrantsQuery,
        ) -> Result<Value, ApiError> {
            let principal = caller(principal.as_ref())?.clone();
            let definition = self.definition()?;
            self.database()
                .read_async(move |connection| -> Result<Value, ApiError> {
                    if !administers(&principal, &operation) {
                        let mut allowed = holds(&principal, &query);
                        if !allowed {
                            if let Some((member, table, filter)) =
                                owned_target(&operation, definition, &query)
                            {
                                let system = Repositories::new(connection, definition)?.as_system();
                                let found =
                                    system.table(&table)?.find_many(Some(&filter), &one())?;
                                allowed = owns(&found.rows, member, &principal.subject);
                            }
                        }
                        if !allowed {
                            return Err(refused(&operation));
                        }
                    }
                    let rows = crate::access::sqlite::active_grants(
                        connection,
                        definition,
                        &query.filter(),
                    )?;
                    Ok(views(rows))
                })
                .await
        }
    }

    // A route of a multi-threaded runtime awaits the handler, so its
    // future is Send.
    #[cfg(test)]
    #[allow(dead_code)]
    fn sends(tables: &Tables, operation: AccessGrantsOperation) {
        fn send<T: Send>(_: T) {}
        send(tables.active_grants_route(operation, None, AccessGrantsQuery::default()));
    }
}

#[cfg(test)]
mod tests {
    use super::{same_id, text_of, AccessGrantsOperation, AccessGrantsQuery};

    #[test]
    fn a_query_reads_each_argument_as_text() {
        let query =
            AccessGrantsQuery::new(Some(&"a"), None::<&String>, Some(&String::new())).unwrap();
        assert_eq!(query.holder.as_deref(), Some("a"));
        assert_eq!(query.target_type, None);
        assert_eq!(query.target_id, None, "an empty argument keeps nothing");
        assert_eq!(text_of(Some(&7)).unwrap().as_deref(), Some("7"));
    }

    #[test]
    fn an_id_is_one_uuid_in_either_form() {
        assert!(same_id(
            "00000000-0000-4000-8000-0000000000e1",
            "00000000-0000-4000-8000-0000000000E1"
        ));
        assert!(!same_id(
            "00000000-0000-4000-8000-0000000000e1",
            "00000000-0000-4000-8000-0000000000e2"
        ));
        assert!(same_id("someone", "someone"));
    }

    #[test]
    fn an_operation_names_the_owner_of_a_table() {
        let operation = AccessGrantsOperation::new("access.admin", &[("Folder", "owner")]);
        assert_eq!(operation.owner_of("Folder"), Some("owner"));
        assert_eq!(operation.owner_of("Contract"), None);
    }
}
