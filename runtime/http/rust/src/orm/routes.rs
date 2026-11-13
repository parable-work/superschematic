//! The handlers of a `@tableOperation` (D106, D116): a read, a list, a
//! create, an update and a delete of a table's rows through its
//! repository, as the TypeScript runtime's table routes serve them, with
//! the path's parents, the row rule (D108), a create's start state (D109)
//! and a list's filter (D110, D112) held beside each. A table that
//! composes behaviors is written through the behaviors' core (D103), as
//! the route's caller with the permissions it holds, and the core's
//! refusal is the problem of its status, code, message and details.
//!
//! A generated router carries each operation as data
//! ([`TableOperation::parse`]) and hands its handler what the route
//! decoded ([`TableRequest`]). The handler runs in one transaction of the
//! repositories, as the route's caller, and answers the row's view, or a
//! page of views.

use serde::Deserialize;
use serde_json::{Map, Value};

use super::run::{Client, Runner};
use super::{
    Column, Constraint, ConstraintKind, Error, PageOptions, Refusal, Table, Values, WriteOptions,
};
use crate::auth::{covers, Principal};
use crate::error::ApiError;
use crate::page::{refused_cursor, Cursor, Page, PageRequest};
use crate::uuid62::parse_uuid;

/// What a table operation does, which its HTTP method and shape decide.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    /// The view of one row.
    Read,
    /// A page of rows' views.
    List,
    /// A new row.
    Create,
    /// The members a body sets, on one row.
    Update,
    /// One row deleted, or stamped deleted.
    Delete,
}

/// A table operation as a generated router carries it (D106): what the
/// loader filled of the operation's `@tableOperation`, and the members of
/// its views, with the members of the TypeScript `TableOperation`.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TableOperation {
    /// What the operation does.
    pub kind: Kind,
    /// The table, by its type: what a refusal names.
    #[serde(rename = "type")]
    pub table: String,
    /// The table's key: the path parameter of a read, an update and a
    /// delete, and the row's member.
    #[serde(default)]
    pub key: Option<String>,
    /// The path parameters beside the key, each the key of one of the
    /// table's to-one relations and the row's member that holds it.
    #[serde(default)]
    pub parents: Vec<String>,
    /// The members of the result's view, each the row's member of the
    /// same name.
    pub view: Vec<String>,
    /// The members of a create's and an update's body, each the row's
    /// member it sets.
    #[serde(default)]
    pub body: Vec<String>,
    /// A list's search argument (D62), by its query parameter.
    #[serde(default)]
    pub search: Option<String>,
    /// The row rule (D108): the row's members that name who may reach it.
    #[serde(default)]
    pub by: Vec<String>,
    /// The permission whose holder the row rule does not hold back.
    #[serde(default)]
    pub override_permission: Option<String>,
    /// The members of a create's body that name the state the row starts
    /// in (D109).
    #[serde(default)]
    pub starts: Vec<String>,
}

impl TableOperation {
    /// Reads an operation from the JSON a generated router holds.
    pub fn parse(json: &str) -> Result<Self, Error> {
        serde_json::from_str(json)
            .map_err(|err| Error::Definition(format!("a table operation's definition: {err}")))
    }

    /// Whether the operation writes, and so runs on the writing side of
    /// its database.
    pub fn writes(&self) -> bool {
        !matches!(self.kind, Kind::Read | Kind::List)
    }
}

/// What a table operation's route decoded of its request.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TableRequest {
    /// The path's parameters by name, each in its wire form.
    pub path: Values,
    /// The body, as the operation's input type's parser read it: a member
    /// the request left out is absent, and one it sent as null is null.
    pub input: Option<Value>,
    /// A list's search text, when the request holds one.
    pub search: Option<String>,
    /// A list's filter, as the repository reads it: each field's member
    /// the row's member of the same name.
    pub filter: Values,
    /// A list's page.
    pub page: Option<PageRequest>,
}

impl TableRequest {
    /// Holds the path parameter `name` at `value`, in its wire form.
    pub fn path_arg<T: serde::Serialize + ?Sized>(
        &mut self,
        name: &str,
        value: &T,
    ) -> Result<(), ApiError> {
        self.path.insert(name.to_owned(), super::wire(value)?);
        Ok(())
    }

