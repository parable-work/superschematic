//! Path, query and body parameters, decoded as the TypeScript runtime's
//! `params.ts` decodes them.
//!
//! The generated router carries one [`ParamSpec`] per declared argument:
//! its kind, its `Validate<>` bounds and, for a scalar type, the scalar's
//! own constraints. This module turns what a request carries into the JSON
//! value of each argument, or refuses the request with 400 and a detail
//! naming the parameter (`details: {location, parameter, path?, reason,
//! errors?}`). The generated `Args` struct then takes each value through
//! serde, so a parameter's Rust type is the type the schema declares.
//!
//! Path and query parameters arrive as strings: a query list accepts
//! repeated keys and comma-separated values, and reads each item as its
//! kind's JSON value, checked as a body list element at `name[i]`, as the
//! Go router's `bodyargs.QueryList` does. Every body parameter arrives as a
//! JSON value: a number is not accepted for a string nor a string for a
//! number, a list is a JSON array whose elements are never split or
//! dropped, a map is a JSON object whose values are checked as list
//! elements, and an object value goes through its type's generated
//! `prepare_<type>`, which fills its defaults, refuses undeclared keys and
//! validates it.
//!
//! Two kinds differ from the TypeScript runtime's. An integer is an `i64`,
//! as in the Go server, where JavaScript stops at 2^53. A UUID or timestamp
//! is checked by its scalar's generated validator, which runs the scalar
//! core, where the TypeScript runtime calls the scalar library's parse.

use std::collections::HashMap;

use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{Map, Number, Value};

use crate::schema::{
    code_points, finite_number, integer_value, ParseError, Pattern, ScalarResult, UnknownFields,
    ValidationError,
};
use crate::ApiError;

/// What a parameter's values are. `Object` is an object type of a body
/// parameter (`T`, `T[]` or `T[][]`), prepared by [`ParamSpec::prepare`];
/// `Json` is a body parameter of a JSON-valued scalar (`Generic.JSON`), any
/// JSON value but null, and null too when it is optional and single;
/// `JsonObject` and `JsonArray` are body parameters of a scalar whose value
/// is a JSON object (`Generic.StringMap`) or a JSON array
/// (`Embedding.Vector`). None of those four is read from a string.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ParamKind {
    String,
    Integer,
    Number,
    Boolean,
    Uuid,
    DateTime,
    Enum,
    Object,
    Json,
    JsonObject,
    JsonArray,
}

/// Where a parameter travels.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ParamLocation {
    Path,
    Query,
    Body,
}

impl ParamLocation {
    pub const fn as_str(self) -> &'static str {
        match self {
            ParamLocation::Path => "path",
            ParamLocation::Query => "query",
            ParamLocation::Body => "body",
        }
    }
}

/// A scalar's generated validator (`validate_<scalar>`), which checks a UUID
/// or timestamp parameter.
pub type ScalarCheck = fn(Option<&Value>) -> ScalarResult;

/// An object type's generated `prepare_<type>`: its defaults filled, its
/// undeclared keys refused and the value validated.
pub type ObjectPrepare = fn(Value, UnknownFields) -> Result<Value, ParseError>;

/// A scalar type's own constraints (its IR lengths, pattern and range), as
/// the schema runtimes check them: lengths count code points, and the
/// pattern reads `\d`, `\w` and `\b` as ASCII. A value that breaks one is
/// refused with one error named by that rule (`minLength`, `maxLength`,
/// `pattern`, `min`, `max`).
#[derive(Clone, Copy, Debug)]
pub struct ScalarConstraints {
    /// Canonical scalar name, for the refusal (`Network.Url`).
    pub name: &'static str,
    pub min_length: Option<usize>,
    pub max_length: Option<usize>,
    pub pattern: Option<&'static Pattern>,
    pub min: Option<f64>,
    pub max: Option<f64>,
}

impl ScalarConstraints {
    pub const fn new(name: &'static str) -> Self {
        Self {
            name,
            min_length: None,
            max_length: None,
            pattern: None,
            min: None,
            max: None,
        }
    }

    #[must_use]
    pub const fn min_length(mut self, min_length: usize) -> Self {
        self.min_length = Some(min_length);
        self
    }

    #[must_use]
    pub const fn max_length(mut self, max_length: usize) -> Self {
        self.max_length = Some(max_length);
        self
    }

    #[must_use]
    pub const fn pattern(mut self, pattern: &'static Pattern) -> Self {
        self.pattern = Some(pattern);
        self
    }

    #[must_use]
    pub const fn min(mut self, min: f64) -> Self {
        self.min = Some(min);
        self
    }

    #[must_use]
    pub const fn max(mut self, max: f64) -> Self {
        self.max = Some(max);
        self
    }
}

/// One declared argument, as the generated router carries it in a `static`:
/// `ParamSpec::new("ids", ParamKind::Uuid).required().array().list_max(100)`.
#[derive(Clone, Copy, Debug)]
pub struct ParamSpec {
    /// Wire name (the schema argument name).
    pub name: &'static str,
    pub kind: ParamKind,
    pub required: bool,
    pub is_array: bool,
    /// A list of lists (`T[][]`), always a body parameter; `is_array` is
    /// also set.
    pub is_array_of_arrays: bool,
    /// A map (`Record<string, T>`), always a body parameter; with
    /// `is_array`, a map of lists.
    pub is_map: bool,
    /// Serialized member values of an `Enum`.
    pub enum_values: &'static [&'static str],
    /// Applied when the parameter is absent, read as a path or query value.
    pub default_value: Option<&'static str>,
    pub min: Option<f64>,
    pub max: Option<f64>,
    pub min_length: Option<usize>,
    pub max_length: Option<usize>,
    pub list_min: Option<usize>,
    pub list_max: Option<usize>,
    pub pattern: Option<&'static Pattern>,
    /// The constraints of the parameter's scalar type, checked on every
    /// value before the argument's own.
    pub scalar: Option<ScalarConstraints>,
    /// A `Uuid` or `DateTime` parameter's scalar validator.
    pub check: Option<ScalarCheck>,
    /// An `Object` parameter's `prepare_<type>`; without one the value is
    /// taken as the JSON object it is.
    pub prepare: Option<ObjectPrepare>,
}

