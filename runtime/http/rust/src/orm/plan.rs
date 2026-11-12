//! A repository's statements, filled in the order the Go ORM fills them
//! (D79): what both dialects' clients run. Nothing here reads or writes a
//! database: each function answers a statement's text and the values it
//! binds, in the wire form, with what each one's column holds, which the
//! dialect's client writes as its database reads it.

use std::fmt::Display;

use serde_json::Value;

use super::{
    invalid, Bound, Column, CoreCreate, CoreDelete, CoreUpdate, Definition, Dialect, Error,
    FieldCell, FindOptions, Marks, OrderBy, PageOptions, Session, Table, UniqueKey, Values,
    WriteOptions,
};
use crate::search::{like_pattern, terms};
use crate::uuid62::parse_uuid;

/// Fills a format string's `%s` and `%d`, in order, with `args`, and `%%`
/// with `%`, as Go's `fmt.Sprintf` fills the statements `sqlutil` writes.
/// A format that takes more arguments than it was given, or holds another
/// verb, is the definition's fault.
pub fn fill(format: &str, args: &[&dyn Display]) -> Result<String, Error> {
    let mut out = String::with_capacity(format.len() + 16 * args.len());
    let mut next = 0;
    let mut chars = format.chars();
    while let Some(c) = chars.next() {
        if c != '%' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('%') => out.push('%'),
            Some('s' | 'd') => {
                let arg = args.get(next).ok_or_else(|| {
                    Error::Definition(format!(
                        "the statement {format:?} takes more than {} arguments",
                        args.len()
                    ))
                })?;
                out.push_str(&arg.to_string());
                next += 1;
            }
            _ => {
                return Err(Error::Definition(format!(
                    "the statement {format:?} holds a verb that is not %s, %d or %%"
                )))
            }
        }
    }
    Ok(out)
}

/// A statement and the values it binds, in order.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct Statement {
    pub(crate) sql: String,
    pub(crate) args: Vec<Bound>,
}

/// A cell of a row's insert: the value a column is written with, or none
/// for a column the row leaves to its default.
type Cell = Option<Bound>;

/// The statements of one table, for a session.
#[derive(Clone)]
pub(crate) struct Plan<'a> {
    pub(crate) definition: &'a Definition,
    pub(crate) table: &'a Table,
    pub(crate) session: &'a Session<'a>,
}

/// What a page's statement was written with, which reading its rows needs.
pub(crate) struct PagePlan {
    pub(crate) statement: Statement,
    pub(crate) limit: usize,
    /// The statement that reads whether the after key's row exists, for a
    /// page that came back empty after a key, in an order by another
    /// column.
    pub(crate) key_exists: Option<Statement>,
}

/// An upsert's statements.
pub(crate) struct UpsertPlan {
    /// The read of the row that holds the input's key before the upsert,
    /// on a dialect whose upsert does not say whether it inserted; none
    /// when the input leaves a column of the key unset, which no row then
    /// holds.
    pub(crate) before: Option<Statement>,
    pub(crate) statement: Statement,
    /// Whether the update set a field: a row the statement did not return
    /// is then deleted, where one that set none was left as it was.
    pub(crate) sets: bool,
    /// The read of the row the key holds, for an upsert that left it as it
    /// was.
    pub(crate) held: Option<Statement>,
    /// The encrypted members the update sets, each to its value or null,
    /// which a second statement seals under the key of the row it updates
    /// (D89).
    pub(crate) sealing: Values,
}

/// A delete's statements.
pub(crate) struct DeletePlan {
    pub(crate) statement: Statement,
    /// The statement that clears the history actor a hard delete of a
    /// versioned table set for its transaction (D17, D32).
    pub(crate) clear: Option<Statement>,
}

impl<'a> Plan<'a> {
    pub(crate) fn new(
        definition: &'a Definition,
        table: &'a Table,
        session: &'a Session<'a>,
    ) -> Result<Self, Error> {
        if let Some(reason) = &table.unsupported {
            return Err(Error::Definition(format!(
                "{} has no repository on {}: {reason}",
                table.name, definition.dialect
            )));
        }
        Ok(Self {
            definition,
            table,
            session,
        })
    }

    fn placeholder(&self, n: usize) -> Result<String, Error> {
        fill(&self.definition.clauses.placeholder, &[&n])
    }

    /// The acting user, hyphenated, or none: a caller whose subject is no
    /// UUID is no user an audit field can hold.
    fn user(&self) -> Option<String> {
        let user = self.session.user.as_deref()?;
        parse_uuid(user).map(|uuid| uuid.hyphenated().to_string())
    }

    fn require_user(&self) -> Result<String, Error> {
        self.user().ok_or(Error::NoUser)
    }

    fn bind(&self, value: &Value, column: &Column, what: &str) -> Result<Bound, Error> {
        check(value, column).map_err(|err| {
            invalid(format!(
                "{}.{}: {what}: {err}",
                self.table.name, column.name
            ))
        })?;
        Ok(Bound {
            value: value.clone(),
            marks: Marks::of(column),
        })
    }

    pub(crate) fn key(&self, key: &Value) -> Result<Bound, Error> {
        if key.is_null() {
            return Err(invalid(format!(
                "{} is keyed by {}, not null",
                self.table.name, self.table.key
            )));
        }
        self.bind(key, self.table.key_column(), "key")
    }

