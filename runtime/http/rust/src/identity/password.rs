//! Passwords: `Auth.Password`'s rule, and argon2id hashes written as PHC
//! strings every identity runtime reads (`runtime/http/testdata/README.md`,
//! sections `passwordRule`, `hashes` and `verify`).

use std::fmt;

use argon2::{Algorithm, Argon2, Block, Params, Version};
use base64::engine::general_purpose::STANDARD_NO_PAD;
use base64::Engine;
use serde::{Deserialize, Serialize};

use super::config::MAX_ARGON2_MEMORY_KIB;

/// The salt a new hash is written with, in bytes. A hash another runtime
/// wrote with another size still verifies, and is written again at login.
pub const SALT_BYTES: usize = 16;
/// The hash a new PHC string holds, in bytes.
pub const KEY_BYTES: usize = 32;

/// The catalog scalar every password is: 8 to 128 characters, counted as
/// Unicode code points, with no composition rule.
pub const PASSWORD_SCALAR: &str = "Auth.Password";
const MIN_PASSWORD_LENGTH: usize = 8;
const MAX_PASSWORD_LENGTH: usize = 128;

/// An argon2id cost: memory in KiB, passes and lanes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Argon2Params {
    #[serde(rename = "memoryKiB")]
    pub memory_kib: u32,
    pub iterations: u32,
    pub parallelism: u32,
}

impl Argon2Params {
    /// The default cost: 19 MiB, 2 passes, 1 lane.
    pub const DEFAULT: Argon2Params = Argon2Params {
        memory_kib: 19456,
        iterations: 2,
        parallelism: 1,
    };

    /// Refuses a cost argon2 does not take or the config does not allow:
    /// iterations at least 1, parallelism 1 to 255, and memory at least 8
    /// KiB a lane and at most 4 GiB. The reason names the member.
    pub fn check(&self) -> Result<(), String> {
        if self.iterations < 1 {
            return Err("iterations must be at least 1".to_owned());
        }
        if !(1..=255).contains(&self.parallelism) {
            return Err(format!(
                "parallelism must be 1 to 255, not {}",
                self.parallelism
            ));
        }
        if u64::from(self.memory_kib) < 8 * u64::from(self.parallelism) {
            return Err(format!(
                "memoryKiB must be at least 8 times parallelism ({}), not {}",
                8 * self.parallelism,
                self.memory_kib
            ));
        }
        if self.memory_kib > MAX_ARGON2_MEMORY_KIB {
            return Err(format!(
                "memoryKiB must be at most {MAX_ARGON2_MEMORY_KIB} (4 GiB), not {}",
                self.memory_kib
            ));
        }
        Ok(())
    }
}

/// Refuses a password outside `Auth.Password`'s rule, 8 to 128 code points,
/// with the scalar's message.
pub fn check_password(password: &str) -> Result<(), String> {
    let length = password.chars().count();
    if length < MIN_PASSWORD_LENGTH {
        return Err(format!(
            "length {length} below minimum {MIN_PASSWORD_LENGTH}"
        ));
    }
    if length > MAX_PASSWORD_LENGTH {
        return Err(format!(
            "length {length} above maximum {MAX_PASSWORD_LENGTH}"
        ));
    }
    Ok(())
}

/// A hash could not be written: a cost argon2 refuses, or no randomness
/// for the salt.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HashError(pub String);

impl fmt::Display for HashError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "identity: hash a password: {}", self.0)
    }
}

impl std::error::Error for HashError {}

/// A stored hash this runtime cannot read: not an argon2id PHC string of
/// version 19, in the one form [`hash_password`] writes, with a cost the
/// config takes. It matches no password.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MalformedHash;

impl fmt::Display for MalformedHash {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("identity: the password hash is not an argon2id PHC string")
    }
}

impl std::error::Error for MalformedHash {}

