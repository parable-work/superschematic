//! Numbering, a number a person reads on each row (D56), ported from the
//! engine's `numbering.rs` with its rules and words: an invoice's
//! INV-2026-0042, a ticket's T-17. A table carries it on a field, `number:
//! Numbering<{ format, scope?, reset? }>`, whose column holds the label and
//! whose `<field>_seq` holds the number in its count (D103's field form; a
//! use anchored on the type is the engine's recordings', named Numbering).
//!
//! Numbering is held to D103's test of a behavior and passes it. Its state
//! is its own: each row's number and label, set once and never changed, and
//! its counts, the last number each scope gave, which outlive the rows they
//! numbered (a delete leaves a gap). Its rules say how that state changes: a
//! create takes the next number of its count in its own transaction, a
//! refused create takes none, a row made before the table composed
//! Numbering is numbered once by `assignNumber`, in the year it was made,
//! and nothing else moves a number. It recurs: an invoice's number, a
//! ticket's, a purchase order's, a case's, each per customer or per year. It
//! is not `AutoGenerate` or a database sequence: a count per relation target
//! and per year, printed by a format, with a gap a refused create never
//! leaves, is rules over state no column default keeps.
//!
//! The config's format prints the number, a template with a closed set of
//! tokens: `{seq}`, or `{seq:N}` zero-padded to N digits (1 to 10), exactly
//! once, and `{yyyy}` and `{yy}`, the UTC year the row was created in, as
//! often as the format wants them. A brace that is not one of these tokens
//! is refused, so a format holds no text a reader could take for a token.
//! The count runs per table; with `scope: { link }`, per target of that
//! to-one relation of the table (the engine's Links link is a relation on a
//! table, D103), whose field a create must give; with `reset: "yearly"`, per
//! UTC year as well, which needs the year in the format. A label is unique
//! within its scope, and only there: two customers each have a C-1. The
//! build can hold it there with a uniqueness over the scope's relation and
//! the field, which the docs show; the core keeps no index of labels.
//!
//! The counts are the use's shared table `counts` (D103's step 4), kept for
//! the table as a whole rather than per row, one row per scope: `scope`,
//! the target's key and the year, each the config counts by, joined with
//! `/`, or `*` for a count of the whole table; `last`, the last number it
//! gave; and `counting`, the format, the scope and the reset it was made
//! under. A create reads its count's row and writes it held to the version
//! it read, inserting it when there is none, so two creates that take a
//! number at once never take the same one: the second's write finds the row
//! moved, or inserted, and the host runs it again from fresh state, on
//! Postgres, SQLite and D1 alike, with no lock. A count whose `counting` is
//! not the use's counts from 1 again: the config may change only while the
//! table has no rows (below), and the engine started its counts again then.
//!
//! The year is the create's time for a create, and for `assignNumber` the
//! row's own `createdAt` when the table holds one (a date-time, the
//! repositories' audit field, which the layout gives the core for a table
//! with a Numbering use), else the command's time. `assignNumber` numbers a
//! row with no number as its create would have, and on one numbered already
//! writes nothing and returns its number (`unchanged`), after every guard,
//! as the engine's did, so Lease's guard holds it unless its `exempt` names
//! it. Moving the scope's relation to another target keeps the number,
//! counted in the count of the target it was numbered under.
//!
//! configChange: Numbering can be added to a table with rows, which have no
//! number until `assignNumber` gives them one. While the table has rows, the
//! format, the scope and the reset stay, since the rows were numbered under
//! them and a new count could print a label one of them holds, and Numbering
//! cannot be removed: added again, it would count from the start. With none,
//! anything may change, and a count made under another config starts again
//! at 1. The core writes no row at a publish (D103): a removed use's counts
//! go with its table in the migration that drops it.
//!
//! A write around the core (raw SQL of the number's columns or the counts)
//! runs none of these rules: nothing refuses it, and the docs say why not to.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    key_of, Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Hook, Refusal,
    SchemaIssue, ValidationIssue, ValidationRequest, View,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::timers::parse_date_time;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Numbering";