    /// The filter's conditions, each value appended to `args`, as the Go
    /// ORM's `conditions` writes them.
    fn conditions(
        &self,
        table: &Table,
        filter: Option<&Values>,
        args: &mut Vec<Bound>,
    ) -> Result<Vec<String>, Error> {
        let Some(filter) = filter else {
            return Ok(Vec::new());
        };
        let c = &self.definition.clauses;
        let mut conditions = Vec::new();
        self.known(table, filter)?;
        let ordered = table
            .columns
            .iter()
            .filter(|column| column.relation.is_none())
            .chain(
                table
                    .columns
                    .iter()
                    .filter(|column| column.relation.is_some()),
            );
        for column in ordered {
            let Some(Value::Object(member)) = filter.get(&column.name) else {
                continue;
            };
            let q = &column.qualified;
            for (op, _) in member {
                if !column.filter.iter().any(|known| known == op) {
                    return Err(invalid(format!(
                        "{}.{} filters by {}, not {op}",
                        table.name,
                        column.name,
                        column.filter.join(", ")
                    )));
                }
            }
            let push = |value: &Value, what: &str, args: &mut Vec<Bound>| -> Result<usize, Error> {
                check(value, column).map_err(|err| {
                    invalid(format!("{}.{}: {what}: {err}", table.name, column.name))
                })?;
                args.push(Bound {
                    value: value.clone(),
                    marks: Marks::of(column),
                });
                Ok(args.len())
            };
            for op in &column.filter {
                let Some(value) = member.get(op).filter(|value| !value.is_null()) else {
                    continue;
                };
                match op.as_str() {
                    "eq" | "ne" | "gt" | "gte" | "lt" | "lte" => {
                        let clause = match op.as_str() {
                            "eq" => &c.eq,
                            "ne" => &c.ne,
                            "gt" => &c.gt,
                            "gte" => &c.gte,
                            "lt" => &c.lt,
                            _ => &c.lte,
                        };
                        let n = push(value, op, args)?;
                        conditions.push(fill(clause, &[q, &n])?);
                    }
                    "ilike" => {
                        let Value::String(text) = value else {
                            return Err(invalid(format!(
                                "{}.{}: ilike takes text",
                                table.name, column.name
                            )));
                        };
                        args.push(Bound {
                            value: Value::String(format!("%{text}%")),
                            marks: Marks::TEXT,
                        });
                        conditions.push(fill(&c.ilike, &[q, &args.len()])?);
                    }
                    "in" | "notIn" => {
                        let Value::Array(values) = value else {
                            return Err(invalid(format!(
                                "{}.{}: {op} takes a list",
                                table.name, column.name
                            )));
                        };
                        if values.is_empty() {
                            continue;
                        }
                        let mut placeholders = Vec::with_capacity(values.len());
                        for value in values {
                            let n = push(value, op, args)?;
                            placeholders.push(self.placeholder(n)?);
                        }
                        let clause = if op == "in" { &c.in_ } else { &c.none_of };
                        conditions.push(fill(clause, &[q, &placeholders.join(", ")])?);
                    }
                    "between" => {
                        let bounds = match value {
                            Value::Array(bounds) if bounds.len() == 2 => bounds,
                            _ => {
                                return Err(invalid(format!(
                                    "{}.{}: between takes [min, max]",
                                    table.name, column.name
                                )))
                            }
                        };
                        let n = push(&bounds[0], op, args)?;
                        conditions.push(fill(&c.gte, &[q, &n])?);
                        let n = push(&bounds[1], op, args)?;
                        conditions.push(fill(&c.lte, &[q, &n])?);
                    }
                    "isNull" => {
                        let clause = if value.as_bool() == Some(true) {
                            &c.is_null
                        } else {
                            &c.is_not_null
                        };
                        conditions.push(fill(clause, &[q])?);
                    }
                    other => {
                        return Err(Error::Definition(format!(
                            "{}.{} filters by {other}, which the repositories do not write",
                            table.name, column.name
                        )))
                    }
                }
            }
        }
        // A filter through a to-one relation (D90): the foreign key in the
        // keys of the target's live rows its filter finds.
        for relation in &table.relations {
            let Some(Value::Object(through)) = filter.get(&relation.field) else {
                continue;
            };
            let target = self.definition.table(&relation.target)?;
            let mut inner = self.conditions(target, Some(through), args)?;
            if target.soft_delete {
                inner.push(target.statements.live_condition.clone());
            }
            conditions.push(fill(&relation.probe, &[&self.where_of(&inner)])?);
        }
        for reference in &table.referenced_by {
            let Some(Value::Object(probe)) = filter.get(&reference.filter) else {
                continue;
            };
            let mut text = reference.probe.clone();
            if probe.get("includeDeleted").and_then(Value::as_bool) != Some(true) {
                text.push_str(&reference.live);
            }
            let clause = if probe.get("exists").and_then(Value::as_bool) == Some(true) {
                &c.exists
            } else {
                &c.not_exists
            };
            conditions.push(fill(clause, &[&text])?);
        }
        if let (Some(search), Some(Value::String(text))) = (&table.search, filter.get("searchText"))
        {
            for term in terms(text) {
                args.push(Bound {
                    value: Value::String(like_pattern(&term)),
                    marks: Marks::TEXT,
                });
                conditions.push(fill(&c.search, &[search, &args.len()])?);
            }
        }
        // allOf's filters' conditions are the filter's own, ANDed (D99).
        if let Some(Value::Array(all)) = filter.get("allOf") {
            for each in all {
                conditions.extend(self.conditions(table, each.as_object(), args)?);
            }
        }
        // anyOf ORs filters of the table (D90), each one's conditions
        // ANDed. A filter of no condition keeps every row, and so does the
        // whole, whose parameters then go too; it is the last condition, so
        // no placeholder follows theirs.
        if let Some(Value::Array(any)) = filter.get("anyOf") {
            if !any.is_empty() {
                let start = args.len();
                let mut branches = Vec::new();
                let mut every = false;
                for branch in any {
                    let inner = self.conditions(table, branch.as_object(), args)?;
                    if inner.is_empty() {
                        every = true;
                        continue;
                    }
                    branches.push(fill(&c.group, &[&inner.join(&c.and)])?);
                }
                if every {
                    args.truncate(start);
                } else {
                    conditions.push(fill(&c.group, &[&branches.join(&c.or)])?);
                }
            }
        }
        Ok(conditions)
    }

    /// Refuses a member of `filter` the table's filter has none of: a
    /// condition dropped in silence would read rows the caller left out.
    fn known(&self, table: &Table, filter: &Values) -> Result<(), Error> {
        for (member, value) in filter {
            if value.is_null() {
                continue;
            }
            let known = matches!(member.as_str(), "allOf" | "anyOf")
                || (member == "searchText" && table.search.is_some())
                || table
                    .column(member)
                    .is_some_and(|column| !column.filter.is_empty())
                || table.relations.iter().any(|r| &r.field == member)
                || table.referenced_by.iter().any(|r| &r.filter == member);
            if !known {
                return Err(invalid(format!("{} has no filter by {member}", table.name)));
            }
        }
        Ok(())
    }

    fn where_of(&self, conditions: &[String]) -> String {
        if conditions.is_empty() {
            return String::new();
        }
        let c = &self.definition.clauses;
        format!("{}{}", c.where_, conditions.join(&c.and))
    }

