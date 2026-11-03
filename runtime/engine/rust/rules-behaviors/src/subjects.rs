//! How a field of a row names a subject (D104): the rule Workflow's `by`
//! holds a caller to, and that later behaviors which name a principal by
//! the row read too. A field that holds a subject has an IR type of
//! [`SUBJECT_TYPES`]; its value names the caller when it is the caller's
//! subject.
//!
//! A UUID-typed field compares by value: every adapter hands the core a
//! UUID column's value in base62, its wire form, and the identity runtime's
//! principal subject is base62 too, but a deployment's own authenticator
//! may give a hyphenated UUID, so the same UUID in either form names the
//! same subject, in every language, without any adapter normalizing the
//! caller. A `string` field compares its text exactly.

use serde_json::Value;

/// The IR types a field that holds a subject may have (D104).
pub const SUBJECT_TYPES: [&str; 3] = ["string", "Identity.UUID", "Identity.UserID"];

// The IR types whose values are UUIDs, compared by value.
const UUID_TYPES: [&str; 2] = ["Identity.UUID", "Identity.UserID"];

// base62's digits, in their order: the UUID scalar's wire form.
const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// Whether a field of the IR type holds a subject.
pub fn holds_subject(type_name: &str) -> bool {
    SUBJECT_TYPES.contains(&type_name)
}

/// Whether a field's value names the caller's subject: a non-empty string
/// value and a non-empty subject that are the same text; for a UUID type
/// (Identity.UUID, Identity.UserID), the same UUID in either of its forms,
/// lowercase or uppercase hyphenated or base62, compared by value, and the
/// text when either is no UUID. An absent, null or empty value, or one
/// that is no string, names no one.
pub fn names_subject(type_name: &str, value: Option<&Value>, subject: &str) -> bool {
    let Some(Value::String(held)) = value else {
        return false;
    };
    if held.is_empty() || subject.is_empty() {
        return false;
    }
    if UUID_TYPES.contains(&type_name) {
        if let (Some(held), Some(subject)) = (uuid_value(held), uuid_value(subject)) {
            return held == subject;
        }
    }
    held == subject
}

/// A UUID's value from its hyphenated form (36 characters, `8-4-4-4-12`
/// hexadecimal digits of either case) or its base62 form (1 to 22
/// characters, case-sensitive, at most 2^128 - 1), as the adapters read a
/// UUID; `None` for any other text.
fn uuid_value(text: &str) -> Option<u128> {
    if text.len() == 36 {
        let hyphenated = text.bytes().enumerate().all(|(i, byte)| match i {
            8 | 13 | 18 | 23 => byte == b'-',
            _ => byte.is_ascii_hexdigit(),
        });
        if !hyphenated {
            return None;
        }
        return u128::from_str_radix(&text.replace('-', ""), 16).ok();
    }
    if text.is_empty() || text.len() > 22 {
        return None;
    }
    text.bytes().try_fold(0u128, |n, byte| {
        let digit = BASE62.iter().position(|c| *c == byte)?;
        n.checked_mul(62)?.checked_add(digit as u128)
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
    const BASE62_FORM: &str = "TMXp8QPR6yGvKuV2GZQ10";

    #[test]
    fn a_uuid_field_names_its_subject_in_either_form() {
        for type_name in ["Identity.UUID", "Identity.UserID"] {
            let held = json!(HYPHENATED);
            assert!(names_subject(type_name, Some(&held), BASE62_FORM));
            assert!(names_subject(type_name, Some(&held), HYPHENATED));
            assert!(names_subject(
                type_name,
                Some(&held),
                &HYPHENATED.to_uppercase()
            ));
            let held = json!(BASE62_FORM);
            assert!(names_subject(type_name, Some(&held), HYPHENATED));
            assert!(names_subject(type_name, Some(&held), BASE62_FORM));
            // Another UUID is another subject.
            assert!(!names_subject(
                type_name,
                Some(&held),
                "3n9R8RlRyqVJBUQUv6toL1"
            ));
        }
    }

    #[test]
    fn a_string_field_compares_the_text() {
        let held = json!(HYPHENATED);
        assert!(!names_subject("string", Some(&held), BASE62_FORM));
        assert!(names_subject("string", Some(&held), HYPHENATED));
        assert!(!names_subject(
            "string",
            Some(&held),
            &HYPHENATED.to_uppercase()
        ));
        assert!(names_subject("string", Some(&json!("bob")), "bob"));
        assert!(!names_subject("string", Some(&json!("bob")), "Bob"));
    }

    #[test]
    fn a_text_that_is_no_uuid_under_a_uuid_type_compares_the_text() {
        let held = json!("bob@example.com");
        assert!(names_subject(
            "Identity.UUID",
            Some(&held),
            "bob@example.com"
        ));
        assert!(!names_subject("Identity.UUID", Some(&held), BASE62_FORM));
        // A hyphenated form with a letter out of place is no UUID.
        let broken = "0f8fad5b-d9cb-469f-a165-70867728950g";
        assert!(!names_subject(
            "Identity.UUID",
            Some(&json!(broken)),
            BASE62_FORM
        ));
        // Past 128 bits, 22 base62 digits are no UUID.
        let over = "zzzzzzzzzzzzzzzzzzzzzz";
        assert_eq!(uuid_value(over), None);
        assert!(names_subject("Identity.UUID", Some(&json!(over)), over));
    }

    #[test]
    fn no_value_and_no_subject_name_no_one() {
        for type_name in SUBJECT_TYPES {
            assert!(!names_subject(type_name, None, "bob"));
            assert!(!names_subject(type_name, Some(&Value::Null), "bob"));
            assert!(!names_subject(type_name, Some(&json!("")), ""));
            assert!(!names_subject(type_name, Some(&json!("bob")), ""));
            assert!(!names_subject(type_name, Some(&json!(5)), "5"));
            assert!(!names_subject(type_name, Some(&json!(["bob"])), "bob"));
        }
    }

    #[test]
    fn only_the_subject_types_hold_a_subject() {
        for type_name in SUBJECT_TYPES {
            assert!(holds_subject(type_name));
        }
        for type_name in ["number", "Temporal.DateTime", "boolean", "Identity.Email"] {
            assert!(!holds_subject(type_name));
        }
    }
}
