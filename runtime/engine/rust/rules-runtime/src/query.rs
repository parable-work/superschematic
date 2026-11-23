//! The handler's reads in SQL (D117): a list's page, a unique key's holders
//! and the rows a delete reaches, each one query over the type's table, so
//! runtime mode reads the rows it answers and not every row of the type.
//!
//! Each answer is the handler's own over every row ([`superschematic_rules_handler::filter`]):
//! a field's value compared as its kind compares it (an instant by its
//! time, a UUID by its value, a number by its value, text by code point),
//! `ne` and `notIn` keeping a row whose field holds no value and every
//! other comparison leaving it out, a value that compares to nothing of
//! the field's kind matching nothing, a sort with no value last in either
//! direction and the key breaking ties in the first term's direction, and a
//! page after a cursor's sort values and key. A soft-deletable table's rows
//! are its live ones alone, a relation's target's too.
//!
//! On Postgres each column is compared as its kind's type (`numeric`,
//! `timestamptz`, `uuid`, `boolean`, `jsonb`, and text under the `C`
//! collation, which orders by code point), with each parameter cast to it.
//! On SQLite a column holds what the store wrote, an instant as its
//! `toISOString` text and a UUID as its lowercase hyphenated text, both of
//! which order as their values do; text compares by `BINARY`, and `ilike`
//! and a JSON value's equality are the handler's own, as functions the
//! connection registers ([`ILIKE`], [`JSON_SAME`]).

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::Refusal;
use superschematic_rules_handler::document::{Kind, Model};
use superschematic_rules_handler::filter::{self, Cond, Order, Place, Sort};
use superschematic_rules_handler::values::{instant_of, iso_of, uuid_of};

use crate::db::{placeholder, quote};
use crate::record::{Served, Types};
use crate::values::{hyphenate, internal, Dialect};

/// The SQLite function a filter's `ilike` calls: whether its first argument
/// holds the second anywhere, as the handler's `ilike` reads the pattern.
pub(crate) const ILIKE: &str = "superschematic_ilike";

/// The SQLite function a JSON field's equality calls: whether two JSON
/// texts are one value as the handler compares them.
pub(crate) const JSON_SAME: &str = "superschematic_json_same";

/// A statement being written: its text's parameters, in order.
struct Statement {
    dialect: Dialect,
    params: Vec<Value>,
    aliases: usize,
}

const TRUE: &str = "(1 = 1)";
const FALSE: &str = "(1 = 0)";

/// A column of a row a statement reads, as its kind compares it.
#[derive(Clone)]
struct Operand {
    /// The column, qualified by its table's alias; `None` for a field the
    /// table holds no column of, which holds no value.
    column: Option<String>,
    kind: Kind,
}

impl Statement {
    fn new(dialect: Dialect) -> Self {
        Statement {
            dialect,
            params: Vec::new(),
            aliases: 0,
        }
    }

    fn alias(&mut self) -> String {
        let alias = format!("t{}", self.aliases);
        self.aliases += 1;
        alias
    }

    /// A parameter of a value in its column form, cast to its kind's type
    /// on Postgres.
    fn bind(&mut self, kind: &Kind, value: Value) -> String {
        self.params.push(value);
        let at = placeholder(self.dialect, self.params.len());
        match self.dialect {
            Dialect::Sqlite => at,
            Dialect::Postgres => format!("{at}::{}", pg_type(kind)),
        }
    }

    /// A column as its kind compares and sorts it.
    fn typed(&self, kind: &Kind, column: &str) -> String {
        match self.dialect {
            Dialect::Sqlite => match kind {
                Kind::Text | Kind::Enum(_) | Kind::Date => format!("{column} COLLATE BINARY"),
                _ => column.to_owned(),
            },
            Dialect::Postgres => match kind {
                Kind::Text | Kind::Enum(_) | Kind::Date => {
                    format!("({column})::text COLLATE \"C\"")
                }
                kind => format!("({column})::{}", pg_type(kind)),
            },
        }
    }