impl ParamSpec {
    pub const fn new(name: &'static str, kind: ParamKind) -> Self {
        Self {
            name,
            kind,
            required: false,
            is_array: false,
            is_array_of_arrays: false,
            is_map: false,
            enum_values: &[],
            default_value: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
            list_min: None,
            list_max: None,
            pattern: None,
            scalar: None,
            check: None,
            prepare: None,
        }
    }

    #[must_use]
    pub const fn required(mut self) -> Self {
        self.required = true;
        self
    }

    #[must_use]
    pub const fn array(mut self) -> Self {
        self.is_array = true;
        self
    }

    #[must_use]
    pub const fn array_of_arrays(mut self) -> Self {
        self.is_array = true;
        self.is_array_of_arrays = true;
        self
    }

    #[must_use]
    pub const fn map(mut self) -> Self {
        self.is_map = true;
        self
    }

    #[must_use]
    pub const fn enum_values(mut self, values: &'static [&'static str]) -> Self {
        self.enum_values = values;
        self
    }

    #[must_use]
    pub const fn default_value(mut self, value: &'static str) -> Self {
        self.default_value = Some(value);
        self
    }

    #[must_use]
    pub const fn min(mut self, min: f64) -> Self {
        self.min = Some(min);
        self
    }

    #[must_use]
    pub const fn max(mut self, max: f64) -> Self {
        self.max = Some(max);
        self
    }

    #[must_use]
    pub const fn min_length(mut self, min_length: usize) -> Self {
        self.min_length = Some(min_length);
        self
    }

    #[must_use]
    pub const fn max_length(mut self, max_length: usize) -> Self {
        self.max_length = Some(max_length);
        self
    }

    #[must_use]
    pub const fn list_min(mut self, list_min: usize) -> Self {
        self.list_min = Some(list_min);
        self
    }

    #[must_use]
    pub const fn list_max(mut self, list_max: usize) -> Self {
        self.list_max = Some(list_max);
        self
    }

    #[must_use]
    pub const fn pattern(mut self, pattern: &'static Pattern) -> Self {
        self.pattern = Some(pattern);
        self
    }

    #[must_use]
    pub const fn scalar(mut self, scalar: ScalarConstraints) -> Self {
        self.scalar = Some(scalar);
        self
    }

    #[must_use]
    pub const fn check(mut self, check: ScalarCheck) -> Self {
        self.check = Some(check);
        self
    }

    #[must_use]
    pub const fn prepare(mut self, prepare: ObjectPrepare) -> Self {
        self.prepare = Some(prepare);
        self
    }

    /// The path parameter: the route's capture of this name, decoded once.
    pub fn path<T: DeserializeOwned>(
        &self,
        captures: &HashMap<String, String>,
    ) -> Result<T, ApiError> {
        let raw: Vec<&str> = captures
            .get(self.name)
            .map(String::as_str)
            .into_iter()
            .collect();
        let value = decode_param(ParamLocation::Path, self, &raw)?;
        typed(ParamLocation::Path, self, value)
    }

    /// The query parameter: every occurrence of its key.
    pub fn query<T: DeserializeOwned>(&self, query: &QueryValues) -> Result<T, ApiError> {
        let value = decode_param(ParamLocation::Query, self, &query.all(self.name))?;
        typed(ParamLocation::Query, self, value)
    }

    /// The body parameter: the field of this name of the body object, which
    /// is `None` when the request has no body.
    pub fn body<T: DeserializeOwned>(
        &self,
        body: Option<&Map<String, Value>>,
    ) -> Result<T, ApiError> {
        let value = decode_json_param(
            ParamLocation::Body,
            self,
            body.and_then(|body| body.get(self.name)),
        )?;
        typed(ParamLocation::Body, self, value)
    }

    /// Checks a value a caller built for the parameter (an `Args` field), not
    /// one a request carries, as the router checks a request's: its JSON is
    /// decoded at `location`, so a value the router would refuse is refused
    /// with the same 400. `None` is an absent parameter.
    pub fn check_value<T: Serialize + ?Sized>(
        &self,
        location: ParamLocation,
        value: &T,
    ) -> Result<(), ApiError> {
        let value = serde_json::to_value(value).map_err(|_| {
            refuse(
                location,
                self,
                "does not match the declared type",
                None,
                None,
            )
        })?;
        decode_json_param(location, self, Some(&value)).map(drop)
    }

    /// An optional single `Generic.JSON` body parameter, whose null is a
    /// value apart from absent: `None` when absent, `Some(Value::Null)` for
    /// null.
    pub fn body_keep_null(
        &self,
        body: Option<&Map<String, Value>>,
    ) -> Result<Option<Value>, ApiError> {
        decode_json_param(
            ParamLocation::Body,
            self,
            body.and_then(|body| body.get(self.name)),
        )
    }
}

/// A request's query: every key and value in the order it holds them, each
/// percent-decoded once.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct QueryValues(Vec<(String, String)>);

impl QueryValues {
    pub fn new(pairs: Vec<(String, String)>) -> Self {
        Self(pairs)
    }

