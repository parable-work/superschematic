//! The filters of a list operation (`FilterBy`, D106).
//!
//! On the wire a filter is the bracket form: `filter[field]=value` is
//! equality, and `filter[field][op]=value` names an operator. The generated
//! router reads a request's filters with [`FilterSpec::query`] against the
//! operation's spec, the fields a caller may filter by and each one's
//! operators, which its `Args` carry in an associated const, and the
//! implementation takes them typed, a member per field ([`Equals`],
//! [`Ordered`], [`Text`]), which the router decodes each value into as an
//! item of a query list of the field's type ([`decode_equals`],
//! [`decode_ordered`], [`decode_text`]). Members are ANDed. An in-process
//! caller builds the typed filter itself, and `Args::check` holds it to the
//! route's rules ([`Equals::check`]).
//!
//! The Go runtime's `filterparse` package and the TypeScript runtime's
//! `filter.ts` read the same, held to
//! `runtime/http/testdata/filter_parity.json`.

use std::collections::BTreeMap;
use std::fmt;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

use crate::page::refuse;
use crate::{ApiError, ParamLocation, ParamSpec, QueryValues};

/// The query parameter every filter travels under.
pub const PARAM: &str = "filter";

/// The most filter parameters a request may hold.
pub const MAX_FILTERS: usize = 50;

/// The most values an `in` may list.
pub const MAX_IN_VALUES: usize = 100;

/// The comparison a filter makes. It writes and reads as the wire names
/// it: `eq`, `in`, `gt`, `gte`, `lt`, `lte`, `contains`, `isNull`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
pub enum Operator {
    #[serde(rename = "eq")]
    Eq,
    #[serde(rename = "in")]
    In,
    #[serde(rename = "gt")]
    Gt,
    #[serde(rename = "gte")]
    Gte,
    #[serde(rename = "lt")]
    Lt,
    #[serde(rename = "lte")]
    Lte,
    #[serde(rename = "contains")]
    Contains,
    #[serde(rename = "isNull")]
    IsNull,
}

impl Operator {
    /// Every operator, in the order a field lists its own and
    /// [`FilterSpec::query`] answers them.
    pub const ALL: [Operator; 8] = [
        Operator::Eq,
        Operator::In,
        Operator::Gt,
        Operator::Gte,
        Operator::Lt,
        Operator::Lte,
        Operator::Contains,
        Operator::IsNull,
    ];

    /// The operator as the wire names it.
    pub const fn as_str(self) -> &'static str {
        match self {
            Operator::Eq => "eq",
            Operator::In => "in",
            Operator::Gt => "gt",
            Operator::Gte => "gte",
            Operator::Lt => "lt",
            Operator::Lte => "lte",
            Operator::Contains => "contains",
            Operator::IsNull => "isNull",
        }
    }

    /// The operator of a wire name; `None` for any other text.
    pub fn parse(text: &str) -> Option<Self> {
        Self::ALL
            .into_iter()
            .find(|operator| operator.as_str() == text)
    }
}

impl fmt::Display for Operator {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// One field of a [`FilterSpec`]: its name, the item's field's JSON name,
/// and the operators it takes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FieldSpec {
    name: &'static str,
    operators: &'static [Operator],
}

impl FieldSpec {
    pub const fn new(name: &'static str, operators: &'static [Operator]) -> Self {
        Self { name, operators }
    }

    /// The item's field, by its JSON name.
    pub const fn name(&self) -> &'static str {
        self.name
    }

    /// The operators the field takes.
    pub const fn operators(&self) -> &'static [Operator] {
        self.operators
    }

    /// Whether the field takes `operator`.
    pub fn takes(&self, operator: Operator) -> bool {
        self.operators.contains(&operator)
    }

    /// The 400 of an operator the field does not take, named as the
    /// request named it.
    fn refuse_operator(&self, operator: &str) -> ApiError {
        let takes: Vec<&str> = self.operators.iter().map(|op| op.as_str()).collect();
        let reason = format!(
            "takes no operator {operator}; expected one of {}",
            takes.join(", ")
        );
        refuse(&at(self.name), &reason, None)
    }
}

/// An operation's `FilterBy` as its `Args` carry it in an associated
/// const: the fields a caller may filter by,
/// `FilterSpec::new(&[FieldSpec::new("stateId", &[Operator::Eq,
/// Operator::In])])`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FilterSpec {
    fields: &'static [FieldSpec],
}

impl FilterSpec {
    pub const fn new(fields: &'static [FieldSpec]) -> Self {
        Self { fields }
    }

