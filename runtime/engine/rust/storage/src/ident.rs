//! Names in SQL text, and the keys of the advisory locks the Postgres
//! storage takes.
//!
//! Postgres keeps an identifier's first 63 bytes and drops the rest, so
//! two long names that share their first 63 bytes would be one table. A
//! behavior's names are `bhv_<key>__<name>`, its key and its name each up
//! to 63 characters, so on Postgres the storage writes a name longer than
//! 63 bytes as its first 52 bytes, `_` and ten hex digits of its SHA-256:
//! the same name in every process and release, and distinct for distinct
//! names. A behavior's prefix stays whole for a key of up to 46 bytes, so
//! the checker's prefix rule still holds of the name.
//!
//! The advisory lock keys are SHA-256 too (D58): one key for the catalog
//! of an engine's Postgres schema, one per namespace in it, and one for
//! the migrations at start, so two engines in different schemas of one
//! database never wait for each other.

use std::borrow::Cow;
use std::fmt::Write as _;

use sha2::{Digest, Sha256};

use crate::dialect::Dialect;

/// The longest identifier Postgres keeps whole, in bytes (NAMEDATALEN - 1).
pub const POSTGRES_IDENTIFIER_MAX: usize = 63;

// How much of a long name stays before its hash suffix: 52 bytes, `_` and
// ten hex digits make 63.
const KEPT: usize = POSTGRES_IDENTIFIER_MAX - 1 - SUFFIX_HEX;
const SUFFIX_HEX: usize = 10;

/// `name` as Postgres keeps it whole: unchanged up to 63 bytes, and
/// otherwise its first 52 bytes (cut at a character), `_`, and the first
/// ten hex digits of the SHA-256 of the whole name.
pub fn postgres_identifier(name: &str) -> Cow<'_, str> {
    if name.len() <= POSTGRES_IDENTIFIER_MAX {
        return Cow::Borrowed(name);
    }
    let mut cut = KEPT;
    while !name.is_char_boundary(cut) {
        cut -= 1;
    }
    let digest = Sha256::digest(name.as_bytes());
    let mut out = String::with_capacity(POSTGRES_IDENTIFIER_MAX);
    out.push_str(&name[..cut]);
    out.push('_');
    out.push_str(&hex(&digest)[..SUFFIX_HEX]);
    Cow::Owned(out)
}

/// `name` as `dialect` keeps it: unchanged on SQLite, which keeps a name
/// of any length, and [`postgres_identifier`] on Postgres.
pub fn identifier(dialect: Dialect, name: &str) -> Cow<'_, str> {
    match dialect {
        Dialect::Sqlite => Cow::Borrowed(name),
        Dialect::Postgres => postgres_identifier(name),
    }
}

/// `name` as a quoted identifier, its double quotes doubled.
pub fn quote_identifier(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// `value` as an SQL string literal, its single quotes doubled. Both
/// databases read it the same way (Postgres with
/// standard_conforming_strings on, its default since 9.1).
pub fn sql_string(value: &str) -> String {
    format!("'{}'", value.replace('\'', "''"))
}

/// The advisory lock key of the catalog of the engine in Postgres schema
/// `schema`: a write takes it shared, and a change to the catalog (a
/// define, a publish, a change of namespaces, a migration) takes it
/// exclusively.
pub fn catalog_lock_key(schema: &str) -> i64 {
    lock_key(&["catalog", schema])
}

/// The advisory lock key of namespace `namespace` of the engine in
/// Postgres schema `schema`, which a write in the namespace holds
/// exclusively, so one writer at a time writes a namespace (D16).
pub fn namespace_lock_key(schema: &str, namespace: &str) -> i64 {
    lock_key(&["namespace", schema, namespace])
}

/// The advisory lock key a process holds for its session while it
/// migrates the engine in Postgres schema `schema`, so engines that start
/// together migrate once.
pub fn migration_lock_key(schema: &str) -> i64 {
    lock_key(&["migrations", schema])
}

/// A lock key of the engine's own: the first eight bytes, big-endian, of
/// the SHA-256 of `superschematic-engine` and `parts`, each ended by a NUL,
/// so no two lists of parts share a key by concatenation.
pub fn lock_key(parts: &[&str]) -> i64 {
    let mut hasher = Sha256::new();
    hasher.update(b"superschematic-engine\0");
    for part in parts {
        hasher.update(part.as_bytes());
        hasher.update(b"\0");
    }
    let digest = hasher.finalize();
    let mut first = [0_u8; 8];
    first.copy_from_slice(&digest[..8]);
    i64::from_be_bytes(first)
}

/// `bytes` as lowercase hex.
pub(crate) fn hex(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        // Writing to a String does not fail.
        let _ = write!(out, "{byte:02x}");
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_long_postgres_name_keeps_its_start_and_takes_a_hash() {
        let short = "bhv_workqueue_assignment__claims";
        assert_eq!(postgres_identifier(short), short);
        let exact = "x".repeat(63);
        assert_eq!(postgres_identifier(&exact), exact.as_str());
        let long = format!("bhv_{}__notes", "k".repeat(60));
        let other = format!("bhv_{}__notez", "k".repeat(60));
        let named = postgres_identifier(&long);
        assert_eq!(named.len(), 63);
        assert!(named.starts_with("bhv_kkk"));
        assert_eq!(named, postgres_identifier(&long), "the same in every call");
        assert_ne!(
            named,
            postgres_identifier(&other),
            "distinct names stay distinct"
        );
        assert_eq!(identifier(Dialect::Sqlite, &long), long.as_str());
        // A cut never splits a character.
        let accented = format!("bhv_{}é{}", "a".repeat(47), "b".repeat(20));
        let cut = postgres_identifier(&accented);
        assert!(cut.len() <= 63);
        assert!(cut.starts_with(&format!("bhv_{}", "a".repeat(47))));
    }

    #[test]
    fn quoting_doubles_the_quote() {
        assert_eq!(quote_identifier("a\"b"), "\"a\"\"b\"");
        assert_eq!(sql_string("it's"), "'it''s'");
    }

    #[test]
    fn lock_keys_differ_by_schema_and_namespace() {
        let a = namespace_lock_key("engine", "default");
        assert_eq!(a, namespace_lock_key("engine", "default"));
        assert_ne!(a, namespace_lock_key("engine", "other"));
        assert_ne!(a, namespace_lock_key("other", "default"));
        assert_ne!(catalog_lock_key("engine"), migration_lock_key("engine"));
        assert_ne!(lock_key(&["ab", "c"]), lock_key(&["a", "bc"]));
    }
}
