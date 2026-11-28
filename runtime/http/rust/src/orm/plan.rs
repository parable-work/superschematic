//! A repository's statements, filled in the order the Go ORM fills them
//! (D79): what both dialects' clients run. Nothing here reads or writes a
//! database: each function answers a statement's text and the values it
//! binds, in the wire form, with what each one's column holds, which the
//! dialect's client writes as its database reads it.

use std::fmt::Display;

use serde_json::Value;

use super::{
    audience_conditions, invalid, Bound, Column, CoreCreate, CoreDelete, CoreOperation,
    CoreTableOperation, CoreUpdate, Definition, Dialect, Error, Fence, FieldCell, FieldKeys,
    FindOptions, Marks, OrderBy, PageOptions, Session, Table, UniqueKey, Values, WriteOptions,
};
use crate::search::{like_pattern, terms};
use crate::uuid62::{parse_uuid, uuid_to_base62};
use serde_json::Map;

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

/// The assignments that advance `fences` in an update whose assignments
/// are `sets`, each of which begins with its column and ` = ` (D119): the
/// bump of each fence that covers a column one of them sets, or of a fence
/// with no covers when they set any, once. Whether a value differs from
/// the stored one is not asked, which the statement cannot know. A fence
/// the update sets itself gets none: its caller is driving the counter,
/// and the fence's trigger judges the value written.
pub(crate) fn fence_bumps(fences: &[Fence], sets: &[String]) -> Vec<String> {
    let assigned = |column: &String| {
        sets.iter().any(|set| {
            set.strip_prefix(column.as_str())
                .is_some_and(|rest| rest.starts_with(" = "))
        })
    };
    fences
        .iter()
        .filter(|fence| !assigned(&fence.column))
        .filter(|fence| {
            if fence.covers.is_empty() {
                !sets.is_empty()
            } else {
                fence.covers.iter().any(assigned)
            }
        })
        .map(|fence| fence.bump.clone())
        .collect()
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

/// An upsert of many rows (D89): its batches, in the inputs' order, each
/// as many rows as one statement binds the parameters of.
pub(crate) struct UpsertManyPlan<'a> {
    pub(crate) batches: Vec<UpsertBatch>,
    /// Whether the update sets a field: a row a statement did not return
    /// is then deleted, unless only if changed left it.
    pub(crate) sets: bool,
    /// The encrypted columns the update takes from each input, which a
    /// second statement seals under the key of the row it updates (D89).
    pub(crate) sealing: Vec<&'a Column>,
    /// The unique key, and its columns in its fields' order, by whose
    /// values a returned row is matched to its input.
    pub(crate) key: &'a UniqueKey,
    pub(crate) key_columns: Vec<&'a Column>,
}

/// A batch of an upsert of many rows.
pub(crate) struct UpsertBatch {
    /// The batch's inputs, by their index among the upsert's.
    pub(crate) inputs: Vec<usize>,
    /// Each input's value of the key as text (`key_text`), or none for one
    /// that leaves a column of it unset, whose key the database writes.
    pub(crate) keys: Vec<Option<String>>,
    /// Each input's cells of the key's columns, by which the rows the
    /// statements left are read back; none as for `keys`.
    pub(crate) key_cells: Vec<Option<Vec<Bound>>>,
    /// The read of the rows that hold the batch's keys, before its
    /// statements, on a dialect whose upsert does not say whether it
    /// inserted; none when no input sets the key.
    pub(crate) before: Option<Statement>,
    /// The statements, each with the inputs whose rows it writes, by their
    /// place in the batch: one on Postgres, one per set of columns the
    /// inputs set on a dialect whose VALUES take no DEFAULT.
    pub(crate) statements: Vec<(Statement, Vec<usize>)>,
}

/// The text a key column's value matches by: a UUID in base62, whichever
/// form it was written in, a string as it is, and any other value its JSON;
/// none for null.
pub(crate) fn key_text(column: &Column, value: &Value) -> Option<String> {
    match value {
        Value::Null => None,
        Value::String(text) if column.uuid => {
            Some(parse_uuid(text).map_or_else(|| text.clone(), uuid_to_base62))
        }
        Value::String(text) => Some(text.clone()),
        other => Some(other.to_string()),
    }
}

/// The text a row's value of a key matches by, its columns' `key_text`s
/// joined; none when one of them is null, which no unique index holds.
pub(crate) fn row_key_text(columns: &[&Column], row: &Values) -> Option<String> {
    let mut parts = Vec::with_capacity(columns.len());
    for column in columns {
        parts.push(key_text(
            column,
            row.get(&column.name).unwrap_or(&Value::Null),
        )?);
    }
    Some(parts.join("\u{1f}"))
}

/// The most parameters an upsert's update stamps beside its rows:
/// updatedAt and updatedBy (`sqlutil.UpsertStamps`).
const UPSERT_STAMPS: usize = 2;

/// An upsert of a table that composes behaviors (D103, D116): the read of
/// whether a row holds the key, before the core's create or update.
pub(crate) struct CoreUpsertPlan {
    /// The read of the rows that hold the input's value of the key; none
    /// when the input leaves a column of the key unset, which no row then
    /// holds.
    pub(crate) held: Option<Statement>,
    /// Whether the update sets a field: one that sets none leaves a row
    /// that holds the key as it is (D105).
    pub(crate) sets: bool,
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

    /// One of a merge's statements with its arguments (D147): the key of
    /// the row merged away, the key of the row that stays, and, for one
    /// that takes it, the acting user in the form its column holds, null
    /// for none.
    pub(crate) fn merge_statement(
        &self,
        statement: &super::MergeStatement,
        from: &Value,
        into: &Value,
    ) -> Result<Statement, Error> {
        let mut args = vec![self.key(from)?, self.key(into)?];
        if let Some(actor) = &statement.actor {
            let marks = if actor == "uuid" {
                Marks {
                    uuid: true,
                    ..Marks::TEXT
                }
            } else {
                Marks::TEXT
            };
            args.push(Bound {
                value: self.user().map_or(Value::Null, Value::String),
                marks,
            });
        }
        Ok(Statement {
            sql: statement.sql.clone(),
            args,
        })
    }

