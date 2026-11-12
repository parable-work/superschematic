//! The envelopes of a DB table's `Encrypted<T>` fields (D85), which the
//! repositories seal on write and open on read, byte for byte as the Go ORM
//! does through `runtime/schema/go/fieldcrypt` and the TypeScript
//! repositories through `fieldcrypt.ts`: a test of each reads one set of
//! vectors (`runtime/schema/testdata/fieldcrypt_parity.json`).
//!
//! A value is sealed with AES-256-GCM under the keyring's current key, with
//! a random 12-byte nonce, and stored as
//!
//! ```text
//! v1:<key id>:<nonce>:<ciphertext and tag>
//! ```
//!
//! the nonce and the ciphertext in unpadded base64url. The additional data
//! binds it to its cell: the table type, the field, and the row's key as
//! text, so a ciphertext copied to another row or column does not open.
//!
//! A keyring is the text of the DB service's key secret: one or more
//! 32-byte keys in base64, separated by commas. The first seals; each
//! opens the envelopes that name its id, the first 8 hex digits of a
//! SHA-256 over it, so a rotation puts the new key first and keeps the old
//! ones until every row is resealed.

use std::fmt;

use base64::engine::general_purpose::{STANDARD, STANDARD_NO_PAD, URL_SAFE_NO_PAD};
use base64::Engine;
use ring::aead::{Aad, LessSafeKey, Nonce, UnboundKey, AES_256_GCM};
use ring::digest::{digest, SHA256};
use ring::rand::{SecureRandom, SystemRandom};

use super::{FieldCell, FieldKeys};

/// The envelope's version, its first part.
pub const VERSION: &str = "v1";

/// The length of a key in bytes, AES-256's.
pub const KEY_SIZE: usize = 32;

/// The length of a nonce in bytes, GCM's standard one.
pub const NONCE_SIZE: usize = 12;

/// Why a seal or an open failed, as the Go and TypeScript runtimes name
/// it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reason {
    /// A key secret that is not one or more 32-byte keys in base64,
    /// separated by commas.
    BadKeys,
    /// An envelope sealed under a key the keyring does not hold: a wrong
    /// key secret, or an old key rotated out too soon.
    UnknownKey,
    /// An envelope that does not open under its key for its cell: changed,
    /// or copied from another row or column.
    Decrypt,
    /// A value that is no envelope.
    Malformed,
    /// A value the system could not seal: it gave no randomness for a
    /// nonce.
    Seal,
}

impl Reason {
    /// The reason as the parity vectors name it.
    pub fn as_str(self) -> &'static str {
        match self {
            Reason::BadKeys => "bad-keys",
            Reason::UnknownKey => "unknown-key",
            Reason::Decrypt => "decrypt",
            Reason::Malformed => "malformed",
            Reason::Seal => "seal",
        }
    }

    fn text(self) -> &'static str {
        match self {
            Reason::BadKeys => {
                "the field keys are not one or more 32-byte keys in base64, separated by commas"
            }
            Reason::UnknownKey => "no field key has the envelope's key id",
            Reason::Decrypt => "the envelope does not open for its row and column",
            Reason::Malformed => "the value is no field envelope",
            Reason::Seal => "the value could not be sealed",
        }
    }
}

/// A seal's or an open's failure: its reason and, for a key secret, which
/// key. It never carries a value or a key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FieldEncryptionError {
    /// Why it failed.
    pub reason: Reason,
    detail: String,
}

impl FieldEncryptionError {
    fn new(reason: Reason) -> Self {
        Self {
            reason,
            detail: String::new(),
        }
    }

    fn bad_keys(detail: String) -> Self {
        Self {
            reason: Reason::BadKeys,
            detail,
        }
    }
}

impl fmt::Display for FieldEncryptionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.reason.text())?;
        if !self.detail.is_empty() {
            write!(f, ": {}", self.detail)?;
        }
        Ok(())
    }
}

impl std::error::Error for FieldEncryptionError {}

