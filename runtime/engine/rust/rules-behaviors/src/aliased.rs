//! Aliased, a name on each row with the names it was also known by
//! (D147): a company that was Acme and is Acme Group, a tag that is also
//! spelled colour. A table carries it on a field, `name: Aliased<string, {
//! normalize? }>`, whose column holds the name; its aliases are a sub-table
//! of the row, and every name a row holds, as its name or an alias, is a
//! row of the use's shared table `names`, keyed by its normalized form.
//!
//! Aliased is held to D103's test of a behavior and passes it. Its state is
//! its own: each row's name and aliases, and which row holds each name.
//! Its rules say how that state changes: a create gives the name, which no
//! other row may hold; a rename keeps the old name as an alias; an alias is
//! added and removed; a delete frees the row's names; and no two rows hold
//! one name, as a name or an alias, in its normalized form. It recurs: a
//! company's former names, a tag's spellings, a place's other names, a
//! product's codes. It is no `@unique` field: a uniqueness across a column
//! and a sub-table together, with the old names kept by a rename, is rules
//! over state no index keeps.
//!
//! The config's `normalize` says how two names read as one: `lower`, the
//! default, drops the white space at a name's ends, makes each run of it
//! within one space and lowers its case (Unicode's lowercase mapping);
//! `slug` keeps only its letters and digits (Unicode's alphanumerics), in
//! lower case, each run of anything else between them one hyphen. A name
//! whose normalized form is empty is refused, and so is one longer than 256
//! characters.
//!
//! The names are the use's shared table `names` (D103's step 4), kept for
//! the table as a whole: one row per normalized form, holding the key of
//! the row that holds it (`holder`). A create, a rename or an addAlias
//! reads the row of its name's normalized form and inserts it when there is
//! none, so two writers that take one name at once never both hold it: the
//! second's insert finds the row inserted and the host runs it again from
//! fresh state, when it finds the name taken, on Postgres, SQLite and D1
//! alike, with no lock. A name another row holds is refused with that row's
//! key, the veto `name_taken`; one whose holder is no live row (a row
//! deleted past the core) is taken over, held to the version it was read
//! at. A removed alias deletes its row, and a delete through the core
//! deletes the row's (D147's second primitive, [`Cx::delete_shared`]), each
//! held to the version it was read at.
//!
//! A create gives the name: the field anchor's member of a create is the
//! use's value (D147's first primitive, the declaration's `fromCreate`),
//! which the core sets before validation, and an update names it as no
//! other owned field: only `rename` changes it. `rename` of a name the row
//! holds as an alias makes it the name, the old name an alias in its place;
//! one only the name's case or spacing changes (the same normalized form)
//! writes the name and keeps no alias; and the name the row has already is
//! answered unchanged. `addAlias` of a name the row holds, as its name or
//! an alias, and `removeAlias` of one it does not, are answered unchanged;
//! `removeAlias` of its name is refused, the veto `is_name`. Each is
//! answered before any other use's guard is asked (D104's settle), so no
//! gate refuses a call that changes nothing, and a name taken is refused
//! there too. `resolve`, an operation of the table that only reads, answers
//! the row whose name or alias a name names, or none.
//!
//! A merge of a table with an Aliased field (D147's `mergeInto`) moves the
//! row merged away's aliases to the row that stays, adds its name as one of
//! the row's aliases and repoints its rows of `names`, statements of
//! `sqlutil.MergePlan` that no command of the core runs; the delete of the
//! row merged away then finds none of its names held by it.
//!
//! configChange: Aliased cannot be added to a table with rows, which would
//! hold no names, nor removed from one; while the table has rows the
//! normalize stays, since its names are keyed by it. With none, it may
//! change. The core writes no row at a publish (D103): a removed use's
//! tables go in the migration that drops them.
//!
//! A write around the core (raw SQL of the name, the aliases or the names)
//! runs none of these rules: nothing refuses it, and the docs say why not
//! to.

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Hook, Refusal,
    SchemaIssue, SharedRow, ValidationIssue, ValidationRequest, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Aliased";