    /// Filters the rows by `filter` of the member `name`, when the request
    /// holds one.
    pub fn filter_by<F: serde::Serialize>(
        &mut self,
        name: &str,
        filter: Option<F>,
    ) -> Result<(), ApiError> {
        if let Some(filter) = filter {
            self.filter.insert(name.to_owned(), super::wire(&filter)?);
        }
        Ok(())
    }
}

/// What a table operation answers: a row's view, or a page of them.
#[derive(Clone, Debug, PartialEq)]
pub enum TableOutcome {
    /// The view of the row a read, a create, an update or a delete names.
    Row(Value),
    /// A list's page, its next cursor the repository's page key.
    Page(Page<Value>),
}

impl TableOutcome {
    /// The row's view, of an operation that answers one.
    pub fn row(self) -> Result<Value, ApiError> {
        match self {
            TableOutcome::Row(row) => Ok(row),
            TableOutcome::Page(_) => Err(ApiError::internal(
                "table operation: the route answers a row, and its handler listed rows",
            )),
        }
    }

    /// The page of views, of a list.
    pub fn page(self) -> Result<Page<Value>, ApiError> {
        match self {
            TableOutcome::Page(page) => Ok(page),
            TableOutcome::Row(_) => Err(ApiError::internal(
                "table operation: the route lists rows, and its handler read one",
            )),
        }
    }
}

/// The user a table operation's repositories act as: the route's caller
/// when its subject is a user's id, a UUID in either form, and no one
/// otherwise, so a write that stamps its caller is the 401 and a read is
/// served, as the Go server's and the TypeScript server's bind it.
pub fn acting(principal: Option<&Principal>) -> Option<String> {
    principal
        .map(|principal| principal.subject.as_str())
        .filter(|subject| parse_uuid(subject).is_some())
        .map(str::to_owned)
}

/// A create's or an update's body as its handler reads it: each member
/// as the route's input type read it, checked and normalized, with a
/// member the request left out absent and one it sent as null null, which
/// the input type reads alike. `parsed` is the input the route decoded,
/// and `raw` the body it was decoded from.
pub fn input_of<T: serde::Serialize>(parsed: &T, raw: Option<&Value>) -> Result<Value, ApiError> {
    let mut input = match serde_json::to_value(parsed) {
        Ok(Value::Object(members)) => members,
        Ok(other) => {
            return Err(ApiError::internal(format!(
                "table operation: the route decoded {other} as its input, no object"
            )))
        }
        Err(err) => return Err(ApiError::internal(format!("table operation: {err}"))),
    };
    let sent = raw.and_then(Value::as_object);
    input.retain(|member, _| sent.is_some_and(|sent| sent.contains_key(member)));
    for (member, value) in sent.into_iter().flatten() {
        if value.is_null() {
            input.insert(member.clone(), Value::Null);
        }
    }
    Ok(Value::Object(input))
}

/// The 400 of one member of a body that breaks a rule, in the shape of an
/// input the route's own validation refuses.
fn member_refusal(member: &str, validator: &str, message: &str) -> ApiError {
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("body"));
    details.insert("reason".to_owned(), Value::from("validation failed"));
    let mut failure = Map::new();
    failure.insert("validator".to_owned(), Value::from(validator));
    failure.insert("message".to_owned(), Value::from(message));
    let mut errors = Map::new();
    errors.insert(
        member.to_owned(),
        Value::Array(vec![Value::Object(failure)]),
    );
    ApiError::bad_request("Request body does not match the declared input")
        .with_details(Value::Object(details))
        .with_errors(Value::Object(errors))
}

/// `names` as a sentence lists them: `a`, `a and b`, `a, b and c`.
fn listed(names: &[&str]) -> String {
    match names {
        [] => String::new(),
        [one] => (*one).to_owned(),
        [rest @ .., last] => format!("{} and {last}", rest.join(", ")),
    }
}

