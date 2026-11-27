use serde::{Serialize, Serializer};
use std::fmt;

/// What a secret prints as: in a log, a JSON form, a debug form. The
/// TypeScript runtime's `SECRET_MASK`.
pub const SECRET_MASK: &str = "[secret]";

/// A secret's value (D159): a provider's secret setting, a sandbox's run
/// token, a credential on its way to the runner. Its `Debug`, `Display`
/// and JSON forms are [`SECRET_MASK`], so a log line or a record that
/// holds one holds no value; [`SecretValue::reveal`] alone reads it.
#[derive(Clone, PartialEq, Eq)]
pub struct SecretValue(String);

impl SecretValue {
    /// The secret holding `value`.
    pub fn new(value: impl Into<String>) -> Self {
        Self(value.into())
    }

    /// The value, for the one call that uses it.
    pub fn reveal(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for SecretValue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(SECRET_MASK)
    }
}

impl fmt::Display for SecretValue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(SECRET_MASK)
    }
}

impl Serialize for SecretValue {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(SECRET_MASK)
    }
}