    /// Every value of name, in order.
    pub fn all(&self, name: &str) -> Vec<&str> {
        self.0
            .iter()
            .filter(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
            .collect()
    }

    /// Each key's last value, as `RequestContext::query_params` holds them.
    pub fn last_values(&self) -> HashMap<String, String> {
        self.0.iter().cloned().collect()
    }

    pub fn pairs(&self) -> &[(String, String)] {
        &self.0
    }
}

/// The request's decoded value of a parameter as its Rust type: `T`, or
/// `Option<T>` for an optional parameter, whose absence is `None`.
fn typed<T: DeserializeOwned>(
    location: ParamLocation,
    spec: &ParamSpec,
    value: Option<Value>,
) -> Result<T, ApiError> {
    serde_json::from_value(value.unwrap_or(Value::Null)).map_err(|_| {
        let reason = match spec.kind {
            ParamKind::Uuid => "expected a UUID".to_owned(),
            ParamKind::DateTime => "expected an RFC 3339 timestamp".to_owned(),
            ParamKind::Enum => expected_one_of(spec),
            _ => "does not match the declared type".to_owned(),
        };
        refuse(location, spec, reason, None, None)
    })
}

/// The 400 refusal of a parameter. `path` names the refused value inside a
/// list or map (`rows[2]`, `rows[2][5]`, `labels[en]`); the detail then
/// carries it beside the parameter name.
fn refuse(
    location: ParamLocation,
    spec: &ParamSpec,
    reason: impl Into<String>,
    errors: Option<Vec<ValidationError>>,
    path: Option<&str>,
) -> ApiError {
    let reason = reason.into();
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from(location.as_str()));
    details.insert("parameter".to_owned(), Value::from(spec.name));
    if let Some(path) = path {
        details.insert("path".to_owned(), Value::from(path));
    }
    details.insert("reason".to_owned(), Value::from(reason.clone()));
    if let Some(errors) = errors.filter(|errors| !errors.is_empty()) {
        let errors = errors
            .iter()
            .map(|error| {
                let mut entry = Map::new();
                entry.insert("validator".to_owned(), Value::from(error.validator.clone()));
                entry.insert("message".to_owned(), Value::from(error.message.clone()));
                Value::Object(entry)
            })
            .collect();
        details.insert("errors".to_owned(), Value::Array(errors));
    }
    ApiError::bad_request(format!(
        "Invalid {} parameter {}: {reason}",
        location.as_str(),
        path.unwrap_or(spec.name)
    ))
    .with_details(Value::Object(details))
}

/// Refuses one value with one error named by the rule it breaks
/// (`required`, `type`, `min`, `maxLength`, `pattern`, ...), reported at
/// path when it sits inside a list or map.
fn refuse_at(
    location: ParamLocation,
    spec: &ParamSpec,
    path: Option<&str>,
    validator: &str,
    message: &str,
) -> ApiError {
    refuse(
        location,
        spec,
        message,
        Some(vec![ValidationError::new(validator, message)]),
        path,
    )
}

fn expected_one_of(spec: &ParamSpec) -> String {
    format!("expected one of {}", spec.enum_values.join(", "))
}

/// A path or query value (or one item of a body string), read as its kind.
fn decode_scalar(
    location: ParamLocation,
    spec: &ParamSpec,
    raw: &str,
    path: Option<&str>,
) -> Result<Value, ApiError> {
    match spec.kind {
        ParamKind::Integer => {
            let text = raw.trim();
            let digits = text.strip_prefix(['+', '-']).unwrap_or(text);
            if digits.is_empty() || !digits.bytes().all(|byte| byte.is_ascii_digit()) {
                return Err(refuse(location, spec, "expected an integer", None, path));
            }
            let value: i64 = text
                .trim_start_matches('+')
                .parse()
                .map_err(|_| refuse(location, spec, "integer out of range", None, path))?;
            #[allow(clippy::cast_precision_loss)]
            check_number(location, spec, value as f64, path)?;
            Ok(Value::from(value))
        }
        ParamKind::Number => {
            let text = raw.trim();
            let value = text
                .parse::<f64>()
                .ok()
                .filter(|value| value.is_finite())
                .ok_or_else(|| refuse(location, spec, "expected a number", None, path))?;
            check_number(location, spec, value, path)?;
            Number::from_f64(value)
                .map(Value::Number)
                .ok_or_else(|| refuse(location, spec, "expected a number", None, path))
        }
        ParamKind::Boolean => match raw.trim().to_ascii_lowercase().as_str() {
            "true" | "1" => Ok(Value::Bool(true)),
            "false" | "0" => Ok(Value::Bool(false)),
            _ => Err(refuse(location, spec, "expected true or false", None, path)),
        },
        ParamKind::Uuid => checked_scalar(location, spec, raw.trim(), "expected a UUID", path),
        ParamKind::DateTime => checked_scalar(
            location,
            spec,
            raw.trim(),
            "expected an RFC 3339 timestamp",
            path,
        ),
        ParamKind::Enum => {
            if spec.enum_values.contains(&raw) {
                Ok(Value::from(raw))
            } else {
                Err(refuse(location, spec, expected_one_of(spec), None, path))
            }
        }
        ParamKind::Object | ParamKind::JsonObject => {
            Err(refuse(location, spec, "expected an object", None, path))
        }
        ParamKind::JsonArray => Err(refuse(location, spec, "expected an array", None, path)),
        ParamKind::String | ParamKind::Json => {
            check_string(location, spec, raw, path)?;
            Ok(Value::from(raw))
        }
    }
}

/// A UUID or timestamp: the scalar's validator, then its text.
fn checked_scalar(
    location: ParamLocation,
    spec: &ParamSpec,
    text: &str,
    reason: &str,
    path: Option<&str>,
) -> Result<Value, ApiError> {
    let value = Value::from(text);
    if text.is_empty() {
        return Err(refuse(location, spec, reason, None, path));
    }
    if let Some(check) = spec.check {
        if let Err(errors) = check(Some(&value)) {
            return Err(refuse(location, spec, reason, Some(errors), path));
        }
    }
    Ok(value)
}

/// A scalar's range, then the argument's own bounds.
fn check_number(
    location: ParamLocation,
    spec: &ParamSpec,
    value: f64,
    path: Option<&str>,
) -> Result<(), ApiError> {
    let scalar = spec.scalar.as_ref();
    let bounds = [
        (scalar.and_then(|scalar| scalar.min), true),
        (scalar.and_then(|scalar| scalar.max), false),
        (spec.min, true),
        (spec.max, false),
    ];
    for (bound, is_min) in bounds {
        match bound {
            Some(min) if is_min && value < min => {
                return Err(refuse_at(
                    location,
                    spec,
                    path,
                    "min",
                    &format!("must be at least {min}"),
                ));
            }
            Some(max) if !is_min && value > max => {
                return Err(refuse_at(
                    location,
                    spec,
                    path,
                    "max",
                    &format!("must be at most {max}"),
                ));
            }
            _ => {}
        }
    }
    Ok(())
}