    /// The WHERE of a filter, then the soft-delete condition unless
    /// deleted rows are asked for, as Go's FindMany writes it.
    fn live_where(&self, where_: String, include_deleted: bool) -> String {
        let s = &self.table.statements;
        if !self.table.soft_delete || include_deleted {
            where_
        } else if where_.is_empty() {
            s.live_where.clone()
        } else {
            where_ + &s.live
        }
    }

    fn order_by(&self, order_by: &[OrderBy]) -> Result<String, Error> {
        if order_by.is_empty() {
            return Ok(String::new());
        }
        let c = &self.definition.clauses;
        let mut terms = Vec::with_capacity(order_by.len());
        for order in order_by {
            let column = self
                .table
                .column(&order.field)
                .filter(|column| column.relation.is_none())
                .ok_or_else(|| {
                    invalid(format!(
                        "{} orders by its columns, not {}",
                        self.table.name, order.field
                    ))
                })?;
            let direction = if order.desc { "DESC" } else { "ASC" };
            terms.push(fill(&c.order_term, &[&column.qualified, &direction])?);
        }
        Ok(format!("{}{}", c.order_by, terms.join(", ")))
    }

    pub(crate) fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Statement, Error> {
        let mut args = Vec::new();
        let conditions = self.conditions(self.table, filter, &mut args)?;
        let where_ = self.live_where(self.where_of(&conditions), options.include_deleted);
        let s = &self.table.statements;
        let sql = fill(
            &s.find_one,
            &[&s.columns, &where_, &self.order_by(&options.order_by)?],
        )?;
        Ok(Statement { sql, args })
    }

    /// `find_many`'s count, unless the options skip it, and its read, in
    /// the order the Go ORM runs them.
    pub(crate) fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<(Option<Statement>, Statement), Error> {
        let mut args = Vec::new();
        let conditions = self.conditions(self.table, filter, &mut args)?;
        let where_ = self.live_where(self.where_of(&conditions), options.include_deleted);
        let c = &self.definition.clauses;
        let limit = options
            .limit
            .filter(|limit| *limit > 0)
            .unwrap_or(self.definition.default_limit);
        let mut limit_offset = fill(&c.limit, &[&limit])?;
        if let Some(offset) = options.offset.filter(|offset| *offset > 0) {
            limit_offset.push_str(&fill(&c.offset, &[&offset])?);
        }
        let s = &self.table.statements;
        let count = if options.skip_count {
            None
        } else {
            Some(Statement {
                sql: fill(&s.count, &[&where_])?,
                args: args.clone(),
            })
        };
        let sql = fill(
            &s.find_many,
            &[
                &s.qualified_columns,
                &where_,
                &self.order_by(&options.order_by)?,
                &limit_offset,
            ],
        )?;
        Ok((count, Statement { sql, args }))
    }

    pub(crate) fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<PagePlan, Error> {
        let table = self.table;
        let limit = options
            .limit
            .filter(|limit| *limit > 0)
            .unwrap_or(self.definition.default_limit);
        let field = options.sort.as_deref().unwrap_or(&table.key);
        let column = table
            .columns
            .iter()
            .find(|column| column.name == field && (column.key || column.sortable))
            .ok_or_else(|| {
                let mut fields = vec![table.key.as_str()];
                fields.extend(
                    table
                        .columns
                        .iter()
                        .filter(|column| column.sortable && !column.key)
                        .map(|column| column.name.as_str()),
                );
                invalid(format!(
                    "{} sorts by {}, not {field}",
                    table.name,
                    fields.join(", ")
                ))
            })?;
        let by_key = column.key;
        let s = &table.statements;
        let c = &self.definition.clauses;
        let mut args = Vec::new();
        let mut conditions = self.conditions(table, filter, &mut args)?;
        if table.soft_delete && !options.include_deleted {
            conditions.push(s.live_condition.clone());
        }
        let (comparison, direction) = if options.desc {
            ("<", "DESC")
        } else {
            (">", "ASC")
        };
        // A column that may be null orders its nulls last in either
        // direction (D112).
        let nulls = !by_key && !column.required;
        let mut key_exists = None;
        if let Some(after) = options.after.as_ref().filter(|after| !after.is_null()) {
            let key = table.key_column();
            let held = match after {
                Value::String(text) if key.uuid => parse_uuid(text).is_some(),
                Value::String(text) => !text.is_empty(),
                Value::Number(_) => true,
                _ => false,
            };
            if !held {
                return Err(Error::PageKey(format!(
                    "the {} key holds no {}",
                    table.name, table.key
                )));
            }
            let bound = self.bind(after, key, "page key")?;
            args.push(bound.clone());
            let placeholder = self.placeholder(args.len())?;
            let after_key = fill(&s.page_after_key, &[&comparison, &placeholder])?;
            if by_key {
                conditions.push(after_key);
            } else {
                let page_key = fill(&c.page_key, &[&column.column])?;
                // The key's row gives the sort field's value. After a null
                // come the nulls past the key; after a value, every null
                // and the values past it.
                let mut after_value = fill(
                    &s.page_after,
                    &[
                        &column.qualified,
                        &comparison,
                        &page_key,
                        &placeholder,
                        &placeholder,
                    ],
                )?;
                if nulls {
                    after_value = fill(
                        &s.page_after_nulls,
                        &[
                            &page_key,
                            &placeholder,
                            &column.qualified,
                            &after_key,
                            &column.qualified,
                            &after_value,
                        ],
                    )?;
                }
                conditions.push(after_value);
                // A key whose row no longer exists places no row: only
                // then is the page empty without being the last.
                key_exists = Some(Statement {
                    sql: s.page_key_exists.clone(),
                    args: vec![bound],
                });
            }
        }
        let order_format = if nulls {
            &s.page_order_nulls
        } else {
            &s.page_order
        };
        let mut order = fill(order_format, &[&column.qualified, &direction])?;
        if !by_key {
            order.push_str(&fill(&s.page_tiebreak, &[&direction])?);
        }
        let sql = fill(
            &s.page,
            &[
                &s.qualified_columns,
                &self.where_of(&conditions),
                &order,
                &(u64::from(limit) + 1),
            ],
        )?;
        Ok(PagePlan {
            statement: Statement { sql, args },
            limit: limit as usize,
            key_exists,
        })
    }

