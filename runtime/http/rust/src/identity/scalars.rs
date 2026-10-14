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

pub use crate::uuid62::{parse_uuid, uuid_to_base62};

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
