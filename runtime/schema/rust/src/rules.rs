//! A field's own `@validate` rules, checked after the field's type checks.
//! A string rule checks a string, a range rule a finite number; a value of
//! another JSON type is the type check's to report, so it passes them.

use serde_json::Value;

use crate::errors::ValidationErrors;
use crate::json::finite_number;
use crate::pattern::Pattern;
use crate::text::code_points;

/// One rule with the message its failure reports. Generated validators hold
/// a field's rules in a `static` slice.
#[derive(Debug, Clone, Copy)]
pub enum Rule {
    MinLength(usize, &'static str),
    MaxLength(usize, &'static str),
    Pattern(&'static Pattern, &'static str),
    Min(f64, &'static str),
    Max(f64, &'static str),
    ListMin(usize, &'static str),
    ListMax(usize, &'static str),
}

/// Checks the list-size rules (`listMin`, `listMax`) against a list, or a
/// map, of len entries at path. On a `T[][]` field they bound the outer
/// list. Every other rule is skipped.
pub fn check_list_rules(errors: &mut ValidationErrors, path: &str, len: usize, rules: &[Rule]) {
    for rule in rules {
        match *rule {
            Rule::ListMin(min, message) if len < min => errors.add(path, "listMin", message),
            Rule::ListMax(max, message) if len > max => errors.add(path, "listMax", message),
            _ => {}
        }
    }
}

/// Checks the value rules (`minLength`, `maxLength`, `pattern`, `min`,
/// `max`) on one value at path, in the order the schema declares them. The
/// list-size rules are skipped.
pub fn check_value_rules(errors: &mut ValidationErrors, path: &str, value: &Value, rules: &[Rule]) {
    for rule in rules {
        match *rule {
            Rule::MinLength(min, message) => {
                if value.as_str().is_some_and(|text| code_points(text) < min) {
                    errors.add(path, "minLength", message);
                }
            }
            Rule::MaxLength(max, message) => {
                if value.as_str().is_some_and(|text| code_points(text) > max) {
                    errors.add(path, "maxLength", message);
                }
            }
            Rule::Pattern(pattern, message) => {
                if value.as_str().is_some_and(|text| !pattern.is_match(text)) {
                    errors.add(path, "pattern", message);
                }
            }
            Rule::Min(min, message) => {
                if number(value).is_some_and(|number| number < min) {
                    errors.add(path, "min", message);
                }
            }
            Rule::Max(max, message) => {
                if number(value).is_some_and(|number| number > max) {
                    errors.add(path, "max", message);
                }
            }
            Rule::ListMin(..) | Rule::ListMax(..) => {}
        }
    }
}

fn number(value: &Value) -> Option<f64> {
    value.as_number().and_then(finite_number)
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    static LOWER: Pattern = Pattern::new("^[a-z]+$");
    static RULES: &[Rule] = &[
        Rule::MaxLength(3, "must be at most 3 characters"),
        Rule::MinLength(2, "must be at least 2 characters"),
        Rule::Pattern(&LOWER, "invalid format"),
        Rule::Min(1.0, "must be at least 1"),
        Rule::Max(10.0, "must be at most 10"),
        Rule::ListMin(1, "must contain at least 1 items"),
        Rule::ListMax(2, "must contain at most 2 items"),
    ];

    fn validators(errors: &ValidationErrors, path: &str) -> Vec<String> {
        errors
            .errors_at(path)
            .map(|errs| errs.iter().map(|e| e.validator.clone()).collect())
            .unwrap_or_default()
    }

    #[test]
    fn value_rules_check_their_own_json_type() {
        let mut errors = ValidationErrors::new();
        check_value_rules(&mut errors, "a", &json!("ABCD"), RULES);
        check_value_rules(&mut errors, "b", &json!("é"), RULES);
        check_value_rules(&mut errors, "c", &json!(11), RULES);
        check_value_rules(&mut errors, "d", &json!(0.5), RULES);
        check_value_rules(&mut errors, "e", &json!(true), RULES);
        check_value_rules(&mut errors, "f", &json!("ab"), RULES);
        assert_eq!(validators(&errors, "a"), ["maxLength", "pattern"]);
        assert_eq!(validators(&errors, "b"), ["minLength", "pattern"]);
        assert_eq!(validators(&errors, "c"), ["max"]);
        assert_eq!(validators(&errors, "d"), ["min"]);
        assert!(errors.get("e").is_none());
        assert!(errors.get("f").is_none());
    }

    #[test]
    fn list_rules_bound_the_length() {
        let mut errors = ValidationErrors::new();
        check_list_rules(&mut errors, "none", 0, RULES);
        check_list_rules(&mut errors, "ok", 2, RULES);
        check_list_rules(&mut errors, "many", 3, RULES);
        assert_eq!(validators(&errors, "none"), ["listMin"]);
        assert!(errors.get("ok").is_none());
        assert_eq!(validators(&errors, "many"), ["listMax"]);
    }
}