/// Numbering's declaration, the engine's file, read in place.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Numbering.behavior.json");

/// The row's number in its count, from 1.
pub const SEQ: &str = "seq";

/// The value field: the number as the format prints it.
pub const LABEL: &str = "label";

/// The operation that numbers a row made before the table composed
/// Numbering.
pub const ASSIGN_NUMBER: &str = "assignNumber";

/// The shared table of the counts, one row per scope.
pub const COUNTS: &str = "counts";

/// The scope key of a count of the whole table.
pub const WHOLE: &str = "*";

/// The own field a row's year of creation is read from by `assignNumber`:
/// the repositories' audit field.
pub const CREATED_AT: &str = "createdAt";

/// The widest `{seq:N}` a format may pad to.
const MAX_SEQ_WIDTH: usize = 10;

/// The longest format the configSchema takes.
const MAX_FORMAT_LENGTH: usize = 128;

/// What the configSchema holds a scope's link to.
const LINK_PATTERN: &str = "^[a-z][A-Za-z0-9]{0,63}$";

const TOKENS: &str = "{seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}";

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 3] = ["format", "reset", "scope"];

/// The number and the year an example of a format prints with.
pub const EXAMPLE_SEQ: i64 = 42;
pub const EXAMPLE_YEAR: i64 = 2026;

/// One piece of a format: text, the number padded to a width (0 for none),
/// or the year in 4 or 2 digits.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FormatPart {
    Text(String),
    Seq(usize),
    Year(usize),
}

/// Numbering's config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NumberingConfig {
    /// How a number prints: `{seq}` or `{seq:N}` once, `{yyyy}` and `{yy}`
    /// as often as wanted, and text.
    pub format: String,
    /// The format's parts.
    pub parts: Vec<FormatPart>,
    /// The to-one relation whose target each count runs per; one count for
    /// the table when `None`.
    pub scope: Option<String>,
    /// The own field that holds the scope relation's target's key
    /// (`customerId` for the relation `customer`).
    pub scope_field: Option<String>,
    /// Whether each count starts again at 1 every UTC year.
    pub yearly: bool,
}

impl NumberingConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the format's tokens, the yearly reset against the year
    /// and the scope against the type's to-one relations, each a rule's
    /// reason in the engine's words, which the core prefixes with the type,
    /// the field and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<NumberingConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let format = raw
            .get("format")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let parts = parse_format(&format)?;
        let yearly = raw.get("reset").and_then(Value::as_str) == Some("yearly");
        if yearly && !parts.iter().any(|part| matches!(part, FormatPart::Year(_))) {
            return Err(format!(
                "reset yearly counts each year from 1, so format \"{format}\" prints the year, {{yyyy}} or {{yy}}, or two years would print one label"
            )
            .into());
        }
        let scope = raw
            .get("scope")
            .and_then(|scope| scope.get("link"))
            .and_then(Value::as_str)
            .map(str::to_owned);
        let scope_field = match &scope {
            Some(link) => Some(scope_field(target, link)?),
            None => None,
        };
        Ok(NumberingConfig {
            format,
            parts,
            scope,
            scope_field,
            yearly,
        })
    }

    /// The number printed by the format: the number padded to its token's
    /// width, a longer one in full, and the year in 4 or 2 digits.
    pub fn label(&self, seq: i64, year: i64) -> String {
        print(&self.parts, seq, year)
    }

    /// What the count is made under: the format, the scope and the reset,
    /// as JSON in that order, which a count's row keeps (`counting`).
    pub fn counting(&self) -> String {
        let text = |value: &str| Value::from(value).to_string();
        format!(
            "{{\"format\":{},\"reset\":{},\"scope\":{}}}",
            text(&self.format),
            if self.yearly {
                text("yearly")
            } else {
                "null".to_owned()
            },
            self.scope.as_deref().map_or("null".to_owned(), text),
        )
    }

    // same reports whether two configs count and print alike.
    fn same(&self, other: &NumberingConfig) -> bool {
        self.format == other.format && self.scope == other.scope && self.yearly == other.yearly
    }
}

