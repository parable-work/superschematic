use std::collections::BTreeMap;
use std::fmt;

use serde::ser::{Serialize, SerializeMap, SerializeStruct, Serializer};

/// The message of a `required` error: a missing or null value where the
/// schema requires one.
pub const REQUIRED_MESSAGE: &str = "required field";

/// The verdict on one value, as a scalar or enum validator returns it: the
/// errors to set at the value's path, or none.
pub type ScalarResult = Result<(), Vec<ValidationError>>;

/// One broken rule: the validator's name (`required`, `type`, `minLength`,
/// `pattern`, ...) and a message for a reader.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ValidationError {
    pub validator: String,
    pub message: String,
}

impl ValidationError {
    pub fn new(validator: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            validator: validator.into(),
            message: message.into(),
        }
    }

    /// A `required` error with its standard message.
    pub fn required() -> Self {
        Self::new("required", REQUIRED_MESSAGE)
    }
}

impl Serialize for ValidationError {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut state = serializer.serialize_struct("ValidationError", 2)?;
        state.serialize_field("validator", &self.validator)?;
        state.serialize_field("message", &self.message)?;
        state.end()
    }
}

/// What a path of [`ValidationErrors`] holds: the errors of the value at
/// that path, or the errors of the object there, keyed by its own fields.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Entry {
    Errors(Vec<ValidationError>),
    Nested(ValidationErrors),
}

impl Serialize for Entry {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        match self {
            Entry::Errors(errors) => errors.serialize(serializer),
            Entry::Nested(nested) => nested.serialize(serializer),
        }
    }
}

/// The errors of one validated value, keyed by path. It serializes as
/// superscalar's Go `ValidationErrors` marshals: an object whose keys are
/// sorted, each holding a list of `{validator, message}` or, for a nested
/// object, an object of the same shape. The serializer is written by hand,
/// so the shape does not change when serde_json's `arbitrary_precision` or
/// `preserve_order` feature is on.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ValidationErrors(BTreeMap<String, Entry>);

impl ValidationErrors {
    pub fn new() -> Self {
        Self::default()
    }

    /// Appends an error at path. A path that holds a nested object's errors
    /// keeps them and takes no error of its own, as superscalar's
    /// TypeScript `addFieldError` leaves such a path.
    pub fn add(
        &mut self,
        path: impl Into<String>,
        validator: impl Into<String>,
        message: impl Into<String>,
    ) {
        self.push(path, ValidationError::new(validator, message));
    }

    /// Appends error at path, as [`ValidationErrors::add`] does.
    pub fn push(&mut self, path: impl Into<String>, error: ValidationError) {
        match self.0.entry(path.into()) {
            std::collections::btree_map::Entry::Occupied(mut slot) => {
                if let Entry::Errors(errors) = slot.get_mut() {
                    errors.push(error);
                }
            }
            std::collections::btree_map::Entry::Vacant(slot) => {
                slot.insert(Entry::Errors(vec![error]));
            }
        }
    }

    /// Replaces what path holds with errors. An empty list leaves the map
    /// unchanged, as superscalar's Go `SetFieldErrors` does.
    pub fn set(&mut self, path: impl Into<String>, errors: Vec<ValidationError>) {
        if !errors.is_empty() {
            self.0.insert(path.into(), Entry::Errors(errors));
        }
    }

    /// Sets the errors of a failed verdict at path, as
    /// [`ValidationErrors::set`] does; a passing verdict changes nothing.
    pub fn set_result(&mut self, path: impl Into<String>, result: ScalarResult) {
        if let Err(errors) = result {
            self.set(path, errors);
        }
    }

    /// Replaces what path holds with the errors of the object there. An
    /// empty nested map leaves the map unchanged, so a valid nested object
    /// adds no key.
    pub fn nest(&mut self, path: impl Into<String>, nested: ValidationErrors) {
        if !nested.is_empty() {
            self.0.insert(path.into(), Entry::Nested(nested));
        }
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }

    pub fn get(&self, path: &str) -> Option<&Entry> {
        self.0.get(path)
    }