/// Aliased's declaration, the copy `make behaviors` writes for the
/// package `@superschematic/rules`, since no engine implements it.
pub const DECLARATION: &str = include_str!("../declarations/Aliased.behavior.json");

/// The value field: the row's name.
pub const NAME_FIELD: &str = "name";

/// The sub-table of a row's aliases, keyed by their normalized form.
pub const ALIASES: &str = "aliases";

/// The shared table of the names the rows hold, keyed by their normalized
/// form.
pub const NAMES: &str = "names";

/// The field of a row of `names` that holds the key of the row holding it.
pub const HOLDER: &str = "holder";

/// The field of a row of `aliases` that holds the alias as given.
pub const ALIAS: &str = "alias";

pub const RENAME: &str = "rename";
pub const ADD_ALIAS: &str = "addAlias";
pub const REMOVE_ALIAS: &str = "removeAlias";
pub const RESOLVE: &str = "resolve";

/// The veto of a name another row holds.
pub const NAME_TAKEN: &str = "name_taken";

/// The veto of a removeAlias of the row's name.
pub const IS_NAME: &str = "is_name";

/// The longest name, in characters.
pub const MAX_NAME_LENGTH: usize = 256;

/// How two names read as one.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Normalize {
    /// Case and runs of white space ignored.
    #[default]
    Lower,
    /// Only letters and digits, in lower case.
    Slug,
}

impl Normalize {
    /// The config's word for it.
    pub fn as_str(self) -> &'static str {
        match self {
            Normalize::Lower => "lower",
            Normalize::Slug => "slug",
        }
    }

    /// A name's normalized form, the key it is unique by.
    pub fn apply(self, name: &str) -> String {
        match self {
            Normalize::Lower => name
                .split_whitespace()
                .collect::<Vec<_>>()
                .join(" ")
                .to_lowercase(),
            Normalize::Slug => {
                let mut out = String::new();
                let mut gap = false;
                for c in name.chars().flat_map(char::to_lowercase) {
                    if c.is_alphanumeric() {
                        if gap && !out.is_empty() {
                            out.push('-');
                        }
                        gap = false;
                        out.push(c);
                    } else {
                        gap = true;
                    }
                }
                out
            }
        }
    }
}

/// Aliased's config, parsed.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct AliasedConfig {
    pub normalize: Normalize,
}

impl AliasedConfig {
    /// Parses and checks a config as a type gives it, as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words.
    pub fn parse(raw: &Value) -> Result<AliasedConfig, ConfigError> {
        let mut issues = Issues::default();
        let Some(config) = raw.as_object() else {
            issues.push("", "type", "must be object");
            return Err(ConfigError::Schema(issues.into_issues()));
        };
        for key in config.keys().filter(|key| key.as_str() != "normalize") {
            issues.push(
                "",
                "additionalProperties",
                format!("unknown key {}", schema::quoted(key)),
            );
        }
        let normalize = match config.get("normalize") {
            None => Normalize::Lower,
            Some(Value::String(word)) if word == "lower" => Normalize::Lower,
            Some(Value::String(word)) if word == "slug" => Normalize::Slug,
            Some(_) => {
                issues.push("/normalize", "enum", "must be one of \"lower\", \"slug\"");
                Normalize::Lower
            }
        };
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        Ok(AliasedConfig { normalize })
    }
}

/// Aliased: a name with aliases, unique across the table's names and
/// aliases together.
#[derive(Clone, Copy, Debug, Default)]
pub struct Aliased;

impl Behavior for Aliased {
    type Config = AliasedConfig;