    /// Whether an operand equals a value in its column form.
    fn equals(&mut self, operand: &Operand, column: &str, value: Value) -> String {
        if operand.kind == Kind::Json && self.dialect == Dialect::Sqlite {
            let bound = self.bind(&operand.kind, value);
            return format!("{JSON_SAME}({column}, {bound})");
        }
        let bound = self.bind(&operand.kind, value);
        format!("{} = {bound}", self.typed(&operand.kind, column))
    }
}

/// The Postgres type a kind's values compare as.
fn pg_type(kind: &Kind) -> &'static str {
    match kind {
        Kind::Number | Kind::Integer => "numeric",
        Kind::Bool => "boolean",
        Kind::Uuid => "uuid",
        Kind::DateTime => "timestamptz",
        Kind::Json | Kind::Object(_) => "jsonb",
        Kind::Text | Kind::Enum(_) | Kind::Date => "text",
    }
}

/// A value a filter, a cursor or a write gives, in the column form it
/// compares as for a field of a kind; `None` for one that compares to no
/// value of the kind, as the handler's comparison finds none: an instant
/// from its text or its epoch milliseconds, a UUID from either form, and a
/// number, a boolean or a text only from one.
fn comparable(kind: &Kind, value: &Value, dialect: Dialect) -> Option<Value> {
    match kind {
        Kind::Uuid => value
            .as_str()
            .and_then(uuid_of)
            .map(|n| Value::String(hyphenate(n))),
        Kind::DateTime => instant_of(value).and_then(iso_of).map(Value::String),
        Kind::Number | Kind::Integer => value.is_number().then(|| value.clone()),
        Kind::Bool => match (dialect, value) {
            (Dialect::Sqlite, Value::Bool(flag)) => Some(Value::from(i64::from(*flag))),
            (Dialect::Postgres, Value::Bool(_)) => Some(value.clone()),
            _ => None,
        },
        Kind::Text | Kind::Enum(_) | Kind::Date => value.is_string().then(|| value.clone()),
        // A JSON value is bound as its text, which SQLite's function and
        // Postgres's jsonb read.
        Kind::Json => Some(Value::String(value.to_string())),
        Kind::Object(_) => None,
    }
}

/// The column of a place of a served type's row, under an alias.
fn operand(served: &Served, alias: &str, place: &Place, kind: &Kind) -> Operand {
    let column = match place {
        Place::Key => Some(served.table.key.column.clone()),
        Place::Own(name) => served
            .own
            .iter()
            .find(|(field, _)| field.name == *name)
            .map(|(_, column)| column.column.clone()),
        Place::Owned(use_id, name) => served
            .table
            .uses
            .iter()
            .find(|used| used.id == *use_id)
            .and_then(|used| used.fields.iter().find(|field| field.name == *name))
            .map(|field| field.column.column.clone()),
    };
    Operand {
        column: column.map(|column| format!("{alias}.{}", quote(&column))),
        kind: kind.clone(),
    }
}

/// The condition a soft-deletable table's live rows meet, under an alias.
fn live(served: &Served, alias: &str) -> Option<String> {
    served
        .table
        .live
        .as_ref()
        .map(|live| format!("{alias}.{} IS NULL", quote(&live.column)))
}

fn all(conditions: Vec<String>) -> String {
    if conditions.is_empty() {
        TRUE.to_owned()
    } else {
        format!("({})", conditions.join(" AND "))
    }
}

/// A list's filter as one statement's conditions.
struct Filtering<'t> {
    types: &'t Types,
}