/// A repository's error as a table operation's route answers it (D106,
/// D42): 404 for a key that holds no live row, 401 for a write that
/// records its caller and has none, the refused cursor's 400, 409 for a
/// unique key another row holds and for a row others still point at; for
/// a foreign key that names no row, 404 when the column is one of the
/// path's parents and the body's 400 at the member that sets it
/// otherwise; and the body's 400 at a member whose column takes no null.
pub fn refusal_of(operation: &TableOperation, table: &Table, err: Error) -> ApiError {
    let member_of = |column: &String| {
        table
            .columns
            .iter()
            .find(|candidate| candidate.unquoted() == column)
    };
    match err {
        Error::Constraint(Constraint {
            kind,
            columns,
            message,
        }) => {
            let held: Option<Vec<&Column>> = columns.iter().map(member_of).collect();
            let held = held.filter(|held| !held.is_empty());
            match kind {
                ConstraintKind::Unique => match held {
                    Some(held) => {
                        let names: Vec<&str> = held.iter().map(|c| c.name.as_str()).collect();
                        ApiError::conflict(format!(
                            "{} with that {} already exists",
                            operation.table,
                            listed(&names)
                        ))
                    }
                    None => ApiError::conflict(format!(
                        "{} conflicts with an existing row",
                        operation.table
                    )),
                },
                // A delete breaks a foreign key only from the other side:
                // a row of another table still points at this one.
                ConstraintKind::ForeignKey if operation.kind == Kind::Delete => {
                    ApiError::conflict(format!("{} is still referenced", operation.table))
                }
                ConstraintKind::ForeignKey => {
                    for column in held.unwrap_or_default() {
                        let related = column.relation.as_deref().unwrap_or("row");
                        if operation.parents.contains(&column.name) {
                            return ApiError::not_found(format!("{related} not found"));
                        }
                        if operation.body.contains(&column.name) {
                            return member_refusal(
                                &column.name,
                                "reference",
                                &format!("names no {related}"),
                            );
                        }
                    }
                    ApiError::conflict(message)
                }
                ConstraintKind::NotNull => match held.as_deref() {
                    Some([column]) if operation.body.contains(&column.name) => {
                        member_refusal(&column.name, "required", "required field")
                    }
                    _ => ApiError::internal(message),
                },
                ConstraintKind::Check => ApiError::unprocessable_entity(message),
            }
        }
        other => other.into(),
    }
}

impl From<Error> for ApiError {
    /// A repository's error as a route answers it when nothing more is
    /// known of the operation: 404, 401, the refused cursor's 400, 409 for
    /// a version another write moved and for a constraint, 503 for a busy
    /// writer, which the caller may try again, and the server's own 500.
    fn from(err: Error) -> Self {
        match err {
            Error::NotFound(message) => ApiError::not_found(message),
            Error::NoUser => ApiError::unauthorized("Authentication required"),
            Error::PageKey(_) => refused_cursor(),
            Error::VersionConflict(message) => ApiError::conflict(message),
            Error::Invalid(message) => ApiError::bad_request(message),
            Error::Constraint(constraint) => ApiError::conflict(constraint.message),
            Error::Busy(_) => ApiError::service_unavailable("The database is busy; try again"),
            // A refusal of the behaviors' core is the problem of its
            // status, code, message and details, as a behavior operation's
            // route answers it; one of status 500, or of no error status,
            // is the server's own.
            Error::Refused(refusal) => {
                let status = http::StatusCode::from_u16(refusal.status)
                    .ok()
                    .filter(|status| {
                        (status.is_client_error() || status.is_server_error())
                            && *status != http::StatusCode::INTERNAL_SERVER_ERROR
                    });
                let Some(status) = status else {
                    return ApiError::internal(format!("{}: {}", refusal.code, refusal.message));
                };
                let Refusal {
                    code,
                    message,
                    details,
                    ..
                } = *refusal;
                let refused = ApiError::new(status, code, message);
                if details.is_null() {
                    refused
                } else {
                    refused.with_details(details)
                }
            }
            Error::Encryption(message) | Error::Definition(message) | Error::Database(message) => {
                ApiError::internal(message)
            }
        }
    }
}

/// The rows an operation's row rule reaches for the request's caller
/// (D108).
enum Reach {
    /// Every row: an operation with no rule, and a caller who holds its
    /// override permission.
    Every,
    /// The rows this filter keeps: one of the rule's members holds the
    /// caller's subject.
    Rows(Values),
    /// No row: a request with no caller, and a subject that is no UUID
    /// against members that hold only UUIDs.
    None,
}

