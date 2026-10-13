//! Numbering, a number a person reads on each instance (D56), the port of
//! `src/behaviors/core/numbering.ts`: an invoice's INV-2026-0042, a
//! ticket's T-17. The config's format prints it, a template with a closed
//! set of tokens: `{seq}`, or `{seq:N}` zero-padded to N digits (1 to 10),
//! exactly once, and `{yyyy}` and `{yy}`, the UTC year the instance was
//! created in, as often as the format wants them. A brace that is not one
//! of these tokens is refused, so a format holds no text a reader could take
//! for a token.
//!
//! The count runs per namespace and schema; with `scope: { link }`, per
//! target of that link of the type's Links config, which must be required
//! and hold one target (a many-valued link is refused, its raw config read
//! from the type's configs, as [`crate::link_many`] reads it); with
//! `reset: "yearly"`, per UTC year as well, which needs the year in the
//! format. Only `{seq}` varies in width, and it is there once, so a label's
//! length fixes where every token sits: two numbers of one count print two
//! labels, and with the year in the label a yearly count's labels differ
//! across years too. A label is unique within its scope, and only there:
//! two targets each have a T-1.
//!
//! The number is set in afterChange of the create, inside its transaction,
//! so Links' initialize has run and every link a create gives is held, and
//! a create a later behavior refuses rolls the count back with it: a
//! refused create takes no number and leaves no gap. A delete leaves one.
//! The year is the instance's `created_at`, read with the scope link's
//! target in one read of the instance (`instances().get`, as the caller),
//! so a number assignNumber gives later still counts in the year the
//! instance was made. `counters` holds the last number of each count, by a
//! scope key: the target's id, the year, both with a `/` between (an id
//! holds no `/`), or `''`. `labels` holds each numbered instance's label
//! under its scope key, unique there, so a defect that would print one
//! label twice fails the write (a BehaviorError) rather than store it; a
//! delete drops its row.
//!
//! The instance's own columns hold its number and label, which the fields
//! `seq` and `label` read and a list filters on through an index of each
//! (`where: { "Numbering.label": "INV-2026-0042" }`). The number's column
//! is named `number`: the relation over the instances (`sql.instances()`)
//! has a `seq` of its own, the instance's event sequence. Nothing changes a
//! number once set: no operation writes the columns but the one that sets
//! them when they are empty, and a create or an update writes own fields
//! only.
//!
//! `assignNumber` numbers an instance made before its schema composed
//! Numbering, as its create would have, with an operation event; on an
//! instance numbered already it is `unchanged()` and returns its number. A
//! deployment backfills by invoking it in creation order. Moving a scope
//! link to another target keeps the number, counted in the count of the
//! target it was numbered under.
//!
//! Storage (D58): every statement is the same on SQLite and Postgres. The
//! upsert that moves a count names the stored count by its table, which
//! Postgres needs where `last` alone could be the row offered for insert.
//!
//! configChange: Numbering can be added to a schema with instances, which
//! have no number until assignNumber gives them one. While the schema has
//! instances, the format, the scope and the reset stay, since the instances
//! were numbered under them and a new count could print a label one of them
//! holds, and Numbering cannot be removed: added again, it would count from
//! the start. With none, anything may change, and afterConfigChange drops
//! the schema's counts and labels whenever the behavior comes, goes, or
//! counts in another way, so numbering starts again at 1.

use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, ConfigChange, InstanceCx, InstanceView,
    PublishCx, TableSpec,
};
use superschematic_engine::json::js_keys;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Behavior, ConfigTarget, Error, Hooks, InstanceChange, Result};

use crate::links::link_many;

const NAME: &str = "Numbering";

/// The widest `{seq:N}` a format may pad to.
const MAX_SEQ_WIDTH: usize = 10;

const TOKENS: &str = "{seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}";

/// Numbering's config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NumberingConfig {
    /// How a number prints: `{seq}` or `{seq:N}` once, `{yyyy}` and `{yy}`
    /// as often as wanted, and text.
    pub format: String,
    /// The link of the type's Links config whose target each count runs
    /// per; one count for the schema when `None`.
    pub scope: Option<String>,
    /// Whether each count starts again at 1 every UTC year.
    pub yearly: bool,
}

impl NumberingConfig {
    // same reports whether two configs count and print alike.
    fn same(&self, other: &NumberingConfig) -> bool {
        self == other
    }
}

/// One piece of a format: text, the number padded to a width (0 for none),
/// or the year in 4 or 2 digits.
#[derive(Clone, Debug, PartialEq, Eq)]
enum FormatPart {
    Text(String),
    Seq(usize),
    Year(usize),
}