/// A scalar's lengths and pattern, then the argument's own constraints.
fn check_string(
    location: ParamLocation,
    spec: &ParamSpec,
    value: &str,
    path: Option<&str>,
) -> Result<(), ApiError> {
    let length = code_points(value);
    if let Some(scalar) = &spec.scalar {
        if let Some(min) = scalar.min_length.filter(|min| length < *min) {
            return Err(refuse_at(
                location,
                spec,
                path,
                "minLength",
                &format!("must be at least {min} characters"),
            ));
        }
        if let Some(max) = scalar.max_length.filter(|max| length > *max) {
            return Err(refuse_at(
                location,
                spec,
                path,
                "maxLength",
                &format!("must be at most {max} characters"),
            ));
        }
        if scalar
            .pattern
            .is_some_and(|pattern| !pattern.is_match(value))
        {
            return Err(refuse_at(
                location,
                spec,
                path,
                "pattern",
                &format!("is not a valid {}", scalar.name),
            ));
        }
    }
    if let Some(min) = spec.min_length.filter(|min| length < *min) {
        return Err(refuse_at(
            location,
            spec,
            path,
            "minLength",
            &format!("must be at least {min} characters"),
        ));
    }
    if let Some(max) = spec.max_length.filter(|max| length > *max) {
        return Err(refuse_at(
            location,
            spec,
            path,
            "maxLength",
            &format!("must be at most {max} characters"),
        ));
    }
    if spec.pattern.is_some_and(|pattern| !pattern.is_match(value)) {
        return Err(refuse_at(
            location,
            spec,
            path,
            "pattern",
            "does not match the required pattern",
        ));
    }
    Ok(())
}

/// Decodes one path or query parameter. `raw` carries every occurrence on
/// the wire; a list parameter accepts repeated keys and comma-separated
/// values, a single one takes the first occurrence. `None` is an absent
/// optional parameter.
pub fn decode_param(
    location: ParamLocation,
    spec: &ParamSpec,
    raw: &[&str],
) -> Result<Option<Value>, ApiError> {
    let mut values = raw.to_vec();
    if values.is_empty() {
        if let Some(default) = spec.default_value {
            values.push(default);
        }
    }
    if spec.is_array {
        let items: Vec<&str> = values
            .iter()
            .flat_map(|value| value.split(','))
            .map(str::trim)
            .filter(|item| !item.is_empty())
            .collect();
        if items.is_empty() {
            return if spec.required {
                Err(refuse(location, spec, "required", None, None))
            } else {
                Ok(None)
            };
        }
        check_list_length(location, spec, items.len())?;
        let decoded = items
            .iter()
            .enumerate()
            .map(|(index, item)| {
                decode_query_list_item(location, spec, &format!("{}[{index}]", spec.name), item)
            })
            .collect::<Result<Vec<_>, _>>()?;
        return Ok(Some(Value::Array(decoded)));
    }
    match values.first() {
        Some(first) if !first.is_empty() || spec.kind == ParamKind::String => {
            decode_scalar(location, spec, first, None).map(Some)
        }
        _ if spec.required => Err(refuse(location, spec, "required", None, None)),
        _ => Ok(None),
    }
}

fn check_list_length(
    location: ParamLocation,
    spec: &ParamSpec,
    length: usize,
) -> Result<(), ApiError> {
    if let Some(min) = spec.list_min.filter(|min| length < *min) {
        return Err(refuse(
            location,
            spec,
            format!("expected at least {min} values"),
            None,
            None,
        ));
    }
    if let Some(max) = spec.list_max.filter(|max| length > *max) {
        return Err(refuse(
            location,
            spec,
            format!("expected at most {max} values"),
            None,
            None,
        ));
    }
    Ok(())
}

/// Whether text is a JSON number as JSON writes one: no sign but a leading
/// minus, no leading zero, no hex, NaN or Infinity.
fn is_json_number(text: &str) -> bool {
    let bytes = text.strip_prefix('-').unwrap_or(text).as_bytes();
    let digits = |from: usize| {
        bytes[from..]
            .iter()
            .take_while(|byte| byte.is_ascii_digit())
            .count()
    };
    let mut at = digits(0);
    if at == 0 || (at > 1 && bytes[0] == b'0') {
        return false;
    }
    if bytes.get(at) == Some(&b'.') {
        let fraction = digits(at + 1);
        if fraction == 0 {
            return false;
        }
        at += 1 + fraction;
    }
    if matches!(bytes.get(at), Some(b'e' | b'E')) {
        at += 1;
        if matches!(bytes.get(at), Some(b'+' | b'-')) {
            at += 1;
        }
        let exponent = digits(at);
        if exponent == 0 {
            return false;
        }
        at += exponent;
    }
    at == bytes.len()
}

/// One item of a query list, checked at path (`name[i]`) as a body list
/// element is, the rules of the Go router's `bodyargs.QueryList`: a number
/// or integer item must be a finite JSON number and a boolean item a
/// spelling Go's `strconv.ParseBool` accepts (`type` otherwise); any other
/// item is its text, a JSON string. The value then passes the checks of its
/// kind: the scalar's rules, then the argument's.
fn decode_query_list_item(
    location: ParamLocation,
    spec: &ParamSpec,
    path: &str,
    item: &str,
) -> Result<Value, ApiError> {
    match spec.kind {
        ParamKind::Integer | ParamKind::Number => {
            let message = if spec.kind == ParamKind::Integer {
                "expected an integer"
            } else {
                "expected a number"
            };
            let number = Some(item)
                .filter(|item| is_json_number(item))
                .and_then(|item| serde_json::from_str::<Number>(item).ok())
                .filter(|number| finite_number(number).is_some())
                .ok_or_else(|| refuse_at(location, spec, Some(path), "type", message))?;
            decode_json_value(location, spec, &Value::Number(number), Some(path))
        }
        ParamKind::Boolean => match item {
            "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(Value::Bool(true)),
            "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(Value::Bool(false)),
            _ => Err(refuse_at(
                location,
                spec,
                Some(path),
                "type",
                "expected a boolean",
            )),
        },
        _ => decode_json_value(location, spec, &Value::from(item), Some(path)),
    }
}

/// The integer a JSON number holds, or why it holds none.
enum Integer {
    Value(i64),
    Fractional,
    OutOfRange,
}

