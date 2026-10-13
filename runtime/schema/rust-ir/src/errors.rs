//! The errors the IR runtime reports, keyed by path in the order the
//! TypeScript runtime's error object holds them.
//!
//! The TypeScript runtime builds a plain object, so a reader that lists
//! the errors (the engine's issue lists) sees them in the object's own
//! order: keys that are array indexes first, ascending, then every other
//! key in the order it was first set. [`FieldErrors`] keeps that order;
//! [`FieldErrors::to_validation_errors`] gives the sorted
//! [`ValidationErrors`] the generated validators return.

use serde::ser::{Serialize, SerializeMap, Serializer};

use superschematic_schema_runtime::{ValidationError, ValidationErrors};

/// What a path of [`FieldErrors`] holds: the errors of the value there, or
/// the errors of the object there, keyed by its own fields.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FieldEntry {
    Errors(Vec<ValidationError>),
    Nested(FieldErrors),
}

impl Serialize for FieldEntry {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        match self {
            FieldEntry::Errors(errors) => errors.serialize(serializer),
            FieldEntry::Nested(nested) => nested.serialize(serializer),
        }
    }
}

/// The errors of one validated or parsed value, keyed by path (`field`,
/// `field[i]`, `field[i][j]`), each holding its errors or, for a nested
/// object, that object's errors. Its setters are superscalar's TypeScript
/// `addFieldError`, `setFieldErrors` and `addNestedErrors`.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct FieldErrors {
    entries: Vec<(String, FieldEntry)>,
}

impl FieldErrors {
    pub fn new() -> Self {
        Self::default()
    }

    fn position(&self, path: &str) -> Option<usize> {
        self.entries.iter().position(|(key, _)| key == path)
    }

    fn put(&mut self, path: String, entry: FieldEntry) {
        match self.position(&path) {
            Some(index) => self.entries[index].1 = entry,
            None => self.entries.push((path, entry)),
        }
    }

    /// Appends an error at path. A path that holds a nested object's errors
    /// keeps them and takes no error of its own.
    pub fn add(
        &mut self,
        path: impl Into<String>,
        validator: impl Into<String>,
        message: impl Into<String>,
    ) {
        let path = path.into();
        let error = ValidationError::new(validator, message);
        match self.position(&path) {
            Some(index) => {
                if let FieldEntry::Errors(errors) = &mut self.entries[index].1 {
                    errors.push(error);
                }
            }
            None => self.entries.push((path, FieldEntry::Errors(vec![error]))),
        }
    }

    /// Replaces what path holds with errors. An empty list changes nothing.
    pub fn set(&mut self, path: impl Into<String>, errors: Vec<ValidationError>) {
        if !errors.is_empty() {
            self.put(path.into(), FieldEntry::Errors(errors));
        }
    }