/// Numbering: a number a person reads, printed by a format, counted per
/// table, relation target or year.
#[derive(Clone, Copy, Debug, Default)]
pub struct Numbering;

impl Behavior for Numbering {
    type Config = NumberingConfig;

    // The declaration file names the value field, label, and the shared
    // table counts (D103's step 4).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<NumberingConfig, ConfigError> {
        NumberingConfig::parse(raw, target)
    }

    // See the module's header: added to a table with rows, it numbers none
    // of them; removed from one, or counting another way on one, it would
    // print labels they hold.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&NumberingConfig>,
        after: Option<&NumberingConfig>,
    ) -> Hook<Option<String>> {
        let Some(before) = before else {
            return Ok(None);
        };
        let Some(after) = after else {
            return Ok(Some("the instances would lose their numbers, and Numbering added again would count from 1, printing labels they hold".to_owned()));
        };
        if before.same(after) || !cx.has_rows()? {
            return Ok(None);
        }
        let scope = |config: &NumberingConfig| {
            config
                .scope
                .as_ref()
                .map_or_else(|| "none".to_owned(), |link| format!("link {link}"))
        };
        let reset = |config: &NumberingConfig| if config.yearly { "yearly" } else { "none" };
        let mut changed = Vec::new();
        if before.format != after.format {
            changed.push(format!(
                "the format from \"{}\" to \"{}\"",
                before.format, after.format
            ));
        }
        if before.scope != after.scope {
            changed.push(format!(
                "the scope from {} to {}",
                scope(before),
                scope(after)
            ));
        }
        if before.yearly != after.yearly {
            changed.push(format!(
                "the reset from {} to {}",
                reset(before),
                reset(after)
            ));
        }
        Ok(Some(format!(
            "the instances were numbered under the format, the scope and the reset, which stay while there are instances; the new config changes {}",
            changed.join(", ")
        )))
    }

    fn check_params(
        &self,
        _config: &NumberingConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != ASSIGN_NUMBER {
            return Vec::new();
        }
        params_issues(params).into_issues()
    }

    // A create gives the scope relation's target, which its number is
    // counted by.
    fn validate(
        &self,
        view: &View<'_, NumberingConfig>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let config = view.config();
        let (ValidationRequest::Create { own }, Some(field), Some(link)) =
            (request, &config.scope_field, &config.scope)
        else {
            return Ok(Vec::new());
        };
        if own.get(field).and_then(key_of).is_some() {
            return Ok(Vec::new());
        }
        Ok(vec![ValidationIssue::new(
            field.as_str(),
            "required",
            format!(
                "{field} is required: Numbering counts {}'s numbers per {link}",
                view.type_name()
            ),
        )])
    }

    // A create takes the next number of its count, in the year of its time.
    fn initialize(&self, cx: &Cx<'_, NumberingConfig>, _params: &Value) -> Hook<()> {
        assign(cx, "create", cx.now())?;
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, NumberingConfig>, name: &str, _params: &Value) -> Hook<Value> {
        if name != ASSIGN_NUMBER {
            return Err(cx.defect(format!("it declares no operation {name}")));
        }
        if let Some(number) = held(cx) {
            cx.unchanged()?;
            return Ok(number_json(&number));
        }
        let created_at = created_at(cx).unwrap_or_else(|| cx.now());
        Ok(number_json(&assign(cx, ASSIGN_NUMBER, created_at)?))
    }

    // Nothing changes a number once set: the core refuses an own write of
    // a field anchor's field, and no hook here writes the number again.
    fn after_change(&self, _cx: &Cx<'_, NumberingConfig>, _change: &Change<'_>) -> Hook<()> {
        Ok(())
    }
}