fn integer_of(number: &Number) -> Integer {
    if let Some(value) = number.as_i64() {
        return Integer::Value(value);
    }
    match integer_value(number) {
        None => Integer::Fractional,
        // i64::MIN is -2^63, exactly an f64; i64::MAX is not, so the range
        // ends below 2^63.
        Some(value)
            if (-9_223_372_036_854_775_808.0..9_223_372_036_854_775_808.0).contains(&value) =>
        {
            #[allow(clippy::cast_possible_truncation)]
            Integer::Value(value as i64)
        }
        Some(_) => Integer::OutOfRange,
    }
}

/// One non-null JSON value, checked as its kind: an object goes through the
/// spec's prepare, a JSON-valued scalar is taken as it is, and every other
/// kind must arrive as its JSON type (`type` otherwise) and then passes the
/// checks of a path or query value of that kind.
fn decode_json_value(
    location: ParamLocation,
    spec: &ParamSpec,
    item: &Value,
    path: Option<&str>,
) -> Result<Value, ApiError> {
    match spec.kind {
        ParamKind::Object => {
            if !item.is_object() {
                return Err(refuse_at(
                    location,
                    spec,
                    path,
                    "type",
                    "expected an object",
                ));
            }
            match spec.prepare {
                // The parse's message names generated functions; as for the
                // input body, the refusal says what failed and where.
                Some(prepare) => prepare(item.clone(), UnknownFields::Refuse).map_err(|_| {
                    refuse(
                        location,
                        spec,
                        "does not match the declared type",
                        None,
                        path,
                    )
                }),
                None => Ok(item.clone()),
            }
        }
        ParamKind::Json => Ok(item.clone()),
        ParamKind::JsonObject if item.is_object() => Ok(item.clone()),
        ParamKind::JsonObject => Err(refuse_at(
            location,
            spec,
            path,
            "type",
            "expected an object",
        )),
        ParamKind::JsonArray if item.is_array() => Ok(item.clone()),
        ParamKind::JsonArray => Err(refuse_at(location, spec, path, "type", "expected an array")),
        ParamKind::Integer => {
            let Value::Number(number) = item else {
                return Err(refuse_at(
                    location,
                    spec,
                    path,
                    "type",
                    "expected an integer",
                ));
            };
            match integer_of(number) {
                Integer::Value(value) => {
                    #[allow(clippy::cast_precision_loss)]
                    check_number(location, spec, value as f64, path)?;
                    Ok(Value::from(value))
                }
                Integer::Fractional => Err(refuse_at(
                    location,
                    spec,
                    path,
                    "type",
                    "expected an integer",
                )),
                Integer::OutOfRange => {
                    Err(refuse(location, spec, "integer out of range", None, path))
                }
            }
        }
        ParamKind::Number => {
            let value = item
                .as_number()
                .and_then(finite_number)
                .ok_or_else(|| refuse_at(location, spec, path, "type", "expected a number"))?;
            check_number(location, spec, value, path)?;
            Ok(item.clone())
        }
        ParamKind::Boolean if item.is_boolean() => Ok(item.clone()),
        ParamKind::Boolean => Err(refuse_at(
            location,
            spec,
            path,
            "type",
            "expected a boolean",
        )),
        ParamKind::String | ParamKind::Uuid | ParamKind::DateTime | ParamKind::Enum => {
            let Some(text) = item.as_str() else {
                return Err(refuse_at(location, spec, path, "type", "expected a string"));
            };
            decode_scalar(location, spec, text, path)
        }
    }
}

/// One list element or map value: never null, then the checks of its kind,
/// reported at path.
fn decode_element(
    location: ParamLocation,
    spec: &ParamSpec,
    path: &str,
    item: &Value,
) -> Result<Value, ApiError> {
    if item.is_null() {
        return Err(refuse_at(
            location,
            spec,
            Some(path),
            "required",
            "required field",
        ));
    }
    decode_json_value(location, spec, item, Some(path))
}

/// Every element of a list, each at `path[i]`.
fn decode_elements(
    location: ParamLocation,
    spec: &ParamSpec,
    path: &str,
    items: &[Value],
) -> Result<Value, ApiError> {
    items
        .iter()
        .enumerate()
        .map(|(index, item)| decode_element(location, spec, &format!("{path}[{index}]"), item))
        .collect::<Result<Vec<_>, _>>()
        .map(Value::Array)
}

/// The outer list is the parameter: absent or null is refused when
/// required, otherwise it is an array within `list_min` and `list_max`.
fn outer_list<'a>(
    location: ParamLocation,
    spec: &ParamSpec,
    value: Option<&'a Value>,
) -> Result<Option<&'a Vec<Value>>, ApiError> {
    let items = match value {
        None | Some(Value::Null) if spec.required => {
            return Err(refuse(location, spec, "required", None, None))
        }
        None | Some(Value::Null) => return Ok(None),
        Some(Value::Array(items)) => items,
        Some(_) => {
            return Err(refuse(
                location,
                spec,
                "expected an array",
                Some(vec![ValidationError::new("type", "expected an array")]),
                None,
            ))
        }
    };
    check_list_length(location, spec, items.len())?;
    Ok(Some(items))
}

/// A list of lists (`T[][]`) from its JSON body value, with the list rules
/// the schema runtimes and the Go router share: the outer list is the
/// parameter (required means present, an empty list is valid, `list_min`
/// and `list_max` bound it); an inner list is never null (`required`) and
/// must be an array (`type`), both at `name[i]`, and may be empty; every
/// innermost element is never null and passes the checks of a `T[]`
/// element, at `name[i][j]`.
fn decode_list_of_lists(
    location: ParamLocation,
    spec: &ParamSpec,
    value: Option<&Value>,
) -> Result<Option<Value>, ApiError> {
    let Some(rows) = outer_list(location, spec, value)? else {
        return Ok(None);
    };
    rows.iter()
        .enumerate()
        .map(|(index, row)| {
            let row_path = format!("{}[{index}]", spec.name);
            match row {
                Value::Null => Err(refuse_at(
                    location,
                    spec,
                    Some(&row_path),
                    "required",
                    "required field",
                )),
                Value::Array(items) => decode_elements(location, spec, &row_path, items),
                _ => Err(refuse_at(
                    location,
                    spec,
                    Some(&row_path),
                    "type",
                    "expected an array",
                )),
            }
        })
        .collect::<Result<Vec<_>, _>>()
        .map(|rows| Some(Value::Array(rows)))
}