impl Filtering<'_> {
    fn conds(
        &self,
        statement: &mut Statement,
        served: &Served,
        alias: &str,
        conds: &[Cond],
    ) -> Result<String, Refusal> {
        let mut out = Vec::with_capacity(conds.len());
        for cond in conds {
            out.push(self.cond(statement, served, alias, cond)?);
        }
        Ok(all(out))
    }

    fn cond(
        &self,
        statement: &mut Statement,
        served: &Served,
        alias: &str,
        cond: &Cond,
    ) -> Result<String, Refusal> {
        let dialect = statement.dialect;
        Ok(match cond {
            Cond::Compare {
                place,
                kind,
                op,
                value,
            } => {
                let operand = operand(served, alias, place, kind);
                let held = comparable(kind, value, dialect);
                match (op.as_str(), &operand.column, held) {
                    // NULL compares as nothing, and ne keeps it (D112).
                    ("ne", None, _) | ("ne", _, None) => TRUE.to_owned(),
                    ("ne", Some(column), Some(value)) => {
                        let equal = statement.equals(&operand, column, value);
                        format!("({column} IS NULL OR NOT ({equal}))")
                    }
                    (_, None, _) | (_, _, None) => FALSE.to_owned(),
                    ("eq", Some(column), Some(value)) => {
                        format!("({})", statement.equals(&operand, column, value))
                    }
                    ("ilike", Some(column), Some(value)) => {
                        let bound = statement.bind(kind, value);
                        match dialect {
                            Dialect::Sqlite => format!("({ILIKE}({column}, {bound}) = 1)"),
                            Dialect::Postgres => {
                                format!("(({column})::text ILIKE ('%' || {bound} || '%'))")
                            }
                        }
                    }
                    (op, Some(column), Some(value)) => {
                        let sign = match op {
                            "gt" => ">",
                            "gte" => ">=",
                            "lt" => "<",
                            "lte" => "<=",
                            other => {
                                return Err(internal(format!(
                                "the filter compiled an operation the store takes none of: {other}"
                            )))
                            }
                        };
                        let bound = statement.bind(kind, value);
                        format!("({} {sign} {bound})", statement.typed(kind, column))
                    }
                }
            }
            Cond::Among {
                place,
                kind,
                values,
                negate,
            } => {
                let operand = operand(served, alias, place, kind);
                let held: Vec<Value> = values
                    .iter()
                    .filter_map(|value| comparable(kind, value, dialect))
                    .collect();
                match (&operand.column, held.is_empty(), negate) {
                    (None, _, false) | (_, true, false) => FALSE.to_owned(),
                    (None, _, true) | (_, true, true) => TRUE.to_owned(),
                    (Some(column), false, negate) => {
                        let any: Vec<String> = held
                            .into_iter()
                            .map(|value| statement.equals(&operand, column, value))
                            .collect();
                        let any = format!("({})", any.join(" OR "));
                        if *negate {
                            format!("({column} IS NULL OR NOT {any})")
                        } else {
                            any
                        }
                    }
                }
            }
            Cond::Null { place, null } => {
                let operand = operand(served, alias, place, &Kind::Json);
                match (place, &operand.column) {
                    (Place::Key, _) => if *null { FALSE } else { TRUE }.to_owned(),
                    (_, None) => if *null { TRUE } else { FALSE }.to_owned(),
                    (_, Some(column)) if *null => format!("({column} IS NULL)"),
                    (_, Some(column)) => format!("({column} IS NOT NULL)"),
                }
            }
            Cond::Through {
                member,
                target,
                conds,
            } => {
                let Some(column) = served
                    .own
                    .iter()
                    .find(|(field, _)| field.name == *member)
                    .map(|(_, column)| format!("{alias}.{}", quote(&column.column)))
                else {
                    return Ok(FALSE.to_owned());
                };
                let reached = self.types.get(target)?;
                let inner = statement.alias();
                let mut conditions = vec![format!(
                    "{inner}.{} = {column}",
                    quote(&reached.table.key.column)
                )];
                conditions.extend(live(reached, &inner));
                conditions.push(self.conds(statement, reached, &inner, conds)?);
                format!(
                    "EXISTS (SELECT 1 FROM {} AS {inner} WHERE {})",
                    quote(&reached.table.table),
                    conditions.join(" AND ")
                )
            }
            Cond::AnyOf(branches) => {
                let mut any = Vec::with_capacity(branches.len());
                for branch in branches {
                    any.push(self.conds(statement, served, alias, branch)?);
                }
                if any.is_empty() {
                    TRUE.to_owned()
                } else {
                    format!("({})", any.join(" OR "))
                }
            }
        })
    }
}

