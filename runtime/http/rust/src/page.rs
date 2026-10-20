//! The page of a `@paginated` operation (D60).
//!
//! The generated router reads the request's `cursor`, `limit` and sort
//! with [`PageSpec::query`], the `Args` of the operation hold the
//! [`PageRequest`] as `page`, and the implementation returns a [`Page`],
//! `{items, next}`, which the router answers as the envelope's `data`. An
//! in-process caller builds the request with [`PageRequest::first`] or
//! [`PageRequest::after`], or from the values a record or a form holds
//! with [`PageSpec::request`] or [`PageSpec::sorted_request`], and
//! `Args::check` holds it to the route's spec with [`PageSpec::check`].
//!
//! A cursor is opaque to the caller: the unpadded base64url of the compact
//! JSON of the implementation's key, the position of the last item of a
//! page in the operation's order ([`Cursor::encode`]). A sorted
//! operation's cursor also names its sort (D60, amended,
//! [`Cursor::bind`]), so the implementation's keys are the same with a sort
//! as without. [`PageSpec::query`] reads a request as it was sent, its
//! cursor still bound; [`PageSpec::unbind`] gives the request the
//! implementation reads, its sort's default filled in and its cursor
//! unbound, and [`Page::bind`] binds the `next` the implementation
//! returns. The router's decode and the in-process call each unbind once,
//! and bind once.
//!
//! The Go runtime's `paging` package and the TypeScript runtime's
//! `page.ts` read and build the same, held to
//! `runtime/http/testdata/page_parity.json`.

use std::fmt;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{ApiError, QueryValues};

/// The page's query parameters.
pub const CURSOR_PARAM: &str = "cursor";
pub const LIMIT_PARAM: &str = "limit";

/// The longest cursor a router reads, in characters.
pub const CURSOR_MAX_LENGTH: usize = 1024;

/// The detail and reason of a cursor the implementation cannot read
/// ([`Cursor::decode`]).
pub const REFUSED_CURSOR_DETAIL: &str = "cursor is not one this operation returned";
pub const REFUSED_CURSOR_REASON: &str = "not a cursor this operation returned";

/// The detail and reason of a cursor returned under another sort (D60,
/// amended).
pub const SORTED_CURSOR_DETAIL: &str = "cursor is not one this sort returned";
pub const SORTED_CURSOR_REASON: &str = "returned under another sort";

/// The bytes a bound cursor's JSON starts with, follows its sort with, and
/// ends with: `{"sort":"-placedAt","key":K}`.
const BOUND_PREFIX: &[u8] = br#"{"sort":""#;
const BOUND_KEY: &[u8] = br#"","key":"#;
const BOUND_SUFFIX: &[u8] = b"}";

/// The largest integer every runtime reads exactly, 2^53 - 1, which
/// JavaScript's numbers stop at.
const MAX_SAFE_INTEGER: i64 = (1 << 53) - 1;

/// The order a page is read in (D60, amended): a field of the item type,
/// by its JSON name, ascending, or descending; ties break by the item's id
/// in the same direction. It writes and reads as its text, `placedAt` or
/// `-placedAt`.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(try_from = "String", into = "String")]
pub struct Sort {
    field: String,
    desc: bool,
}

impl Sort {
    /// Reads `placedAt` or `-placedAt`; `None` for any other text, a
    /// field's name being a letter or `_`, then letters, digits and `_`.
    pub fn parse(text: &str) -> Option<Self> {
        let (field, desc) = match text.strip_prefix('-') {
            Some(field) => (field, true),
            None => (text, false),
        };
        let mut bytes = field.bytes();
        let starts = bytes
            .next()
            .is_some_and(|byte| byte.is_ascii_alphabetic() || byte == b'_');
        if !starts || !bytes.all(|byte| byte.is_ascii_alphanumeric() || byte == b'_') {
            return None;
        }
        Some(Self {
            field: field.to_owned(),
            desc,
        })
    }

    /// The field, by its JSON name.
    pub fn field(&self) -> &str {
        &self.field
    }

    /// Whether the page descends.
    pub fn desc(&self) -> bool {
        self.desc
    }

    /// The sort by the same field in the other direction.
    pub fn reversed(&self) -> Self {
        Self {
            field: self.field.clone(),
            desc: !self.desc,
        }
    }
}

impl fmt::Display for Sort {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.desc {
            f.write_str("-")?;
        }
        f.write_str(&self.field)
    }
}

impl TryFrom<String> for Sort {
    type Error = String;

    fn try_from(text: String) -> Result<Self, Self::Error> {
        Self::parse(&text).ok_or_else(|| format!("{text:?} is not a field, or - and a field"))
    }
}

impl From<Sort> for String {
    fn from(sort: Sort) -> Self {
        sort.to_string()
    }
}

/// An operation's `SortBy` argument (D60, amended), as its `Args` carry it
/// in `PAGE`: the query parameter, the fields by their JSON names, and
/// `@paginated`'s `defaultSort`, the order of a request that sends none.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SortSpec {
    param: &'static str,
    fields: &'static [&'static str],
    default: &'static str,
}