/// The id of a 32-byte key: the first 8 hex digits of a SHA-256 over it.
pub fn field_key_id(raw: &[u8]) -> String {
    let mut input = b"superschematic field key\0".to_vec();
    input.extend_from_slice(raw);
    let sum = digest(&SHA256, &input);
    sum.as_ref()[..4]
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn additional_data(cell: FieldCell<'_>) -> Vec<u8> {
    format!(
        "superschematic field {VERSION}\0{}\0{}\0{}",
        cell.table, cell.field, cell.row
    )
    .into_bytes()
}

/// The 32 bytes of key `n` of a secret: base64 of the standard alphabet,
/// padded or not, with no stray bits, as the Go runtime reads it.
fn decode_key(text: &str, n: usize) -> Result<Vec<u8>, FieldEncryptionError> {
    if text.is_empty() {
        return Err(FieldEncryptionError::bad_keys(format!("key {n} is empty")));
    }
    let decoded = if text.ends_with('=') {
        STANDARD.decode(text)
    } else {
        STANDARD_NO_PAD.decode(text)
    };
    match decoded {
        Ok(raw) if raw.len() == KEY_SIZE => Ok(raw),
        Ok(raw) => Err(FieldEncryptionError::bad_keys(format!(
            "key {n} is {} bytes, not {KEY_SIZE}",
            raw.len()
        ))),
        Err(_) => Err(FieldEncryptionError::bad_keys(format!(
            "key {n} is not base64"
        ))),
    }
}

struct Key {
    id: String,
    aead: LessSafeKey,
}

/// The keys a DB service's key secret names: the first seals, and each
/// opens the envelopes sealed under it. It is the [`FieldKeys`] a server
/// opens its repositories with.
pub struct FieldKeyring {
    keys: Vec<Key>,
    random: SystemRandom,
}

impl fmt::Debug for FieldKeyring {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        // The ids alone: a keyring never prints a key.
        let ids: Vec<&str> = self.keys.iter().map(|key| key.id.as_str()).collect();
        f.debug_struct("FieldKeyring").field("keys", &ids).finish()
    }
}

impl FieldKeyring {
    /// Reads a key secret: one or more 32-byte keys in base64, with or
    /// without padding, separated by commas, the first the current one.
    pub fn parse(secret: &str) -> Result<Self, FieldEncryptionError> {
        let mut keys: Vec<Key> = Vec::new();
        for (i, part) in secret.split(',').enumerate() {
            let n = i + 1;
            let raw = decode_key(part.trim(), n)?;
            let id = field_key_id(&raw);
            if keys.iter().any(|key| key.id == id) {
                return Err(FieldEncryptionError::bad_keys(format!(
                    "key {n} repeats the key id {id}"
                )));
            }
            let unbound = UnboundKey::new(&AES_256_GCM, &raw).map_err(|_| {
                FieldEncryptionError::bad_keys(format!("key {n} is no AES-256 key"))
            })?;
            keys.push(Key {
                id,
                aead: LessSafeKey::new(unbound),
            });
        }
        Ok(Self {
            keys,
            random: SystemRandom::new(),
        })
    }

    fn current(&self) -> &Key {
        // `parse` reads at least one key: a secret splits into one part or
        // more, and each is a key or the secret is refused.
        &self.keys[0]
    }

    /// The id of the key that seals.
    pub fn current_id(&self) -> &str {
        &self.current().id
    }

    /// The start of every envelope the current key seals, as a LIKE
    /// pattern: the rows whose column does not match it hold an envelope
    /// of another key.
    pub fn current_prefix(&self) -> String {
        format!("{VERSION}:{}:%", self.current_id())
    }

    /// Whether `envelope` was sealed under the current key.
    pub fn is_current(&self, envelope: &str) -> bool {
        envelope
            .strip_prefix(VERSION)
            .and_then(|rest| rest.strip_prefix(':'))
            .and_then(|rest| rest.strip_prefix(self.current_id()))
            .is_some_and(|rest| rest.starts_with(':'))
    }