/// A map (`Record<string, T>`, or `Record<string, T[]>` when `is_array` is
/// set) from its JSON body value, with the rules the Go router's map
/// arguments follow: the map is the parameter (absent or null is refused
/// when required, anything but a JSON object is `type`, an empty object is
/// valid); a value is never null (`required`) and passes the checks of a
/// `T[]` element, at `name[key]`; in a map of lists a value is a list
/// (`type` otherwise) whose elements are checked at `name[key][i]`.
/// `list_min` and `list_max` do not bound a map or its lists, as in the
/// generated types.
fn decode_map(
    location: ParamLocation,
    spec: &ParamSpec,
    value: Option<&Value>,
) -> Result<Option<Value>, ApiError> {
    let entries = match value {
        None | Some(Value::Null) if spec.required => {
            return Err(refuse(location, spec, "required", None, None))
        }
        None | Some(Value::Null) => return Ok(None),
        Some(Value::Object(entries)) => entries,
        Some(_) => {
            return Err(refuse(
                location,
                spec,
                "expected an object",
                Some(vec![ValidationError::new("type", "expected an object")]),
                None,
            ))
        }
    };
    let mut decoded = Map::new();
    for (key, entry) in entries {
        let path = format!("{}[{key}]", spec.name);
        let value = if !spec.is_array {
            decode_element(location, spec, &path, entry)?
        } else {
            match entry {
                Value::Null => {
                    return Err(refuse_at(
                        location,
                        spec,
                        Some(&path),
                        "required",
                        "required field",
                    ))
                }
                Value::Array(items) => decode_elements(location, spec, &path, items)?,
                _ => {
                    return Err(refuse_at(
                        location,
                        spec,
                        Some(&path),
                        "type",
                        "expected an array",
                    ))
                }
            }
        };
        decoded.insert(key.clone(), value);
    }
    Ok(Some(Value::Object(decoded)))
}