    // The declaration file names the value field, name, which a create
    // gives (fromCreate), the sub-table aliases and the shared table names.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<AliasedConfig, ConfigError> {
        AliasedConfig::parse(raw)
    }

    // See the module's header: the names are keyed by the normalize, which
    // stays while the table has rows.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&AliasedConfig>,
        after: Option<&AliasedConfig>,
    ) -> Hook<Option<String>> {
        match (before, after) {
            (None, _) => Ok(Some(
                "the instances would hold no names, which a create gives".to_owned(),
            )),
            (_, None) => Ok(Some(
                "the instances would lose their names and aliases, which Aliased added again would not find".to_owned(),
            )),
            (Some(before), Some(after)) if before == after || !cx.has_rows()? => Ok(None),
            (Some(before), Some(after)) => Ok(Some(format!(
                "the instances' names are kept unique by their normalized form, which stays while there are instances; the new config changes normalize from {} to {}",
                before.normalize.as_str(),
                after.normalize.as_str()
            ))),
        }
    }

    fn check_params(
        &self,
        _config: &AliasedConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // A create gives the name, a string whose normalized form is not empty.
    fn validate(
        &self,
        view: &View<'_, AliasedConfig>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        if !matches!(request, ValidationRequest::Create { .. }) {
            return Ok(Vec::new());
        }
        let field = view.field().unwrap_or(NAME_FIELD);
        let issue = match view.get(NAME_FIELD) {
            None => Some((
                "required",
                format!(
                    "{field} is required: Aliased names each {}",
                    view.type_name()
                ),
            )),
            Some(Value::String(name)) => name_problem(view.config(), &name)
                .map(|problem| ("format", format!("{field}: {problem}"))),
            Some(_) => Some(("type", format!("{field} must be a string"))),
        };
        Ok(issue
            .map(|(rule, message)| vec![ValidationIssue::new(field, rule, message)])
            .unwrap_or_default())
    }

    // A create takes its name, which no other row may hold.
    fn initialize(&self, cx: &Cx<'_, AliasedConfig>, _params: &Value) -> Hook<()> {
        let name = name_of(cx);
        let key = own_key(cx, "create")?;
        let normalized = cx.config().normalize.apply(&name);
        claim(cx, &key, &name, &normalized)
    }

    // settle answers what changes nothing and refuses a name taken, before
    // any use's guard is asked (D104).
    fn settle(
        &self,
        view: &View<'_, AliasedConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        let normalize = view.config().normalize;
        match operation {
            RENAME => {
                let given = text_param(params, "name");
                if let Some(problem) = name_problem(view.config(), &given) {
                    return Err(bad_name(view, operation, "/name", &problem));
                }
                let current = name_of(view);
                if given == current {
                    return Ok(Some(json!({"name": current, "unchanged": true})));
                }
                let normalized = normalize.apply(&given);
                if normalized != normalize.apply(&current) && !holds_alias(view, &normalized)? {
                    refuse_taken(view, &given, &normalized)?;
                }
                Ok(None)
            }
            ADD_ALIAS => {
                let given = text_param(params, "alias");
                if let Some(problem) = name_problem(view.config(), &given) {
                    return Err(bad_name(view, operation, "/alias", &problem));
                }
                let normalized = normalize.apply(&given);
                if normalized == normalize.apply(&name_of(view)) || holds_alias(view, &normalized)?
                {
                    return Ok(Some(json!({"alias": given, "unchanged": true})));
                }
                refuse_taken(view, &given, &normalized)?;
                Ok(None)
            }
            REMOVE_ALIAS => {
                let given = text_param(params, "alias");
                let normalized = normalize.apply(&given);
                let name = name_of(view);
                if normalized == normalize.apply(&name) {
                    let mut details = Map::new();
                    details.insert("name".into(), Value::from(name.as_str()));
                    return Err(view.veto(
                        Veto::new(format!(
                            "\"{given}\" is the name of {} {}, not one of its aliases: a rename gives it another name first",
                            view.type_name(),
                            view.key().unwrap_or_default()
                        ))
                        .with_code(IS_NAME)
                        .with_details(details),
                    ));
                }
                if !holds_alias(view, &normalized)? {
                    return Ok(Some(json!({"alias": given, "unchanged": true})));
                }
                Ok(None)
            }
            _ => Ok(None),
        }
    }

    fn operation(&self, cx: &Cx<'_, AliasedConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            RENAME => rename(cx, &text_param(params, "name")),
            ADD_ALIAS => add_alias(cx, &text_param(params, "alias")),
            REMOVE_ALIAS => remove_alias(cx, &text_param(params, "alias")),
            RESOLVE => resolve(cx, &text_param(params, "name")),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // A delete frees the row's names: each row of names it holds, its name
    // and each alias, held to the version it was read at. A row of a merge
    // holds none of them by then (the merge repointed them).
    fn after_change(&self, cx: &Cx<'_, AliasedConfig>, change: &Change<'_>) -> Hook<()> {
        if !matches!(change, Change::Delete) {
            return Ok(());
        }
        let Some(key) = cx.key().map(str::to_owned) else {
            return Ok(());
        };
        let mut held = vec![cx.config().normalize.apply(&name_of(cx))];
        held.extend(cx.sub_rows(ALIASES)?.into_iter().map(|row| row.key));
        for normalized in held {
            if let Some(row) = cx.shared_row(NAMES, &normalized)? {
                if holder_of(&row).as_deref() == Some(key.as_str()) {
                    cx.delete_shared(NAMES, &normalized, row.version)?;
                }
            }
        }
        Ok(())
    }
}