// parse_format splits a format into its parts, refusing a brace outside a
// token, a token it does not know, and a {seq} that is not there once.
fn parse_format(format: &str) -> Result<Vec<FormatPart>> {
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
            return Err(Error::config(format!(
                "format \"{format}\" has a \"{}\" that is not part of a token; its tokens are {TOKENS}",
                char::from(byte)
            )));
        };
        let token = &format[at + 1..end];
        last = end + 1;
        at = end + 1;
        match token {
            "yyyy" => parts.push(FormatPart::Year(4)),
            "yy" => parts.push(FormatPart::Year(2)),
            _ => {
                let Some(width) = seq_width(token).filter(|width| *width <= MAX_SEQ_WIDTH) else {
                    return Err(Error::config(format!(
                        "format \"{format}\" has {{{token}}}, which is not one of its tokens: {TOKENS}"
                    )));
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
        return Err(Error::config(if seqs == 0 {
            format!("format \"{format}\" has no {{seq}}: it prints the number with {{seq}}, or {{seq:N}} padded to N digits, once")
        } else {
            format!("format \"{format}\" has the number {seqs} times: it prints it with {{seq}} or {{seq:N}} once, so two numbers never print one label")
        }));
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

// number_label prints a number by a format the config accepted: the number
// padded to its token's width, a longer one in full, and the year in 4 or
// 2 digits.
fn number_label(format: &str, seq: i64, year: i64) -> Result<String> {
    Ok(parse_format(format)?
        .into_iter()
        .map(|part| match part {
            FormatPart::Text(text) => text,
            FormatPart::Seq(width) => pad(seq, width),
            FormatPart::Year(4) => pad(year, 4),
            FormatPart::Year(width) => pad(year % 100, width),
        })
        .collect())
}

/// The number and the year the guidance prints its example with.
pub(crate) const EXAMPLE_SEQ: i64 = 42;
pub(crate) const EXAMPLE_YEAR: i64 = 2026;

/// A format's number 42 of 2026, as the engine would print it, for the
/// guidance; the format itself when the config would not take it.
pub(crate) fn example(format: &str) -> String {
    number_label(format, EXAMPLE_SEQ, EXAMPLE_YEAR).unwrap_or_else(|_| format.to_owned())
}

/// Whether a format prints the year.
pub(crate) fn dated(format: &str) -> bool {
    format.contains("{yyyy}") || format.contains("{yy}")
}

/// The UTC year of an instant in epoch milliseconds, as JavaScript's
/// `getUTCFullYear` reads it.
fn utc_year(ms: i64) -> i64 {
    // Days since 1970-01-01, then the civil year (Howard Hinnant's
    // algorithm, the year starting in March).
    let days = ms.div_euclid(86_400_000);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    yoe + era * 400 + i64::from(month <= 2)
}

// check_scope holds the scope's link to the type's Links config: one of its
// links, required, and holding one target. It reads the raw config, so a
// link marked many is refused before Links reads that key.
fn check_scope(target: &ConfigTarget<'_>, link: &str) -> Result<()> {
    let at = format!("scope.link {link}");
    if !target.behaviors().iter().any(|name| name == "Links") {
        return Err(Error::config(format!(
            "{at} is a link of Links, which the type does not list"
        )));
    }
    let Some(links) = target
        .configs()
        .get("Links")
        .and_then(|config| config.get("links"))
        .and_then(Value::as_object)
    else {
        // A Links config of the wrong shape is Links' to refuse.
        return Ok(());
    };
    let Some(spec) = links.get(link) else {
        return Err(Error::config(format!(
            "{at} is not a link of the type's Links config (its links: {})",
            js_keys(links).join(", ")
        )));
    };
    if link_many(spec) {
        return Err(Error::config(format!(
            "{at} holds many targets (many: true), so an instance has no one target to count by; name a link that holds one"
        )));
    }
    if spec.get("required") != Some(&Value::Bool(true)) {
        return Err(Error::config(format!(
            "{at} is not required, so an instance may hold no target to count by; make it required"
        )));
    }
    Ok(())
}

// held is the instance's number and label, or None before it has one.
fn held(view: &InstanceView<'_, NumberingConfig>) -> Result<Option<(i64, String)>> {
    let columns = view.columns().get()?;
    let Some(number) = columns.opt_i64("number")? else {
        return Ok(None);
    };
    Ok(Some((
        number,
        columns.opt_text("label")?.unwrap_or_default().to_owned(),
    )))
}

fn number_json((seq, label): &(i64, String)) -> Value {
    json!({"seq": seq, "label": label})
}

// assign numbers the instance: the next number of its count, read from the
// instance's created_at and its scope link's target, printed by the
// format. action names the write, the create or assignNumber.
fn assign(cx: &InstanceCx<'_, NumberingConfig>, action: &str) -> Result<(i64, String)> {
    let config = cx.config();
    let (namespace, schema, id) = (cx.namespace(), cx.schema(), cx.id());
    let link = config.scope.as_deref();
    let fields: &[&str] = if link.is_some() {
        &["Links.targets"]
    } else {
        &[]
    };
    let record = cx.instances().get(schema, id, Some(fields))?;
    let created_at = record.as_ref().map_or(cx.now(), |record| record.created_at);
    let year = utc_year(created_at);
    let mut keys = Vec::new();
    if let Some(link) = link {
        // A required link that holds one target is always held: Links gives
        // it at create, moves it and never unlinks it, and refuses its
        // target's delete.
        let target = record
            .as_ref()
            .and_then(|record| record.behavior_field("Links", "targets"))
            .and_then(|targets| targets.get(link))
            .and_then(|target| target.get("id"))
            .and_then(Value::as_str);
        let Some(target) = target else {
            return Err(Error::behavior(
                NAME,
                format!("{action}: {schema} {id} holds no target of its required link {link}, which its number is counted by"),
            ));
        };
        keys.push(target.to_owned());
    }
    if config.yearly {
        keys.push(year.to_string());
    }
    let scope_key = keys.join("/");
    let counters = cx.sql().table("counters")?;
    let count = [
        SqlValue::from(namespace),
        SqlValue::from(schema),
        SqlValue::from(scope_key.as_str()),
    ];
    cx.sql().run(
        format!(
            "INSERT INTO {counters} (namespace, schema, scope_key, last) VALUES (?, ?, ?, 1)
             ON CONFLICT (namespace, schema, scope_key) DO UPDATE SET last = {counters}.last + 1"
        ),
        &count,
    )?;
    let seq = cx
        .sql()
        .get(
            format!(
                "SELECT last FROM {counters} WHERE namespace = ? AND schema = ? AND scope_key = ?"
            ),
            &count,
        )?
        .ok_or_else(|| Error::behavior(NAME, "the count is gone after its upsert"))?
        .i64("last")?;
    let label = number_label(&config.format, seq, year)?;
    let labels = cx.sql().table("labels")?;
    let stored = cx.sql().run(
        format!(
            "INSERT INTO {labels} (namespace, schema, id, scope_key, label) VALUES (?, ?, ?, ?, ?)
             ON CONFLICT DO NOTHING"
        ),
        &[
            SqlValue::from(namespace),
            SqlValue::from(schema),
            SqlValue::from(id),
            SqlValue::from(scope_key.as_str()),
            SqlValue::from(label.as_str()),
        ],
    )?;
    if stored.changes == 0 {
        return Err(Error::behavior(
            NAME,
            format!("{schema} {id} would be numbered {label}, which another instance of its count holds"),
        ));
    }
    cx.columns().set(&[
        ("number", SqlValue::Integer(seq)),
        ("label", SqlValue::from(label.as_str())),
    ])?;
    Ok((seq, label))
}

/// Numbering: a number a person reads, printed by a format, counted per
/// schema, link target or year.
#[derive(Clone, Copy, Debug, Default)]
pub struct Numbering;

impl Behavior for Numbering {
    type Config = NumberingConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Numbering.behavior.json")
    }

    // The configSchema holds the shape; this holds the format to its tokens
    // and the scope to the type's Links.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<NumberingConfig> {
        let format = raw
            .get("format")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let parts = parse_format(&format)?;
        let yearly = raw.get("reset").and_then(Value::as_str) == Some("yearly");
        if yearly && !parts.iter().any(|part| matches!(part, FormatPart::Year(_))) {
            return Err(Error::config(format!(
                "reset yearly counts each year from 1, so format \"{format}\" prints the year, {{yyyy}} or {{yy}}, or two years would print one label"
            )));
        }
        let scope = raw
            .get("scope")
            .and_then(|scope| scope.get("link"))
            .and_then(Value::as_str)
            .map(str::to_owned);
        if let Some(link) = &scope {
            check_scope(target, link)?;
        }
        Ok(NumberingConfig {
            format,
            scope,
            yearly,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(config_change),
            after_config_change: Some(after_config_change),
            guidance: Some(|_, config, target| {
                crate::guidance::numbering::guidance(config, target)
            }),
            // The indexes let a list that filters on a number or a label
            // read only the instances that hold it, in creation order.
            migrations: vec![BehaviorMigration::new(1, "numbers")
                .column("number", ColumnSpec::integer())
                .column("label", ColumnSpec::text())
                .index("number", &["number"])
                .index("label", &["label"])
                .table(
                    TableSpec::new("counters")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("scope_key", ColumnSpec::text().not_null())
                        .column("last", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "schema", "scope_key"]),
                )
                .table(
                    TableSpec::new("labels")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("scope_key", ColumnSpec::text().not_null())
                        .column("label", ColumnSpec::text().not_null())
                        .primary_key(&["namespace", "schema", "id"])
                        .unique(&["namespace", "schema", "scope_key", "label"]),
                )],
            filters: vec![
                ("seq", BehaviorFilter::new("number", "integer")),
                ("label", BehaviorFilter::new("label", "string")),
            ],
            operations: vec![("assignNumber", |_, cx, _| {
                if let Some(number) = held(cx)? {
                    cx.unchanged();
                    return Ok(number_json(&number));
                }
                Ok(number_json(&assign(cx, "assignNumber")?))
            })],
            fields: vec![
                ("seq", |_, view| {
                    Ok(held(view)?.map(|(seq, _)| Value::from(seq)))
                }),
                ("label", |_, view| {
                    Ok(held(view)?.map(|(_, label)| Value::from(label)))
                }),
            ],
            after_change: Some(|_, cx, change| {
                match change {
                    InstanceChange::Create => {
                        assign(cx, "create")?;
                    }
                    InstanceChange::Delete => {
                        let labels = cx.sql().table("labels")?;
                        cx.sql().run(
                            format!(
                                "DELETE FROM {labels} WHERE namespace = ? AND schema = ? AND id = ?"
                            ),
                            &[
                                SqlValue::from(cx.namespace()),
                                SqlValue::from(cx.schema()),
                                SqlValue::from(cx.id()),
                            ],
                        )?;
                    }
                    _ => {}
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn config_change(
    _: &Numbering,
    before: Option<&NumberingConfig>,
    after: Option<&NumberingConfig>,
    change: &ConfigChange<'_>,
) -> Result<Option<String>> {
    let Some(before) = before else {
        return Ok(None);
    };
    let Some(after) = after else {
        return Ok(Some("the instances would lose their numbers, and Numbering added again would count from 1, printing labels they hold".to_owned()));
    };
    // No instance holds a number a new count could print again.
    if !change.instances()? || before.same(after) {
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

// after_config_change starts a count again at 1 when Numbering came, went or
// counts another way, which configChange allows only on a schema with no
// instance to hold a number (an added one's instances have none).
fn after_config_change(_: &Numbering, cx: &PublishCx<'_, NumberingConfig>) -> Result<()> {
    if let (Some(before), Some(config)) = (cx.before(), cx.config()) {
        if before.same(config) {
            return Ok(());
        }
    }
    for table in ["counters", "labels"] {
        let table = cx.sql().table(table)?;
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
            &[SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())],
        )?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_format_prints_its_number_padded_and_its_year() {
        assert_eq!(
            number_label("INV-{yyyy}-{seq:4}", 42, 2026).unwrap(),
            "INV-2026-0042"
        );
        assert_eq!(
            number_label("{yy}/{seq:1}:{yyyy}", 10, 2005).unwrap(),
            "05/10:2005"
        );
        assert_eq!(number_label("No. {seq}", 7, 2026).unwrap(), "No. 7");
        assert_eq!(number_label("{seq:3}", 12345, 2026).unwrap(), "12345");
        assert!(parse_format("{seq:10}").is_ok());
        for refused in [
            "{seq:0}", "{seq:04}", "{seq:11}", "{n}", "{seq", "{{seq}}", "}{seq}", "{yyyy}",
        ] {
            assert!(parse_format(refused).is_err(), "{refused}");
        }
    }

    #[test]
    fn a_year_is_the_utc_year_of_an_instant() {
        assert_eq!(utc_year(0), 1970);
        assert_eq!(utc_year(1_798_761_599_999), 2026);
        assert_eq!(utc_year(1_798_761_600_000), 2027);
        assert_eq!(utc_year(951_782_400_000), 2000);
        assert_eq!(utc_year(-1), 1969);
    }
}