// assign numbers the row: the next number of its count, read from its
// shared row, written held to the version it was read at, printed by the
// format. action names the write, the create or assignNumber.
fn assign(cx: &Cx<'_, NumberingConfig>, action: &str, created_at: i64) -> Hook<(i64, String)> {
    let config = cx.config();
    let year = utc_year(created_at);
    let mut keys = Vec::new();
    if let (Some(field), Some(link)) = (&config.scope_field, &config.scope) {
        let Some(target) = cx.own_field(field).as_ref().and_then(key_of) else {
            return Err(Refusal::invalid_argument(format!(
                "{action}: {} {} holds no {link}, which its number is counted by",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ))
            .into());
        };
        keys.push(target);
    }
    if config.yearly {
        keys.push(year.to_string());
    }
    let scope = if keys.is_empty() {
        WHOLE.to_owned()
    } else {
        keys.join("/")
    };
    let counting = config.counting();
    let held = cx.shared_row(COUNTS, &scope)?;
    let last = held
        .as_ref()
        .filter(|row| row.value("counting").and_then(Value::as_str) == Some(counting.as_str()))
        .and_then(|row| row.value("last"))
        .and_then(whole)
        .unwrap_or(0);
    let seq = last + 1;
    let label = config.label(seq, year);
    let mut values = Map::new();
    values.insert("last".into(), Value::from(seq));
    values.insert("counting".into(), Value::from(counting));
    match held {
        Some(row) => cx.update_shared(COUNTS, &scope, row.version, values)?,
        None => cx.insert_shared(COUNTS, &scope, values)?,
    }
    cx.set(SEQ, seq)?;
    cx.set(LABEL, label.as_str())?;
    Ok((seq, label))
}

// held is the row's number and label, or None before it has one.
fn held(view: &View<'_, NumberingConfig>) -> Option<(i64, String)> {
    let seq = view.get(SEQ).as_ref().and_then(whole)?;
    let label = view
        .get(LABEL)
        .and_then(|label| label.as_str().map(str::to_owned))
        .unwrap_or_default();
    Some((seq, label))
}

fn number_json((seq, label): &(i64, String)) -> Value {
    json!({"seq": seq, "label": label})
}

// created_at is the row's own createdAt, when the type holds one as a
// date-time: epoch milliseconds from a table's adapter, or ISO 8601 text.
fn created_at(view: &View<'_, NumberingConfig>) -> Option<i64> {
    match view.own_field(CREATED_AT)? {
        Value::String(text) => parse_date_time(&text),
        value => whole(&value),
    }
}

// scope_field is the own field holding the key of a to-one relation of the
// type by the relation's name, which a scope's link names on a table.
fn scope_field(target: &ConfigTarget<'_>, link: &str) -> Result<String, ConfigError> {
    let relations: Vec<(&str, &str)> = target
        .fields()
        .iter()
        .filter_map(|field| {
            field
                .relation
                .as_ref()
                .map(|relation| (relation.name.as_str(), field.name.as_str()))
        })
        .collect();
    if let Some((_, field)) = relations.iter().find(|(name, _)| *name == link) {
        return Ok((*field).to_owned());
    }
    let listed = if relations.is_empty() {
        "it has none".to_owned()
    } else {
        format!(
            "its to-one relations: {}",
            relations
                .iter()
                .map(|(name, _)| *name)
                .collect::<Vec<_>>()
                .join(", ")
        )
    };
    Err(format!(
        "scope.link {link} is not a to-one relation of {} ({listed}), so a row has no one target to count by",
        target.type_name()
    )
    .into())
}