/// Hashes `password` with argon2id at `params` and 16 random salt bytes,
/// and writes the PHC string:
///
/// ```text
/// $argon2id$v=19$m=<memoryKiB>,t=<iterations>,p=<parallelism>$<salt>$<hash>
/// ```
///
/// with the salt and the 32-byte hash in standard base64 without padding.
/// The password is hashed as its UTF-8 bytes, with no normalization.
pub fn hash_password(password: &str, params: Argon2Params) -> Result<String, HashError> {
    let salt: [u8; SALT_BYTES] = super::token::random_bytes()
        .map_err(|_| HashError("no random bytes for a salt".to_owned()))?;
    hash_password_with_salt(password, &salt, params)
}

/// [`hash_password`] with the caller's salt. It is deterministic, for the
/// parity vectors; a login never reuses a salt.
pub fn hash_password_with_salt(
    password: &str,
    salt: &[u8],
    params: Argon2Params,
) -> Result<String, HashError> {
    params.check().map_err(HashError)?;
    let mut key = [0u8; KEY_BYTES];
    derive(password, salt, params, &mut key)?;
    Ok(encode_phc(params, salt, &key))
}

/// What verifying a password against a stored hash found.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Verified {
    /// The password matches the hash.
    pub ok: bool,
    /// Only when `ok`: the hash should be written again at the config's
    /// cost, since its memory, iterations or parallelism differ from it, or
    /// its salt is not 16 bytes or its hash not 32.
    pub rehash: bool,
}

/// Verifies `password` against the PHC string `phc`, comparing in constant
/// time, and says whether the hash should be written again at `current`'s
/// cost. A string this runtime cannot read is [`MalformedHash`].
pub fn verify_password(
    phc: &str,
    password: &str,
    current: Argon2Params,
) -> Result<Verified, MalformedHash> {
    let stored = parse_phc(phc)?;
    let mut key = vec![0u8; stored.key.len()];
    derive(password, &stored.salt, stored.params, &mut key).map_err(|_| MalformedHash)?;
    if !constant_time_eq(&key, &stored.key) {
        return Ok(Verified::default());
    }
    let rehash = stored.params != current
        || stored.salt.len() != SALT_BYTES
        || stored.key.len() != KEY_BYTES;
    Ok(Verified { ok: true, rehash })
}

/// A hash of a random password at `params`, which a login for an unknown
/// account verifies against, so its timing does not tell it from a wrong
/// password.
pub(crate) fn dummy_hash(params: Argon2Params) -> Result<String, HashError> {
    let password: [u8; 32] = super::token::random_bytes()
        .map_err(|_| HashError("no random bytes for a dummy password".to_owned()))?;
    hash_password(&STANDARD_NO_PAD.encode(password), params)
}

fn derive(
    password: &str,
    salt: &[u8],
    params: Argon2Params,
    out: &mut [u8],
) -> Result<(), HashError> {
    let argon_params = Params::new(
        params.memory_kib,
        params.iterations,
        params.parallelism,
        Some(out.len()),
    )
    .map_err(|err| HashError(err.to_string()))?;
    let mut blocks = vec![Block::default(); argon_params.block_count()];
    Argon2::new(Algorithm::Argon2id, Version::V0x13, argon_params)
        .hash_password_into_with_memory(password.as_bytes(), salt, out, &mut blocks)
        .map_err(|err| HashError(err.to_string()))
}

fn encode_phc(params: Argon2Params, salt: &[u8], key: &[u8]) -> String {
    format!(
        "$argon2id$v=19$m={},t={},p={}${}${}",
        params.memory_kib,
        params.iterations,
        params.parallelism,
        STANDARD_NO_PAD.encode(salt),
        STANDARD_NO_PAD.encode(key)
    )
}

struct Phc {
    params: Argon2Params,
    salt: Vec<u8>,
    key: Vec<u8>,
}