// rename gives the row a new name, keeping the old one as an alias, as the
// module's header says. settle answered the name the row has and refused
// one another row holds.
fn rename(cx: &Cx<'_, AliasedConfig>, given: &str) -> Hook<Value> {
    let normalize = cx.config().normalize;
    let key = own_key(cx, RENAME)?;
    let current = name_of(cx);
    let normalized = normalize.apply(given);
    let previous = normalize.apply(&current);
    cx.set(NAME_FIELD, given)?;
    if normalized == previous {
        // Only its case or spacing changes: the name keeps its row of
        // names, and no alias is kept.
        return Ok(json!({"name": given}));
    }
    if holds_alias(cx, &normalized)? {
        // An alias becomes the name: its row of names is the row's already.
        cx.delete_row(ALIASES, &normalized)?;
    } else {
        claim(cx, &key, given, &normalized)?;
    }
    let mut values = Map::new();
    values.insert(ALIAS.into(), Value::from(current.as_str()));
    cx.insert_row(ALIASES, &previous, values)?;
    Ok(json!({"name": given, "alias": current}))
}

// add_alias adds an alias the row is also known by; settle answered one it
// holds and refused one another row holds.
fn add_alias(cx: &Cx<'_, AliasedConfig>, given: &str) -> Hook<Value> {
    let key = own_key(cx, ADD_ALIAS)?;
    let normalized = cx.config().normalize.apply(given);
    claim(cx, &key, given, &normalized)?;
    let mut values = Map::new();
    values.insert(ALIAS.into(), Value::from(given));
    cx.insert_row(ALIASES, &normalized, values)?;
    Ok(json!({"alias": given}))
}

// remove_alias removes one of the row's aliases and frees its name; settle
// answered one it does not hold and refused its name.
fn remove_alias(cx: &Cx<'_, AliasedConfig>, given: &str) -> Hook<Value> {
    let key = own_key(cx, REMOVE_ALIAS)?;
    let normalized = cx.config().normalize.apply(given);
    let alias = cx
        .sub_rows(ALIASES)?
        .into_iter()
        .find(|row| row.key == normalized)
        .and_then(|row| {
            row.values
                .get(ALIAS)
                .and_then(Value::as_str)
                .map(str::to_owned)
        })
        .unwrap_or_else(|| given.to_owned());
    cx.delete_row(ALIASES, &normalized)?;
    if let Some(row) = cx.shared_row(NAMES, &normalized)? {
        if holder_of(&row).as_deref() == Some(key.as_str()) {
            cx.delete_shared(NAMES, &normalized, row.version)?;
        }
    }
    Ok(json!({"alias": alias}))
}