    /// Encrypts `plaintext` for `cell` under the current key, with a nonce
    /// of its own.
    pub fn seal(
        &self,
        cell: FieldCell<'_>,
        plaintext: &str,
    ) -> Result<String, FieldEncryptionError> {
        let mut nonce = [0_u8; NONCE_SIZE];
        self.random
            .fill(&mut nonce)
            .map_err(|_| FieldEncryptionError {
                reason: Reason::Seal,
                detail: "the system gave no randomness for a nonce".to_owned(),
            })?;
        self.seal_with_nonce(cell, plaintext, nonce)
    }

    /// Encrypts `plaintext` for `cell` under the current key with `nonce`:
    /// what the parity vectors hold a seal to. A nonce is used once a key,
    /// so nothing but a test names one.
    pub fn seal_with_nonce(
        &self,
        cell: FieldCell<'_>,
        plaintext: &str,
        nonce: [u8; NONCE_SIZE],
    ) -> Result<String, FieldEncryptionError> {
        let key = self.current();
        let mut sealed = plaintext.as_bytes().to_vec();
        key.aead
            .seal_in_place_append_tag(
                Nonce::assume_unique_for_key(nonce),
                Aad::from(additional_data(cell)),
                &mut sealed,
            )
            .map_err(|_| FieldEncryptionError::new(Reason::Seal))?;
        Ok(format!(
            "{VERSION}:{}:{}:{}",
            key.id,
            URL_SAFE_NO_PAD.encode(nonce),
            URL_SAFE_NO_PAD.encode(sealed)
        ))
    }

    /// Decrypts the envelope of `cell`, under the key its id names.
    pub fn open(
        &self,
        cell: FieldCell<'_>,
        envelope: &str,
    ) -> Result<String, FieldEncryptionError> {
        let malformed = || FieldEncryptionError::new(Reason::Malformed);
        let parts: Vec<&str> = envelope.split(':').collect();
        let [version, id, nonce, sealed] = parts.as_slice() else {
            return Err(malformed());
        };
        if *version != VERSION {
            return Err(malformed());
        }
        let nonce: [u8; NONCE_SIZE] = URL_SAFE_NO_PAD
            .decode(nonce)
            .ok()
            .and_then(|nonce| nonce.try_into().ok())
            .ok_or_else(malformed)?;
        let mut sealed = URL_SAFE_NO_PAD.decode(sealed).map_err(|_| malformed())?;
        let key = self
            .keys
            .iter()
            .find(|key| key.id == *id)
            .ok_or_else(|| FieldEncryptionError::new(Reason::UnknownKey))?;
        let decrypt = || FieldEncryptionError::new(Reason::Decrypt);
        let plain = key
            .aead
            .open_in_place(
                Nonce::assume_unique_for_key(nonce),
                Aad::from(additional_data(cell)),
                &mut sealed,
            )
            .map_err(|_| decrypt())?;
        String::from_utf8(plain.to_vec()).map_err(|_| decrypt())
    }
}

impl FieldKeys for FieldKeyring {
    fn seal(&self, cell: FieldCell<'_>, plaintext: &str) -> Result<String, String> {
        FieldKeyring::seal(self, cell, plaintext).map_err(|err| err.to_string())
    }