impl SortSpec {
    pub const fn new(
        param: &'static str,
        fields: &'static [&'static str],
        default: &'static str,
    ) -> Self {
        Self {
            param,
            fields,
            default,
        }
    }

    /// The query parameter the router reads the sort from.
    pub const fn param(&self) -> &'static str {
        self.param
    }

    /// The fields a page sorts by, by their JSON names.
    pub const fn fields(&self) -> &'static [&'static str] {
        self.fields
    }

    /// The order of a request that sends none, as its text.
    pub const fn default(&self) -> &'static str {
        self.default
    }

    /// The values the parameter takes: each field, then the field after
    /// `-` (`placedAt`, `-placedAt`, `totalCents`, `-totalCents`).
    pub fn values(&self) -> Vec<String> {
        self.fields
            .iter()
            .flat_map(|field| [(*field).to_owned(), format!("-{field}")])
            .collect()
    }

    /// Whether `sort` is one of the values.
    pub fn offers(&self, sort: &Sort) -> bool {
        self.fields.contains(&sort.field())
    }

    /// The default as a [`Sort`]; a default that is no sort's text is the
    /// generator's error, a 500.
    fn default_sort(&self) -> Result<Sort, ApiError> {
        Sort::parse(self.default).ok_or_else(|| {
            ApiError::internal(format!(
                "the default sort {:?} is not a field, or - and a field",
                self.default
            ))
        })
    }

    /// The sort of a value as sent: one of the values, or the 400 an enum
    /// query parameter gets.
    fn read(&self, text: &str) -> Result<Sort, ApiError> {
        match Sort::parse(text) {
            Some(sort) if self.offers(&sort) => Ok(sort),
            _ => Err(self.refuse()),
        }
    }

    fn refuse(&self) -> ApiError {
        let reason = format!("expected one of {}", self.values().join(", "));
        refuse(self.param, &reason, None)
    }
}

/// An operation's `@paginated` limits, and its `SortBy` argument when it
/// has one, as its `Args` carry them in an associated const:
/// `PageSpec::new(20, 100)`, or `PageSpec::new(2, 5).sorted(SortSpec::new("sort",
/// &["name", "sku"], "name"))`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PageSpec {
    default_limit: i64,
    max_limit: i64,
    sort: Option<SortSpec>,
}

impl PageSpec {
    pub const fn new(default_limit: i64, max_limit: i64) -> Self {
        Self {
            default_limit,
            max_limit,
            sort: None,
        }
    }

    /// The spec of an operation that sorts by `sort`.
    pub const fn sorted(self, sort: SortSpec) -> Self {
        Self {
            sort: Some(sort),
            ..self
        }
    }

    /// The operation's `SortBy` argument; `None` for one without.
    pub const fn sort(&self) -> Option<&SortSpec> {
        self.sort.as_ref()
    }

    /// The limit of a request that sends none.
    pub const fn default_limit(&self) -> i64 {
        self.default_limit
    }

    /// The largest limit a request may send.
    pub const fn max_limit(&self) -> i64 {
        self.max_limit
    }

    /// The router's reader, of the request as it was sent: the first value
    /// of `cursor`, of `limit` and of the sort's parameter in the query, an
    /// empty one as absent. A cursor is 1 to [`CURSOR_MAX_LENGTH`]
    /// characters of the unpadded base64url alphabet, still bound to its
    /// sort; a limit a whole number from 1 to the spec's maximum, its
    /// default when absent; a sort one of the spec's values, `None` when
    /// absent. A refusal is the 400 at the parameter, as a string, integer
    /// or enum query parameter's (`ParamSpec`), in this order: the
    /// cursor's text, the limit, then the sort. [`PageSpec::unbind`] gives
    /// the request the implementation reads.
    pub fn query(&self, query: &QueryValues) -> Result<PageRequest, ApiError> {
        let cursor = read_cursor(query.all(CURSOR_PARAM).first().copied())?;
        let limit = match query.all(LIMIT_PARAM).first().copied() {
            None | Some("") => self.default_limit,
            Some(text) => self.limit_of(parse_limit(text)?)?,
        };
        let sort = match &self.sort {
            None => None,
            Some(spec) => match query.all(spec.param()).first().copied() {
                None | Some("") => None,
                Some(text) => Some(spec.read(text)?),
            },
        };
        Ok(PageRequest {
            cursor,
            limit,
            sort,
        })
    }

    /// The request of the values a record or a form holds, with the
    /// router's refusals: an absent or empty cursor is the first page, an
    /// absent limit the default.
    pub fn request(
        &self,
        cursor: Option<&str>,
        limit: Option<i64>,
    ) -> Result<PageRequest, ApiError> {
        self.sorted_request(cursor, limit, None)
    }