/// Reads an argon2id PHC string in the one form [`encode_phc`] writes: the
/// version 19, the parameters m, t and p in that order as decimal integers
/// without sign or leading zero, and the salt (8 bytes or more) and hash (4
/// bytes or more) in standard base64 without padding. A cost the config
/// would refuse is refused too, so a stored hash cannot make a login run
/// without end.
fn parse_phc(phc: &str) -> Result<Phc, MalformedHash> {
    let fields: Vec<&str> = phc.split('$').collect();
    let [empty, algorithm, version, params, salt, key] = fields.as_slice() else {
        return Err(MalformedHash);
    };
    if !empty.is_empty() || *algorithm != "argon2id" || *version != "v=19" {
        return Err(MalformedHash);
    }
    let values: Vec<&str> = params.split(',').collect();
    let [m, t, p] = values.as_slice() else {
        return Err(MalformedHash);
    };
    let params = Argon2Params {
        memory_kib: parameter(m, "m")?,
        iterations: parameter(t, "t")?,
        parallelism: parameter(p, "p")?,
    };
    params.check().map_err(|_| MalformedHash)?;
    let salt = decode_base64(salt).filter(|salt| salt.len() >= 8);
    let key = decode_base64(key).filter(|key| key.len() >= 4);
    match (salt, key) {
        (Some(salt), Some(key)) => Ok(Phc { params, salt, key }),
        _ => Err(MalformedHash),
    }
}

/// One `<name>=<decimal>` parameter of a PHC string.
fn parameter(field: &str, name: &str) -> Result<u32, MalformedHash> {
    let (found, value) = field.split_once('=').ok_or(MalformedHash)?;
    let decimal = !value.is_empty()
        && value.bytes().all(|c| c.is_ascii_digit())
        && !(value.len() > 1 && value.starts_with('0'));
    if found != name || !decimal {
        return Err(MalformedHash);
    }
    value.parse().map_err(|_| MalformedHash)
}

/// Standard base64 without padding: anything outside the alphabet
/// (whitespace, a line break, padding or the URL-safe alphabet) is refused,
/// and so are trailing bits that are not zero.
fn decode_base64(text: &str) -> Option<Vec<u8>> {
    let bytes = STANDARD_NO_PAD.decode(text).ok()?;
    (STANDARD_NO_PAD.encode(&bytes) == text).then_some(bytes)
}

/// Compares two byte strings of one public length in time that does not
/// depend on where they differ.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let diff = a
        .iter()
        .zip(b)
        .fold(0u8, |acc, (x, y)| acc | std::hint::black_box(x ^ y));
    std::hint::black_box(diff) == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    const LOW: Argon2Params = Argon2Params {
        memory_kib: 64,
        iterations: 1,
        parallelism: 1,
    };

    #[test]
    fn a_new_hash_verifies_and_is_current() {
        let phc = hash_password("correct horse battery", LOW).unwrap();
        assert!(phc.starts_with("$argon2id$v=19$m=64,t=1,p=1$"), "{phc}");
        let verified = verify_password(&phc, "correct horse battery", LOW).unwrap();
        assert_eq!(
            verified,
            Verified {
                ok: true,
                rehash: false
            }
        );
        assert_eq!(
            verify_password(&phc, "wrong horse battery", LOW).unwrap(),
            Verified::default()
        );
        assert_ne!(phc, hash_password("correct horse battery", LOW).unwrap());
    }

    #[test]
    fn a_dummy_hash_matches_no_password_a_caller_sends() {
        let dummy = dummy_hash(LOW).unwrap();
        assert!(
            !verify_password(&dummy, "correct horse battery", LOW)
                .unwrap()
                .ok
        );
    }

    #[test]
    fn base64_is_read_strictly() {
        assert_eq!(decode_base64("AAECAw"), Some(vec![0, 1, 2, 3]));
        assert_eq!(decode_base64("AAECAw=="), None);
        assert_eq!(decode_base64("AAECAx"), None, "trailing bits set");
        assert_eq!(decode_base64("-_8"), None);
        assert_eq!(decode_base64("AAEC Aw"), None);
        assert_eq!(decode_base64("AAEC\nAw"), None);
        assert_eq!(decode_base64("AAEC\rAw"), None);
    }
}