    /// The acting user a merge that stamps a row deleted needs (D147).
    pub(crate) fn merge_user(&self) -> Result<String, Error> {
        self.require_user()
    }

    /// The read of a row of a merge's step the caller may not write
    /// (D156): one of the step's rows, those that reference `from`, that
    /// the conditions of a write of their table leave out. None for
    /// repositories opened for no caller, and for a table no audience or
    /// grant holds for the caller, whose rows it may all write.
    pub(crate) fn merge_reach(
        &self,
        reach: &super::MergeReach,
        from: &Value,
    ) -> Result<Option<Statement>, Error> {
        if self.session.access.is_none() {
            return Ok(None);
        }
        let table = self.definition.table(&reach.table)?;
        let mut args = vec![self.key(from)?];
        let conditions = self.access_conditions(table, &mut args, true)?;
        if conditions.is_empty() {
            return Ok(None);
        }
        let c = &self.definition.clauses;
        let writable = fill(&c.group, &[&conditions.join(&c.and)])?;
        Ok(Some(Statement {
            sql: format!("{} AND NOT COALESCE({writable}, FALSE) LIMIT 1", reach.rows),
            args,
        }))
    }

    /// The statement that sets the history actor for a merge's
    /// transaction, whose steps delete a @versioned table's rows (D147).
    pub(crate) fn merge_history_actor(&self) -> Statement {
        Statement {
            sql: format!(
                "{}SELECT _history_actor FROM history_actor",
                self.definition.history_actor_cte
            ),
            args: vec![Bound {
                value: self.user().map_or(Value::Null, Value::String),
                marks: Marks::TEXT,
            }],
        }
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

    /// Whether the reads of `table` keep to a caller now (D156): an
    /// `@audience` table's, or any table's for a caller with grants, when
    /// the repositories were opened for a caller.
    pub(crate) fn kept(&self, table: &Table) -> bool {
        self.session
            .access
            .as_ref()
            .is_some_and(|sees| table.audience.is_some() || sees.grants.is_some())
    }

    /// The filter's conditions, each value appended to `args`, as the Go
    /// ORM's `conditions` writes them, then the conditions that keep the
    /// rows to what the repositories' caller sees (D156,
    /// [`Plan::access_conditions`]).
    fn conditions(
        &self,
        table: &Table,
        filter: Option<&Values>,
        args: &mut Vec<Bound>,
    ) -> Result<Vec<String>, Error> {
        self.conditions_for(table, filter, args, false)
    }

    /// The caller's access to `table`'s rows alone, of a read: what a read
    /// across tables holds each table's rows to (D188, #615).
    #[cfg(any(feature = "postgres", feature = "sqlite"))]
    pub(crate) fn read_conditions(
        &self,
        table: &Table,
        args: &mut Vec<Bound>,
    ) -> Result<Vec<String>, Error> {
        self.access_conditions(table, args, false)
    }

    /// The filter's conditions, then the caller's access, of a read, or
    /// with `write` of a write.
    fn conditions_for(
        &self,
        table: &Table,
        filter: Option<&Values>,
        args: &mut Vec<Bound>,
        write: bool,
    ) -> Result<Vec<String>, Error> {
        let mut conditions = self.filter_conditions(table, filter, args, true)?;
        conditions.extend(self.access_conditions(table, args, write)?);
        Ok(conditions)
    }

    /// The conditions that keep a read, or with `write` a write, of `table`
    /// to what the caller sees (D156), as the TypeScript repositories'
    /// `accessConditions` writes them: an `@audience` table's audience
    /// ([`audience_conditions`]), and for a party or an agent run, the rows
    /// one of its grants reaches, its target and the rows its role's paths
    /// lead to it from. None for repositories opened for no caller, and on a
    /// table no audience holds for a caller with no grants. A table no grant
    /// reaches has no row for a caller with grants.
    fn access_conditions(
        &self,
        table: &Table,
        args: &mut Vec<Bound>,
        write: bool,
    ) -> Result<Vec<String>, Error> {
        let Some(sees) = &self.session.access else {
            return Ok(Vec::new());
        };
        let c = &self.definition.clauses;
        let mut out = Vec::new();
        if let Some(audience) = &table.audience {
            out.extend(audience_conditions(
                self.definition,
                audience,
                sees,
                args,
                write,
            )?);
        }
        let Some(grants) = &sees.grants else {
            return Ok(out);
        };
        let start = args.len();
        let mut branches = Vec::new();
        for grant in grants {
            if grant.target_type == table.name {
                let key = table.key_column();
                let target = Value::String(grant.target_id.clone());
                check(&target, key).map_err(|err| {
                    invalid(format!("{}.{}: grant target: {err}", table.name, key.name))
                })?;
                args.push(Bound {
                    value: target,
                    marks: Marks::of(key),
                });
                branches.push(fill(&c.eq, &[&key.qualified, &args.len()])?);
            }
            for path in grant.reaches.get(&table.name).into_iter().flatten() {
                // Every row of the table, whatever the other branches
                // bound.
                if path == "*" {
                    args.truncate(start);
                    return Ok(out);
                }
                let Some(filter) =
                    self.reach_filter(table, path, &grant.target_type, &grant.target_id)?
                else {
                    continue;
                };
                let inner = self.filter_conditions(table, Some(&filter), args, false)?;
                if !inner.is_empty() {
                    branches.push(fill(&c.group, &[&inner.join(&c.and)])?);
                }
            }
        }
        // No grant reaches a row of the table: none is there.
        out.push(if branches.is_empty() {
            "FALSE".to_owned()
        } else {
            fill(&c.group, &[&branches.join(&c.or)])?
        });
        Ok(out)
    }

    /// The filter of a role's path from `table` to a grant's target (D156):
    /// `unit.contract` is `{"unit": {"contract": {<key>: {"eq": target}}}}`,
    /// each step a to-one or a one-to-many relation of the table it is on.
    /// None for a path that names no relation, or that ends at another
    /// table than the target's: it reaches no row.
    fn reach_filter(
        &self,
        table: &Table,
        path: &str,
        target_type: &str,
        target_id: &str,
    ) -> Result<Option<Values>, Error> {
        let steps: Vec<&str> = path.split('.').collect();
        let mut at = table;
        for step in &steps {
            let Some(next) = at.relation_target(step) else {
                return Ok(None);
            };
            let Ok(next) = self.definition.table(next) else {
                return Ok(None);
            };
            at = next;
        }
        if at.name != target_type {
            return Ok(None);
        }
        let mut eq = Map::new();
        eq.insert("eq".to_owned(), Value::String(target_id.to_owned()));
        let mut filter = Values::new();
        filter.insert(at.key.clone(), Value::Object(eq));
        for step in steps.iter().rev() {
            let mut outer = Values::new();
            outer.insert((*step).to_owned(), Value::Object(filter));
            filter = outer;
        }
        Ok(Some(filter))
    }

    /// The read of the live row whose key is `key` if the caller sees it,
    /// or with `write` may write it (D156): what a write of a row checks
    /// first, so a row the caller may not write is not there.
    pub(crate) fn reach(&self, key: &Value, write: bool) -> Result<Statement, Error> {
        let table = self.table;
        let column = table.key_column();
        let mut args = vec![self.key(key)?];
        let mut conditions = vec![fill(
            &self.definition.clauses.eq,
            &[&column.qualified, &args.len()],
        )?];
        conditions.extend(self.access_conditions(table, &mut args, write)?);
        let where_ = self.live_where(self.where_of(&conditions), false);
        let s = &table.statements;
        let sql = fill(&s.find_one, &[&s.columns, &where_, &""])?;
        Ok(Statement { sql, args })
    }

    /// The statement that reads which of the keys name rows the caller may
    /// reach (D156), as [`Plan::reach`] reads one: the keys in one `IN`
    /// list, so a batch create checks the rows it made in one read.
    pub(crate) fn reach_many(&self, keys: &[Value], write: bool) -> Result<Statement, Error> {
        let table = self.table;
        let column = table.key_column();
        let mut args = Vec::with_capacity(keys.len());
        let mut placeholders = Vec::with_capacity(keys.len());
        for key in keys {
            args.push(self.key(key)?);
            placeholders.push(self.placeholder(args.len())?);
        }
        let mut conditions = vec![fill(
            &self.definition.clauses.in_,
            &[&column.qualified, &placeholders.join(", ")],
        )?];
        conditions.extend(self.access_conditions(table, &mut args, write)?);
        let where_ = self.live_where(self.where_of(&conditions), false);
        let s = &table.statements;
        let sql = fill(&s.find_many, &[&s.columns, &where_, &"", &""])?;
        Ok(Statement { sql, args })
    }

    /// The conditions of a filter through the table's relations. Through a
    /// to-one relation (D90): the foreign key in the keys of the target's
    /// live rows its filter finds. Through a one-to-many relation (D156):
    /// the key in the keys its rows' foreign key holds, of the rows the
    /// filter finds, so a path to a row's members reaches through a list of
    /// them. Each holds the other table's rows to what the caller sees,
    /// unless `access` is false.
    fn relation_conditions(
        &self,
        table: &Table,
        filter: &Values,
        args: &mut Vec<Bound>,
        access: bool,
    ) -> Result<Vec<String>, Error> {
        let mut conditions = Vec::new();
        let inner_of = |target: &Table, through: &Values, args: &mut Vec<Bound>| {
            let mut inner = if access {
                self.conditions(target, Some(through), args)?
            } else {
                self.filter_conditions(target, Some(through), args, false)?
            };
            if target.soft_delete {
                inner.push(target.statements.live_condition.clone());
            }
            Ok::<_, Error>(self.where_of(&inner))
        };
        for relation in &table.relations {
            let Some(Value::Object(through)) = filter.get(&relation.field) else {
                continue;
            };
            let target = self.definition.table(&relation.target)?;
            let where_ = inner_of(target, through, args)?;
            conditions.push(fill(&relation.probe, &[&where_])?);
        }
        for many in &table.many {
            let Some(Value::Object(through)) = filter.get(&many.name) else {
                continue;
            };
            let source = self.definition.table(&many.target)?;
            let foreign = source.column(&many.column).ok_or_else(|| {
                Error::Definition(format!(
                    "{}'s relation {} reads {}, which {} has no column of",
                    table.name, many.name, many.column, source.name
                ))
            })?;
            let where_ = inner_of(source, through, args)?;
            conditions.push(format!(
                "{} IN (SELECT {} FROM {}{where_})",
                table.key_column().qualified,
                foreign.qualified,
                source.table,
            ));
        }
        Ok(conditions)
    }

    /// The filter's conditions alone. A filter through a relation holds
    /// the target's rows to what the caller sees too, unless `access` is
    /// false, as a grant's path is: its steps are the role's to name.
    fn filter_conditions(
        &self,
        table: &Table,
        filter: Option<&Values>,
        args: &mut Vec<Bound>,
        access: bool,
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
            // A JSON column compares JSON values, by the dialect's own
            // conditions where its column holds text (D116).
            let json = if column.json {
                self.definition.json_clauses.as_ref()
            } else {
                None
            };
            for op in &column.filter {
                let Some(value) = member.get(op).filter(|value| !value.is_null()) else {
                    continue;
                };
                match op.as_str() {
                    "eq" | "ne" | "gt" | "gte" | "lt" | "lte" => {
                        let clause = match (op.as_str(), json) {
                            ("eq", Some(json)) => &json.eq,
                            ("ne", Some(json)) => &json.ne,
                            ("eq", None) => &c.eq,
                            ("ne", None) => &c.ne,
                            ("gt", _) => &c.gt,
                            ("gte", _) => &c.gte,
                            ("lt", _) => &c.lt,
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
                            placeholders.push(match json {
                                Some(json) => fill(&json.value, &[&n])?,
                                None => self.placeholder(n)?,
                            });
                        }
                        let clause = match (op == "in", json) {
                            (true, Some(json)) => &json.in_,
                            (false, Some(json)) => &json.none_of,
                            (true, None) => &c.in_,
                            (false, None) => &c.none_of,
                        };
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
        conditions.extend(self.relation_conditions(table, filter, args, access)?);
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
                conditions.extend(self.filter_conditions(table, each.as_object(), args, access)?);
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
                    let inner = self.filter_conditions(table, branch.as_object(), args, access)?;
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
                || table.many.iter().any(|m| &m.name == member)
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

    /// The keys a reencrypt seals with, refusing a table with no
    /// `Encrypted<T>` field, repositories opened with no keys, and keys that
    /// name no current key.
    fn reencrypt_keys(&self) -> Result<&'a dyn FieldKeys, Error> {
        let table = self.table;
        let s = &table.statements;
        let Some(first) = table.encrypted().next() else {
            return Err(invalid(format!(
                "{} has no Encrypted<T> field to reencrypt",
                table.name
            )));
        };
        if s.stale.is_empty() || s.reseal.is_empty() {
            return Err(Error::Definition(format!(
                "{} has no statements to reencrypt with: regenerate the types crate",
                table.name
            )));
        }
        self.session.keys.ok_or_else(|| {
            Error::Encryption(format!(
                "{}.{} is an Encrypted<T> field, and the repositories were opened with no field keys",
                table.name, first.field
            ))
        })
    }

    /// The read of up to `batch` rows, deleted ones too, in key order, a
    /// column of which another key than the current one sealed (D85), and
    /// the columns it reads: the key, then each encrypted column.
    pub(crate) fn stale(&self, batch: Option<u32>) -> Result<(Statement, Vec<&'a Column>), Error> {
        let keys = self.reencrypt_keys()?;
        let prefix = keys.current_prefix().ok_or_else(|| {
            Error::Encryption(format!(
                "{}: the field keys name no current key, so a reencrypt cannot tell another key's envelope",
                self.table.name
            ))
        })?;
        let batch = batch
            .filter(|batch| *batch > 0)
            .unwrap_or(self.definition.default_limit);
        let columns = std::iter::once(self.table.key_column())
            .chain(self.table.encrypted())
            .collect();
        let statement = Statement {
            sql: self.table.statements.stale.clone(),
            args: vec![
                Bound {
                    value: Value::String(prefix),
                    marks: Marks::TEXT,
                },
                Bound {
                    value: Value::from(batch),
                    marks: Marks::TEXT,
                },
            ],
        };
        Ok((statement, columns))
    }

    /// The reseal of a row the stale read returned, its key and then the
    /// envelope each encrypted column held: each envelope another key
    /// sealed is opened and sealed again under the current key, for the
    /// row, and the statement sets them on the row when each column still
    /// holds what was read, so a write between the two keeps its own.
    pub(crate) fn reseal(&self, cells: &[Value]) -> Result<Statement, Error> {
        let keys = self.reencrypt_keys()?;
        let table = self.table;
        let key = cells.first().cloned().unwrap_or(Value::Null);
        let row = self.cell_key(&key).ok_or_else(|| {
            Error::Definition(format!(
                "a row of {} holds no {}, which its envelopes are bound to",
                table.name, table.key
            ))
        })?;
        let mut sealed = Vec::new();
        let mut held = Vec::new();
        for (i, column) in table.encrypted().enumerate() {
            let envelope = cells.get(i + 1).cloned().unwrap_or(Value::Null);
            let next = match &envelope {
                Value::String(text) if !keys.is_current(text) => {
                    let cell = FieldCell {
                        table: &table.name,
                        field: &column.field,
                        row: &row,
                    };
                    let plaintext = keys.open(cell, text).map_err(|err| {
                        Error::Encryption(format!("{}.{}: {err}", table.name, column.field))
                    })?;
                    let resealed = keys.seal(cell, &plaintext).map_err(|err| {
                        Error::Encryption(format!("{}.{}: {err}", table.name, column.field))
                    })?;
                    Value::String(resealed)
                }
                other => other.clone(),
            };
            sealed.push(Bound {
                value: next,
                marks: Marks::of(column),
            });
            held.push(Bound {
                value: envelope,
                marks: Marks::of(column),
            });
        }
        let mut args = sealed;
        args.push(self.key(&key)?);
        args.extend(held);
        Ok(Statement {
            sql: table.statements.reseal.clone(),
            args,
        })
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

    /// The stamps an update writes after its assignments: updatedAt, the
    /// database's clock, which its insert's default read too, so the
    /// host's can't stamp a row earlier than its insert; and updatedBy, the
    /// update's own when it names one, else the acting user.
    fn stamp(
        &self,
        sets: &mut Vec<String>,
        update: &Values,
        args: &mut Vec<Bound>,
    ) -> Result<(), Error> {
        let s = &self.table.statements;
        if self.table.audit("updatedAt").is_some() {
            sets.push(fill(&s.updated_at, &[])?);
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

    /// Adds to an update's assignments, its stamps among them, the bump
    /// of each fence they advance (D119).
    fn advance(&self, sets: &mut Vec<String>) {
        let bumps = fence_bumps(&self.table.fences, sets);
        sets.extend(bumps);
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
        self.advance(&mut sets);
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
        self.advance(&mut sets);
        let conditions = self.conditions_for(self.table, filter, &mut args, true)?;
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
        if table.behaviors.is_some() {
            return Err(Error::Definition(format!(
                "upsert_one of {}: a table that composes behaviors upserts through the core (D103), with core_upsert",
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
            self.advance(&mut assignments);
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

    /// An upsert of many rows (D89): each input inserted, or, when a row
    /// holds its value of the unique key `on`, each field `fields` names,
    /// by member or by field, set to the value the input would have
    /// inserted, its default or NULL for one it leaves unset; the stamps
    /// and fences as an update's. A batch is as many rows as one statement
    /// binds the parameters of, or `options.batch`. On a dialect whose
    /// VALUES take no DEFAULT, SQLite's, a batch is a statement per set of
    /// columns its inputs set, and the rows that hold its keys are read
    /// before them, which say which rows each inserted. Two inputs of one
    /// batch that hold one key are refused before anything runs, as
    /// Postgres refuses to change a row twice in one statement.
    pub(crate) fn upsert_many(
        &self,
        inputs: &[Values],
        on: &[&str],
        fields: &[&str],
        options: super::UpsertManyOptions,
    ) -> Result<UpsertManyPlan<'a>, Error> {
        let table = self.table;
        if table.behaviors.is_some() {
            return Err(invalid(format!(
                "upsert_many of {}: a table that composes behaviors upserts through the core (D103), a row at a time: upsert each with upsert_one",
                table.name
            )));
        }
        let c = &self.definition.clauses;
        if c.set_excluded.is_empty() || c.excluded.is_empty() || self.definition.max_parameters == 0
        {
            return Err(Error::Definition(format!(
                "upsert_many of {}: the definition carries no upsert of many rows; regenerate the types crate",
                table.name
            )));
        }
        let name = on.join(",");
        let key = self.unique_key(&name)?;
        if key.rows.is_empty() {
            return Err(invalid(format!(
                "upsert_many of {} on {name}, a unique index over an expression, cannot read back the rows it leaves as they were or match the rows it returns to its inputs by their columns; upsert on another key, or with upsert_one alone",
                table.name
            )));
        }
        let settable = |column: &&Column| !column.key && column.audit.is_none() && !column.internal;
        for named in fields {
            let found = table
                .columns
                .iter()
                .find(|column| column.name == *named || column.field == *named);
            if !found.is_some_and(|column| settable(&column)) {
                return Err(invalid(format!(
                    "upsert_many of {} sets the fields an update sets, not {named:?}",
                    table.name
                )));
            }
        }
        // In UpdateOne's order, whatever the order `fields` names them in:
        // the fields, then the relations' keys.
        let named = |column: &&Column| {
            fields
                .iter()
                .any(|named| column.name == *named || column.field == *named)
        };
        let chosen: Vec<&'a Column> = table
            .columns
            .iter()
            .filter(|column| column.relation.is_none())
            .chain(
                table
                    .columns
                    .iter()
                    .filter(|column| column.relation.is_some()),
            )
            .filter(settable)
            .filter(named)
            .collect();
        let (sealing, plain): (Vec<&'a Column>, Vec<&'a Column>) =
            chosen.into_iter().partition(|column| column.encrypted);
        if options.batch == Some(0) {
            return Err(invalid(format!(
                "upsert_many writes 1 or more {} rows a statement, not 0",
                table.name
            )));
        }
        let order = self.insert_order();
        let most = ((self.definition.max_parameters - UPSERT_STAMPS) / order.len().max(1)).max(1);
        let batch = options.batch.unwrap_or(most).min(most);
        let key_columns = self.key_columns(&name)?;

        let mut rows = Vec::with_capacity(inputs.len());
        for input in inputs {
            self.known_members(input, "upsert")?;
            let sealed = self.sealed(self.stamped(input)?, "upsert")?;
            let mut cells = Vec::with_capacity(order.len());
            for column in &order {
                cells.push(self.insert_value(&sealed, column)?);
            }
            rows.push(cells);
        }
        let key_at: Vec<usize> = key_columns
            .iter()
            .map(|column| {
                order
                    .iter()
                    .position(|c| std::ptr::eq(*c, *column))
                    .ok_or_else(|| {
                        Error::Definition(format!(
                            "{}'s unique key {name} has a column no insert writes",
                            table.name
                        ))
                    })
            })
            .collect::<Result<_, _>>()?;

        let mut batches = Vec::new();
        for (start, chunk) in rows.chunks(batch).enumerate() {
            let start = start * batch;
            let mut keys = Vec::with_capacity(chunk.len());
            let mut key_cells = Vec::with_capacity(chunk.len());
            for (i, cells) in chunk.iter().enumerate() {
                let held: Option<Vec<Bound>> = key_at.iter().map(|&at| cells[at].clone()).collect();
                let text = match &held {
                    Some(bounds) => {
                        let mut parts = Vec::with_capacity(bounds.len());
                        for (column, bound) in key_columns.iter().zip(bounds) {
                            parts.push(key_text(column, &bound.value));
                        }
                        parts
                            .into_iter()
                            .collect::<Option<Vec<_>>>()
                            .map(|parts| parts.join("\u{1f}"))
                    }
                    None => None,
                };
                if let Some(text) = &text {
                    if let Some(first) = keys
                        .iter()
                        .position(|k: &Option<String>| k.as_ref() == Some(text))
                    {
                        return Err(invalid(format!(
                            "upsert_many of {}: inputs {} and {} of one statement hold one {name}, and an upsert changes a row once",
                            table.name,
                            start + first,
                            start + i
                        )));
                    }
                }
                keys.push(text);
                key_cells.push(held);
            }
            // One statement on a dialect whose VALUES take DEFAULT, else
            // one per set of columns the inputs set, in the order the
            // sets first appear.
            let mut groups: Vec<(Vec<bool>, Vec<usize>)> = Vec::new();
            for (i, cells) in chunk.iter().enumerate() {
                let set: Vec<bool> = if c.default.is_empty() {
                    cells.iter().map(Option::is_some).collect()
                } else {
                    Vec::new()
                };
                match groups.iter_mut().find(|(s, _)| *s == set) {
                    Some((_, members)) => members.push(i),
                    None => groups.push((set, vec![i])),
                }
            }
            let mut statements = Vec::with_capacity(groups.len());
            for (_, members) in groups {
                let group: Vec<&Vec<Option<Bound>>> = members.iter().map(|&i| &chunk[i]).collect();
                statements.push((
                    self.upsert_rows(&order, &group, key, &plain, options.only_if_changed)?,
                    members,
                ));
            }
            let before = match self.definition.dialect {
                Dialect::Sqlite => self.rows_of_keys(key, key_cells.iter().flatten())?,
                Dialect::Postgres => None,
            };
            batches.push(UpsertBatch {
                inputs: (start..start + chunk.len()).collect(),
                keys,
                key_cells,
                before,
                statements,
            });
        }
        Ok(UpsertManyPlan {
            batches,
            sets: !plain.is_empty(),
            sealing,
            key,
            key_columns,
        })
    }

    /// The upsert of `rows`, each a cell per column of `order`: the insert
    /// of the columns any row sets, DEFAULT for a cell one leaves unset,
    /// and on a conflict on `key` each of `fields` set from the row it
    /// would have inserted, with the stamps and the fences; or, when it
    /// sets none, an update that compares nothing, which leaves each row a
    /// key holds as it is. Its WHERE holds the live rows, and, only if
    /// changed, those whose `fields` differ from the row's.
    fn upsert_rows(
        &self,
        order: &[&'a Column],
        rows: &[&Vec<Option<Bound>>],
        key: &UniqueKey,
        fields: &[&'a Column],
        only_if_changed: bool,
    ) -> Result<Statement, Error> {
        let c = &self.definition.clauses;
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
            for (cell, inserted) in row.iter().zip(&inserted) {
                if !inserted {
                    continue;
                }
                match cell {
                    Some(bound) => {
                        args.push(bound.clone());
                        cells.push(self.placeholder(args.len())?);
                    }
                    None => cells.push(c.default.clone()),
                }
            }
            tuples.push(format!("({})", cells.join(", ")));
        }
        let mut assignments = Vec::with_capacity(fields.len() + 2);
        let mut current = Vec::with_capacity(fields.len());
        let mut next = Vec::with_capacity(fields.len());
        for column in fields {
            assignments.push(fill(&c.set_excluded, &[&column.column, &column.column])?);
            current.push(column.qualified.clone());
            next.push(fill(&c.excluded, &[&column.column])?);
        }
        let s = &self.table.statements;
        let mut conditions = Vec::new();
        if self.table.soft_delete {
            conditions.push(s.live_condition.clone());
        }
        if assignments.is_empty() {
            assignments.push(key.keep.clone());
            conditions.push(c.never.clone());
        } else {
            self.stamp(&mut assignments, &Values::new(), &mut args)?;
            self.advance(&mut assignments);
            if only_if_changed {
                conditions.push(fill(&c.changed, &[&current.join(", "), &next.join(", ")])?);
            }
        }
        let sql = fill(
            &s.upsert,
            &[
                &columns.join(", "),
                &tuples.join(", "),
                &key.conflict,
                &assignments.join(", "),
                &self.where_of(&conditions),
            ],
        )?;
        Ok(Statement { sql, args })
    }

    /// The read of the rows that hold the keys of `keys`, each the cells
    /// of a unique key's columns, among the rows the key's index holds;
    /// none for no key.
    pub(crate) fn rows_of_keys<'k>(
        &self,
        key: &UniqueKey,
        keys: impl Iterator<Item = &'k Vec<Bound>>,
    ) -> Result<Option<Statement>, Error> {
        let mut args = Vec::new();
        let mut tuples = Vec::new();
        for cells in keys {
            let mut placeholders = Vec::with_capacity(cells.len());
            for cell in cells {
                args.push(cell.clone());
                placeholders.push(self.placeholder(args.len())?);
            }
            tuples.push(format!("({})", placeholders.join(", ")));
        }
        if tuples.is_empty() {
            return Ok(None);
        }
        Ok(Some(Statement {
            sql: fill(&key.rows, &[&tuples.join(", ")])?,
            args,
        }))
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

    /// An upsert of a table that composes behaviors (D103, D116): the read
    /// of the rows that hold the input's value of the unique key `on`, after
    /// which the repository runs the core's create of the input, or the
    /// core's update of the row that holds it. The input and the update are
    /// checked before anything is read: a member the table has no column
    /// for, a field the update sets that a behavior owns, which only the
    /// behavior's operations change, and a value its column refuses.
    pub(crate) fn core_upsert(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
    ) -> Result<CoreUpsertPlan, Error> {
        let table = self.table;
        if table.behaviors.as_ref().is_some_and(|b| b.no_upsert) {
            return Err(invalid(format!(
                "upsert_one of {}: the table's behaviors keep sub-tables or arm deadlines, so the table has no upserts in any language (D103); create the row with create_one",
                table.name
            )));
        }
        self.known_members(input, "upsert")?;
        if let Some(member) = update.keys().find(|member| self.owned().contains(member)) {
            return Err(invalid(format!(
                "upsert_one of {} sets the fields an update sets, not {member}, which a behavior owns (D103): only its operations change it",
                table.name
            )));
        }
        let name = on.join(",");
        let key = self.unique_key(&name)?;
        let sets = self.set_clauses(update, &mut Vec::new(), None)?;
        Ok(CoreUpsertPlan {
            held: self.held(&name, key, &self.stamped(input)?)?,
            sets: !sets.is_empty(),
        })
    }

    /// The read of the live row whose key is `key` when a column `update`
    /// sets holds another value than the one it sets, as the database
    /// compares them, for an upsert only if changed (D89): none when the
    /// update sets no column the database can compare, an encrypted one's
    /// envelope being bound to its row.
    pub(crate) fn changed(&self, key: &Value, update: &Values) -> Result<Option<Statement>, Error> {
        let mut plain = update.clone();
        for column in self.table.encrypted() {
            plain.remove(&column.name);
        }
        let c = &self.definition.clauses;
        let key_column = self.table.key_column();
        let mut args = vec![self.key(key)?];
        let mut compared = (Vec::new(), Vec::new());
        self.set_clauses(&plain, &mut args, Some(&mut compared))?;
        if compared.0.is_empty() {
            return Ok(None);
        }
        let conditions = [
            fill(&c.eq, &[&key_column.qualified, &1])?,
            fill(
                &c.changed,
                &[&compared.0.join(", "), &compared.1.join(", ")],
            )?,
        ];
        let where_ = self.live_where(self.where_of(&conditions), false);
        let s = &self.table.statements;
        Ok(Some(Statement {
            sql: fill(&s.find_one, &[&s.columns, &where_, &""])?,
            args,
        }))
    }

    /// The read of the live rows `filter` finds, in the key's order, every
    /// one of them: the rows an update of many of a table that composes
    /// behaviors updates through the core, a row at a time (D103, D116).
    pub(crate) fn rows_of(&self, filter: Option<&Values>) -> Result<Statement, Error> {
        let mut args = Vec::new();
        let conditions = self.conditions_for(self.table, filter, &mut args, true)?;
        let where_ = self.live_where(self.where_of(&conditions), false);
        let s = &self.table.statements;
        let order = self.order_by(&[OrderBy::asc(self.table.key.clone())])?;
        let sql = fill(&s.find_many, &[&s.qualified_columns, &where_, &order, &""])?;
        Ok(Statement { sql, args })
    }

    /// The members of the columns the table's behaviors own.
    fn owned(&self) -> &[String] {
        self.table
            .behaviors
            .as_ref()
            .map_or(&[], |behaviors| behaviors.owned.as_slice())
    }

    /// The fields of `input` as the core reads them, by unquoted column:
    /// each column's member the input names but an encrypted one, whose
    /// plaintext the core has no use for, as the TypeScript repositories
    /// hand it an input's.
    fn core_fields(&self, input: &Values) -> Values {
        let mut fields = Values::new();
        for column in &self.table.columns {
            if column.encrypted {
                continue;
            }
            if let Some(value) = input.get(&column.name) {
                fields.insert(column.unquoted().to_owned(), value.clone());
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
                columns.push((column.unquoted().to_owned(), Some(bound)));
            }
        }
        let key = match sealed.get(&table.key) {
            None | Some(Value::Null) => None,
            Some(Value::String(text)) if text.is_empty() => None,
            Some(key) => Some(self.wire_key(key)?),
        };
        Ok(CoreCreate {
            table,
            key,
            fields: self.core_fields(&sealed),
            columns,
            caller: self.session.caller(),
            params: Values::new(),
        })
    }

    /// An update of a row through the core: the fields it sets, its own
    /// columns as the update binds them, with updatedBy, and updatedAt,
    /// which takes the database's clock; an owned column is left to the
    /// core, which refuses it.
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
        let fields = self.core_fields(&sealed);
        if fields.is_empty() && columns.is_empty() {
            return Err(invalid(format!(
                "update {}: no fields to update",
                table.name
            )));
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
            table,
            key: self.wire_key(key)?,
            fields,
            columns,
            now: self.now_columns(),
            preconditions: options.preconditions.as_ref(),
            caller: self.session.caller(),
        })
    }

    /// A delete of a row through the core: a soft-deletable table's live
    /// row stamped deleted in place of deleted, unless `hard`, which removes
    /// the row whatever the table.
    pub(crate) fn core_delete<'p>(
        &self,
        key: &Value,
        options: &'p WriteOptions,
        hard: bool,
    ) -> Result<CoreDelete<'p>, Error>
    where
        'a: 'p,
    {
        let table = self.table;
        let soft_delete = if table.soft_delete && !hard {
            let user = self.require_user()?;
            let mut stamps = Vec::new();
            if let Some(deleted_at) = table.audit("deletedAt") {
                stamps.push((
                    deleted_at.unquoted().to_owned(),
                    Some(Bound {
                        value: Value::String(self.session.now()),
                        marks: Marks::TIMESTAMP,
                    }),
                ));
            }
            if let Some(deleted_by) = table.audit("deletedBy") {
                stamps.push((
                    deleted_by.unquoted().to_owned(),
                    Some(self.bind(&Value::String(user), deleted_by, "deletedBy")?),
                ));
            }
            Some(stamps)
        } else {
            None
        };
        Ok(CoreDelete {
            table,
            key: self.wire_key(key)?,
            soft_delete,
            preconditions: options.preconditions.as_ref(),
            caller: self.session.caller(),
        })
    }

    /// An operation of one of the table's behaviors on a row, through the
    /// core: its parameters, and the stamps its write of the row sets,
    /// updatedAt and, when the repositories act as a user, updatedBy, as
    /// the TypeScript repositories' operations stamp them.
    pub(crate) fn core_operation<'p>(
        &self,
        member: &'p str,
        operation: &'p str,
        key: &Value,
        params: &Value,
        options: &'p WriteOptions,
    ) -> Result<CoreOperation<'p>, Error>
    where
        'a: 'p,
    {
        let table = self.table;
        if table.behaviors.is_none() {
            return Err(invalid(format!(
                "{} composes no behaviors (D103), so it has no operation {member}.{operation}",
                table.name
            )));
        }
        let mut columns = Vec::new();
        if let (Some(updated_by), Some(user)) = (table.audit("updatedBy"), self.user()) {
            columns.push((
                updated_by.unquoted().to_owned(),
                Some(self.bind(&Value::String(user), updated_by, "updatedBy")?),
            ));
        }
        Ok(CoreOperation {
            table,
            key: self.wire_key(key)?,
            member,
            operation,
            params: match params {
                Value::Null => Value::Object(Values::new()),
                params => params.clone(),
            },
            columns,
            now: self.now_columns(),
            preconditions: options.preconditions.as_ref(),
            caller: self.session.caller(),
        })
    }

    /// The columns the core's write of a row sets to the database's
    /// clock, as an update's statement stamps them: `updated_at`.
    fn now_columns(&self) -> Vec<String> {
        self.table
            .audit("updatedAt")
            .map(|updated_at| vec![updated_at.unquoted().to_owned()])
            .unwrap_or_default()
    }

    /// An operation of one of the table's behaviors on the table as a
    /// whole (D104), through the core: its parameters, and who it runs as.
    pub(crate) fn core_table_operation<'p>(
        &self,
        member: &'p str,
        operation: &'p str,
        params: &Value,
    ) -> Result<CoreTableOperation<'p>, Error>
    where
        'a: 'p,
    {
        let table = self.table;
        if table.behaviors.is_none() {
            return Err(invalid(format!(
                "{} composes no behaviors (D103), so it has no operation {member}.{operation}",
                table.name
            )));
        }
        Ok(CoreTableOperation {
            table,
            member,
            operation,
            params: match params {
                Value::Null => Value::Object(Values::new()),
                params => params.clone(),
            },
            caller: self.session.caller(),
        })
    }

    /// The key a command names, in its wire form: a UUID in base62, which
    /// the core names a row by, in whichever form the caller wrote it.
    fn wire_key(&self, key: &Value) -> Result<String, Error> {
        let refused = || {
            invalid(format!(
                "{} is keyed by {}, not {key}",
                self.table.name, self.table.key
            ))
        };
        match key {
            Value::String(text) if self.table.key_column().uuid => {
                parse_uuid(text).map(uuid_to_base62).ok_or_else(refused)
            }
            Value::String(text) if !text.is_empty() => Ok(text.clone()),
            Value::Number(number) => Ok(number.to_string()),
            _ => Err(refused()),
        }
    }

    /// The statements a delete through the core of a versioned table's row
    /// runs around it, when it removes the row (`hard`, or a table that
    /// keeps no deleted rows): the one that sets the transaction's history
    /// actor to the acting user, which the history trigger reads the
    /// tombstone's actor from, and the one that clears it (D17, D32), as a
    /// hard delete's do. None for any other table.
    pub(crate) fn core_delete_actor(&self, hard: bool) -> Option<(Statement, Statement)> {
        if (self.table.soft_delete && !hard) || !self.table.history_actor {
            return None;
        }
        let set = Statement {
            sql: format!(
                "{}SELECT _history_actor FROM history_actor",
                self.definition.history_actor_cte
            ),
            args: vec![Bound {
                value: self.user().map_or(Value::Null, Value::String),
                marks: Marks::TEXT,
            }],
        };
        let clear = Statement {
            sql: self.definition.clear_history_actor.clone(),
            args: Vec::new(),
        };
        Some((set, clear))
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

    /// The fences of fixture-fenced-db's tables, as its types crate's
    /// definition holds them (D119); rustgen's tests run the repositories
    /// over them on SQLite and on Postgres.
    #[test]
    fn an_update_advances_each_fence_that_covers_a_column_it_sets() {
        let fence = |column: &str, covers: &[&str], table: &str| Fence {
            column: column.to_owned(),
            covers: covers.iter().map(|&covered| covered.to_owned()).collect(),
            bump: format!("{column} = {table}.{column} + 1"),
        };
        let delivery = [
            fence("plan_version", &["address", "courier_id"], "delivery"),
            fence("run_version", &["attempts", "proof"], "delivery"),
        ];
        let parcel = [fence("version", &[], "parcel")];
        let depot = [fence("revision", &["\"name\"", "deleted_at"], "depot")];
        let plan = "plan_version = delivery.plan_version + 1";
        let run = "run_version = delivery.run_version + 1";
        let cases: [(&[Fence], &[&str], &[&str]); 14] = [
            (
                &delivery,
                &[
                    "address = ?1",
                    "updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')",
                ],
                &[plan],
            ),
            (&delivery, &["courier_id = NULL"], &[plan]),
            (&delivery, &["proof = $1", "address = $2"], &[plan, run]),
            (&delivery, &["attempts = $1", "proof = $2"], &[run]),
            (
                &delivery,
                &["note = $1", "updated_at = statement_timestamp()"],
                &[],
            ),
            // The caller drives planVersion; runVersion is still advanced.
            (
                &delivery,
                &["address = $1", "plan_version = $2", "attempts = $3"],
                &[run],
            ),
            (&delivery, &["plan_version = $1"], &[]),
            (&delivery, &[], &[]),
            (&parcel, &["note = NULL"], &["version = parcel.version + 1"]),
            (&parcel, &["note = $1", "version = $2"], &[]),
            (&parcel, &[], &[]),
            (&depot, &["capacity = $1"], &[]),
            (
                &depot,
                &["\"name\" = $1"],
                &["revision = depot.revision + 1"],
            ),
            // A column is read whole: name_note is no write of name.
            (&depot, &["\"name_note\" = $1", "name = $2"], &[]),
        ];
        for (fences, sets, want) in cases {
            let sets: Vec<String> = sets.iter().map(|&set| set.to_owned()).collect();
            assert_eq!(fence_bumps(fences, &sets), want, "a write of {sets:?}");
        }
    }
}