fn reach_of(
    operation: &TableOperation,
    table: &Table,
    principal: Option<&Principal>,
) -> Result<Reach, ApiError> {
    if operation.by.is_empty() {
        return Ok(Reach::Every);
    }
    // A member the repository keeps no equality filter for is the
    // generator's error, refused before anything is read: a rule that
    // dropped one would reach every row.
    let mut members = Vec::with_capacity(operation.by.len());
    for member in &operation.by {
        let column = table
            .column(member)
            .filter(|column| column.filter.iter().any(|op| op == "eq"))
            .ok_or_else(|| {
                ApiError::internal(format!(
                    "table operation: the row rule of {} names {member}, which its repository keeps no equality filter for; rebuild the database's types",
                    operation.table
                ))
            })?;
        members.push(column);
    }
    if let (Some(permission), Some(principal)) = (&operation.override_permission, principal) {
        if principal
            .permissions
            .iter()
            .any(|granted| covers(granted, permission))
        {
            return Ok(Reach::Every);
        }
    }
    let Some(subject) = principal
        .map(|principal| principal.subject.as_str())
        .filter(|subject| !subject.is_empty())
    else {
        return Ok(Reach::None);
    };
    let mut named = Vec::new();
    for column in members {
        if column.uuid && parse_uuid(subject).is_none() {
            continue;
        }
        named.push(condition(&column.name, "eq", Value::from(subject)));
    }
    if named.is_empty() {
        return Ok(Reach::None);
    }
    let mut reach = Values::new();
    reach.insert(
        "anyOf".to_owned(),
        Value::Array(named.into_iter().map(Value::Object).collect()),
    );
    Ok(Reach::Rows(reach))
}

/// The filter of one member's one condition.
fn condition(member: &str, op: &str, value: Value) -> Values {
    let mut held = Map::new();
    held.insert(op.to_owned(), value);
    let mut filter = Values::new();
    filter.insert(member.to_owned(), Value::Object(held));
    filter
}

/// The row's view: the members the operation's result lists.
fn view_of(operation: &TableOperation, row: &Values) -> Value {
    let mut view = Map::new();
    for member in &operation.view {
        view.insert(
            member.clone(),
            row.get(member).cloned().unwrap_or(Value::Null),
        );
    }
    Value::Object(view)
}

/// The conditions the operation's parents are: the row's member holds the
/// path's.
fn parents_of(operation: &TableOperation, request: &TableRequest) -> Values {
    let mut parents = Values::new();
    for parent in &operation.parents {
        let value = request.path.get(parent).cloned().unwrap_or(Value::Null);
        parents.extend(condition(parent, "eq", value));
    }
    parents
}

/// The body's members, as its input type's parser read them.
fn body_of<'r>(
    operation: &TableOperation,
    request: &'r TableRequest,
) -> Result<&'r Values, ApiError> {
    request
        .input
        .as_ref()
        .and_then(Value::as_object)
        .ok_or_else(|| {
            ApiError::internal(format!(
            "table operation: a {:?} of {} takes its body's members, and the route decoded none",
            operation.kind, operation.table
        ))
        })
}

/// The handlers of one operation, over its table's repository.
pub(crate) struct Routes<'a, C> {
    pub(crate) runner: Runner<'a, C>,
    pub(crate) operation: &'a TableOperation,
    pub(crate) principal: Option<&'a Principal>,
}