    /// The fields a caller may filter by, in `FilterBy`'s order.
    pub const fn fields(&self) -> &'static [FieldSpec] {
        self.fields
    }

    /// The spec's field of a name, and its place in the spec.
    fn field(&self, name: &str) -> Option<(usize, &FieldSpec)> {
        self.fields
            .iter()
            .enumerate()
            .find(|(_, field)| field.name == name)
    }

    /// The router's reader: a request's filters, of its query. A key that
    /// is `filter`, or starts with `filter[`, is a filter parameter; every
    /// other key is left alone. The filters come in the spec's field order
    /// and, within a field, in the order of [`Operator::ALL`]. The keys are
    /// read in ascending byte order, so a request with two faults is
    /// refused for the same one whatever order it sent them in, and a
    /// refusal is the 400 every runtime answers, in this order:
    ///
    /// - more than [`MAX_FILTERS`] filter parameters, counting each
    ///   occurrence, at `filter`;
    /// - a key that is not `filter[field]` or `filter[field][operator]`,
    ///   at the key;
    /// - a field the spec does not name, an operator the field does not
    ///   take, and a field's operator sent twice (`filter[f]` and
    ///   `filter[f][eq]` are one), each at `filter[field]`;
    /// - a value the operator does not take, at `filter[field]`:
    ///   `isNull`'s is `true` or `false`, `in`'s is 1 to [`MAX_IN_VALUES`]
    ///   values, and `contains` needs text.
    ///
    /// A value's own type is the route's to check, as it decodes it
    /// ([`decode_equals`]).
    pub fn query(&self, query: &QueryValues) -> Result<Vec<Filter>, ApiError> {
        // A BTreeMap of strings is in ascending order of their bytes.
        let mut sent: BTreeMap<&str, Vec<&str>> = BTreeMap::new();
        let mut total = 0;
        for (key, value) in query.pairs() {
            if key == PARAM || key.starts_with("filter[") {
                sent.entry(key).or_default().push(value);
                total += 1;
            }
        }
        if total > MAX_FILTERS {
            let reason = format!("must hold at most {MAX_FILTERS} filters");
            return Err(refuse(PARAM, &reason, Some("max")));
        }

        let mut found: BTreeMap<(usize, Operator), Filter> = BTreeMap::new();
        for (key, values) in sent {
            let Some((name, op)) = bracket_key(key) else {
                let reason = "expected filter[field] or filter[field][operator]";
                return Err(refuse(key, reason, None));
            };
            let Some((index, field)) = self.field(name) else {
                let mut reason = "is not a field this operation filters by".to_owned();
                if !self.fields.is_empty() {
                    let names: Vec<&str> = self.fields.iter().map(FieldSpec::name).collect();
                    reason.push_str("; expected one of ");
                    reason.push_str(&names.join(", "));
                }
                return Err(refuse(&at(name), &reason, None));
            };
            let operator = match op {
                None => Some(Operator::Eq),
                Some(op) => Operator::parse(op),
            };
            let Some(operator) = operator.filter(|operator| field.takes(*operator)) else {
                return Err(field.refuse_operator(op.unwrap_or(Operator::Eq.as_str())));
            };
            let place = (index, operator);
            let &[value] = values.as_slice() else {
                return Err(more_than_once(name, operator));
            };
            if found.contains_key(&place) {
                return Err(more_than_once(name, operator));
            }
            let values = match operator {
                Operator::IsNull if value != "true" && value != "false" => {
                    return Err(refuse(&at(name), "isNull expects true or false", None));
                }
                Operator::In => in_values(name, value)?,
                Operator::Contains if value.is_empty() => {
                    return Err(refuse(&at(name), "contains needs text", None));
                }
                _ => vec![value.to_owned()],
            };
            found.insert(
                place,
                Filter {
                    field: name.to_owned(),
                    operator,
                    values,
                },
            );
        }
        Ok(found.into_values().collect())
    }
}

/// One filter a request holds: the field, the operator and its values as
/// the query sent them. Every operator but `in` holds one value; `in`
/// holds its list, split at commas, each value trimmed. `isNull`'s value
/// is `true` or `false`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Filter {
    pub field: String,
    pub operator: Operator,
    pub values: Vec<String>,
}

/// The parameter a field's refusal names: `filter[field]`.
fn at(field: &str) -> String {
    format!("{PARAM}[{field}]")
}

/// The 400 of a field's operator sent twice.
fn more_than_once(field: &str, operator: Operator) -> ApiError {
    let reason = format!("holds {operator} more than once");
    refuse(&at(field), &reason, None)
}