/// Splits a format into its parts, refusing a brace outside a token, a
/// token it does not know, and a {seq} that is not there once, in the
/// engine's words.
pub fn parse_format(format: &str) -> Result<Vec<FormatPart>, String> {
    let mut parts = Vec::new();
    let mut seqs = 0;
    let mut last = 0;
    let mut at = 0;
    let bytes = format.as_bytes();
    while at < bytes.len() {
        let byte = bytes[at];
        if byte != b'{' && byte != b'}' {
            at += 1;
            continue;
        }
        // A token is a { and the text up to the next brace, when that
        // brace is a }; any other brace stands alone.
        let close = (byte == b'{')
            .then(|| {
                bytes[at + 1..]
                    .iter()
                    .position(|next| *next == b'{' || *next == b'}')
                    .map(|offset| at + 1 + offset)
                    .filter(|end| bytes[*end] == b'}')
            })
            .flatten();
        if at > last {
            parts.push(FormatPart::Text(format[last..at].to_owned()));
        }
        let Some(end) = close else {
            return Err(format!(
                "format \"{format}\" has a \"{}\" that is not part of a token; its tokens are {TOKENS}",
                char::from(byte)
            ));
        };
        let token = &format[at + 1..end];
        last = end + 1;
        at = end + 1;
        match token {
            "yyyy" => parts.push(FormatPart::Year(4)),
            "yy" => parts.push(FormatPart::Year(2)),
            _ => {
                let Some(width) = seq_width(token).filter(|width| *width <= MAX_SEQ_WIDTH) else {
                    return Err(format!(
                        "format \"{format}\" has {{{token}}}, which is not one of its tokens: {TOKENS}"
                    ));
                };
                seqs += 1;
                parts.push(FormatPart::Seq(width));
            }
        }
    }
    if last < format.len() {
        parts.push(FormatPart::Text(format[last..].to_owned()));
    }
    if seqs != 1 {
        return Err(if seqs == 0 {
            format!("format \"{format}\" has no {{seq}}: it prints the number with {{seq}}, or {{seq:N}} padded to N digits, once")
        } else {
            format!("format \"{format}\" has the number {seqs} times: it prints it with {{seq}} or {{seq:N}} once, so two numbers never print one label")
        });
    }
    Ok(parts)
}

// seq_width reads a token `seq` (width 0) or `seq:N` with N of one or two
// digits, the first not 0 (`^seq(?::([1-9][0-9]?))?$`).
fn seq_width(token: &str) -> Option<usize> {
    let rest = token.strip_prefix("seq")?;
    if rest.is_empty() {
        return Some(0);
    }
    let digits = rest.strip_prefix(':')?;
    let valid = matches!(digits.len(), 1 | 2)
        && digits.bytes().all(|b| b.is_ascii_digit())
        && !digits.starts_with('0');
    valid.then(|| digits.parse().ok()).flatten()
}

// pad writes a number padded with zeros to a width, as padStart does.
fn pad(number: i64, width: usize) -> String {
    format!("{number:0>width$}")
}

// print writes a number and a year by a format's parts.
fn print(parts: &[FormatPart], seq: i64, year: i64) -> String {
    parts
        .iter()
        .map(|part| match part {
            FormatPart::Text(text) => text.clone(),
            FormatPart::Seq(width) => pad(seq, *width),
            FormatPart::Year(4) => pad(year, 4),
            FormatPart::Year(width) => pad(year % 100, *width),
        })
        .collect()
}

/// A format's number 42 of 2026, as a create would print it, or the format
/// itself when the config would not take it: what a guide prints as an
/// example.
pub fn example(format: &str) -> String {
    parse_format(format).map_or_else(
        |_| format.to_owned(),
        |parts| print(&parts, EXAMPLE_SEQ, EXAMPLE_YEAR),
    )
}

/// The UTC year of an instant in epoch milliseconds, as JavaScript's
/// `getUTCFullYear` reads it.
pub fn utc_year(ms: i64) -> i64 {
    superschematic_rules::civil::from_days(ms.div_euclid(86_400_000)).0
}

// whole reads a whole number: 2, 2.0, or a column's decimal text.
fn whole(value: &Value) -> Option<i64> {
    match value {
        Value::Number(number) => number.as_i64().or_else(|| {
            number
                .as_f64()
                .filter(|n| n.is_finite() && n.fract() == 0.0)
                .map(|n| n as i64)
        }),
        Value::String(text) => text.parse().ok(),
        _ => None,
    }
}