    /// `input` with the members a create stamps: createdBy needs a user,
    /// as the Go ORM's CreateOne does.
    fn stamped(&self, input: &Values) -> Result<Values, Error> {
        let mut out = input.clone();
        if let Some(created_by) = self.table.audit("createdBy") {
            out.insert(created_by.name.clone(), Value::String(self.require_user()?));
        }
        if let Some(updated_by) = self.table.audit("updatedBy") {
            if updated_by.required || self.definition.require_updated_by {
                out.insert(updated_by.name.clone(), Value::String(self.require_user()?));
            } else if let Some(user) = self.user() {
                out.insert(updated_by.name.clone(), Value::String(user));
            }
        }
        Ok(out)
    }

    /// The cell `column`'s envelope is bound to in the row whose key is
    /// `key`.
    fn seal(
        &self,
        column: &Column,
        value: &Value,
        key: &Value,
        what: &str,
    ) -> Result<Value, Error> {
        let Value::String(plaintext) = value else {
            return Err(invalid(format!(
                "{what} {}: {} is Encrypted<string>, and takes a string",
                self.table.name, column.name
            )));
        };
        let row = self.cell_key(key).ok_or_else(|| {
            invalid(format!(
                "{what} {}: {} is required, since {}'s envelope is bound to it",
                self.table.name, self.table.key, column.name
            ))
        })?;
        let keys = self.session.keys.ok_or_else(|| {
            Error::Encryption(format!(
                "{}.{} is an Encrypted<T> field, and the repositories were opened with no field keys",
                self.table.name, column.field
            ))
        })?;
        let cell = FieldCell {
            table: &self.table.name,
            field: &column.field,
            row: &row,
        };
        keys.seal(cell, plaintext)
            .map(Value::String)
            .map_err(|err| {
                Error::Encryption(format!("{}.{}: {err}", self.table.name, column.field))
            })
    }

    /// A row's key as an envelope is bound to it: a UUID hyphenated, any
    /// other key its text; none for a key that holds nothing.
    fn cell_key(&self, key: &Value) -> Option<String> {
        match key {
            Value::String(text) if self.table.key_column().uuid => {
                parse_uuid(text).map(|uuid| uuid.hyphenated().to_string())
            }
            Value::String(text) if !text.is_empty() => Some(text.clone()),
            Value::Number(number) => Some(number.to_string()),
            _ => None,
        }
    }

    /// `input` with its key, which a table with an encrypted column
    /// generates when a UUID key is left unset (D85), and each encrypted
    /// member sealed under it.
    fn sealed(&self, mut input: Values, what: &str) -> Result<Values, Error> {
        if self.table.encrypted().next().is_none() {
            return Ok(input);
        }
        let key = self.table.key_column();
        let unset = match input.get(&key.name) {
            None | Some(Value::Null) => true,
            Some(Value::String(text)) => text.is_empty(),
            Some(_) => false,
        };
        if key.uuid && unset {
            input.insert(
                key.name.clone(),
                Value::String(uuid::Uuid::new_v4().hyphenated().to_string()),
            );
        }
        let key_value = input.get(&key.name).cloned().unwrap_or(Value::Null);
        for column in self.table.encrypted() {
            let Some(value) = input.get(&column.name).filter(|value| !value.is_null()) else {
                continue;
            };
            let envelope = self.seal(column, value, &key_value, what)?;
            input.insert(column.name.clone(), envelope);
        }
        Ok(input)
    }

    /// `update` with each encrypted member it sets sealed for the row
    /// whose key is `key`.
    fn sealed_update(&self, mut update: Values, key: &Value, what: &str) -> Result<Values, Error> {
        for column in self.table.encrypted() {
            let Some(value) = update.get(&column.name).filter(|value| !value.is_null()) else {
                continue;
            };
            let envelope = self.seal(column, value, key, what)?;
            update.insert(column.name.clone(), envelope);
        }
        Ok(update)
    }

    /// A row as the repository returns it: each encrypted member opened
    /// under the row's key.
    pub(crate) fn open(&self, mut row: Values) -> Result<Values, Error> {
        if self.table.encrypted().next().is_none() {
            return Ok(row);
        }
        let key = row.get(&self.table.key).cloned().unwrap_or(Value::Null);
        for column in self.table.encrypted() {
            let Some(Value::String(envelope)) = row.get(&column.name) else {
                continue;
            };
            let keys = self.session.keys.ok_or_else(|| {
                Error::Encryption(format!(
                    "{}.{} is an Encrypted<T> field, and the repositories were opened with no field keys",
                    self.table.name, column.field
                ))
            })?;
            let row_key = self.cell_key(&key).ok_or_else(|| {
                Error::Definition(format!(
                    "a row of {} holds no {}, which {}'s envelope is bound to",
                    self.table.name, self.table.key, column.name
                ))
            })?;
            let cell = FieldCell {
                table: &self.table.name,
                field: &column.field,
                row: &row_key,
            };
            let plaintext = keys.open(cell, envelope).map_err(|err| {
                Error::Encryption(format!("{}.{}: {err}", self.table.name, column.field))
            })?;
            row.insert(column.name.clone(), Value::String(plaintext));
        }
        Ok(row)
    }

    /// Whether a create writes the column, and what: the member when set,
    /// else its default, as Go writes an unset enum's. Null sets a JSON
    /// column that takes no NULL to the JSON null.
    fn insert_value(&self, input: &Values, column: &Column) -> Result<Cell, Error> {
        let value = input.get(&column.name);
        if matches!(value, Some(Value::Null)) && column.json_null() {
            return self.bind(&Value::Null, column, "create").map(|mut bound| {
                bound.marks.json = true;
                Some(bound)
            });
        }
        if let Some(value) = value.filter(|value| !value.is_null()) {
            let blank = matches!(value, Value::String(text) if text.is_empty());
            if !(column.default.is_some() && blank) {
                return self.bind(value, column, "create").map(Some);
            }
        }
        if let Some(default) = &column.default {
            return self.bind(default, column, "default").map(Some);
        }
        if column.required && !column.auto && !column.key {
            return Err(invalid(format!(
                "create {}: {} is required",
                self.table.name, column.name
            )));
        }
        Ok(None)
    }

    /// Whether a create writes the column at all: never createdAt,
    /// updatedAt or internal metadata.
    fn insertable(column: &Column) -> bool {
        !column.internal && !matches!(column.audit.as_deref(), Some("createdAt" | "updatedAt"))
    }