    /// The request of the values a record or a form holds, the sort among
    /// them, as sent, with the router's refusals: an absent or empty
    /// cursor is the first page, an absent limit the default, and an
    /// absent or empty sort none, the default once unbound. A sort for an
    /// operation without one is the caller's error, a 500.
    pub fn sorted_request(
        &self,
        cursor: Option<&str>,
        limit: Option<i64>,
        sort: Option<&str>,
    ) -> Result<PageRequest, ApiError> {
        let cursor = read_cursor(cursor)?;
        let limit = match limit {
            None => self.default_limit,
            Some(limit) => self.limit_of(limit)?,
        };
        let sort = match (sort, &self.sort) {
            (None | Some(""), _) => None,
            (Some(text), Some(spec)) => Some(spec.read(text)?),
            (Some(_), None) => return Err(unsorted()),
        };
        Ok(PageRequest {
            cursor,
            limit,
            sort,
        })
    }

    /// Holds a request an in-process caller built to the route's spec
    /// (`Args::check`): the router would refuse anything this refuses,
    /// with the same 400, the cursor's text, the limit, then the sort. A
    /// sort for an operation without one is the caller's error, a 500.
    pub fn check(&self, request: &PageRequest) -> Result<(), ApiError> {
        if let Some(cursor) = &request.cursor {
            check_cursor_text(cursor.as_str())?;
        }
        self.limit_of(request.limit)?;
        match (&request.sort, &self.sort) {
            (None, _) => Ok(()),
            (Some(sort), Some(spec)) if spec.offers(sort) => Ok(()),
            (Some(_), Some(spec)) => Err(spec.refuse()),
            (Some(_), None) => Err(unsorted()),
        }
    }

    /// The request the implementation reads, of one as it was sent or an
    /// in-process caller built it: a sorted operation's sort, its default
    /// filled in, and its cursor unbound from that sort
    /// ([`Cursor::unbind`]), so the cursor is the implementation's own. A
    /// cursor of another sort, or one bound to none, is its 400. The
    /// router's decode and the in-process call each call it once, after
    /// [`PageSpec::query`] or [`PageSpec::check`]. An unsorted operation's
    /// request is as it is.
    pub fn unbind(&self, request: PageRequest) -> Result<PageRequest, ApiError> {
        let Some(spec) = &self.sort else {
            return match request.sort {
                None => Ok(request),
                Some(_) => Err(unsorted()),
            };
        };
        let sort = match request.sort {
            Some(sort) if spec.offers(&sort) => sort,
            Some(_) => return Err(spec.refuse()),
            None => spec.default_sort()?,
        };
        let cursor = request
            .cursor
            .map(|cursor| cursor.unbind(&sort))
            .transpose()?;
        Ok(PageRequest {
            cursor,
            limit: request.limit,
            sort: Some(sort),
        })
    }

    /// The first page at the default limit.
    pub const fn first(&self) -> PageRequest {
        PageRequest::first(self.default_limit)
    }

    fn limit_of(&self, limit: i64) -> Result<i64, ApiError> {
        if !(-MAX_SAFE_INTEGER..=MAX_SAFE_INTEGER).contains(&limit) {
            return Err(refuse(LIMIT_PARAM, "integer out of range", None));
        }
        if limit < 1 {
            return Err(refuse(LIMIT_PARAM, "must be at least 1", Some("min")));
        }
        if limit > self.max_limit {
            let reason = format!("must be at most {}", self.max_limit);
            return Err(refuse(LIMIT_PARAM, &reason, Some("max")));
        }
        Ok(limit)
    }
}

/// The page a caller asks for: the cursor of the page before, none for the
/// first page, the limit, its default filled in, and a sorted operation's
/// sort (D60, amended). The implementation reads it unbound
/// ([`PageSpec::unbind`]): the cursor its own, and the sort `Some`, the
/// default filled in. An unsorted operation's sort is `None`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct PageRequest {
    pub cursor: Option<Cursor>,
    pub limit: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sort: Option<Sort>,
}

impl PageRequest {
    /// The first page, `limit` items, in the default order.
    pub const fn first(limit: i64) -> Self {
        Self {
            cursor: None,
            limit,
            sort: None,
        }
    }

    /// The page after `cursor`, a page's `next`, `limit` items, in the
    /// default order.
    pub fn after(cursor: Cursor, limit: i64) -> Self {
        Self {
            cursor: Some(cursor),
            limit,
            sort: None,
        }
    }

    /// The request in `sort`'s order.
    pub fn sorted(self, sort: Sort) -> Self {
        Self {
            sort: Some(sort),
            ..self
        }
    }
}

/// A position in an operation's order, as a page's `next` names it. It
/// writes and reads as its text.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Cursor(String);