    fn open(&self, cell: FieldCell<'_>, envelope: &str) -> Result<String, String> {
        FieldKeyring::open(self, cell, envelope).map_err(|err| err.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    /// The envelopes the Go runtime writes (`go test ./fieldcrypt -run
    /// TestParityVectors -update` in runtime/schema/go), which this
    /// runtime seals byte for byte and opens alike, refusing the same
    /// envelopes for the same reasons.
    const VECTORS: &str = include_str!("../../../../schema/testdata/fieldcrypt_parity.json");

    #[derive(Deserialize)]
    struct Vectors {
        keyrings: Vec<Keyring>,
        seals: Vec<Seal>,
        opens: Vec<Open>,
    }

    #[derive(Deserialize)]
    struct Keyring {
        keyring: String,
        #[serde(default)]
        ids: Vec<String>,
        error: Option<String>,
    }

    #[derive(Deserialize)]
    struct Seal {
        name: String,
        keyring: String,
        table: String,
        field: String,
        row: String,
        plaintext: String,
        nonce: String,
        envelope: String,
    }

    #[derive(Deserialize)]
    struct Open {
        name: String,
        keyring: Option<String>,
        table: String,
        field: String,
        row: String,
        envelope: String,
        plaintext: Option<String>,
        error: Option<String>,
    }

    fn vectors() -> Vectors {
        serde_json::from_str(VECTORS).expect("the parity vectors")
    }

    #[test]
    fn a_key_secret_reads_as_the_go_runtime_reads_it() {
        for k in vectors().keyrings {
            let parsed = FieldKeyring::parse(&k.keyring);
            match (&k.error, parsed) {
                (Some(reason), Err(err)) => {
                    assert_eq!(err.reason.as_str(), reason, "{:?}", k.keyring)
                }
                (Some(reason), Ok(_)) => panic!("{:?} read, and is {reason}", k.keyring),
                (None, Err(err)) => panic!("{:?}: {err}", k.keyring),
                (None, Ok(ring)) => {
                    let ids: Vec<&str> = ring.keys.iter().map(|key| key.id.as_str()).collect();
                    assert_eq!(ids, k.ids, "{:?}", k.keyring);
                    assert_eq!(ring.current_id(), k.ids[0]);
                }
            }
        }
    }

    #[test]
    fn a_seal_is_the_go_runtimes_byte_for_byte() {
        for s in vectors().seals {
            let ring = FieldKeyring::parse(&s.keyring).expect("the keyring");
            let cell = FieldCell {
                table: &s.table,
                field: &s.field,
                row: &s.row,
            };
            let nonce: [u8; NONCE_SIZE] = URL_SAFE_NO_PAD
                .decode(&s.nonce)
                .expect("the nonce")
                .try_into()
                .expect("a nonce of 12 bytes");
            let sealed = ring
                .seal_with_nonce(cell, &s.plaintext, nonce)
                .expect("seal");
            assert_eq!(sealed, s.envelope, "{}", s.name);
            assert_eq!(
                ring.open(cell, &s.envelope).expect("open"),
                s.plaintext,
                "{}",
                s.name
            );
        }
    }

    #[test]
    fn an_envelope_opens_or_is_refused_as_the_go_runtime_refuses_it() {
        for o in vectors().opens {
            let cell = FieldCell {
                table: &o.table,
                field: &o.field,
                row: &o.row,
            };
            let Some(keyring) = &o.keyring else {
                // A repository without keys refuses before it opens
                // anything.
                assert_eq!(o.error.as_deref(), Some("no-keys"), "{}", o.name);
                continue;
            };
            let ring = FieldKeyring::parse(keyring).expect("the keyring");
            match (o.error, ring.open(cell, &o.envelope)) {
                (Some(reason), Err(err)) => assert_eq!(err.reason.as_str(), reason, "{}", o.name),
                (Some(reason), Ok(_)) => panic!("{} opened, and is {reason}", o.name),
                (None, Err(err)) => panic!("{}: {err}", o.name),
                (None, Ok(plaintext)) => assert_eq!(Some(plaintext), o.plaintext, "{}", o.name),
            }
        }
    }

    #[test]
    fn a_keyring_seals_each_value_under_a_nonce_of_its_own() {
        let secret = &vectors().keyrings[0].keyring;
        let ring = FieldKeyring::parse(secret).expect("the keyring");
        let cell = FieldCell {
            table: "PlaidItem",
            field: "accessToken",
            row: "0b9c4d8e-5f1a-4c2b-9d3e-7a6b5c4d3e2f",
        };
        let a = ring.seal(cell, "secret").expect("seal");
        let b = ring.seal(cell, "secret").expect("seal");
        assert_ne!(a, b);
        assert!(ring.is_current(&a));
        assert!(a.starts_with(ring.current_prefix().trim_end_matches('%')));
        assert_eq!(ring.open(cell, &a).expect("open"), "secret");
        // A keyring never prints a key, and an error never a value.
        assert!(!format!("{ring:?}").contains(secret.as_str()));
        let other = FieldCell {
            row: "another",
            ..cell
        };
        let refused = ring.open(other, &a).expect_err("another row");
        assert!(!refused.to_string().contains("secret"));
    }
}