/// The values of an `in`: its text split at commas, each value trimmed and
/// an empty one dropped, 1 to [`MAX_IN_VALUES`] of them.
fn in_values(field: &str, text: &str) -> Result<Vec<String>, ApiError> {
    let values: Vec<String> = text
        .split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
        .collect();
    if values.is_empty() {
        return Err(refuse(&at(field), "in names no value", None));
    }
    check_in_length(field, values.len())?;
    Ok(values)
}

/// An `in` holds at most [`MAX_IN_VALUES`] values.
fn check_in_length(field: &str, length: usize) -> Result<(), ApiError> {
    if length > MAX_IN_VALUES {
        let reason = format!("in must hold at most {MAX_IN_VALUES} values");
        return Err(refuse(&at(field), &reason, Some("max")));
    }
    Ok(())
}

/// Reads `filter[field]` or `filter[field][op]`: the field, and the
/// operator, `None` when the key names none. `None` for any other key:
/// `filter` alone, an empty field or operator, a third bracket, or text
/// outside the brackets. A field and an operator hold no bracket.
fn bracket_key(key: &str) -> Option<(&str, Option<&str>)> {
    let chain = key
        .strip_prefix(PARAM)?
        .strip_prefix('[')?
        .strip_suffix(']')?;
    let mut segments = chain.split("][");
    let field = segments.next()?;
    let op = segments.next();
    if segments.next().is_some() {
        return None;
    }
    let named = |segment: &str| !segment.is_empty() && !segment.contains(['[', ']']);
    if !named(field) || !op.is_none_or(named) {
        return None;
    }
    Some((field, op))
}

// A field's filter as an implementation takes it (D106): a member per
// operator, each `None`, or empty, when the request holds none. Which type
// a field's is follows its operators: Equals for eq and in alone (a UUID,
// an enum, a boolean), Ordered for a number, a date and a time, which add
// gt, gte, lt and lte, and Text for text, which adds contains. `is_null`
// is set only on a field that may be null: true keeps the rows whose field
// is null, false the others. The `in` operator's member is `in_`, `in`
// being a keyword.

/// The filter of a field compared for equality alone.
#[derive(Clone, Debug, PartialEq)]
pub struct Equals<T> {
    pub eq: Option<T>,
    pub in_: Vec<T>,
    pub is_null: Option<bool>,
}

/// The filter of a field whose values have an order: a number, a date or
/// a time.
#[derive(Clone, Debug, PartialEq)]
pub struct Ordered<T> {
    pub eq: Option<T>,
    pub in_: Vec<T>,
    pub gt: Option<T>,
    pub gte: Option<T>,
    pub lt: Option<T>,
    pub lte: Option<T>,
    pub is_null: Option<bool>,
}

/// The filter of a field of text. `contains` is the text a value holds, in
/// any case.
#[derive(Clone, Debug, PartialEq)]
pub struct Text<T> {
    pub eq: Option<T>,
    pub in_: Vec<T>,
    pub contains: Option<String>,
    pub is_null: Option<bool>,
}

// The filter that sets no member, whatever T is: what a caller starts
// from, `Ordered { gte: Some(5), ..Default::default() }`.

impl<T> Default for Equals<T> {
    fn default() -> Self {
        Self {
            eq: None,
            in_: Vec::new(),
            is_null: None,
        }
    }
}

impl<T> Default for Ordered<T> {
    fn default() -> Self {
        Self {
            eq: None,
            in_: Vec::new(),
            gt: None,
            gte: None,
            lt: None,
            lte: None,
            is_null: None,
        }
    }
}

impl<T> Default for Text<T> {
    fn default() -> Self {
        Self {
            eq: None,
            in_: Vec::new(),
            contains: None,
            is_null: None,
        }
    }
}

/// One field's filters of every shape, which each decode function narrows
/// to its type.
struct Decoded<T> {
    eq: Option<T>,
    in_: Vec<T>,
    gt: Option<T>,
    gte: Option<T>,
    lt: Option<T>,
    lte: Option<T>,
    contains: Option<String>,
    is_null: Option<bool>,
}