    /// The columns of one row's insert and their cells, in CreateOne's
    /// order: the key, the fields, the relations.
    fn insert_cells(&self, input: &Values) -> Result<Vec<(&'a Column, Bound)>, Error> {
        let table = self.table;
        let mut cells = Vec::new();
        let key = table.key_column();
        if let Some(bound) = self.insert_value(input, key)? {
            cells.push((key, bound));
        }
        for column in &table.columns {
            if column.key || column.relation.is_some() || !Self::insertable(column) {
                continue;
            }
            if let Some(bound) = self.insert_value(input, column)? {
                cells.push((column, bound));
            }
        }
        for column in table.columns.iter().filter(|c| c.relation.is_some()) {
            if let Some(bound) = self.insert_value(input, column)? {
                cells.push((column, bound));
            }
        }
        Ok(cells)
    }

    /// Refuses a member of a create's input the table has no column for.
    fn known_members(&self, input: &Values, what: &str) -> Result<(), Error> {
        for member in input.keys() {
            if self.table.column(member).is_none() {
                return Err(invalid(format!(
                    "{what} {}: it has no member {member}",
                    self.table.name
                )));
            }
        }
        Ok(())
    }

    pub(crate) fn create_one(&self, input: &Values) -> Result<Statement, Error> {
        self.known_members(input, "create")?;
        let sealed = self.sealed(self.stamped(input)?, "create")?;
        self.insert(&sealed)
    }

    /// The insert of one row whose members are stamped and sealed.
    fn insert(&self, sealed: &Values) -> Result<Statement, Error> {
        let cells = self.insert_cells(sealed)?;
        let mut args = Vec::with_capacity(cells.len());
        let mut columns = Vec::with_capacity(cells.len());
        let mut placeholders = Vec::with_capacity(cells.len());
        for (column, bound) in cells {
            columns.push(column.column.as_str());
            args.push(bound);
            placeholders.push(self.placeholder(args.len())?);
        }
        let s = &self.table.statements;
        let sql = fill(
            &s.insert,
            &[&columns.join(", "), &placeholders.join(", "), &s.columns],
        )?;
        Ok(Statement { sql, args })
    }