    /// The errors at path, when it holds errors rather than nested ones.
    pub fn errors_at(&self, path: &str) -> Option<&[ValidationError]> {
        match self.0.get(path) {
            Some(Entry::Errors(errors)) => Some(errors),
            _ => None,
        }
    }

    pub fn iter(&self) -> impl Iterator<Item = (&String, &Entry)> {
        self.0.iter()
    }

    /// Every error under one path each: a nested object's paths are joined
    /// to the field that holds it with a dot (`items[0].name`), as the
    /// cross-language parity corpus spells them.
    pub fn flatten(&self) -> BTreeMap<String, Vec<ValidationError>> {
        let mut out = BTreeMap::new();
        self.flatten_into("", &mut out);
        out
    }

    fn flatten_into(&self, prefix: &str, out: &mut BTreeMap<String, Vec<ValidationError>>) {
        for (key, entry) in &self.0 {
            let path = if prefix.is_empty() {
                key.clone()
            } else {
                format!("{prefix}.{key}")
            };
            match entry {
                Entry::Errors(errors) => {
                    out.entry(path).or_default().extend(errors.iter().cloned());
                }
                Entry::Nested(nested) => nested.flatten_into(&path, out),
            }
        }
    }
}

impl Serialize for ValidationErrors {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(self.0.len()))?;
        for (key, entry) in &self.0 {
            map.serialize_entry(key, entry)?;
        }
        map.end()
    }
}

/// One `path: message` per error, in path order, joined with `; `.
impl fmt::Display for ValidationErrors {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut first = true;
        for (path, errors) in self.flatten() {
            for error in errors {
                if !first {
                    f.write_str("; ")?;
                }
                first = false;
                write!(f, "{path}: {}", error.message)?;
            }
        }
        Ok(())
    }
}

impl std::error::Error for ValidationErrors {}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> ValidationErrors {
        let mut item = ValidationErrors::new();
        item.add("name", "maxLength", "must be at most 3 characters");
        let mut errors = ValidationErrors::new();
        errors.add("title", "required", REQUIRED_MESSAGE);
        errors.add("tags[1]", "pattern", "invalid format");
        errors.nest("items[0]", item);
        errors
    }

    #[test]
    fn serializes_as_superscalar_go_marshals() {
        let json = serde_json::to_string(&sample()).unwrap();
        assert_eq!(
            json,
            concat!(
                r#"{"items[0]":{"name":[{"validator":"maxLength","message":"must be at most 3 characters"}]},"#,
                r#""tags[1]":[{"validator":"pattern","message":"invalid format"}],"#,
                r#""title":[{"validator":"required","message":"required field"}]}"#
            )
        );
    }

    #[test]
    fn flatten_joins_nested_paths_with_a_dot() {
        let flat = sample().flatten();
        let keys: Vec<&str> = flat.keys().map(String::as_str).collect();
        assert_eq!(keys, ["items[0].name", "tags[1]", "title"]);
        assert_eq!(flat["items[0].name"][0].validator, "maxLength");
    }

    #[test]
    fn add_appends_and_leaves_nested_errors_alone() {
        let mut errors = sample();
        errors.add("title", "type", "expected a string");
        assert_eq!(errors.errors_at("title").map(<[_]>::len), Some(2));
        errors.add("items[0]", "type", "expected an object");
        assert!(matches!(errors.get("items[0]"), Some(Entry::Nested(_))));
    }

    #[test]
    fn set_result_sets_a_failed_verdict_only() {
        let mut errors = ValidationErrors::new();
        errors.set_result("a", Ok(()));
        assert!(errors.is_empty());
        errors.add("b", "type", "expected a string");
        errors.set_result("b", Err(vec![ValidationError::required()]));
        assert_eq!(
            errors.errors_at("b"),
            Some(&[ValidationError::required()][..])
        );
    }

    #[test]
    fn empty_lists_and_maps_add_no_key() {
        let mut errors = ValidationErrors::new();
        errors.set("a", Vec::new());
        errors.nest("b", ValidationErrors::new());
        assert!(errors.is_empty());
    }

    #[test]
    fn display_lists_each_error_with_its_path() {
        assert_eq!(
            sample().to_string(),
            "items[0].name: must be at most 3 characters; tags[1]: invalid format; title: required field"
        );
    }
}