impl Cursor {
    /// The cursor of a position: the unpadded base64url of the compact
    /// JSON of `key`, which serde_json writes as the Go and TypeScript
    /// runtimes do. A key every router would refuse to read back is the
    /// implementation's error, a 500: one that does not serialize as JSON
    /// (a map with keys that are not strings, say), one whose cursor is
    /// past [`CURSOR_MAX_LENGTH`] characters, about 768 bytes of JSON, and
    /// one [`Cursor::decode`] refuses (null, an empty object, arrays and
    /// objects nested 128 deep).
    pub fn encode<K: Serialize + ?Sized>(key: &K) -> Result<Self, ApiError> {
        let unreadable = |problem: &str| {
            ApiError::internal(format!(
                "encode a cursor's key: {problem}, which no router reads back"
            ))
        };
        let json = serde_json::to_vec(key).map_err(|err| {
            ApiError::internal(format!("encode a cursor's key: it is not JSON: {err}"))
        })?;
        let value: Value = serde_json::from_slice(&json)
            .map_err(|_| unreadable("the key nests 128 arrays or objects deep"))?;
        if let Some(problem) = key_problem(&value) {
            return Err(unreadable(problem));
        }
        let text = URL_SAFE_NO_PAD.encode(json);
        if text.len() > CURSOR_MAX_LENGTH {
            return Err(ApiError::internal(format!(
                "encode a cursor's key: its cursor is {} characters, past the {CURSOR_MAX_LENGTH} a router reads",
                text.len()
            )));
        }
        Ok(Self(text))
    }

    /// Reads the cursor back into the implementation's key type. A cursor
    /// that is not of the alphabet, not the canonical unpadded base64url of
    /// JSON, whose JSON names no position or holds what another runtime
    /// cannot read (null, an empty object, a number past `f64`'s range, a
    /// lone surrogate escape, arrays and objects nested 128 deep), or whose
    /// JSON is not a `K`, is refused with the 400 every runtime answers, at
    /// `cursor`: the caller starts again.
    pub fn decode<K: DeserializeOwned>(&self) -> Result<K, ApiError> {
        let text = self.0.as_str();
        if text.is_empty() || text.len() > CURSOR_MAX_LENGTH || !text.bytes().all(is_cursor_byte) {
            return Err(refused_cursor());
        }
        let bytes = URL_SAFE_NO_PAD.decode(text).map_err(|_| refused_cursor())?;
        // serde_json refuses the escape and the depth itself; the key is
        // read as a JSON value first so the rest is refused whatever `K`
        // is.
        let value: Value = serde_json::from_slice(&bytes).map_err(|_| refused_cursor())?;
        if key_problem(&value).is_some() {
            return Err(refused_cursor());
        }
        serde_json::from_slice(&bytes).map_err(|_| refused_cursor())
    }

    /// A cursor's text, held to the router's rules: 1 to
    /// [`CURSOR_MAX_LENGTH`] characters of the unpadded base64url alphabet.
    pub fn parse(text: &str) -> Result<Self, ApiError> {
        check_cursor_text(text)?;
        Ok(Self(text.to_owned()))
    }

    /// The cursor of this, the implementation's cursor, under `sort` (D60,
    /// amended): the unpadded base64url of exactly the bytes
    /// `{"sort":"-placedAt","key":K}`, `K` the key's JSON as this cursor
    /// holds it, byte for byte. A cursor [`Cursor::decode`] would refuse,
    /// and a bound cursor past [`CURSOR_MAX_LENGTH`] characters, are the
    /// implementation's error, a 500.
    pub fn bind(&self, sort: &Sort) -> Result<Cursor, ApiError> {
        if self.decode::<Value>().is_err() {
            return Err(ApiError::internal(format!(
                "bind a cursor: {:?} is not a cursor Cursor::encode writes",
                self.0
            )));
        }
        // The key's bytes as the cursor holds them, which decode read.
        let key = URL_SAFE_NO_PAD
            .decode(&self.0)
            .map_err(|_| ApiError::internal("bind a cursor: its key does not decode"))?;
        let text = sort.to_string();
        let mut bytes = Vec::with_capacity(
            BOUND_PREFIX.len() + text.len() + BOUND_KEY.len() + key.len() + BOUND_SUFFIX.len(),
        );
        for part in [BOUND_PREFIX, text.as_bytes(), BOUND_KEY, &key, BOUND_SUFFIX] {
            bytes.extend_from_slice(part);
        }
        let bound = URL_SAFE_NO_PAD.encode(bytes);
        if bound.len() > CURSOR_MAX_LENGTH {
            return Err(ApiError::internal(format!(
                "bind a cursor: its cursor under {text} is {} characters, past the {CURSOR_MAX_LENGTH} a router reads",
                bound.len()
            )));
        }
        Ok(Self(bound))
    }