// resolve answers the row whose name or alias a name names: its key, its
// name and whether the name found is an alias; none when no live row holds
// it.
fn resolve(cx: &Cx<'_, AliasedConfig>, given: &str) -> Hook<Value> {
    let normalize = cx.config().normalize;
    let normalized = normalize.apply(given);
    if normalized.is_empty() {
        return Ok(json!({"row": null}));
    }
    let Some(held) = cx.shared_row(NAMES, &normalized)? else {
        return Ok(json!({"row": null}));
    };
    let Some(holder) = holder_of(&held) else {
        return Ok(json!({"row": null}));
    };
    let Some(row) = cx.read_row(cx.type_name(), &holder)? else {
        return Ok(json!({"row": null}));
    };
    let name = row
        .owned(cx.use_id(), NAME_FIELD)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let alias = normalize.apply(&name) != normalized;
    Ok(json!({"row": {"key": holder, "name": name, "alias": alias}}))
}

// claim takes a name for the row of key: it inserts the name's row of
// names, or takes over one whose holder is no live row, held to the
// version it read; a name another live row holds is refused.
fn claim(cx: &Cx<'_, AliasedConfig>, key: &str, given: &str, normalized: &str) -> Hook<()> {
    let mut values = Map::new();
    values.insert(HOLDER.into(), Value::from(key));
    match cx.shared_row(NAMES, normalized)? {
        None => cx.insert_shared(NAMES, normalized, values),
        Some(row) => match holder_of(&row) {
            Some(holder) if holder == key => Ok(()),
            Some(holder) if cx.read_row(cx.type_name(), &holder)?.is_some() => {
                Err(taken(cx, given, normalized, &holder))
            }
            _ => cx.update_shared(NAMES, normalized, row.version, values),
        },
    }
}

// refuse_taken refuses a name another live row holds, from settle.
fn refuse_taken(view: &View<'_, AliasedConfig>, given: &str, normalized: &str) -> Hook<()> {
    let Some(row) = view.shared_row(NAMES, normalized)? else {
        return Ok(());
    };
    let Some(holder) = holder_of(&row) else {
        return Ok(());
    };
    if Some(holder.as_str()) == view.key() || view.read_row(view.type_name(), &holder)?.is_none() {
        return Ok(());
    }
    Err(taken(view, given, normalized, &holder))
}

// taken is the veto name_taken of a name another row holds, its key in its
// details.
fn taken(
    view: &View<'_, AliasedConfig>,
    given: &str,
    normalized: &str,
    holder: &str,
) -> superschematic_rules::Stop {
    let mut details = Map::new();
    details.insert("name".into(), Value::from(given));
    details.insert("normalized".into(), Value::from(normalized));
    details.insert(HOLDER.into(), Value::from(holder));
    view.veto(
        Veto::new(format!(
            "\"{given}\" is held by {} {holder}, as its name or an alias: a name is one row's",
            view.type_name()
        ))
        .with_code(NAME_TAKEN)
        .with_details(details),
    )
}

// holds_alias reports whether the row holds an alias of a normalized form.
fn holds_alias(view: &View<'_, AliasedConfig>, normalized: &str) -> Hook<bool> {
    Ok(view
        .sub_rows(ALIASES)?
        .iter()
        .any(|row| row.key == normalized))
}

// name_of is the row's name, as the run has it; empty before it has one.
fn name_of(view: &View<'_, AliasedConfig>) -> String {
    view.get(NAME_FIELD)
        .and_then(|name| name.as_str().map(str::to_owned))
        .unwrap_or_default()
}

// holder_of is the key a row of names holds.
fn holder_of(row: &SharedRow) -> Option<String> {
    match row.value(HOLDER)? {
        Value::String(text) => Some(text.clone()),
        Value::Number(number) => Some(number.to_string()),
        _ => None,
    }
}