/// One term of a sort, as a statement orders and pages by it: its column
/// as its kind sorts it, or `NULL` for a field no column holds, and
/// whether it descends.
struct Term {
    expr: String,
    kind: Kind,
    desc: bool,
}

/// The statement of a list's page: at most `limit` of the type's rows the
/// filter keeps, in the sort's order, after the cursor. Its rows are the
/// table's columns, as a row read reads them.
pub(crate) fn page(
    types: &Types,
    served: &Served,
    dialect: Dialect,
    (filter_given, sort, cursor, limit): (&Value, &[Sort], Option<&str>, usize),
) -> Result<(String, Vec<Value>), Refusal> {
    let model = types.models.get(&served.table.type_name).ok_or_else(|| {
        internal(format!(
            "{} has no model the store serves",
            served.table.type_name
        ))
    })?;
    let conds = filter::compile(model, &types.models, filter_given)?;
    let order = filter::ordering(model, sort)?;
    let mut statement = Statement::new(dialect);
    let alias = statement.alias();
    let mut conditions: Vec<String> = live(served, &alias).into_iter().collect();
    conditions.push(Filtering { types }.conds(&mut statement, served, &alias, &conds)?);
    let terms: Vec<Term> = order
        .terms()
        .iter()
        .map(|(place, kind, desc)| Term {
            expr: operand(served, &alias, place, kind).column.map_or_else(
                || "NULL".to_owned(),
                |column| statement.typed(kind, &column),
            ),
            kind: kind.clone(),
            desc: *desc,
        })
        .collect();
    let key_kind = order.key_kind().clone();
    let key = statement.typed(
        &key_kind,
        &format!("{alias}.{}", quote(&served.table.key.column)),
    );
    if let Some(cursor) = cursor {
        conditions.push(after(
            &mut statement,
            &order,
            &terms,
            &key,
            cursor,
            &served.table.type_name,
        )?);
    }
    let mut orders: Vec<String> = terms
        .iter()
        .filter(|term| term.expr != "NULL")
        .map(|term| {
            format!(
                "{} {} NULLS LAST",
                term.expr,
                if term.desc { "DESC" } else { "ASC" }
            )
        })
        .collect();
    orders.push(format!(
        "{key} {}",
        if order.key_descends() { "DESC" } else { "ASC" }
    ));
    let sql = format!(
        "SELECT {alias}.* FROM {} AS {alias} WHERE {} ORDER BY {} LIMIT {limit}",
        quote(&served.table.table),
        conditions.join(" AND "),
        orders.join(", ")
    );
    Ok((sql, statement.params))
}

/// The condition of the rows after a cursor, in the sort's order: past it
/// in a term the rows before agree on, or agreeing on every term and past
/// its key. A term's value of none sorts last, so nothing is past it.
fn after(
    statement: &mut Statement,
    order: &Order,
    terms: &[Term],
    key: &str,
    cursor: &str,
    type_name: &str,
) -> Result<String, Refusal> {
    let (values, at) = order.read_cursor(cursor, type_name)?;
    let wrong = || {
        Refusal::invalid_argument(format!(
            "list of {type_name}: the cursor is not one this sort returned"
        ))
    };
    let dialect = statement.dialect;
    let mut held = Vec::with_capacity(values.len());
    for (term, value) in terms.iter().zip(values) {
        held.push(match value {
            None => None,
            Some(value) => Some(comparable(&term.kind, &value, dialect).ok_or_else(wrong)?),
        });
    }
    let key_value = comparable(order.key_kind(), &Value::String(at), dialect).ok_or_else(wrong)?;
    let mut any = Vec::new();
    let mut agree: Vec<String> = Vec::new();
    for (term, value) in terms.iter().zip(&held) {
        if let Some(value) = value {
            let bound = statement.bind(&term.kind, value.clone());
            let sign = if term.desc { "<" } else { ">" };
            let past = format!("({} {sign} {bound} OR {} IS NULL)", term.expr, term.expr);
            let mut here = agree.clone();
            here.push(past);
            any.push(all(here));
            let bound = statement.bind(&term.kind, value.clone());
            agree.push(format!("{} = {bound}", term.expr));
        } else {
            agree.push(format!("{} IS NULL", term.expr));
        }
    }
    let bound = statement.bind(order.key_kind(), key_value);
    let sign = if order.key_descends() { "<" } else { ">" };
    agree.push(format!("{key} {sign} {bound}"));
    any.push(all(agree));
    Ok(format!("({})", any.join(" OR ")))
}