    /// The implementation's cursor of this, a cursor bound under `sort`
    /// (D60, amended): the key's JSON of `{"sort":"-placedAt","key":K}`,
    /// read by its bytes, encoded again as [`Cursor::encode`] would. A
    /// cursor bound under another sort is the 400 [`SORTED_CURSOR_DETAIL`]
    /// at `cursor`; one that is not of the alphabet, not canonical unpadded
    /// base64url, or not those bytes around a key, names no sort and is
    /// [`refused_cursor`]. The key itself is the implementation's
    /// [`Cursor::decode`]'s to read.
    pub fn unbind(&self, sort: &Sort) -> Result<Cursor, ApiError> {
        let text = self.0.as_str();
        if text.is_empty() || text.len() > CURSOR_MAX_LENGTH || !text.bytes().all(is_cursor_byte) {
            return Err(refused_cursor());
        }
        let bytes = URL_SAFE_NO_PAD.decode(text).map_err(|_| refused_cursor())?;
        let rest = bytes
            .strip_prefix(BOUND_PREFIX)
            .ok_or_else(refused_cursor)?;
        let end = rest
            .iter()
            .position(|&byte| byte == b'"')
            .ok_or_else(refused_cursor)?;
        let bound = std::str::from_utf8(&rest[..end])
            .ok()
            .and_then(Sort::parse)
            .ok_or_else(refused_cursor)?;
        let key = rest[end..]
            .strip_prefix(BOUND_KEY)
            .and_then(|rest| rest.strip_suffix(BOUND_SUFFIX))
            .filter(|key| !key.is_empty())
            .ok_or_else(refused_cursor)?;
        if &bound != sort {
            return Err(sorted_cursor());
        }
        Ok(Self(URL_SAFE_NO_PAD.encode(key)))
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for Cursor {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

/// What a paged operation returns: the page's items, which may be fewer
/// than its limit, even none, and the cursor of the next page, `None`
/// after the last. It writes as `{"items": [...], "next": null}`.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Page<T> {
    pub items: Vec<T>,
    pub next: Option<Cursor>,
}

impl<T> Page<T> {
    /// The last page: items and no next.
    pub fn last(items: Vec<T>) -> Self {
        Self { items, next: None }
    }

    /// A page with a next page after it.
    pub fn more(items: Vec<T>, next: Cursor) -> Self {
        Self {
            items,
            next: Some(next),
        }
    }

    /// The page of rows an implementation read for a request: it reads up
    /// to `limit + 1` rows in the operation's order, after the request's
    /// cursor, and `from_rows` keeps `limit` of them and, when there were
    /// more, sets `next` to the cursor of the last one kept,
    /// `Cursor::encode(&key(row))`, whose error it returns. `limit` is the
    /// request's; one below 1 is taken as 1.
    pub fn from_rows<K: Serialize>(
        mut rows: Vec<T>,
        limit: i64,
        key: impl Fn(&T) -> K,
    ) -> Result<Self, ApiError> {
        let keep = usize::try_from(limit.max(1)).unwrap_or(usize::MAX);
        if rows.len() <= keep {
            return Ok(Self::last(rows));
        }
        rows.truncate(keep);
        let next = rows
            .last()
            .map(|row| Cursor::encode(&key(row)))
            .transpose()?;
        Ok(Self { items: rows, next })
    }

    /// The page with each item mapped, its next kept.
    pub fn map<U>(self, f: impl FnMut(T) -> U) -> Page<U> {
        Page {
            items: self.items.into_iter().map(f).collect(),
            next: self.next,
        }
    }

    /// The page with its `next` bound to the request's sort
    /// ([`Cursor::bind`]), which the router answers; the page as it is when
    /// the request has no sort or the page no `next`. Its error is
    /// [`Cursor::bind`]'s, the implementation's.
    pub fn bind(self, request: &PageRequest) -> Result<Self, ApiError> {
        let (Some(sort), Some(next)) = (&request.sort, &self.next) else {
            return Ok(self);
        };
        let next = next.bind(sort)?;
        Ok(Self {
            items: self.items,
            next: Some(next),
        })
    }
}

/// The 400 of a cursor the implementation cannot read, which it may return
/// itself for one that decodes but names no position of its order.
pub fn refused_cursor() -> ApiError {
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("query"));
    details.insert("parameter".to_owned(), Value::from(CURSOR_PARAM));
    details.insert("reason".to_owned(), Value::from(REFUSED_CURSOR_REASON));
    ApiError::bad_request(REFUSED_CURSOR_DETAIL).with_details(Value::Object(details))
}

/// The 400 of a cursor returned under another sort (D60, amended).
pub fn sorted_cursor() -> ApiError {
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("query"));
    details.insert("parameter".to_owned(), Value::from(CURSOR_PARAM));
    details.insert("reason".to_owned(), Value::from(SORTED_CURSOR_REASON));
    ApiError::bad_request(SORTED_CURSOR_DETAIL).with_details(Value::Object(details))
}

/// A sort an in-process caller gave an operation without a `SortBy`
/// argument, which no request can send: the caller's error.
fn unsorted() -> ApiError {
    ApiError::internal("the operation has no SortBy argument, so its page takes no sort")
}

/// The 400 of a parameter a request carries, worded as `ParamSpec` words a
/// string or integer query parameter's, with the broken rule when there is
/// one.
fn refuse(parameter: &str, reason: &str, validator: Option<&str>) -> ApiError {
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("query"));
    details.insert("parameter".to_owned(), Value::from(parameter));
    details.insert("reason".to_owned(), Value::from(reason));
    if let Some(validator) = validator {
        let mut error = Map::new();
        error.insert("validator".to_owned(), Value::from(validator));
        error.insert("message".to_owned(), Value::from(reason));
        details.insert(
            "errors".to_owned(),
            Value::Array(vec![Value::Object(error)]),
        );
    }
    ApiError::bad_request(format!("Invalid query parameter {parameter}: {reason}"))
        .with_details(Value::Object(details))
}