/// Reads the request's filters of one field, `None` when it holds none:
/// each value as an item of a query list of `T`
/// ([`ParamSpec::query_item`]), refused at `value`'s name, `filter[field]`;
/// `contains` is its text, and `isNull` the boolean the reader held it to.
fn decode_field<T: DeserializeOwned>(
    filters: &[Filter],
    field: &str,
    value: &ParamSpec,
) -> Result<Option<Decoded<T>>, ApiError> {
    let mut out = Decoded {
        eq: None,
        in_: Vec::new(),
        gt: None,
        gte: None,
        lt: None,
        lte: None,
        contains: None,
        is_null: None,
    };
    let mut any = false;
    for filter in filters.iter().filter(|filter| filter.field == field) {
        let Some(first) = filter.values.first() else {
            continue;
        };
        any = true;
        match filter.operator {
            Operator::Eq => out.eq = Some(value.query_item(first)?),
            Operator::In => {
                out.in_ = filter
                    .values
                    .iter()
                    .map(|text| value.query_item(text))
                    .collect::<Result<_, _>>()?;
            }
            Operator::Gt => out.gt = Some(value.query_item(first)?),
            Operator::Gte => out.gte = Some(value.query_item(first)?),
            Operator::Lt => out.lt = Some(value.query_item(first)?),
            Operator::Lte => out.lte = Some(value.query_item(first)?),
            Operator::Contains => out.contains = Some(first.clone()),
            Operator::IsNull => out.is_null = Some(first == "true"),
        }
    }
    Ok(any.then_some(out))
}

/// Reads the request's filters of a field compared for equality alone, or
/// `None` when it holds none. `value` is the field's value as a query
/// parameter, named `filter[field]`, which a value's refusal names: the
/// 400 an item of a query list of the field's type gets.
pub fn decode_equals<T: DeserializeOwned>(
    filters: &[Filter],
    field: &str,
    value: &ParamSpec,
) -> Result<Option<Equals<T>>, ApiError> {
    Ok(decode_field(filters, field, value)?.map(|d| Equals {
        eq: d.eq,
        in_: d.in_,
        is_null: d.is_null,
    }))
}

/// Reads the request's filters of an ordered field, or `None` when it
/// holds none, as [`decode_equals`] reads a field's.
pub fn decode_ordered<T: DeserializeOwned>(
    filters: &[Filter],
    field: &str,
    value: &ParamSpec,
) -> Result<Option<Ordered<T>>, ApiError> {
    Ok(decode_field(filters, field, value)?.map(|d| Ordered {
        eq: d.eq,
        in_: d.in_,
        gt: d.gt,
        gte: d.gte,
        lt: d.lt,
        lte: d.lte,
        is_null: d.is_null,
    }))
}

/// Reads the request's filters of a field of text, or `None` when it holds
/// none, as [`decode_equals`] reads a field's.
pub fn decode_text<T: DeserializeOwned>(
    filters: &[Filter],
    field: &str,
    value: &ParamSpec,
) -> Result<Option<Text<T>>, ApiError> {
    Ok(decode_field(filters, field, value)?.map(|d| Text {
        eq: d.eq,
        in_: d.in_,
        contains: d.contains,
        is_null: d.is_null,
    }))
}

