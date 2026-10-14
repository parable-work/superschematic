//! A UUID's base62 form, as superscalar's `Identity.UUID` writes it on the
//! wire, and its reading of a UUID in either form: what the identity store
//! moves a key with, and what a queue's message writes into a `UUID`
//! column (D65), since a Rust type's `Identity.UUID` serializes as base62.

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// A UUID's base62 form, as superscalar's `Identity.UUID` writes it: the
/// 128-bit value in base62, most significant digit first, with no padding
/// (`0` for the nil UUID).
#[cfg_attr(not(feature = "identity"), allow(dead_code))]
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