/// Why every runtime refuses to read a cursor's key, beyond what
/// serde_json refuses as it reads it (a lone surrogate escape, arrays and
/// objects nested 128 deep), or `None`: JSON null or an empty object,
/// which name no position whatever the key's type, and a number past
/// `f64`'s range, which serde_json reads with `arbitrary_precision`.
fn key_problem(value: &Value) -> Option<&'static str> {
    fn infinite(value: &Value) -> bool {
        match value {
            Value::Number(number) => !number.as_f64().is_some_and(f64::is_finite),
            Value::Array(items) => items.iter().any(infinite),
            Value::Object(members) => members.values().any(infinite),
            _ => false,
        }
    }
    match value {
        Value::Null => Some("the key is null, which names no position"),
        Value::Object(members) if members.is_empty() => {
            Some("the key is an empty object, which names no position")
        }
        _ if infinite(value) => Some("a number of the key is past f64's range"),
        _ => None,
    }
}

/// A byte of the unpadded base64url alphabet.
fn is_cursor_byte(byte: u8) -> bool {
    byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_'
}

/// The alphabet is checked first, on bytes, so a cursor outside ASCII is
/// refused for its pattern however long it is, as in every runtime.
fn check_cursor_text(text: &str) -> Result<(), ApiError> {
    if text.is_empty() || !text.bytes().all(is_cursor_byte) {
        let reason = "does not match the required pattern";
        return Err(refuse(CURSOR_PARAM, reason, Some("pattern")));
    }
    if text.len() > CURSOR_MAX_LENGTH {
        let reason = format!("must be at most {CURSOR_MAX_LENGTH} characters");
        return Err(refuse(CURSOR_PARAM, &reason, Some("maxLength")));
    }
    Ok(())
}

/// An absent or empty cursor is the first page.
fn read_cursor(text: Option<&str>) -> Result<Option<Cursor>, ApiError> {
    match text {
        None | Some("") => Ok(None),
        Some(text) => Cursor::parse(text).map(Some),
    }
}