/// The statement of a unique key's holders: at most two live rows whose own
/// fields hold every one of the values, each compared as its kind
/// compares it; `None` when a value compares to none of its kind, which no
/// row holds.
pub(crate) fn holders(
    served: &Served,
    dialect: Dialect,
    values: &Map<String, Value>,
) -> Result<Option<(String, Vec<Value>)>, Refusal> {
    let mut statement = Statement::new(dialect);
    let alias = statement.alias();
    let mut conditions: Vec<String> = live(served, &alias).into_iter().collect();
    for (name, value) in values {
        let Some((field, _)) = served.own.iter().find(|(field, _)| field.name == *name) else {
            return Err(internal(format!(
                "{} has no own field {name} a unique key holds",
                served.table.type_name
            )));
        };
        if field.list {
            return Ok(None);
        }
        let place = Place::Own(name.clone());
        let operand = operand(served, &alias, &place, &field.kind);
        let (Some(column), Some(value)) = (
            operand.column.clone(),
            comparable(&field.kind, value, dialect),
        ) else {
            return Ok(None);
        };
        conditions.push(statement.equals(&operand, &column, value));
    }
    let sql = format!(
        "SELECT {alias}.* FROM {} AS {alias} WHERE {} LIMIT 2",
        quote(&served.table.table),
        all(conditions)
    );
    Ok(Some((sql, statement.params)))
}

/// The statement of the rows a delete of a row of `target` reaches through
/// a referrer's member: the referrer's live rows whose member holds the
/// row's key. `None` when the target's delete is a soft delete, an update
/// no foreign key hears.
pub(crate) fn reached(
    types: &Types,
    referrer: &Served,
    member: &str,
    target: &str,
    id: &str,
    dialect: Dialect,
) -> Result<Option<(String, Vec<Value>)>, Refusal> {
    let target = types.get(target)?;
    if target.table.live.is_some() {
        return Ok(None);
    }
    let mut statement = Statement::new(dialect);
    let alias = statement.alias();
    let place = Place::Own(member.to_owned());
    let kind = target.model.key.kind.clone();
    let operand = operand(referrer, &alias, &place, &kind);
    let Some(column) = operand.column.clone() else {
        return Err(internal(format!(
            "{} has no column of its relation {member}",
            referrer.table.type_name
        )));
    };
    let Some(key) = comparable(&kind, &Value::String(id.to_owned()), dialect) else {
        return Ok(None);
    };
    let mut conditions: Vec<String> = live(referrer, &alias).into_iter().collect();
    conditions.push(statement.equals(&operand, &column, key));
    let sql = format!(
        "SELECT {alias}.* FROM {} AS {alias} WHERE {}",
        quote(&referrer.table.table),
        all(conditions)
    );
    Ok(Some((sql, statement.params)))
}

/// The rows of a page in the sort's order, as the handler orders them,
/// whatever order the database returned them in.
pub(crate) fn sorted(
    model: &Model,
    sort: &[Sort],
    rows: Vec<superschematic_rules_handler::StoredRow>,
) -> Result<Vec<superschematic_rules_handler::StoredRow>, Refusal> {
    let order = filter::ordering(model, sort)?;
    let mut held: Vec<&superschematic_rules_handler::StoredRow> = rows.iter().collect();
    order.sort(&mut held);
    let at: BTreeMap<&str, usize> = held
        .iter()
        .enumerate()
        .map(|(i, row)| (row.id.as_str(), i))
        .collect();
    let mut out = rows.clone();
    out.sort_by_key(|row| at.get(row.id.as_str()).copied().unwrap_or(usize::MAX));
    Ok(out)
}
