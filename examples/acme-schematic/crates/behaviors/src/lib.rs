//! acme.Rating for the Rust engine (D58), the port of
//! `packages/behaviors/src/rating.ts`: shoppers rate a catalog item from one
//! star to the maximum its type configures (D16). It is an extension's
//! behavior, a crate of the project that declares it, which implements the
//! trait the core's behaviors implement and reaches the engine only
//! through the plug-in API they use.
//!
//! The declaration is the copy `acme-schematic behaviors --extension acme
//! --out crates/behaviors/declarations` writes from
//! `ext/rating.behavior.json`, the file the compiler registers, so the
//! engine and the compiler read one declaration; the smoke runs the same
//! command with `--check`.
//!
//! The behavior keeps two columns on each instance, the number of ratings
//! and their sum, and reads its declared fields from them. `rate` adds one
//! rating; `ratingSummary` reads them. A new version may raise maxStars,
//! which every stored rating still fits, and may not lower it.
//!
//! An engine runs it once a host registers it after the core's behaviors,
//! with the meta-schema acme's binary writes (`acme-schematic
//! json-schema`), which declares acme.Rating, and the tool options acme's
//! binary registers ([`tools`]), as the TypeScript package's `behaviors`
//! and `tools` are passed to `openEngine`:
//!
//! ```no_run
//! use superschematic_engine::{Engine, EngineOptions, MetaSchema, Policy};
//!
//! let mut options = EngineOptions::sqlite("shop.db");
//! options.policy = Some(Policy::allow_all());
//! options.meta_schema = Some(MetaSchema::Text(std::fs::read_to_string("acme-schema-file.json")?));
//! options.behaviors = superschematic_engine_behaviors::core().extend(acme_behaviors::behaviors());
//! options.tools = acme_behaviors::tools();
//! let engine = Engine::open(options)?;
//! # Ok::<(), Box<dyn std::error::Error>>(())
//! ```
//!
//! A project's build assembles that set itself: for a schema that composes
//! acme.Rating, the `projectengine` generator writes an engine crate whose
//! `behaviors()` is the core's, the work queue's when the schema uses one
//! of them, and [`Rating`], the expression acme's `BehaviorSpec` registers
//! (`ext/behavior.go`).

use serde_json::{Map, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::json::{js_number, number_value};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, EngineError, Error, Hooks, InvocationPolicy, Result,
    ToolOptions,
};

/// The behavior's registered name: acme's own, `<extension>.<Name>`.
pub const NAME: &str = "acme.Rating";

/// Every behavior acme declares, in the order [`behaviors`] registers them.
pub const NAMES: [&str; 1] = [NAME];

/// A type's config of acme.Rating, as `ext/rating.behavior.json` declares
/// it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RatingConfig {
    /// The highest rating a shopper can give, 3 to 10.
    pub max_stars: i64,
}

/// The behavior's two columns on an instance.
struct Totals {
    /// How many ratings it has.
    count: i64,
    /// The sum of their stars.
    stars: i64,
}

fn totals(columns: &Row) -> Result<Totals> {
    Ok(Totals {
        count: columns.i64("count")?,
        stars: columns.i64("stars")?,
    })
}

/// What `rate` and `ratingSummary` return, and the fields read: the count,
/// and the mean as JavaScript divides it, 0 before the first rating.
fn summary(totals: &Totals) -> Value {
    let mut out = Map::new();
    out.insert("ratingCount".into(), Value::from(totals.count));
    out.insert("ratingAverage".into(), average(totals));
    Value::Object(out)
}

fn average(totals: &Totals) -> Value {
    if totals.count == 0 {
        return Value::from(0);
    }
    let mean = totals.stars as f64 / totals.count as f64;
    // The number's shortest text, as JSON.stringify writes it: 4.5, or 4
    // for a mean that is whole.
    serde_json::from_str(&js_number(mean)).unwrap_or(Value::Null)
}

/// acme.Rating: a rating from one star to the type's maxStars.
#[derive(Clone, Copy, Debug, Default)]
pub struct Rating;