/// A limit as a query sends it: a sign, then digits, within 2^53 - 1.
fn parse_limit(text: &str) -> Result<i64, ApiError> {
    let digits = text.strip_prefix(['+', '-']).unwrap_or(text);
    if digits.is_empty() || !digits.bytes().all(|byte| byte.is_ascii_digit()) {
        return Err(refuse(LIMIT_PARAM, "expected an integer", None));
    }
    text.parse::<i64>()
        .ok()
        .filter(|limit| (-MAX_SAFE_INTEGER..=MAX_SAFE_INTEGER).contains(limit))
        .ok_or_else(|| refuse(LIMIT_PARAM, "integer out of range", None))
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    const SPEC: PageSpec = PageSpec::new(2, 5);

    #[derive(Debug, PartialEq, Serialize, Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct OrderKey {
        placed_at: String,
        id: String,
    }

    #[derive(Clone, Debug, PartialEq)]
    struct Order {
        id: &'static str,
        placed_at: &'static str,
    }

    fn reason(err: &ApiError) -> &str {
        err.details.as_ref().unwrap()["reason"].as_str().unwrap()
    }

    #[test]
    fn a_page_built_from_rows_reads_back_as_its_last_items_key() {
        let rows = vec![
            Order {
                id: "a",
                placed_at: "2026-10-09T14:30:00Z",
            },
            Order {
                id: "b",
                placed_at: "2026-10-09T14:31:00Z",
            },
            Order {
                id: "c",
                placed_at: "2026-10-09T14:32:00Z",
            },
        ];
        let key = |row: &Order| OrderKey {
            placed_at: row.placed_at.to_owned(),
            id: row.id.to_owned(),
        };
        let page = Page::from_rows(rows.clone(), 2, key).unwrap();
        assert_eq!(page.items, rows[..2]);
        let next = page.next.clone().unwrap();
        assert_eq!(
            next.decode::<OrderKey>().unwrap(),
            OrderKey {
                placed_at: "2026-10-09T14:31:00Z".to_owned(),
                id: "b".to_owned()
            }
        );
        assert_eq!(
            Page::from_rows(rows.clone(), 3, key).unwrap(),
            Page::last(rows.clone())
        );
        assert_eq!(
            Page::from_rows(rows.clone(), 0, key).unwrap().items.len(),
            1
        );

        // The next request starts after it, and the page maps to records.
        let request = SPEC.request(Some(next.as_str()), Some(2)).unwrap();
        assert_eq!(request, PageRequest::after(next.clone(), 2));
        let ids = page.map(|order| order.id);
        assert_eq!(ids, Page::more(vec!["a", "b"], next));
        assert_eq!(
            serde_json::to_value(Page::<i32>::last(vec![])).unwrap(),
            json!({"items": [], "next": null})
        );
    }

    #[test]
    fn a_cursor_of_another_shape_is_refused() {
        let forged = Cursor::encode(&json!({"id": 1})).unwrap();
        let err = forged.decode::<OrderKey>().unwrap_err();
        assert_eq!(err.status, http::StatusCode::BAD_REQUEST);
        assert_eq!(err.message, REFUSED_CURSOR_DETAIL);
        assert_eq!(reason(&err), REFUSED_CURSOR_REASON);

        // Null and an empty object name no position, whatever the key's
        // type: one of optional members would read either.
        #[derive(Debug, Deserialize)]
        #[allow(dead_code)]
        struct Optional {
            id: Option<String>,
        }
        for json in ["null", "{}", " { } "] {
            let forged = Cursor(URL_SAFE_NO_PAD.encode(json));
            assert_eq!(
                forged.decode::<Option<Optional>>().unwrap_err().message,
                REFUSED_CURSOR_DETAIL,
                "{json}"
            );
        }
    }

    #[test]
    fn a_key_no_router_reads_back_is_the_implementations_error() {
        let long = "x".repeat(767);
        let err = Cursor::encode(&long).unwrap_err();
        assert_eq!(err.status, http::StatusCode::INTERNAL_SERVER_ERROR);
        assert!(
            err.message
                .contains("its cursor is 1026 characters, past the 1024 a router reads"),
            "{}",
            err.message
        );
        assert_eq!(Cursor::encode(&long[1..]).unwrap().as_str().len(), 1024);
        assert!(Cursor::encode(&()).is_err());
        assert!(Cursor::encode(&f64::INFINITY).is_err());
        assert!(Cursor::encode(&json!({})).is_err());
        let mut map = std::collections::BTreeMap::new();
        map.insert((1, 2), 3);
        assert!(Cursor::encode(&map).is_err());

        let rows = vec!["a".to_owned(), long, "c".to_owned()];
        let err = Page::from_rows(rows, 2, Clone::clone).unwrap_err();
        assert_eq!(err.status, http::StatusCode::INTERNAL_SERVER_ERROR);
    }

    #[test]
    fn an_in_process_request_is_held_to_the_routes_spec() {
        assert_eq!(SPEC.first(), PageRequest::first(2));
        assert_eq!(SPEC.request(None, None).unwrap(), PageRequest::first(2));
        assert_eq!(
            SPEC.request(Some(""), Some(5)).unwrap(),
            PageRequest::first(5)
        );
        assert!(SPEC.check(&PageRequest::first(5)).is_ok());

        let err = SPEC.check(&PageRequest::first(6)).unwrap_err();
        assert_eq!(
            err.message,
            "Invalid query parameter limit: must be at most 5"
        );
        assert_eq!(
            reason(&SPEC.check(&PageRequest::first(0)).unwrap_err()),
            "must be at least 1"
        );
        assert_eq!(
            reason(&SPEC.check(&PageRequest::first(1 << 60)).unwrap_err()),
            "integer out of range"
        );
        let padded = PageRequest::after(Cursor("e30=".to_owned()), 2);
        assert_eq!(
            reason(&SPEC.check(&padded).unwrap_err()),
            "does not match the required pattern"
        );
        assert_eq!(
            reason(&SPEC.request(Some("e3 0"), None).unwrap_err()),
            "does not match the required pattern"
        );
        assert_eq!(
            reason(&SPEC.request(None, Some(9)).unwrap_err()),
            "must be at most 5"
        );
    }

    const SORTED: PageSpec = PageSpec::new(2, 5).sorted(SortSpec::new(
        "sort",
        &["placedAt", "totalCents"],
        "placedAt",
    ));

    #[test]
    fn a_sort_reads_and_writes_as_its_text() {
        let sort = Sort::parse("-totalCents").unwrap();
        assert_eq!((sort.field(), sort.desc()), ("totalCents", true));
        assert_eq!(sort.to_string(), "-totalCents");
        assert_eq!(sort.reversed().to_string(), "totalCents");
        assert_eq!(serde_json::to_value(&sort).unwrap(), json!("-totalCents"));
        assert_eq!(
            serde_json::from_value::<Sort>(json!("placedAt"))
                .unwrap()
                .to_string(),
            "placedAt"
        );
        assert!(serde_json::from_value::<Sort>(json!("placed At")).is_err());
        for text in [
            "",
            "-",
            "--placedAt",
            "+placedAt",
            "placedAt,id",
            "2nd",
            "placed At",
            "é",
        ] {
            assert_eq!(Sort::parse(text), None, "{text}");
        }
        assert_eq!(
            SORTED.sort().unwrap().values(),
            ["placedAt", "-placedAt", "totalCents", "-totalCents"]
        );
        assert_eq!(SPEC.sort(), None);

        // An unsorted request writes as it did before sorts.
        assert_eq!(
            serde_json::to_value(PageRequest::first(2)).unwrap(),
            json!({"cursor": null, "limit": 2})
        );
        let request = PageRequest::first(2).sorted(sort);
        let back: PageRequest =
            serde_json::from_value(serde_json::to_value(&request).unwrap()).unwrap();
        assert_eq!(back, request);
    }

    #[test]
    fn check_refuses_a_sort_the_spec_does_not_offer() {
        let status = Sort::parse("status").unwrap();
        let err = SORTED
            .check(&PageRequest::first(2).sorted(status.clone()))
            .unwrap_err();
        assert_eq!(
            err.message,
            "Invalid query parameter sort: expected one of placedAt, -placedAt, totalCents, -totalCents"
        );
        assert_eq!(err.details.as_ref().unwrap()["parameter"], "sort");
        // The limit is refused before the sort.
        let err = SORTED
            .check(&PageRequest::first(9).sorted(status.clone()))
            .unwrap_err();
        assert_eq!(reason(&err), "must be at most 5");
        assert!(SORTED.check(&PageRequest::first(2)).is_ok());
        assert!(SORTED
            .check(&PageRequest::first(2).sorted(Sort::parse("-totalCents").unwrap()))
            .is_ok());
        assert_eq!(
            reason(
                &SORTED
                    .sorted_request(None, None, Some("status"))
                    .unwrap_err()
            ),
            "expected one of placedAt, -placedAt, totalCents, -totalCents"
        );
        assert_eq!(
            SORTED.sorted_request(None, None, Some("")).unwrap(),
            PageRequest::first(2)
        );

        // An operation without a sort takes none: the caller's error.
        let err = SPEC
            .check(&PageRequest::first(2).sorted(status.clone()))
            .unwrap_err();
        assert_eq!(err.status, http::StatusCode::INTERNAL_SERVER_ERROR);
        assert!(SPEC.unbind(PageRequest::first(2).sorted(status)).is_err());
        assert!(SPEC.sorted_request(None, None, Some("placedAt")).is_err());
        assert_eq!(
            SPEC.unbind(PageRequest::first(2)).unwrap(),
            PageRequest::first(2)
        );
    }

    #[test]
    fn an_in_process_call_unbinds_and_binds_as_the_router_does() {
        let rows = vec![("a", 300), ("b", 200), ("c", 100)];
        let key = |row: &(&str, i64)| json!({"totalCents": row.1, "id": row.0});

        // The first page, sorted, its default filled in by unbind.
        let first = SORTED
            .sorted_request(None, None, Some("-totalCents"))
            .unwrap();
        SORTED.check(&first).unwrap();
        let first = SORTED.unbind(first).unwrap();
        assert_eq!(first.sort.as_ref().unwrap().to_string(), "-totalCents");
        let page = Page::from_rows(rows.clone(), first.limit, key).unwrap();
        let own = page.next.clone().unwrap();
        let page = page.bind(&first).unwrap();
        let next = page.next.clone().unwrap();
        assert_ne!(next, own);
        // The key's bytes as the implementation's cursor holds them: its
        // members in serde_json's order, which preserve_order changes.
        let own_key = String::from_utf8(URL_SAFE_NO_PAD.decode(own.as_str()).unwrap()).unwrap();
        assert_eq!(
            String::from_utf8(URL_SAFE_NO_PAD.decode(next.as_str()).unwrap()).unwrap(),
            format!(r#"{{"sort":"-totalCents","key":{own_key}}}"#)
        );
        assert_eq!(
            next.decode::<Value>().unwrap(),
            json!({"sort": "-totalCents", "key": {"id": "b", "totalCents": 200}})
        );

        // Its next, sent back under the sort, is the implementation's own.
        let second =
            PageRequest::after(next.clone(), 2).sorted(Sort::parse("-totalCents").unwrap());
        SORTED.check(&second).unwrap();
        let second = SORTED.unbind(second).unwrap();
        assert_eq!(second.cursor.as_ref(), Some(&own));
        assert_eq!(
            own.decode::<Value>().unwrap(),
            json!({"totalCents": 200, "id": "b"})
        );

        // Under the default, it is another sort's.
        let err = SORTED.unbind(PageRequest::after(next, 2)).unwrap_err();
        assert_eq!(
            (err.message.as_str(), reason(&err)),
            (SORTED_CURSOR_DETAIL, SORTED_CURSOR_REASON)
        );
        // The implementation's own cursor names no sort.
        let err = SORTED.unbind(PageRequest::after(own, 2)).unwrap_err();
        assert_eq!(err.message, REFUSED_CURSOR_DETAIL);

        // A page without a next, or a request without a sort, is as it is.
        let last = Page::last(vec![1]);
        assert_eq!(last.clone().bind(&first).unwrap(), last);
        let unsorted = Page::more(vec![1], Cursor("MQ".to_owned()));
        assert_eq!(
            unsorted.clone().bind(&PageRequest::first(1)).unwrap(),
            unsorted
        );
        // A next no router reads back under the sort is the
        // implementation's error.
        let long = Page::more(vec![1], Cursor::encode(&"x".repeat(740)).unwrap());
        let err = long.bind(&first).unwrap_err();
        assert_eq!(err.status, http::StatusCode::INTERNAL_SERVER_ERROR);
        assert!(
            err.message.contains("past the 1024 a router reads"),
            "{}",
            err.message
        );
    }
}