    /// The columns a batch's rows have cells for, in CreateMany's order:
    /// the fields, the key at its place among them, then the relations
    /// (`sqlutil.TableModel.InsertColumns`).
    fn insert_order(&self) -> Vec<&'a Column> {
        let columns = &self.table.columns;
        columns
            .iter()
            .filter(|c| c.relation.is_none() && (c.key || Self::insertable(c)))
            .chain(columns.iter().filter(|c| c.relation.is_some()))
            .collect()
    }

    /// The statements of a batch insert: one, each row a tuple of
    /// placeholders and DEFAULT, on a dialect whose VALUES take DEFAULT,
    /// and one a row, in order, on one whose do not.
    pub(crate) fn create_many(&self, inputs: &[Values]) -> Result<Vec<Statement>, Error> {
        let c = &self.definition.clauses;
        let mut sealed = Vec::with_capacity(inputs.len());
        for input in inputs {
            self.known_members(input, "create")?;
            sealed.push(self.sealed(self.stamped(input)?, "create")?);
        }
        if c.default.is_empty() {
            return sealed.iter().map(|row| self.insert(row)).collect();
        }
        let order = self.insert_order();
        let mut rows = Vec::with_capacity(sealed.len());
        for row in &sealed {
            let mut cells = Vec::with_capacity(order.len());
            for column in &order {
                cells.push(self.insert_value(row, column)?);
            }
            rows.push(cells);
        }
        let inserted: Vec<bool> = (0..order.len())
            .map(|i| rows.iter().any(|row| row[i].is_some()))
            .collect();
        let columns: Vec<&str> = order
            .iter()
            .zip(&inserted)
            .filter(|(_, inserted)| **inserted)
            .map(|(column, _)| column.column.as_str())
            .collect();
        let mut args = Vec::new();
        let mut tuples = Vec::with_capacity(rows.len());
        for row in rows {
            let mut cells = Vec::with_capacity(columns.len());
            for (cell, inserted) in row.into_iter().zip(&inserted) {
                if !inserted {
                    continue;
                }
                match cell {
                    Some(bound) => {
                        args.push(bound);
                        cells.push(self.placeholder(args.len())?);
                    }
                    None => cells.push(c.default.clone()),
                }
            }
            tuples.push(format!("({})", cells.join(", ")));
        }
        let s = &self.table.statements;
        let sql = fill(
            &s.insert_many,
            &[&columns.join(", "), &tuples.join(", "), &s.columns],
        )?;
        Ok(vec![Statement { sql, args }])
    }

    /// The assignments of `update`, each value appended to `args`, in
    /// UpdateOne's order: the fields, the relations' keys. `compared`
    /// takes, for an upsert only if changed, each column it sets and the
    /// value it sets (D89).
    fn set_clauses(
        &self,
        update: &Values,
        args: &mut Vec<Bound>,
        mut compared: Option<&mut (Vec<String>, Vec<String>)>,
    ) -> Result<Vec<String>, Error> {
        let c = &self.definition.clauses;
        let table = self.table;
        for member in update.keys() {
            let settable = table.column(member).is_some_and(|column| {
                !column.key
                    && !column.internal
                    && (column.audit.is_none() || column.audit.as_deref() == Some("updatedBy"))
            });
            if !settable {
                return Err(invalid(format!(
                    "update {}: it sets no member {member}",
                    table.name
                )));
            }
        }
        let settable = |column: &&Column| !column.key && column.audit.is_none() && !column.internal;
        let ordered = table
            .columns
            .iter()
            .filter(|column| column.relation.is_none())
            .filter(settable)
            .chain(
                table
                    .columns
                    .iter()
                    .filter(|column| column.relation.is_some()),
            );
        let mut sets = Vec::new();
        for column in ordered {
            let Some(value) = update.get(&column.name) else {
                continue;
            };
            if value.is_null() && !column.json_null() {
                if column.required {
                    return Err(invalid(format!(
                        "update {}: {} cannot be null",
                        table.name, column.name
                    )));
                }
                sets.push(fill(&c.set_null, &[&column.column])?);
                if let Some(compared) = compared.as_deref_mut() {
                    compared.0.push(column.qualified.clone());
                    compared.1.push(c.null.clone());
                }
                continue;
            }
            let mut bound = self.bind(value, column, "update")?;
            if value.is_null() {
                bound.marks.json = true;
            }
            args.push(bound);
            sets.push(fill(&c.set, &[&column.column, &args.len()])?);
            if let Some(compared) = compared.as_deref_mut() {
                compared.0.push(column.qualified.clone());
                compared.1.push(self.placeholder(args.len())?);
            }
        }
        Ok(sets)
    }

    /// The stamps an update writes after its assignments: updatedAt, and
    /// updatedBy, the update's own when it names one, else the acting
    /// user.
    fn stamp(
        &self,
        sets: &mut Vec<String>,
        update: &Values,
        args: &mut Vec<Bound>,
    ) -> Result<(), Error> {
        let s = &self.table.statements;
        if self.table.audit("updatedAt").is_some() {
            args.push(Bound {
                value: Value::String(self.session.now()),
                marks: Marks::TIMESTAMP,
            });
            sets.push(fill(&s.updated_at, &[&args.len()])?);
        }
        if let Some(updated_by) = self.table.audit("updatedBy") {
            let by = match update
                .get(&updated_by.name)
                .filter(|value| !value.is_null())
            {
                Some(by) => Some(by.clone()),
                None => self.user().map(Value::String),
            };
            match by {
                Some(by) => {
                    args.push(self.bind(&by, updated_by, "updatedBy")?);
                    sets.push(fill(&s.updated_by, &[&args.len()])?);
                }
                None if self.definition.require_updated_by => return Err(Error::NoUser),
                None => {}
            }
        }
        Ok(())
    }

    /// Refuses preconditions given to a write of a table that composes no
    /// behaviors (D104): nothing would check them.
    pub(crate) fn refuse_preconditions(&self, options: &WriteOptions) -> Result<(), Error> {
        match &options.preconditions {
            Some(preconditions) if !preconditions.is_empty() => Err(invalid(format!(
                "preconditions are a behavior table's (D104): {} composes none",
                self.table.name
            ))),
            _ => Ok(()),
        }
    }

    fn version(&self, options: &WriteOptions) -> Result<Option<Bound>, Error> {
        let Some(version) = options.expected_version else {
            return Ok(None);
        };
        if !self.table.versioned() {
            return Err(invalid(format!(
                "{} holds no _version to fence a write on: it is not @optimistic",
                self.table.name
            )));
        }
        Ok(Some(Bound {
            value: Value::from(version),
            marks: Marks::TEXT,
        }))
    }

    pub(crate) fn update_one(
        &self,
        key: &Value,
        update: &Values,
        options: &WriteOptions,
    ) -> Result<Statement, Error> {
        let key_bound = self.key(key)?;
        let update = self.sealed_update(update.clone(), key, "update")?;
        let mut args = Vec::new();
        let mut sets = self.set_clauses(&update, &mut args, None)?;
        self.stamp(&mut sets, &update, &mut args)?;
        if sets.is_empty() {
            return Err(invalid(format!(
                "update {}: no fields to update",
                self.table.name
            )));
        }
        args.push(key_bound);
        let s = &self.table.statements;
        let mut sql = fill(&s.update, &[&sets.join(", "), &args.len()])?;
        if let Some(version) = self.version(options)? {
            args.push(version);
            sql.push_str(&fill(&s.version_fence, &[&args.len()])?);
        }
        sql.push_str(&s.live);
        sql.push_str(&s.returning);
        Ok(Statement { sql, args })
    }

    /// The read of the live row's `_version`, which tells a fenced
    /// write's miss from a conflict.
    pub(crate) fn version_of(&self, key: &Value) -> Result<Statement, Error> {
        Ok(Statement {
            sql: self.table.statements.version.clone(),
            args: vec![self.key(key)?],
        })
    }

    pub(crate) fn update_many(
        &self,
        filter: Option<&Values>,
        update: &Values,
    ) -> Result<Statement, Error> {
        if let Some(column) = self.table.encrypted().find(|column| {
            update
                .get(&column.name)
                .is_some_and(|value| !value.is_null())
        }) {
            return Err(invalid(format!(
                "update_many of {} cannot set {}, an Encrypted<T> field: each row's envelope is bound to its key; set it with update_one",
                self.table.name, column.name
            )));
        }
        let mut args = Vec::new();
        let mut sets = self.set_clauses(update, &mut args, None)?;
        self.stamp(&mut sets, update, &mut args)?;
        if sets.is_empty() {
            return Err(invalid(format!(
                "update {}: no fields to update",
                self.table.name
            )));
        }
        let conditions = self.conditions(self.table, filter, &mut args)?;
        let where_ = self.live_where(self.where_of(&conditions), false);
        let sql = fill(
            &self.table.statements.update_many,
            &[&sets.join(", "), &where_],
        )?;
        Ok(Statement { sql, args })
    }

    fn unique_key(&self, name: &str) -> Result<&'a UniqueKey, Error> {
        self.table.unique_keys.get(name).ok_or_else(|| {
            let keys: Vec<&str> = self.table.unique_keys.keys().map(String::as_str).collect();
            invalid(format!(
                "{} has no unique key {name:?}; its keys are {}",
                self.table.name,
                keys.join("; ")
            ))
        })
    }

    /// The columns of the unique key `name`, in its fields' order.
    fn key_columns(&self, name: &str) -> Result<Vec<&'a Column>, Error> {
        name.split(',')
            .map(|field| {
                self.table
                    .columns
                    .iter()
                    .find(|column| column.field == field)
                    .ok_or_else(|| {
                        Error::Definition(format!(
                            "{}'s unique key {name} names no column {field}",
                            self.table.name
                        ))
                    })
            })
            .collect()
    }

    /// The read of the row that holds the unique key's value of `sealed`,
    /// an input as its insert binds it: by the key's columns, or, for a
    /// key over an expression, by the expression's value over them
    /// (D105). None when the input leaves a column of the key unset.
    fn held(
        &self,
        name: &str,
        key: &UniqueKey,
        sealed: &Values,
    ) -> Result<Option<Statement>, Error> {
        let mut args = Vec::new();
        for column in self.key_columns(name)? {
            match self.insert_value(sealed, column)? {
                Some(bound) => args.push(bound),
                None => return Ok(None),
            }
        }
        if key.rows.is_empty() {
            if key.held.is_empty() {
                return Err(Error::Definition(format!(
                    "{}'s unique key {name}, over an expression, has no read of the row that holds it",
                    self.table.name
                )));
            }
            return Ok(Some(Statement {
                sql: key.held.clone(),
                args,
            }));
        }
        let mut placeholders = Vec::with_capacity(args.len());
        for n in 1..=args.len() {
            placeholders.push(self.placeholder(n)?);
        }
        let tuple = format!("({})", placeholders.join(", "));
        Ok(Some(Statement {
            sql: fill(&key.rows, &[&tuple])?,
            args,
        }))
    }

    pub(crate) fn upsert_one(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
        only_if_changed: bool,
    ) -> Result<UpsertPlan, Error> {
        let table = self.table;
        if let Some(behaviors) = &table.behaviors {
            if behaviors.no_upsert {
                return Err(invalid(format!(
                    "upsert_one of {}: the table's behaviors keep sub-tables or arm deadlines, which an upsert's insert would leave unwritten, so the table has no upserts (D103); create the row with create_one",
                    table.name
                )));
            }
            return Err(invalid(format!(
                "upsert_one of {}: a table that composes behaviors writes its rows through the core (D103); create the row with create_one and update it with update_one",
                table.name
            )));
        }
        self.known_members(input, "upsert")?;
        let name = on.join(",");
        let key = self.unique_key(&name)?;
        if only_if_changed && key.rows.is_empty() {
            return Err(invalid(format!(
                "upsert_one of {} on {name}, a unique index over an expression, cannot read back the row it leaves as it was; upsert on another key",
                table.name
            )));
        }
        let sealed = self.sealed(self.stamped(input)?, "upsert")?;
        let cells = self.insert_cells(&sealed)?;
        let mut args = Vec::with_capacity(cells.len());
        let mut columns = Vec::with_capacity(cells.len());
        let mut placeholders = Vec::with_capacity(cells.len());
        for (column, bound) in cells {
            columns.push(column.column.as_str());
            args.push(bound);
            placeholders.push(self.placeholder(args.len())?);
        }
        // An encrypted field the update sets is sealed under the key of
        // the row it updates, which only the upsert's statement names, so
        // a second statement sets it (D89).
        let mut plain = update.clone();
        let mut sealing = Values::new();
        for column in table.encrypted() {
            if let Some(value) = plain.remove(&column.name) {
                sealing.insert(column.name.clone(), value);
            }
        }
        let mut compared = (Vec::new(), Vec::new());
        let sets = self.set_clauses(&plain, &mut args, Some(&mut compared))?;
        let c = &self.definition.clauses;
        let s = &table.statements;
        let mut assignments = sets.clone();
        if sets.is_empty() {
            assignments.push(key.keep.clone());
        } else {
            self.stamp(&mut assignments, update, &mut args)?;
        }
        // The WHERE of the update: the live rows, and, only if changed or
        // for an update that sets nothing, the rows whose columns differ
        // from the values it sets, none when it compares none.
        let mut conditions = Vec::new();
        if table.soft_delete {
            conditions.push(s.live_condition.clone());
        }
        if only_if_changed || sets.is_empty() {
            conditions.push(if compared.0.is_empty() {
                c.never.clone()
            } else {
                fill(
                    &c.changed,
                    &[&compared.0.join(", "), &compared.1.join(", ")],
                )?
            });
        }
        let tuple = format!("({})", placeholders.join(", "));
        let sql = fill(
            &s.upsert,
            &[
                &columns.join(", "),
                &tuple,
                &key.conflict,
                &assignments.join(", "),
                &self.where_of(&conditions),
            ],
        )?;
        let held = self.held(&name, key, &sealed)?;
        Ok(UpsertPlan {
            before: match self.definition.dialect {
                Dialect::Sqlite => held.clone(),
                Dialect::Postgres => None,
            },
            statement: Statement { sql, args },
            sets: !sets.is_empty(),
            held,
            sealing,
        })
    }

    /// A delete of the live row whose key is `key`: a soft-deletable
    /// table's stamped, any other's removed.
    pub(crate) fn delete_one(
        &self,
        key: &Value,
        options: &WriteOptions,
    ) -> Result<DeletePlan, Error> {
        if !self.table.soft_delete {
            return self.hard_delete_one(key, options);
        }
        let user = self.require_user()?;
        let key = self.key(key)?;
        let s = &self.table.statements;
        let mut args = vec![Bound {
            value: Value::String(self.session.now()),
            marks: Marks::TIMESTAMP,
        }];
        if let Some(deleted_by) = self.table.audit("deletedBy") {
            args.push(self.bind(&Value::String(user), deleted_by, "deletedBy")?);
        }
        args.push(key);
        let sql = match self.version(options)? {
            Some(version) => {
                args.push(version);
                s.soft_delete_if_version.clone()
            }
            None => s.soft_delete.clone(),
        };
        Ok(DeletePlan {
            statement: Statement { sql, args },
            clear: None,
        })
    }

    /// A removal of the row whose key is `key`, deleted or not. A
    /// versioned table with an actor column's follows the statement that
    /// sets the transaction's history actor, whose parameter is the acting
    /// user, which the history trigger reads the tombstone's actor from.
    pub(crate) fn hard_delete_one(
        &self,
        key: &Value,
        options: &WriteOptions,
    ) -> Result<DeletePlan, Error> {
        let s = &self.table.statements;
        let mut args = Vec::new();
        let mut prefix = "";
        let mut clear = None;
        if self.table.history_actor {
            args.push(Bound {
                value: self.user().map_or(Value::Null, Value::String),
                marks: Marks::TEXT,
            });
            prefix = &self.definition.history_actor_cte;
            clear = Some(Statement {
                sql: self.definition.clear_history_actor.clone(),
                args: Vec::new(),
            });
        }
        args.push(self.key(key)?);
        let delete = match self.version(options)? {
            Some(version) => {
                args.push(version);
                &s.delete_if_version
            }
            None => &s.delete,
        };
        Ok(DeletePlan {
            statement: Statement {
                sql: format!("{prefix}{delete}"),
                args,
            },
            clear,
        })
    }

    /// The read of the live row whose key is `key`.
    pub(crate) fn get_one(&self, key: &Value) -> Result<Statement, Error> {
        let s = &self.table.statements;
        Ok(Statement {
            sql: format!("{}{}", s.get_one, s.live),
            args: vec![self.key(key)?],
        })
    }

    // ---- Behaviors (D103) ----

    /// The members of the columns the table's behaviors own.
    fn owned(&self) -> &[String] {
        self.table
            .behaviors
            .as_ref()
            .map_or(&[], |behaviors| behaviors.owned.as_slice())
    }

    /// The fields of `input` as the core reads them, by member: each
    /// column's member the input names but an encrypted one, whose
    /// plaintext the core has no use for.
    fn core_fields(&self, input: &Values) -> Values {
        let mut fields = Values::new();
        for column in &self.table.columns {
            if column.encrypted || column.internal || column.audit.is_some() {
                continue;
            }
            if let Some(value) = input.get(&column.name) {
                fields.insert(column.name.clone(), value.clone());
            }
        }
        fields
    }

    /// A create of a row through the core: its fields, and its own
    /// columns as the insert binds them, every column a create writes but
    /// the key, which the command names, and the owned columns, which the
    /// core sets.
    pub(crate) fn core_create(&self, input: &Values) -> Result<CoreCreate<'a>, Error> {
        self.known_members(input, "create")?;
        let sealed = self.sealed(self.stamped(input)?, "create")?;
        let table = self.table;
        let owned = self.owned();
        let mut columns = Vec::new();
        for column in &table.columns {
            if column.key || !Self::insertable(column) || owned.contains(&column.name) {
                continue;
            }
            if let Some(bound) = self.insert_value(&sealed, column)? {
                columns.push((column.unquoted().to_owned(), bound));
            }
        }
        let key = match sealed.get(&table.key) {
            Some(Value::String(text)) if !text.is_empty() => Some(text.clone()),
            Some(Value::Number(number)) => Some(number.to_string()),
            _ => None,
        };
        Ok(CoreCreate {
            table: &table.name,
            key,
            fields: self.core_fields(input),
            columns,
            caller: self.session.caller(),
        })
    }

    /// An update of a row through the core: the fields it sets, and its
    /// own columns as the update binds them, with updatedAt and updatedBy;
    /// an owned column is left to the core, which refuses it.
    pub(crate) fn core_update<'p>(
        &self,
        key: &Value,
        update: &Values,
        options: &'p WriteOptions,
    ) -> Result<CoreUpdate<'p>, Error>
    where
        'a: 'p,
    {
        let table = self.table;
        let sealed = self.sealed_update(update.clone(), key, "update")?;
        let owned = self.owned();
        let mut columns = Vec::new();
        for column in &table.columns {
            if column.key
                || column.audit.is_some()
                || column.internal
                || owned.contains(&column.name)
            {
                continue;
            }
            let Some(value) = sealed.get(&column.name) else {
                continue;
            };
            if value.is_null() && !column.json_null() {
                if column.required {
                    return Err(invalid(format!(
                        "update {}: {} cannot be null",
                        table.name, column.name
                    )));
                }
                columns.push((column.unquoted().to_owned(), None));
                continue;
            }
            columns.push((
                column.unquoted().to_owned(),
                Some(self.bind(value, column, "update")?),
            ));
        }
        let fields = self.core_fields(update);
        if fields.is_empty() && columns.is_empty() {
            return Err(invalid(format!(
                "update {}: no fields to update",
                table.name
            )));
        }
        if let Some(updated_at) = table.audit("updatedAt") {
            columns.push((
                updated_at.unquoted().to_owned(),
                Some(Bound {
                    value: Value::String(self.session.now()),
                    marks: Marks::TIMESTAMP,
                }),
            ));
        }
        if let Some(updated_by) = table.audit("updatedBy") {
            let by = match sealed
                .get(&updated_by.name)
                .filter(|value| !value.is_null())
            {
                Some(by) => Some(by.clone()),
                None => self.user().map(Value::String),
            };
            match by {
                Some(by) => columns.push((
                    updated_by.unquoted().to_owned(),
                    Some(self.bind(&by, updated_by, "updatedBy")?),
                )),
                None if self.definition.require_updated_by => return Err(Error::NoUser),
                None => {}
            }
        }
        Ok(CoreUpdate {
            table: &table.name,
            key: self.wire_key(key)?,
            fields,
            columns,
            preconditions: options.preconditions.as_ref(),
            caller: self.session.caller(),
        })
    }

    /// A delete of a row through the core: a soft-deletable table's live
    /// row stamped deleted in place of deleted.
    pub(crate) fn core_delete<'p>(
        &self,
        key: &Value,
        options: &'p WriteOptions,
    ) -> Result<CoreDelete<'p>, Error>
    where
        'a: 'p,
    {
        let table = self.table;
        let soft_delete = if table.soft_delete {
            let user = self.require_user()?;
            let mut stamps = Vec::new();
            if let Some(deleted_at) = table.audit("deletedAt") {
                stamps.push((
                    deleted_at.unquoted().to_owned(),
                    Bound {
                        value: Value::String(self.session.now()),
                        marks: Marks::TIMESTAMP,
                    },
                ));
            }
            if let Some(deleted_by) = table.audit("deletedBy") {
                stamps.push((
                    deleted_by.unquoted().to_owned(),
                    self.bind(&Value::String(user), deleted_by, "deletedBy")?,
                ));
            }
            Some(stamps)
        } else {
            None
        };
        Ok(CoreDelete {
            table: &table.name,
            key: self.wire_key(key)?,
            soft_delete,
            preconditions: options.preconditions.as_ref(),
            caller: self.session.caller(),
        })
    }

    /// The key a command names, in its wire form.
    fn wire_key(&self, key: &Value) -> Result<String, Error> {
        match key {
            Value::String(text) if !text.is_empty() => Ok(text.clone()),
            Value::Number(number) => Ok(number.to_string()),
            _ => Err(invalid(format!(
                "{} is keyed by {}, not {key}",
                self.table.name, self.table.key
            ))),
        }
    }
}

/// Holds `value` to what `column` holds, as far as its JSON form says: a
/// UUID that reads as one, a boolean that is one. The dialect's client
/// refuses the rest as it writes the value.
fn check(value: &Value, column: &Column) -> Result<(), String> {
    if column.json || column.list || value.is_null() {
        return Ok(());
    }
    match value {
        Value::String(text) if column.uuid && parse_uuid(text).is_none() => {
            Err(format!("{text:?} is no UUID"))
        }
        Value::Array(_) | Value::Object(_) => Err(format!("{value} is no value of the column")),
        _ if column.boolean && !value.is_boolean() => Err(format!("{value} is no boolean")),
        _ => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fill_reads_the_verbs_sqlutil_writes() {
        assert_eq!(
            fill("SELECT %s FROM t%s LIMIT %d", &[&"a, b", &" WHERE x", &5]).unwrap(),
            "SELECT a, b FROM t WHERE x LIMIT 5"
        );
        assert_eq!(fill("LIKE '100%%' %s", &[&"x"]).unwrap(), "LIKE '100%' x");
        assert!(fill("%s %s", &[&"a"]).is_err());
        assert!(fill("%v", &[&"a"]).is_err());
    }
}