    /// Replaces what path holds with the errors of the object there. An
    /// empty nested map changes nothing, so a valid nested object adds no
    /// key.
    pub fn nest(&mut self, path: impl Into<String>, nested: FieldErrors) {
        if !nested.is_empty() {
            self.put(path.into(), FieldEntry::Nested(nested));
        }
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn get(&self, path: &str) -> Option<&FieldEntry> {
        self.position(path).map(|index| &self.entries[index].1)
    }

    /// The errors at path, when it holds errors rather than nested ones.
    pub fn errors_at(&self, path: &str) -> Option<&[ValidationError]> {
        match self.get(path) {
            Some(FieldEntry::Errors(errors)) => Some(errors),
            _ => None,
        }
    }

    /// The paths and what each holds, in the order the TypeScript object
    /// iterates them: array-index keys ascending, then the rest as first
    /// set.
    pub fn iter(&self) -> impl Iterator<Item = (&str, &FieldEntry)> {
        let mut indexes: Vec<(u32, usize)> = Vec::new();
        let mut rest: Vec<usize> = Vec::new();
        for (position, (key, _)) in self.entries.iter().enumerate() {
            match array_index(key) {
                Some(index) => indexes.push((index, position)),
                None => rest.push(position),
            }
        }
        indexes.sort_unstable();
        indexes
            .into_iter()
            .map(|(_, position)| position)
            .chain(rest)
            .map(move |position| {
                let (key, entry) = &self.entries[position];
                (key.as_str(), entry)
            })
    }

    /// Every error under one path each, in [`FieldErrors::iter`] order: a
    /// nested object's paths are joined to the field that holds it with a
    /// dot (`items[0].name`), as the cross-language parity corpus spells
    /// them.
    pub fn flatten(&self) -> Vec<(String, Vec<ValidationError>)> {
        let mut out = Vec::new();
        self.flatten_into("", &mut out);
        out
    }

    fn flatten_into(&self, prefix: &str, out: &mut Vec<(String, Vec<ValidationError>)>) {
        for (key, entry) in self.iter() {
            let path = if prefix.is_empty() {
                key.to_owned()
            } else {
                format!("{prefix}.{key}")
            };
            match entry {
                FieldEntry::Errors(errors) => out.push((path, errors.clone())),
                FieldEntry::Nested(nested) => nested.flatten_into(&path, out),
            }
        }
    }

    /// The same errors as the sorted [`ValidationErrors`] map.
    pub fn to_validation_errors(&self) -> ValidationErrors {
        let mut out = ValidationErrors::new();
        for (key, entry) in &self.entries {
            match entry {
                FieldEntry::Errors(errors) => out.set(key.clone(), errors.clone()),
                FieldEntry::Nested(nested) => out.nest(key.clone(), nested.to_validation_errors()),
            }
        }
        out
    }

    /// Each list of errors appended under its path, and each nested map
    /// set at its, over self: superscalar's TypeScript
    /// `mergeValidationErrors`.
    pub fn merge(&mut self, other: &FieldErrors) {
        for (key, entry) in &other.entries {
            match (entry, self.position(key)) {
                (FieldEntry::Errors(errors), Some(index)) => {
                    if let FieldEntry::Errors(have) = &mut self.entries[index].1 {
                        have.extend(errors.iter().cloned());
                    }
                }
                _ => self.put(key.clone(), entry.clone()),
            }
        }
    }
}

/// The index a key names when it is an array index to JavaScript: the
/// canonical decimal text of an integer below 2^32 - 1.
fn array_index(key: &str) -> Option<u32> {
    if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    if key.len() > 1 && key.starts_with('0') {
        return None;
    }
    key.parse::<u32>().ok().filter(|index| *index != u32::MAX)
}

/// An object whose keys are in [`FieldErrors::iter`] order, each holding a
/// list of `{validator, message}` or an object of the same shape: what
/// `JSON.stringify` writes for the TypeScript runtime's errors.
impl Serialize for FieldErrors {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(self.entries.len()))?;
        for (key, entry) in self.iter() {
            map.serialize_entry(key, entry)?;
        }
        map.end()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_the_order_errors_are_first_set() {
        let mut errors = FieldErrors::new();
        errors.add("zeta", "required", "zeta is required.");
        errors.add("alpha", "type", "expected an array");
        errors.add("zeta", "type", "again");
        let keys: Vec<&str> = errors.iter().map(|(key, _)| key).collect();
        assert_eq!(keys, ["zeta", "alpha"]);
        assert_eq!(errors.errors_at("zeta").map(<[_]>::len), Some(2));
        errors.set("alpha", vec![ValidationError::new("enum", "replaced")]);
        let keys: Vec<&str> = errors.iter().map(|(key, _)| key).collect();
        assert_eq!(keys, ["zeta", "alpha"]);
    }

    #[test]
    fn array_index_keys_come_first_as_javascript_orders_them() {
        let mut errors = FieldErrors::new();
        errors.add("b", "type", "x");
        errors.add("10", "type", "x");
        errors.add("2", "type", "x");
        errors.add("01", "type", "x");
        let keys: Vec<&str> = errors.iter().map(|(key, _)| key).collect();
        assert_eq!(keys, ["2", "10", "b", "01"]);
    }

    #[test]
    fn nested_errors_flatten_with_a_dot_and_stay_unmixed() {
        let mut point = FieldErrors::new();
        point.add("shade", "enum", "Shade must be one of the allowed values.");
        let mut errors = FieldErrors::new();
        errors.nest("point", point);
        errors.add("point", "type", "ignored");
        errors.nest("empty", FieldErrors::new());
        errors.set("none", Vec::new());
        assert_eq!(errors.len(), 1);
        let flat = errors.flatten();
        assert_eq!(flat[0].0, "point.shade");
        let sorted = errors.to_validation_errors();
        assert_eq!(sorted.flatten()["point.shade"][0].validator, "enum");
        assert_eq!(
            serde_json::to_string(&errors).unwrap(),
            r#"{"point":{"shade":[{"validator":"enum","message":"Shade must be one of the allowed values."}]}}"#
        );
    }

    #[test]
    fn merge_appends_lists_and_sets_nested_maps() {
        let mut first = FieldErrors::new();
        first.add("a", "type", "one");
        let mut second = FieldErrors::new();
        second.add("a", "required", "two");
        second.add("b", "type", "three");
        first.merge(&second);
        assert_eq!(first.errors_at("a").map(<[_]>::len), Some(2));
        assert_eq!(first.errors_at("b").map(<[_]>::len), Some(1));
    }
}