/// The members a filter sets, as [`check_members`] reads them: each
/// operator with its values of the field's type, then `contains` and
/// `is_null`.
struct Members<'a, T> {
    values: [(Operator, &'a [T]); 6],
    contains: Option<&'a str>,
    is_null: Option<bool>,
}

/// Holds a filter an in-process caller built to the route's rules: the
/// router would refuse a request that says the same, with the same 400 at
/// `filter[field]`. An operator the field does not take, `is_null` on a
/// field that is never null among them; an `in` past [`MAX_IN_VALUES`]
/// values; a `contains` of no text; then each value, checked as the
/// router checks one of the field's type. A field the spec does not name
/// is the generator's error, a 500.
fn check_members<T: Serialize>(
    spec: &FilterSpec,
    field: &str,
    value: &ParamSpec,
    members: &Members<'_, T>,
) -> Result<(), ApiError> {
    let Some((_, field_spec)) = spec.field(field) else {
        return Err(ApiError::internal(format!(
            "the operation's filter has no field {field}"
        )));
    };
    let mut set: Vec<Operator> = members
        .values
        .iter()
        .filter(|(_, values)| !values.is_empty())
        .map(|(operator, _)| *operator)
        .collect();
    if members.contains.is_some() {
        set.push(Operator::Contains);
    }
    if members.is_null.is_some() {
        set.push(Operator::IsNull);
    }
    if let Some(operator) = set.iter().find(|operator| !field_spec.takes(**operator)) {
        return Err(field_spec.refuse_operator(operator.as_str()));
    }
    for (operator, values) in &members.values {
        if *operator == Operator::In {
            check_in_length(field, values.len())?;
        }
    }
    if members.contains == Some("") {
        return Err(refuse(&at(field), "contains needs text", None));
    }
    for (_, values) in &members.values {
        for item in *values {
            value.check_value(ParamLocation::Query, item)?;
        }
    }
    Ok(())
}

impl<T: Serialize> Equals<T> {
    /// Holds a filter a caller built, in-process rather than in a request,
    /// to the route's rules (`Args::check`): `spec` is the operation's
    /// filter, `field` this filter's field and `value` its value's
    /// parameter. The router would refuse a request that says the same,
    /// with the same 400 at `filter[field]`: an operator the field does
    /// not take, `is_null` on a field that is never null among them, an
    /// `in_` past [`MAX_IN_VALUES`] values, then a value its type refuses.
    /// An empty `in_` is none.
    pub fn check(&self, spec: &FilterSpec, field: &str, value: &ParamSpec) -> Result<(), ApiError> {
        let members = Members {
            values: [
                (Operator::Eq, self.eq.as_slice()),
                (Operator::In, &self.in_),
                (Operator::Gt, &[]),
                (Operator::Gte, &[]),
                (Operator::Lt, &[]),
                (Operator::Lte, &[]),
            ],
            contains: None,
            is_null: self.is_null,
        };
        check_members(spec, field, value, &members)
    }
}

impl<T: Serialize> Ordered<T> {
    /// Holds a filter a caller built to the route's rules, as
    /// [`Equals::check`] holds one.
    pub fn check(&self, spec: &FilterSpec, field: &str, value: &ParamSpec) -> Result<(), ApiError> {
        let members = Members {
            values: [
                (Operator::Eq, self.eq.as_slice()),
                (Operator::In, &self.in_),
                (Operator::Gt, self.gt.as_slice()),
                (Operator::Gte, self.gte.as_slice()),
                (Operator::Lt, self.lt.as_slice()),
                (Operator::Lte, self.lte.as_slice()),
            ],
            contains: None,
            is_null: self.is_null,
        };
        check_members(spec, field, value, &members)
    }
}

impl<T: Serialize> Text<T> {
    /// Holds a filter a caller built to the route's rules, as
    /// [`Equals::check`] holds one; a `contains` of no text is refused as
    /// the router refuses one.
    pub fn check(&self, spec: &FilterSpec, field: &str, value: &ParamSpec) -> Result<(), ApiError> {
        let members = Members {
            values: [
                (Operator::Eq, self.eq.as_slice()),
                (Operator::In, &self.in_),
                (Operator::Gt, &[]),
                (Operator::Gte, &[]),
                (Operator::Lt, &[]),
                (Operator::Lte, &[]),
            ],
            contains: self.contains.as_deref(),
            is_null: self.is_null,
        };
        check_members(spec, field, value, &members)
    }
}

#[cfg(test)]
mod tests {
    use serde_json::{json, Value};

    use super::*;
    use crate::schema::ValidationError;
    use crate::ParamKind;

    const SPEC: FilterSpec = FilterSpec::new(&[
        FieldSpec::new("kind", &[Operator::Eq, Operator::In]),
        FieldSpec::new(
            "quantity",
            &[
                Operator::Eq,
                Operator::In,
                Operator::Gt,
                Operator::Gte,
                Operator::Lt,
                Operator::Lte,
            ],
        ),
        FieldSpec::new("name", &[Operator::Eq, Operator::In, Operator::Contains]),
        FieldSpec::new("retired", &[Operator::Eq, Operator::In, Operator::IsNull]),
        FieldSpec::new("weight", &Operator::ALL),
        FieldSpec::new("retiredOn", &Operator::ALL),
        FieldSpec::new("price", &Operator::ALL),
    ]);

    static KIND: ParamSpec = ParamSpec::new("filter[kind]", ParamKind::Enum)
        .required()
        .enum_values(&["bolt", "nut"]);
    static QUANTITY: ParamSpec = ParamSpec::new("filter[quantity]", ParamKind::Integer)
        .required()
        .min(0.0);
    static NAME: ParamSpec = ParamSpec::new("filter[name]", ParamKind::String)
        .required()
        .max_length(8);
    static RETIRED: ParamSpec = ParamSpec::new("filter[retired]", ParamKind::Boolean).required();
    static WEIGHT: ParamSpec = ParamSpec::new("filter[weight]", ParamKind::Number).required();
    static RETIRED_ON: ParamSpec = ParamSpec::new("filter[retiredOn]", ParamKind::Date)
        .required()
        .parse(parse_date);
    static PRICE: ParamSpec = ParamSpec::new("filter[price]", ParamKind::Decimal)
        .required()
        .parse(parse_decimal);

    /// A stand-in for the scalar core's parse of a date: three numbers
    /// between dashes or slashes, written between dashes.
    fn parse_date(text: &str) -> Result<String, Vec<ValidationError>> {
        let parts: Vec<&str> = text.split(['-', '/']).collect();
        let numbers = parts
            .iter()
            .all(|part| !part.is_empty() && part.bytes().all(|byte| byte.is_ascii_digit()));
        if parts.len() != 3 || !numbers {
            let message = format!("failed to parse Date: {text}");
            return Err(vec![ValidationError::new("parse", message)]);
        }
        Ok(format!("{}-{:0>2}-{:0>2}", parts[0], parts[1], parts[2]))
    }

    /// A stand-in for the scalar core's parse of a decimal: a number,
    /// written without the zeros that end its fraction.
    fn parse_decimal(text: &str) -> Result<String, Vec<ValidationError>> {
        if text.parse::<f64>().is_err() {
            let message = format!("failed to parse Decimal: {text}");
            return Err(vec![ValidationError::new("parse", message)]);
        }
        Ok(text.trim_end_matches('0').trim_end_matches('.').to_owned())
    }

    /// A query's pairs, each a key and a value.
    type Pairs<'a> = &'a [(&'a str, &'a str)];

    fn read(pairs: Pairs<'_>) -> Vec<Filter> {
        let pairs = pairs
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
            .collect();
        SPEC.query(&QueryValues::new(pairs)).expect("the filters")
    }

    /// A refusal's detail and details, the 400 it is.
    fn refusal<T: fmt::Debug>(result: Result<T, ApiError>) -> (String, Value) {
        let err = result.expect_err("expected a refusal");
        assert_eq!(err.status, axum::http::StatusCode::BAD_REQUEST);
        assert_eq!(err.code, "bad_request");
        (
            err.message,
            *err.details.expect("a refusal carries details"),
        )
    }

    #[test]
    fn operators_read_and_write_as_the_wire_names_them() {
        for operator in Operator::ALL {
            assert_eq!(Operator::parse(operator.as_str()), Some(operator));
            assert_eq!(
                serde_json::to_value(operator).unwrap(),
                json!(operator.as_str())
            );
            assert_eq!(operator.to_string(), operator.as_str());
        }
        assert_eq!(Operator::parse("neq"), None);
        assert_eq!(Operator::parse("isnull"), None);
    }

    #[test]
    fn a_field_without_a_filter_is_none() {
        let filters = read(&[("filter[kind]", "bolt"), ("limit", "5")]);
        assert_eq!(
            decode_ordered::<i64>(&filters, "quantity", &QUANTITY).unwrap(),
            None
        );
        assert_eq!(
            decode_text::<String>(&filters, "name", &NAME).unwrap(),
            None
        );
        assert_eq!(
            decode_equals::<String>(&filters, "kind", &KIND).unwrap(),
            Some(Equals {
                eq: Some("bolt".to_owned()),
                ..Default::default()
            })
        );
    }

    #[test]
    fn each_shape_decodes_its_operators() {
        let filters = read(&[
            ("filter[quantity][lte]", "9"),
            ("filter[kind][in]", "bolt, nut"),
            ("filter[quantity][gt]", "1"),
            ("filter[name][contains]", "M4 "),
            ("filter[quantity][in]", "2,3"),
            ("filter[retired][isNull]", "false"),
            ("filter[quantity][gte]", "2"),
            ("filter[name][eq]", ""),
            ("filter[quantity]", "4"),
            ("filter[retired]", "T"),
            ("filter[quantity][lt]", "10"),
        ]);
        assert_eq!(
            decode_equals::<String>(&filters, "kind", &KIND).unwrap(),
            Some(Equals {
                in_: vec!["bolt".to_owned(), "nut".to_owned()],
                ..Default::default()
            })
        );
        assert_eq!(
            decode_ordered::<i64>(&filters, "quantity", &QUANTITY).unwrap(),
            Some(Ordered {
                eq: Some(4),
                in_: vec![2, 3],
                gt: Some(1),
                gte: Some(2),
                lt: Some(10),
                lte: Some(9),
                is_null: None,
            })
        );
        // contains is the text as sent, and an empty eq a value of text.
        assert_eq!(
            decode_text::<String>(&filters, "name", &NAME).unwrap(),
            Some(Text {
                eq: Some(String::new()),
                contains: Some("M4 ".to_owned()),
                ..Default::default()
            })
        );
        // A boolean reads as an item of a query list does: T is true.
        assert_eq!(
            decode_equals::<bool>(&filters, "retired", &RETIRED).unwrap(),
            Some(Equals {
                eq: Some(true),
                in_: Vec::new(),
                is_null: Some(false),
            })
        );
    }

    #[test]
    fn a_value_its_type_refuses_is_refused_at_the_filter() {
        let cases: [(Pairs<'_>, &str, Value); 6] = [
            (
                &[("filter[quantity]", "many")],
                "Invalid query parameter filter[quantity]: expected an integer",
                json!({
                    "location": "query",
                    "parameter": "filter[quantity]",
                    "reason": "expected an integer",
                    "errors": [{"validator": "type", "message": "expected an integer"}],
                }),
            ),
            (
                &[("filter[quantity][in]", "1,2.5")],
                "Invalid query parameter filter[quantity]: expected an integer",
                json!({
                    "location": "query",
                    "parameter": "filter[quantity]",
                    "reason": "expected an integer",
                    "errors": [{"validator": "type", "message": "expected an integer"}],
                }),
            ),
            (
                &[("filter[quantity][gte]", "-1")],
                "Invalid query parameter filter[quantity]: must be at least 0",
                json!({
                    "location": "query",
                    "parameter": "filter[quantity]",
                    "reason": "must be at least 0",
                    "errors": [{"validator": "min", "message": "must be at least 0"}],
                }),
            ),
            (
                &[("filter[kind]", "gear")],
                "Invalid query parameter filter[kind]: expected one of bolt, nut",
                json!({
                    "location": "query",
                    "parameter": "filter[kind]",
                    "reason": "expected one of bolt, nut",
                }),
            ),
            (
                &[("filter[name][in]", "bolt,a long name")],
                "Invalid query parameter filter[name]: must be at most 8 characters",
                json!({
                    "location": "query",
                    "parameter": "filter[name]",
                    "reason": "must be at most 8 characters",
                    "errors": [{"validator": "maxLength", "message": "must be at most 8 characters"}],
                }),
            ),
            (
                &[("filter[retired]", "yes")],
                "Invalid query parameter filter[retired]: expected a boolean",
                json!({
                    "location": "query",
                    "parameter": "filter[retired]",
                    "reason": "expected a boolean",
                    "errors": [{"validator": "type", "message": "expected a boolean"}],
                }),
            ),
        ];
        for (pairs, detail, details) in cases {
            let filters = read(pairs);
            let refused = match pairs[0].0 {
                key if key.starts_with("filter[quantity]") => {
                    refusal(decode_ordered::<i64>(&filters, "quantity", &QUANTITY))
                }
                key if key.starts_with("filter[kind]") => {
                    refusal(decode_equals::<String>(&filters, "kind", &KIND))
                }
                key if key.starts_with("filter[name]") => {
                    refusal(decode_text::<String>(&filters, "name", &NAME))
                }
                _ => refusal(decode_equals::<bool>(&filters, "retired", &RETIRED)),
            };
            assert_eq!(refused, (detail.to_owned(), details), "{pairs:?}");
        }

        // A contains is text whatever the field's type refuses.
        let filters = read(&[("filter[name][contains]", "a long name")]);
        assert!(decode_text::<String>(&filters, "name", &NAME).is_ok());
    }

    #[test]
    fn a_date_and_a_decimal_are_read_by_their_scalars_parse() {
        // Each value is its canonical text, an in's too.
        let filters = read(&[
            ("filter[retiredOn][gte]", " 2026/3/1 "),
            ("filter[retiredOn][in]", "2026-03-01,2026/12/5"),
            ("filter[price]", "2.50"),
        ]);
        let retired = decode_ordered::<String>(&filters, "retiredOn", &RETIRED_ON).unwrap();
        assert_eq!(
            retired,
            Some(Ordered {
                gte: Some("2026-03-01".to_owned()),
                in_: vec!["2026-03-01".to_owned(), "2026-12-05".to_owned()],
                ..Default::default()
            })
        );
        let price = decode_ordered::<String>(&filters, "price", &PRICE).unwrap();
        assert_eq!(price.unwrap().eq.as_deref(), Some("2.5"));

        // Text that is none is refused at the filter, with the parse's
        // errors, in a request and in a filter a caller built.
        let filters = read(&[("filter[retiredOn][lt]", "yesterday")]);
        let refused = refusal(decode_ordered::<String>(&filters, "retiredOn", &RETIRED_ON));
        assert_eq!(
            refused,
            (
                "Invalid query parameter filter[retiredOn]: expected a date".to_owned(),
                json!({
                    "location": "query",
                    "parameter": "filter[retiredOn]",
                    "reason": "expected a date",
                    "errors": [{"validator": "parse", "message": "failed to parse Date: yesterday"}],
                }),
            )
        );
        let built = Ordered {
            lt: Some("yesterday"),
            ..Default::default()
        };
        assert_eq!(
            refusal(built.check(&SPEC, "retiredOn", &RETIRED_ON)),
            refused
        );
        let built = Ordered {
            lt: Some("2026-03-01"),
            ..Default::default()
        };
        assert!(built.check(&SPEC, "retiredOn", &RETIRED_ON).is_ok());

        let filters = read(&[("filter[price][in]", "1.5,cheap")]);
        let refused = refusal(decode_ordered::<String>(&filters, "price", &PRICE));
        assert_eq!(
            refused.0,
            "Invalid query parameter filter[price]: expected a decimal number"
        );
        assert_eq!(refused.1["errors"][0]["validator"], "parse");
        let built = Ordered {
            in_: vec!["1.5", "cheap"],
            ..Default::default()
        };
        assert_eq!(refusal(built.check(&SPEC, "price", &PRICE)), refused);

        // An empty value is no date.
        let filters = read(&[("filter[retiredOn]", "")]);
        let (detail, details) =
            refusal(decode_ordered::<String>(&filters, "retiredOn", &RETIRED_ON));
        assert_eq!(
            detail,
            "Invalid query parameter filter[retiredOn]: expected a date"
        );
        assert_eq!(details.get("errors"), None);
    }

    #[test]
    fn a_number_reads_as_an_item_of_a_query_list() {
        let filters = read(&[("filter[weight][lt]", "2.5e1"), ("filter[weight]", "+1")]);
        let (detail, _) = refusal(decode_ordered::<f64>(&filters, "weight", &WEIGHT));
        assert_eq!(
            detail,
            "Invalid query parameter filter[weight]: expected a number"
        );
        let filters = read(&[("filter[weight][lt]", "2.5e1")]);
        let weight = decode_ordered::<f64>(&filters, "weight", &WEIGHT).unwrap();
        assert_eq!(weight.unwrap().lt, Some(25.0));
    }

    #[test]
    fn check_holds_a_built_filter_to_the_routes_rules() {
        let quantity = Ordered {
            eq: Some(4_i64),
            in_: vec![2, 3],
            gte: Some(0),
            ..Default::default()
        };
        assert!(quantity.check(&SPEC, "quantity", &QUANTITY).is_ok());
        assert!(Equals::<bool>::default()
            .check(&SPEC, "retired", &RETIRED)
            .is_ok());

        // A value its type refuses is the router's 400.
        let negative = Ordered {
            lt: Some(-1_i64),
            ..Default::default()
        };
        let filters = read(&[("filter[quantity][lt]", "-1")]);
        assert_eq!(
            refusal(negative.check(&SPEC, "quantity", &QUANTITY)),
            refusal(decode_ordered::<i64>(&filters, "quantity", &QUANTITY))
        );
        let gear = Equals {
            in_: vec!["bolt", "gear"],
            ..Default::default()
        };
        let filters = read(&[("filter[kind][in]", "bolt,gear")]);
        assert_eq!(
            refusal(gear.check(&SPEC, "kind", &KIND)),
            refusal(decode_equals::<String>(&filters, "kind", &KIND))
        );

        // is_null on a field that is never null, as the reader refuses it.
        let null = Equals::<&str> {
            is_null: Some(true),
            ..Default::default()
        };
        let pairs = vec![("filter[kind][isNull]".to_owned(), "true".to_owned())];
        assert_eq!(
            refusal(null.check(&SPEC, "kind", &KIND)),
            refusal(SPEC.query(&QueryValues::new(pairs)))
        );
        let (detail, _) = refusal(null.check(&SPEC, "kind", &KIND));
        assert_eq!(
            detail,
            "Invalid query parameter filter[kind]: takes no operator isNull; expected one of eq, in"
        );
        let null = Equals::<bool> {
            is_null: Some(true),
            ..Default::default()
        };
        assert!(null.check(&SPEC, "retired", &RETIRED).is_ok());

        // An in past its most values, and a contains of no text.
        let many = Ordered {
            in_: vec![1_i64; MAX_IN_VALUES + 1],
            ..Default::default()
        };
        let (detail, details) = refusal(many.check(&SPEC, "quantity", &QUANTITY));
        assert_eq!(
            detail,
            "Invalid query parameter filter[quantity]: in must hold at most 100 values"
        );
        assert_eq!(details["errors"][0]["validator"], "max");
        let blank = Text::<String> {
            contains: Some(String::new()),
            ..Default::default()
        };
        let (detail, _) = refusal(blank.check(&SPEC, "name", &NAME));
        assert_eq!(
            detail,
            "Invalid query parameter filter[name]: contains needs text"
        );

        // A shape the field's operators do not hold, and a field the spec
        // does not name.
        let (detail, _) = refusal(quantity.check(&SPEC, "kind", &QUANTITY));
        assert_eq!(
            detail,
            "Invalid query parameter filter[kind]: takes no operator gte; expected one of eq, in"
        );
        let unnamed = quantity.check(&SPEC, "color", &QUANTITY).unwrap_err();
        assert_eq!(unnamed.status.as_u16(), 500);
    }
}