// ---- the configSchema and the paramsSchema, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    match config.get("format") {
        None => issues.push("", "required", "must have required property 'format'"),
        Some(Value::String(format)) => {
            if schema::length(format) < 1 {
                issues.push(
                    "/format",
                    "minLength",
                    "must NOT have fewer than 1 characters",
                );
            }
            if schema::length(format) > MAX_FORMAT_LENGTH {
                issues.push(
                    "/format",
                    "maxLength",
                    format!("must NOT have more than {MAX_FORMAT_LENGTH} characters"),
                );
            }
        }
        Some(_) => issues.push("/format", "type", "must be string"),
    }
    if let Some(scope) = config.get("scope") {
        scope_issues(scope, &mut issues);
    }
    if let Some(reset) = config.get("reset") {
        if reset != "yearly" {
            issues.push("/reset", "enum", "must be one of \"yearly\"");
        }
    }
    issues
}

fn scope_issues(scope: &Value, issues: &mut Issues) {
    let at = "/scope";
    let Some(members) = scope.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    for key in members.keys().filter(|key| key.as_str() != "link") {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    match members.get("link") {
        None => issues.push(at, "required", "must have required property 'link'"),
        Some(Value::String(link)) => {
            if !is_link(link) {
                issues.push(
                    "/scope/link",
                    "pattern",
                    format!("must match pattern \"{LINK_PATTERN}\""),
                );
            }
        }
        Some(_) => issues.push("/scope/link", "type", "must be string"),
    }
}

// is_link holds a link's name to LINK_PATTERN: a lowercase letter, then at
// most 63 letters and digits, ASCII alone.
fn is_link(link: &str) -> bool {
    let mut chars = link.chars();
    chars.next().is_some_and(|first| first.is_ascii_lowercase())
        && link.len() <= 64
        && chars.all(|c| c.is_ascii_alphanumeric())
}

// params_issues lists what assignNumber's parameters break of its closed,
// empty paramsSchema, in the behavior registry's words.
fn params_issues(params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given.keys() {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_format_prints_its_number_padded_and_its_year() {
        let label = |format: &str, seq, year| print(&parse_format(format).unwrap(), seq, year);
        assert_eq!(label("INV-{yyyy}-{seq:4}", 42, 2026), "INV-2026-0042");
        assert_eq!(label("{yy}/{seq:1}:{yyyy}", 10, 2005), "05/10:2005");
        assert_eq!(label("No. {seq}", 7, 2026), "No. 7");
        assert_eq!(label("{seq:3}", 12345, 2026), "12345");
        assert!(parse_format("{seq:10}").is_ok());
        for refused in [
            "{seq:0}", "{seq:04}", "{seq:11}", "{n}", "{seq", "{{seq}}", "}{seq}", "{yyyy}",
        ] {
            assert!(parse_format(refused).is_err(), "{refused}");
        }
        assert_eq!(example("INV-{yyyy}-{seq:4}"), "INV-2026-0042");
        assert_eq!(example("{n}"), "{n}");
    }

    #[test]
    fn a_year_is_the_utc_year_of_an_instant() {
        assert_eq!(utc_year(0), 1970);
        assert_eq!(utc_year(1_798_761_599_999), 2026);
        assert_eq!(utc_year(1_798_761_600_000), 2027);
        assert_eq!(utc_year(951_782_400_000), 2000);
        assert_eq!(utc_year(-1), 1969);
    }

    #[test]
    fn a_link_is_held_to_its_pattern() {
        assert!(is_link("customer"));
        assert!(is_link("c2"));
        assert!(!is_link("Customer"));
        assert!(!is_link("cust_omer"));
        assert!(!is_link(""));
        assert!(!is_link(&format!("c{}", "x".repeat(64))));
    }
}