impl<C: Client> Routes<'_, C> {
    /// Runs the operation, and answers the repository's error as the
    /// route answers it.
    pub(crate) async fn serve(&self, request: &TableRequest) -> Result<TableOutcome, ApiError> {
        match self.operation.kind {
            Kind::Read => self.read(request).await.map(TableOutcome::Row),
            Kind::List => self.list(request).await.map(TableOutcome::Page),
            Kind::Create => self.create(request).await.map(TableOutcome::Row),
            Kind::Update => self.update(request).await.map(TableOutcome::Row),
            Kind::Delete => self.delete(request).await.map(TableOutcome::Row),
        }
    }

    fn refused(&self, err: Error) -> ApiError {
        refusal_of(self.operation, self.runner.table, err)
    }

    /// A write's error as the route answers it. A foreign key SQLite
    /// refused names no column, where Postgres's names its own, so the
    /// reference that names no row is found by reading each one the write
    /// set: a parent of the path is the 404, and a member of the body the
    /// 400 at that member, as `refusal_of` answers a named one.
    async fn refused_write(&self, err: Error, written: &Values) -> ApiError {
        let unnamed = matches!(
            &err,
            Error::Constraint(constraint)
                if constraint.kind == ConstraintKind::ForeignKey && constraint.columns.is_empty()
        );
        if unnamed {
            let operation = self.operation;
            let members = operation.parents.iter().chain(&operation.body);
            for member in members {
                let Some(column) = self.runner.table.column(member) else {
                    continue;
                };
                let (Some(related), Some(value)) = (
                    column.relation.as_deref(),
                    written.get(member).filter(|value| !value.is_null()),
                ) else {
                    continue;
                };
                if self.holds(related, value).await != Some(false) {
                    continue;
                }
                return if operation.parents.contains(member) {
                    ApiError::not_found(format!("{related} not found"))
                } else {
                    member_refusal(member, "reference", &format!("names no {related}"))
                };
            }
        }
        self.refused(err)
    }

    /// Whether a row of the table type `related` holds the key `value`,
    /// deleted or not, as a foreign key reads it; none when it cannot be
    /// read.
    async fn holds(&self, related: &str, value: &Value) -> Option<bool> {
        let plan = &self.runner.plan;
        let table = plan.definition.table(related).ok()?;
        let target = Runner {
            plan: super::plan::Plan::new(plan.definition, table, plan.session).ok()?,
            table,
            client: self.runner.client,
        };
        let options = super::FindOptions {
            include_deleted: true,
            ..Default::default()
        };
        let filter = condition(&table.key, "eq", value.clone());
        let found = target.find_one(Some(&filter), &options).await.ok()?;
        Some(found.is_some())
    }

    fn not_found(&self) -> ApiError {
        ApiError::not_found(format!("{} not found", self.operation.table))
    }

    /// The live row whose key is the path's, whose parents are the path's
    /// and which the operation's row rule reaches for the caller (D108),
    /// or the 404: a row that names another caller is not told apart from
    /// one that is not there.
    async fn live_row(&self, request: &TableRequest) -> Result<Values, ApiError> {
        let key = self.operation.key.as_deref().ok_or_else(|| {
            ApiError::internal(format!(
                "table operation: a {:?} of {} names no key",
                self.operation.kind, self.operation.table
            ))
        })?;
        let mut filter = parents_of(self.operation, request);
        let value = request.path.get(key).cloned().unwrap_or(Value::Null);
        filter.extend(condition(key, "eq", value));
        match reach_of(self.operation, self.runner.table, self.principal)? {
            Reach::Every => {}
            Reach::None => return Err(self.not_found()),
            Reach::Rows(reach) => {
                filter.insert("allOf".to_owned(), Value::Array(vec![Value::Object(reach)]));
            }
        }
        self.runner
            .find_one(Some(&filter), &super::FindOptions::default())
            .await
            .map_err(|err| self.refused(err))?
            .ok_or_else(|| self.not_found())
    }

    /// A read (D106): the view of the live row whose key is the path's,
    /// under the path's parents.
    async fn read(&self, request: &TableRequest) -> Result<Value, ApiError> {
        let row = self.live_row(request).await?;
        Ok(view_of(self.operation, &row))
    }

    /// A list (D106): a page of the live rows' views through the
    /// repository's `find_page`. The rows are the ones under the path's
    /// parents that the request's filter keeps and, with a search argument
    /// that holds text, whose search fields hold each of its words (D62).
    /// A cursor is the repository's page key; one that names no row of the
    /// order is the refused-cursor 400.
    async fn list(&self, request: &TableRequest) -> Result<Page<Value>, ApiError> {
        let operation = self.operation;
        let table = self.runner.table;
        // The parents and the row rule hold beside the filter, whatever it
        // says of them.
        let mut held = Vec::new();
        let parents = parents_of(operation, request);
        if !parents.is_empty() {
            held.push(Value::Object(parents));
        }
        match reach_of(operation, table, self.principal)? {
            Reach::Every => {}
            // A caller the row rule names in no row lists none.
            Reach::None => return Ok(Page::last(Vec::new())),
            Reach::Rows(reach) => held.push(Value::Object(reach)),
        }
        let mut rows = request.filter.clone();
        if let Some(search) = request.search.as_deref().filter(|text| !text.is_empty()) {
            rows.insert("searchText".to_owned(), Value::from(search));
        }
        if !held.is_empty() {
            rows.insert("allOf".to_owned(), Value::Array(held));
        }
        let page = request.page.as_ref().ok_or_else(|| {
            ApiError::internal(format!(
                "table operation: a list of {} takes a page, and the route decoded none",
                operation.table
            ))
        })?;
        let after = match &page.cursor {
            None => None,
            // A cursor is the repository's page key, an object that holds
            // the table's key; any other names no position.
            Some(cursor) => match cursor.decode::<Value>()? {
                Value::Object(key) => match key.get(&table.key) {
                    Some(held) if !held.is_null() => Some(held.clone()),
                    _ => return Err(refused_cursor()),
                },
                _ => return Err(refused_cursor()),
            },
        };
        let options = PageOptions {
            sort: page.sort.as_ref().map(|sort| sort.field().to_owned()),
            desc: page.sort.as_ref().is_some_and(crate::page::Sort::desc),
            after,
            limit: u32::try_from(page.limit).ok().filter(|limit| *limit > 0),
            include_deleted: false,
        };
        let found = self
            .runner
            .find_page(Some(&rows), &options)
            .await
            .map_err(|err| self.refused(err))?;
        let items = found
            .rows
            .iter()
            .map(|row| view_of(operation, row))
            .collect();
        let next = match found.next {
            None => None,
            Some(key) => {
                let mut position = Map::new();
                position.insert(table.key.clone(), key);
                Some(Cursor::encode(&Value::Object(position))?)
            }
        };
        Ok(Page { items, next })
    }

    /// A create (D106): a row of the body's members, one that is absent or
    /// null left unset, so its column takes its default, and of each
    /// parent the path names; the view of the row as the write left it.
    async fn create(&self, request: &TableRequest) -> Result<Value, ApiError> {
        let operation = self.operation;
        let table = self.runner.table;
        let body = body_of(operation, request)?;
        let mut input = Values::new();
        for member in &operation.body {
            // The state a row starts in is its Workflow's to set, below.
            if operation.starts.contains(member) {
                continue;
            }
            if let Some(value) = body.get(member).filter(|value| !value.is_null()) {
                input.insert(member.clone(), value.clone());
            }
        }
        for parent in &operation.parents {
            let value = request.path.get(parent).cloned().unwrap_or(Value::Null);
            input.insert(parent.clone(), value);
        }
        // The row rule's members name the caller in the new row (D108):
        // createdBy as the repository stamps it, each other one set here.
        for member in &operation.by {
            let column = table.column(member);
            if column.is_some_and(|column| column.audit.is_some()) {
                continue;
            }
            let subject = self
                .principal
                .map(|principal| principal.subject.as_str())
                .filter(|subject| !subject.is_empty())
                .ok_or_else(|| ApiError::unauthorized("Authentication required"))?;
            if column.is_some_and(|column| column.uuid) && parse_uuid(subject).is_none() {
                // The row names who it is for by id, and the caller has
                // none.
                return Err(ApiError::forbidden("Access denied"));
            }
            input.insert(member.clone(), Value::from(subject));
        }
        let created = match self.runner.create_one(&input).await {
            Ok(created) => created,
            Err(err) => return Err(self.refused_write(err, &input).await),
        };
        // The state the body names for the row to start in (D109): the row
        // is created in its Workflow's initial state, and moved by the
        // Workflow's own transition, in this transaction and as the
        // route's caller, so the transition's permission, its by and every
        // behavior that hears the move hold as they do for a caller who
        // makes it. A refusal of the move rolls the create back.
        let key = created.get(&table.key).cloned().unwrap_or(Value::Null);
        let mut moved = false;
        for member in &operation.starts {
            let Some(to) = body.get(member).filter(|to| !to.is_null()) else {
                continue;
            };
            if created.get(member) == Some(to) {
                continue;
            }
            let mut params = Map::new();
            params.insert("to".to_owned(), to.clone());
            self.runner
                .operation(
                    member,
                    "transition",
                    &key,
                    &Value::Object(params),
                    &WriteOptions::default(),
                )
                .await
                .map_err(|err| self.refused(err))?;
            moved = true;
        }
        if !moved {
            return Ok(view_of(operation, &created));
        }
        let row = self
            .runner
            .get_one(&key)
            .await
            .map_err(|err| self.refused(err))?;
        Ok(view_of(operation, &row))
    }

    /// An update (D106): of the live row under the path's key and parents,
    /// 404 without one, the members the body sets. A member the request
    /// left out leaves its column as it is; one that is null sets a column
    /// that may be null to NULL, and is the 400 at that member for a
    /// column that may not; a body that sets no member writes nothing
    /// (D105).
    async fn update(&self, request: &TableRequest) -> Result<Value, ApiError> {
        let operation = self.operation;
        let table = self.runner.table;
        let row = self.live_row(request).await?;
        let body = body_of(operation, request)?;
        let mut update = Values::new();
        for member in &operation.body {
            let Some(value) = body.get(member) else {
                continue;
            };
            if value.is_null() && table.column(member).is_some_and(|column| column.required) {
                return Err(member_refusal(member, "required", "required field"));
            }
            update.insert(member.clone(), value.clone());
        }
        if update.is_empty() {
            return Ok(view_of(operation, &row));
        }
        let key = row.get(&table.key).cloned().unwrap_or(Value::Null);
        let updated = match self
            .runner
            .update_one(&key, &update, &WriteOptions::default())
            .await
        {
            Ok(updated) => updated,
            Err(err) => return Err(self.refused_write(err, &update).await),
        };
        Ok(view_of(operation, &updated))
    }

    /// A delete (D106): of the live row under the path's key and parents,
    /// 404 without one, through the repository's `delete_one`. It answers
    /// the view of the row as it was before.
    async fn delete(&self, request: &TableRequest) -> Result<Value, ApiError> {
        let row = self.live_row(request).await?;
        let key = row
            .get(&self.runner.table.key)
            .cloned()
            .unwrap_or(Value::Null);
        self.runner
            .delete_one(&key, &WriteOptions::default())
            .await
            .map_err(|err| self.refused(err))?;
        Ok(view_of(self.operation, &row))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_body_keeps_an_absent_member_apart_from_a_null_one() {
        #[derive(serde::Serialize)]
        struct Input {
            title: Option<String>,
            #[serde(skip_serializing_if = "Option::is_none")]
            memo: Option<String>,
            #[serde(skip_serializing_if = "Option::is_none")]
            due: Option<String>,
        }
        // The input type read the title normalized, the memo's null as
        // none, and no due date.
        let parsed = Input {
            title: Some("Ship it".to_owned()),
            memo: None,
            due: None,
        };
        let raw = serde_json::json!({"title": "  Ship it ", "memo": null});
        assert_eq!(
            input_of(&parsed, Some(&raw)).unwrap(),
            serde_json::json!({"title": "Ship it", "memo": null})
        );
        assert_eq!(input_of(&parsed, None).unwrap(), serde_json::json!({}));
    }

    #[test]
    fn names_are_listed_as_a_sentence_lists_them() {
        assert_eq!(listed(&["code"]), "code");
        assert_eq!(listed(&["a", "b"]), "a and b");
        assert_eq!(listed(&["a", "b", "c"]), "a, b and c");
    }

    #[test]
    fn a_caller_acts_as_a_user_only_by_an_id() {
        let principal = |subject: &str| Principal::new(subject, Vec::<String>::new());
        assert_eq!(
            acting(Some(&principal("11111111-1111-4111-8111-111111111111"))).as_deref(),
            Some("11111111-1111-4111-8111-111111111111")
        );
        assert_eq!(acting(Some(&principal("service:billing"))), None);
        assert_eq!(acting(None), None);
    }
}