// own_key is the key of the row the use runs on, which its names record: a
// create that names none (a host that keys rows itself) has none to record.
fn own_key(view: &View<'_, AliasedConfig>, action: &str) -> Hook<String> {
    view.key().map(str::to_owned).ok_or_else(|| {
        Refusal::invalid_argument(format!(
            "{action} of {}: the command names no key, which Aliased records with each name the row holds; the host gives a create its key",
            view.type_name()
        ))
        .into()
    })
}

// name_problem is why a name is refused: longer than MAX_NAME_LENGTH
// characters, or one whose normalized form is empty.
fn name_problem(config: &AliasedConfig, name: &str) -> Option<String> {
    if schema::length(name) > MAX_NAME_LENGTH {
        return Some(format!("a name holds at most {MAX_NAME_LENGTH} characters"));
    }
    if config.normalize.apply(name).is_empty() {
        return Some(format!(
            "\"{name}\" normalizes ({}) to nothing, so it names nothing",
            config.normalize.as_str()
        ));
    }
    None
}

// bad_name refuses an operation's name parameter that names nothing.
fn bad_name(
    view: &View<'_, AliasedConfig>,
    operation: &str,
    at: &str,
    problem: &str,
) -> superschematic_rules::Stop {
    Refusal::operation_params(
        NAME,
        view.use_id(),
        operation,
        vec![SchemaIssue::new(at, problem)],
    )
    .into()
}

// text_param is an operation's string parameter, which check_params held.
fn text_param(params: &Value, name: &str) -> String {
    params
        .get(name)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words: rename and resolve name
// a name, addAlias and removeAlias an alias, each a string of 1 to 256
// characters.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let key = match operation {
        RENAME | RESOLVE => "name",
        ADD_ALIAS | REMOVE_ALIAS => "alias",
        _ => return issues,
    };
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for other in given.keys().filter(|other| other.as_str() != key) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {other}"),
        );
    }
    match given.get(key) {
        None => issues.push(
            "",
            "required",
            format!("must have required property '{key}'"),
        ),
        Some(value) => {
            let at = schema::member("", key);
            schema::text_issues(&at, value, &mut issues);
            if value
                .as_str()
                .is_some_and(|text| schema::length(text) > MAX_NAME_LENGTH)
            {
                issues.push(
                    at,
                    "maxLength",
                    format!("must NOT have more than {MAX_NAME_LENGTH} characters"),
                );
            }
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lower_ignores_case_and_runs_of_white_space() {
        let lower = Normalize::Lower;
        assert_eq!(lower.apply("  Acme   Group "), "acme group");
        assert_eq!(lower.apply("ÉCOLE\tNormale"), "école normale");
        assert_eq!(lower.apply("Acme, Inc."), "acme, inc.");
        assert_eq!(lower.apply(" \n "), "");
    }

    #[test]
    fn slug_keeps_letters_and_digits_each_gap_one_hyphen() {
        let slug = Normalize::Slug;
        assert_eq!(slug.apply("Acme, Inc."), "acme-inc");
        assert_eq!(slug.apply("  --Acme__Group 2-- "), "acme-group-2");
        assert_eq!(slug.apply("Café Noir"), "café-noir");
        assert_eq!(slug.apply("!!!"), "");
    }

    #[test]
    fn a_config_takes_lower_or_slug() {
        assert_eq!(
            AliasedConfig::parse(&json!({})).unwrap().normalize,
            Normalize::Lower
        );
        assert_eq!(
            AliasedConfig::parse(&json!({"normalize": "slug"}))
                .unwrap()
                .normalize,
            Normalize::Slug
        );
        assert!(AliasedConfig::parse(&json!({"normalize": "upper"})).is_err());
        assert!(AliasedConfig::parse(&json!({"case": "lower"})).is_err());
        assert!(AliasedConfig::parse(&json!("lower")).is_err());
    }
}