impl Behavior for Rating {
    type Config = RatingConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/acme.Rating.behavior.json")
    }

    // The configSchema holds maxStars to an integer from 3 to 10, so a
    // config the engine hands over always has one.
    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<RatingConfig> {
        match raw.get("maxStars") {
            Some(Value::Number(max)) => Ok(RatingConfig {
                max_stars: number_value(max) as i64,
            }),
            _ => Err(Error::config(
                "maxStars, the highest rating a shopper can give, is required",
            )),
        }
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| {
                Ok(match (before, after) {
                    (Some(before), Some(after)) => {
                        (after.max_stars < before.max_stars).then(|| {
                            format!(
                                "maxStars cannot fall from {} to {}",
                                before.max_stars, after.max_stars
                            )
                        })
                    }
                    _ => Some(
                        "the ratings an item has would be lost or start from nothing".to_owned(),
                    ),
                })
            }),
            migrations: vec![BehaviorMigration::new(1, "rating totals")
                .column("count", ColumnSpec::integer().not_null().default(0))
                .column("stars", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("rate", |_, cx, params| {
                    // The paramsSchema holds stars to an integer of at least 1.
                    let stars = match params.get("stars") {
                        Some(Value::Number(stars)) => number_value(stars),
                        _ => 0.0,
                    };
                    let max_stars = cx.config().max_stars;
                    if stars > max_stars as f64 {
                        return Err(EngineError::invalid_argument(format!(
                            "a rating of {} is 1 to {max_stars} stars, not {}",
                            cx.schema(),
                            js_number(stars)
                        ))
                        .into());
                    }
                    let before = totals(&cx.columns().get()?)?;
                    let after = Totals {
                        count: before.count + 1,
                        stars: before.stars + stars as i64,
                    };
                    cx.columns().set(&[
                        ("count", SqlValue::Integer(after.count)),
                        ("stars", SqlValue::Integer(after.stars)),
                    ])?;
                    Ok(summary(&after))
                }),
                ("ratingSummary", |_, cx, _| {
                    Ok(summary(&totals(&cx.columns().get()?)?))
                }),
            ],
            fields: vec![
                ("ratingCount", |_, view| {
                    Ok(Some(Value::from(totals(&view.columns().get()?)?.count)))
                }),
                ("ratingAverage", |_, view| {
                    Ok(Some(average(&totals(&view.columns().get()?)?)))
                }),
            ],
            ..Hooks::default()
        }
    }
}

/// Every behavior acme declares, for `EngineOptions::behaviors` after the
/// core's (`superschematic_engine_behaviors::core`): the TypeScript
/// package's `behaviors`.
pub fn behaviors() -> BehaviorSet {
    BehaviorSet::new().with(Rating)
}

/// The invocation policy and vendor keys acme's binary registers
/// (`ext/mcp.go`), for `EngineOptions::tools`: the engine writes acme's
/// tools as acme's generated SDKs write theirs, and takes acme.Rating's
/// rate, whose declaration asks confirm "always". The TypeScript package's
/// `tools`.
pub fn tools() -> ToolOptions {
    ToolOptions {
        invocation_policy: Some(InvocationPolicy {
            key: "confirm".to_owned(),
            values: vec!["never".to_owned(), "always".to_owned()],
            default: "never".to_owned(),
        }),
        scalar_key: Some("x-acme-scalar".to_owned()),
        guidance_key: Some("acme/operation-guidance".to_owned()),
        parameter_keys: Some(vec![("x-acme-arguments".to_owned(), Value::from(1))]),
        ..ToolOptions::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_declaration_is_acme_rating_with_its_fields_and_operations() {
        let declaration: Value =
            serde_json::from_str(Rating.declaration()).expect("the declaration");
        assert_eq!(declaration["name"], Value::from(NAME));
        let names = |key: &str| -> Vec<String> {
            declaration[key]
                .as_array()
                .expect(key)
                .iter()
                .map(|entry| entry["name"].as_str().expect("a name").to_owned())
                .collect()
        };
        assert_eq!(names("fields"), ["ratingCount", "ratingAverage"]);
        assert_eq!(names("operations"), ["rate", "ratingSummary"]);
        assert_eq!(behaviors().len(), NAMES.len());
    }

    #[test]
    fn the_summary_is_the_count_and_the_mean_as_javascript_divides() {
        let of = |count, stars| summary(&Totals { count, stars }).to_string();
        assert_eq!(of(0, 0), r#"{"ratingCount":0,"ratingAverage":0}"#);
        assert_eq!(of(2, 9), r#"{"ratingCount":2,"ratingAverage":4.5}"#);
        assert_eq!(of(2, 8), r#"{"ratingCount":2,"ratingAverage":4}"#);
        assert_eq!(
            of(3, 10),
            r#"{"ratingCount":3,"ratingAverage":3.3333333333333335}"#
        );
    }
}