/// Decodes a body parameter from its JSON value (`None` when the body
/// object lacks it):
///
/// - a map follows the map rules, and `T[][]` the list-of-lists rules;
/// - `T[]` follows the list rules one level down: the list is the parameter
///   (required means present, an empty list is valid, `list_min` and
///   `list_max` bound it), and each element is never null and passes the
///   checks of its kind, both at `name[i]`;
/// - `T` is refused when required and absent or null, and otherwise passes
///   the checks of its kind. An optional `Json` `T` (`Generic.JSON`) takes
///   null as a value: it decodes to `Some(Value::Null)`, apart from an
///   absent one, which is `None`.
///
/// A parameter of any other kind with a default that is absent or null
/// decodes the default as a path or query value would.
pub fn decode_json_param(
    location: ParamLocation,
    spec: &ParamSpec,
    value: Option<&Value>,
) -> Result<Option<Value>, ApiError> {
    if spec.is_map {
        return decode_map(location, spec, value);
    }
    let absent = matches!(value, None | Some(Value::Null));
    if value.is_some_and(Value::is_null)
        && spec.kind == ParamKind::Json
        && !spec.required
        && !spec.is_array
    {
        return Ok(Some(Value::Null));
    }
    if absent
        && spec.default_value.is_some()
        && !spec.is_array_of_arrays
        && spec.kind != ParamKind::Object
    {
        return decode_param(location, spec, &[]);
    }
    if spec.is_array_of_arrays {
        return decode_list_of_lists(location, spec, value);
    }
    if spec.is_array {
        return match outer_list(location, spec, value)? {
            Some(items) => decode_elements(location, spec, spec.name, items).map(Some),
            None => Ok(None),
        };
    }
    match value {
        Some(value) if !value.is_null() => decode_json_value(location, spec, value, None).map(Some),
        _ if spec.required => Err(refuse(location, spec, "required", None, None)),
        _ => Ok(None),
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn refusal<T: std::fmt::Debug>(result: Result<T, ApiError>) -> Value {
        let err = result.expect_err("expected a refusal");
        assert_eq!(err.status, axum::http::StatusCode::BAD_REQUEST);
        assert_eq!(err.code, "bad_request");
        *err.details.expect("a parameter refusal carries details")
    }

    fn query(pairs: &[(&str, &str)]) -> QueryValues {
        QueryValues::new(
            pairs
                .iter()
                .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
                .collect(),
        )
    }

    #[test]
    fn a_built_value_is_checked_as_a_request_value_is() {
        static NAME_PATTERN: Pattern = Pattern::new("^[a-z]+$");
        static NAME: ParamSpec = ParamSpec::new("name", ParamKind::String)
            .required()
            .pattern(&NAME_PATTERN)
            .scalar(ScalarConstraints::new("Identity.Name").min_length(2));
        static TAGS: ParamSpec = ParamSpec::new("tags", ParamKind::String)
            .array()
            .list_max(1);
        NAME.check_value(ParamLocation::Query, "ab").unwrap();
        assert_eq!(
            refusal(NAME.check_value(ParamLocation::Query, "a")),
            refusal(NAME.query::<String>(&query(&[("name", "a")])))
        );
        assert_eq!(
            refusal(NAME.check_value(ParamLocation::Path, "AB")),
            refusal(NAME.path::<String>(&HashMap::from([("name".to_owned(), "AB".to_owned())])))
        );
        TAGS.check_value(ParamLocation::Body, &None::<Vec<String>>)
            .unwrap();
        let details = refusal(TAGS.check_value(ParamLocation::Body, &Some(vec!["a", "b"])));
        assert_eq!(details["parameter"], "tags");
        assert_eq!(details["location"], "body");
    }

    #[test]
    fn integers_and_numbers_from_text() {
        static COUNT: ParamSpec = ParamSpec::new("count", ParamKind::Integer)
            .required()
            .min(1.0)
            .max(10.0);
        assert_eq!(COUNT.query::<i64>(&query(&[("count", " 7 ")])).unwrap(), 7);
        assert_eq!(COUNT.query::<i64>(&query(&[("count", "+3")])).unwrap(), 3);
        let details = refusal(COUNT.query::<i64>(&query(&[("count", "1.5")])));
        assert_eq!(
            details,
            json!({"location": "query", "parameter": "count", "reason": "expected an integer"})
        );
        let details = refusal(COUNT.query::<i64>(&query(&[("count", "11")])));
        assert_eq!(
            details["errors"],
            json!([{"validator": "max", "message": "must be at most 10"}])
        );
        let details = refusal(COUNT.query::<i64>(&query(&[("count", "99999999999999999999")])));
        assert_eq!(details["reason"], "integer out of range");
        let details = refusal(COUNT.query::<i64>(&query(&[])));
        assert_eq!(details["reason"], "required");

        static RATIO: ParamSpec = ParamSpec::new("ratio", ParamKind::Number);
        assert_eq!(
            RATIO
                .query::<Option<f64>>(&query(&[("ratio", "0.25")]))
                .unwrap(),
            Some(0.25)
        );
        assert_eq!(
            RATIO
                .query::<Option<f64>>(&query(&[("ratio", "")]))
                .unwrap(),
            None
        );
        assert_eq!(RATIO.query::<Option<f64>>(&query(&[])).unwrap(), None);
        assert_eq!(
            refusal(RATIO.query::<Option<f64>>(&query(&[("ratio", "inf")])))["reason"],
            "expected a number"
        );
    }

    #[test]
    fn booleans_enums_and_defaults() {
        static FLAG: ParamSpec = ParamSpec::new("flag", ParamKind::Boolean).required();
        assert!(FLAG.query::<bool>(&query(&[("flag", "TRUE")])).unwrap());
        assert!(!FLAG.query::<bool>(&query(&[("flag", "0")])).unwrap());
        assert_eq!(
            refusal(FLAG.query::<bool>(&query(&[("flag", "yes")])))["reason"],
            "expected true or false"
        );

        static STATUS: ParamSpec = ParamSpec::new("status", ParamKind::Enum)
            .enum_values(&["active", "suspended"])
            .default_value("active");
        assert_eq!(
            STATUS
                .query::<Option<String>>(&query(&[]))
                .unwrap()
                .as_deref(),
            Some("active")
        );
        assert_eq!(
            refusal(STATUS.query::<Option<String>>(&query(&[("status", "deleted")])))["reason"],
            "expected one of active, suspended"
        );
    }

    #[test]
    fn strings_check_the_scalar_then_the_argument() {
        static SLUG_PATTERN: Pattern = Pattern::new(r"^[a-z0-9-]+$");
        static SLUG: ParamSpec = ParamSpec::new("slug", ParamKind::String)
            .required()
            .scalar(
                ScalarConstraints::new("Identity.Slug")
                    .max_length(5)
                    .pattern(&SLUG_PATTERN),
            )
            .min_length(3);
        assert_eq!(
            SLUG.query::<String>(&query(&[("slug", "abc")])).unwrap(),
            "abc"
        );
        let details = refusal(SLUG.query::<String>(&query(&[("slug", "a_b")])));
        assert_eq!(
            details["errors"],
            json!([{"validator": "pattern", "message": "is not a valid Identity.Slug"}])
        );
        let details = refusal(SLUG.query::<String>(&query(&[("slug", "abcdef")])));
        assert_eq!(details["errors"][0]["validator"], "maxLength");
        let details = refusal(SLUG.query::<String>(&query(&[("slug", "ab")])));
        assert_eq!(
            details["errors"],
            json!([{"validator": "minLength", "message": "must be at least 3 characters"}])
        );
        // An empty string is a value of a string parameter, which the
        // scalar's pattern checks before the argument's own length.
        assert_eq!(
            refusal(SLUG.query::<String>(&query(&[("slug", "")])))["errors"][0]["validator"],
            "pattern"
        );
    }

    fn uuid_check(value: Option<&Value>) -> ScalarResult {
        match value.and_then(Value::as_str) {
            Some(text) if text.len() == 36 => Ok(()),
            _ => Err(vec![ValidationError::new("pattern", "invalid format")]),
        }
    }

    #[test]
    fn a_uuid_is_checked_by_its_scalar() {
        static ID: ParamSpec = ParamSpec::new("id", ParamKind::Uuid)
            .required()
            .check(uuid_check);
        let captures = HashMap::from([(
            "id".to_owned(),
            "00000000-0000-4000-8000-000000000001".to_owned(),
        )]);
        assert_eq!(
            ID.path::<String>(&captures).unwrap(),
            "00000000-0000-4000-8000-000000000001"
        );
        let captures = HashMap::from([("id".to_owned(), "nope".to_owned())]);
        assert_eq!(
            refusal(ID.path::<String>(&captures)),
            json!({"location": "path", "parameter": "id", "reason": "expected a UUID",
                   "errors": [{"validator": "pattern", "message": "invalid format"}]})
        );
    }

    #[test]
    fn a_query_list_takes_repeated_keys_and_comma_lists() {
        static IDS: ParamSpec = ParamSpec::new("ids", ParamKind::Integer)
            .required()
            .array()
            .list_max(3);
        let values = IDS
            .query::<Vec<i64>>(&query(&[("ids", "1,2"), ("other", "x"), ("ids", " 3 ")]))
            .unwrap();
        assert_eq!(values, [1, 2, 3]);
        assert_eq!(
            IDS.query::<Vec<i64>>(&query(&[("ids", "1e1")])).unwrap(),
            [10]
        );
        let details = refusal(IDS.query::<Vec<i64>>(&query(&[("ids", "1,2,3,4")])));
        assert_eq!(details["reason"], "expected at most 3 values");
        let details = refusal(IDS.query::<Vec<i64>>(&query(&[("ids", "1,0x2")])));
        assert_eq!(details["path"], "ids[1]");
        assert_eq!(
            details["errors"],
            json!([{"validator": "type", "message": "expected an integer"}])
        );
        assert_eq!(
            refusal(IDS.query::<Vec<i64>>(&query(&[("ids", ",")])))["reason"],
            "required"
        );

        static FLAGS: ParamSpec = ParamSpec::new("flags", ParamKind::Boolean).array();
        assert_eq!(
            FLAGS
                .query::<Option<Vec<bool>>>(&query(&[("flags", "t,False")]))
                .unwrap(),
            Some(vec![true, false])
        );
        assert_eq!(FLAGS.query::<Option<Vec<bool>>>(&query(&[])).unwrap(), None);
    }

    fn body(value: Value) -> Map<String, Value> {
        value.as_object().unwrap().clone()
    }

    #[test]
    fn body_values_keep_their_json_types() {
        static NAME: ParamSpec = ParamSpec::new("name", ParamKind::String).required();
        let fields = body(json!({"name": 7}));
        let details = refusal(NAME.body::<String>(Some(&fields)));
        assert_eq!(
            details["errors"],
            json!([{"validator": "type", "message": "expected a string"}])
        );
        assert_eq!(refusal(NAME.body::<String>(None))["reason"], "required");

        static COUNT: ParamSpec = ParamSpec::new("count", ParamKind::Integer);
        assert_eq!(
            COUNT
                .body::<Option<i64>>(Some(&body(json!({"count": 2.0}))))
                .unwrap(),
            Some(2)
        );
        let details = refusal(COUNT.body::<Option<i64>>(Some(&body(json!({"count": "2"})))));
        assert_eq!(details["errors"][0]["message"], "expected an integer");
        let details = refusal(COUNT.body::<Option<i64>>(Some(&body(json!({"count": 1e19})))));
        assert_eq!(details["reason"], "integer out of range");

        static TAGS: ParamSpec = ParamSpec::new("tags", ParamKind::String)
            .array()
            .list_min(1);
        let details =
            refusal(TAGS.body::<Option<Vec<String>>>(Some(&body(json!({"tags": ["a", null]})))));
        assert_eq!(details["path"], "tags[1]");
        assert_eq!(
            details["errors"],
            json!([{"validator": "required", "message": "required field"}])
        );
        assert_eq!(
            refusal(TAGS.body::<Option<Vec<String>>>(Some(&body(json!({"tags": []})))))["reason"],
            "expected at least 1 values"
        );
        // A body list element is never split on commas.
        assert_eq!(
            TAGS.body::<Option<Vec<String>>>(Some(&body(json!({"tags": ["a,b"]}))))
                .unwrap(),
            Some(vec!["a,b".to_owned()])
        );
    }

    #[test]
    fn lists_of_lists_and_maps() {
        static GRID: ParamSpec = ParamSpec::new("grid", ParamKind::Number)
            .required()
            .array_of_arrays();
        let fields = body(json!({"grid": [[1, 2], []]}));
        assert_eq!(
            GRID.body::<Vec<Vec<f64>>>(Some(&fields)).unwrap(),
            vec![vec![1.0, 2.0], vec![]]
        );
        let details =
            refusal(GRID.body::<Vec<Vec<f64>>>(Some(&body(json!({"grid": [[1], null]})))));
        assert_eq!(details["path"], "grid[1]");
        let details = refusal(GRID.body::<Vec<Vec<f64>>>(Some(&body(json!({"grid": [[1, "x"]]})))));
        assert_eq!(details["path"], "grid[0][1]");

        static LABELS: ParamSpec = ParamSpec::new("labels", ParamKind::String).map().array();
        let fields = body(json!({"labels": {"en": ["a"], "fr": []}}));
        let labels = LABELS
            .body::<Option<HashMap<String, Vec<String>>>>(Some(&fields))
            .unwrap()
            .unwrap();
        assert_eq!(labels["en"], ["a"]);
        let details = refusal(
            LABELS.body::<Option<HashMap<String, Vec<String>>>>(Some(&body(
                json!({"labels": {"en": "a"}}),
            ))),
        );
        assert_eq!(details["path"], "labels[en]");
        let details = refusal(
            LABELS.body::<Option<HashMap<String, Vec<String>>>>(Some(&body(json!({"labels": []})))),
        );
        assert_eq!(details["reason"], "expected an object");
    }

    #[test]
    fn an_optional_json_value_keeps_its_null() {
        static DATA: ParamSpec = ParamSpec::new("data", ParamKind::Json);
        assert_eq!(
            DATA.body_keep_null(Some(&body(json!({"data": null}))))
                .unwrap(),
            Some(Value::Null)
        );
        assert_eq!(DATA.body_keep_null(Some(&body(json!({})))).unwrap(), None);
        assert_eq!(
            DATA.body_keep_null(Some(&body(json!({"data": [1]}))))
                .unwrap(),
            Some(json!([1]))
        );
    }

    fn prepare_point(value: Value, _: UnknownFields) -> Result<Value, ParseError> {
        let mut object = value.as_object().cloned().ok_or(ParseError::NotAnObject)?;
        if object.keys().any(|key| key != "x") {
            return Err(ParseError::UnknownFields(vec!["?".to_owned()]));
        }
        object.entry("x").or_insert(json!(0));
        Ok(Value::Object(object))
    }

    #[test]
    fn an_object_goes_through_its_prepare() {
        static POINTS: ParamSpec = ParamSpec::new("points", ParamKind::Object)
            .required()
            .array()
            .prepare(prepare_point);
        let fields = body(json!({"points": [{}, {"x": 2}]}));
        assert_eq!(
            POINTS.body::<Value>(Some(&fields)).unwrap(),
            json!([{"x": 0}, {"x": 2}])
        );
        let details = refusal(POINTS.body::<Value>(Some(&body(json!({"points": [{"y": 1}]})))));
        assert_eq!(
            details,
            json!({"location": "body", "parameter": "points", "path": "points[0]", "reason": "does not match the declared type"})
        );
        let details = refusal(POINTS.body::<Value>(Some(&body(json!({"points": [1]})))));
        assert_eq!(
            details["errors"],
            json!([{"validator": "type", "message": "expected an object"}])
        );
    }

    #[test]
    fn a_value_serde_refuses_is_a_refusal() {
        static SMALL: ParamSpec = ParamSpec::new("small", ParamKind::Integer).required();
        let details = refusal(SMALL.query::<u8>(&query(&[("small", "300")])));
        assert_eq!(details["reason"], "does not match the declared type");
    }

    #[test]
    fn json_numbers_are_spelt_as_json_spells_them() {
        for good in ["0", "-1", "1.5", "1e3", "1E-2", "-0.0e+1"] {
            assert!(is_json_number(good), "{good}");
        }
        for bad in ["", "-", "01", "+1", "1.", ".5", "1e", "0x1", "NaN", "1 "] {
            assert!(!is_json_number(bad), "{bad}");
        }
    }
}
