//! The scalar catalog the store parses logins, display names and keys with,
//! and the codec of a key between its wire form and the database's.
//!
//! The model adds no case or shape rule of its own (D50): a login is the
//! login scalar's parse, looked up by equality. This crate does not link the
//! scalar crate, whose name and registry the naming file chooses
//! (`scalar_rust_crate`, `scalar_rust_registry`), so the service passes the
//! catalog as a [`Scalars`]. The generated crate's wiring implements it over
//! its scalar registry:
//!
//! ```ignore
//! struct Catalog;
//!
//! impl identity::Scalars for Catalog {
//!     fn parse(&self, scalar: &str, value: &str) -> Option<Result<String, String>> {
//!         let registry = superscalar::Registry::builtin();
//!         let found = registry.scalar(scalar)?;
//!         Some(found.parse(registry, value).map_err(|err| err.message))
//!     }
//! }
//! ```

use std::fmt;

/// The scalar catalog: each scalar's parse, its canonical form of a value.
pub trait Scalars: Send + Sync + 'static {
    /// The scalar's canonical form of `value`, or the scalar's reason for
    /// refusing it; `None` when the catalog has no scalar named `scalar`
    /// (a builtin type such as `string`, say), whose values the store takes
    /// as they are.
    fn parse(&self, scalar: &str, value: &str) -> Option<Result<String, String>>;

    /// Whether the scalar's SQL type is `UUID`, so its keys are hyphenated
    /// in the database and base62 on the wire: `Identity.UUID` and
    /// `Identity.UserID` in the catalog superscalar builds.
    fn is_uuid(&self, scalar: &str) -> bool {
        matches!(scalar, "Identity.UUID" | "Identity.UserID")
    }
}

impl fmt::Debug for dyn Scalars {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Scalars")
    }
}

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// A UUID's base62 form, as superscalar's `Identity.UUID` writes it: the
/// 128-bit value in base62, most significant digit first, with no padding
/// (`0` for the nil UUID).
pub fn uuid_to_base62(uuid: uuid::Uuid) -> String {
    let mut number = uuid.as_u128();
    if number == 0 {
        return "0".to_owned();
    }
    let mut digits = Vec::new();
    while number > 0 {
        let digit = usize::try_from(number % 62).unwrap_or(0);
        digits.push(BASE62[digit]);
        number /= 62;
    }
    digits.reverse();
    String::from_utf8(digits).unwrap_or_default()
}

/// Reads a UUID as superscalar's `Identity.UUID` does: the hyphenated form
/// (36 characters), base62 (1 to 22 characters), or any other form the
/// uuid crate reads.
pub fn parse_uuid(text: &str) -> Option<uuid::Uuid> {
    if text.len() == 36 {
        if let Ok(uuid) = uuid::Uuid::parse_str(text) {
            return Some(uuid);
        }
    }
    if (1..=22).contains(&text.len()) {
        let mut number: u128 = 0;
        for c in text.bytes() {
            let digit = BASE62.iter().position(|d| *d == c)?;
            number = number
                .checked_mul(62)?
                .checked_add(u128::try_from(digit).ok()?)?;
        }
        return Some(uuid::Uuid::from_u128(number));
    }
    uuid::Uuid::parse_str(text).ok()
}

/// Moves a key between its wire form and the database's. A key of a scalar
/// whose SQL type is `UUID` is base62 on the wire and hyphenated lowercase
/// in the database; any other is the same text in both, parsed by its
/// scalar on the way in when the catalog has it.
#[derive(Clone, Debug)]
pub(crate) struct KeyCodec {
    scalar: String,
    uuid: bool,
}

impl KeyCodec {
    pub(crate) fn new(scalar: &str, scalars: &dyn Scalars) -> KeyCodec {
        KeyCodec {
            scalar: scalar.to_owned(),
            uuid: scalars.is_uuid(scalar),
        }
    }

    /// A wire key's database form, or `None` for a value that is no key of
    /// the scalar, which names no row.
    pub(crate) fn to_db(&self, key: &str, scalars: &dyn Scalars) -> Option<String> {
        if self.uuid {
            return parse_uuid(key).map(|uuid| uuid.hyphenated().to_string());
        }
        match scalars.parse(&self.scalar, key) {
            Some(parsed) => parsed.ok(),
            None => (!key.is_empty()).then(|| key.to_owned()),
        }
    }

    /// A database key's wire form.
    pub(crate) fn to_wire(&self, key: &str) -> String {
        if self.uuid {
            if let Ok(uuid) = uuid::Uuid::parse_str(key) {
                return uuid_to_base62(uuid);
            }
        }
        key.to_owned()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_uuid_is_base62_on_the_wire() {
        let uuid = uuid::Uuid::parse_str("123e4567-e89b-12d3-a456-426614174000").unwrap();
        let base62 = uuid_to_base62(uuid);
        assert!(base62.len() <= 22, "{base62}");
        assert_eq!(parse_uuid(&base62), Some(uuid));
        assert_eq!(
            parse_uuid("123e4567-e89b-12d3-a456-426614174000"),
            Some(uuid)
        );
        assert_eq!(uuid_to_base62(uuid::Uuid::nil()), "0");
        assert_eq!(parse_uuid("not-a-key!"), None);
        assert_eq!(parse_uuid(""), None);
        assert_eq!(parse_uuid("zzzzzzzzzzzzzzzzzzzzzz"), None, "past 2^128");
    }
}
