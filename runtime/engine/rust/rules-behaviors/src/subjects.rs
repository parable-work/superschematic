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
//!
//! A subject a behavior keeps as text of its own, Seats' occupants (D104),
//! is kept in its canonical form ([`canonical_subject`]): a hyphenated UUID
//! in base62, any other text as it is.

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

/// The texts a field's value names one subject in (D104): for a UUID type
/// (Identity.UUID, Identity.UserID), a value that reads as a UUID gives its
/// base62 form (the adapters' wire form, its fewest digits), its lowercase
/// hyphenated form and its uppercase one, in that order, the value's own
/// first when it is one of them; any other value, and a value of any other
/// type, itself alone. What a behavior that finds rows by a subject another
/// behavior kept as its text reads (Presence's releaseLeases over Lease's
/// holder), so a subject an authenticator gives in either case is found; a
/// hyphenated subject of mixed case is not.
pub fn subject_forms(type_name: &str, value: &str) -> Vec<String> {
    let uuid = UUID_TYPES
        .contains(&type_name)
        .then(|| uuid_value(value))
        .flatten();
    let Some(uuid) = uuid else {
        return vec![value.to_owned()];
    };
    let lower = hyphenated(uuid);
    let upper = lower.to_uppercase();
    let mut forms: Vec<String> = Vec::with_capacity(3);
    let own = [base62(uuid), lower.clone(), upper.clone()]
        .into_iter()
        .find(|form| form == value);
    for form in own.into_iter().chain([base62(uuid), lower, upper]) {
        if !forms.contains(&form) {
            forms.push(form);
        }
    }
    forms
}

/// A subject's text as Seats keeps and compares it (D104): a hyphenated
/// UUID (36 characters, its hexadecimal digits of either case) as its
/// base62 form, the adapters' wire form and the identity runtime's
/// principal subject, and any other text as it is, so no two texts that
/// are not the same UUID compare equal: `7` and `07` stay two subjects,
/// where reading every text as a UUID by value would make them one.
pub fn canonical_subject(subject: &str) -> String {
    match hyphenated_value(subject) {
        Some(uuid) => base62(uuid),
        None => subject.to_owned(),
    }
}

// base62 is a UUID's value in base62, its fewest digits.
fn base62(mut n: u128) -> String {
    let mut digits = Vec::new();
    loop {
        digits.push(BASE62[(n % 62) as usize]);
        n /= 62;
        if n == 0 {
            break;
        }
    }
    digits.reverse();
    String::from_utf8(digits).unwrap_or_default()
}

// hyphenated is a UUID's value as lowercase 8-4-4-4-12 hexadecimal digits.
fn hyphenated(n: u128) -> String {
    let hex = format!("{n:032x}");
    format!(
        "{}-{}-{}-{}-{}",
        &hex[..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..]
    )
}

/// A UUID's value from its hyphenated form (36 characters, `8-4-4-4-12`
/// hexadecimal digits of either case) or its base62 form (1 to 22
/// characters, case-sensitive, at most 2^128 - 1), as the adapters read a
/// UUID; `None` for any other text.
fn uuid_value(text: &str) -> Option<u128> {
    if text.len() == 36 {
        return hyphenated_value(text);
    }
    if text.is_empty() || text.len() > 22 {
        return None;
    }
    text.bytes().try_fold(0u128, |n, byte| {
        let digit = BASE62.iter().position(|c| *c == byte)?;
        n.checked_mul(62)?.checked_add(digit as u128)
    })
}

// hyphenated_value is a UUID's value from its hyphenated form alone (36
// characters, `8-4-4-4-12` hexadecimal digits of either case); `None` for
// any other text.
fn hyphenated_value(text: &str) -> Option<u128> {
    if text.len() != 36 {
        return None;
    }
    let hyphenated = text.bytes().enumerate().all(|(i, byte)| match i {
        8 | 13 | 18 | 23 => byte == b'-',
        _ => byte.is_ascii_hexdigit(),
    });
    if !hyphenated {
        return None;
    }
    u128::from_str_radix(&text.replace('-', ""), 16).ok()
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
    fn a_uuid_subject_has_its_three_forms_the_values_own_first() {
        let upper = HYPHENATED.to_uppercase();
        for type_name in ["Identity.UUID", "Identity.UserID"] {
            assert_eq!(
                subject_forms(type_name, BASE62_FORM),
                [BASE62_FORM, HYPHENATED, upper.as_str()]
            );
            assert_eq!(
                subject_forms(type_name, HYPHENATED),
                [HYPHENATED, BASE62_FORM, upper.as_str()]
            );
            assert_eq!(
                subject_forms(type_name, &upper),
                [upper.as_str(), BASE62_FORM, HYPHENATED]
            );
            // Mixed case, or base62 with a leading zero, is none of them:
            // base62 comes first.
            let mixed = format!("{}{}", &upper[..8], &HYPHENATED[8..]);
            assert_eq!(
                subject_forms(type_name, &mixed),
                [BASE62_FORM, HYPHENATED, upper.as_str()]
            );
            assert_eq!(
                subject_forms(type_name, &format!("0{BASE62_FORM}")),
                [BASE62_FORM, HYPHENATED, upper.as_str()]
            );
            // A text that is no UUID is itself alone; a short base62 text
            // is one, as names_subject reads it.
            assert_eq!(
                subject_forms(type_name, "bob@example.com"),
                ["bob@example.com"]
            );
            assert_eq!(
                subject_forms(type_name, "bob"),
                [
                    "bob",
                    "00000000-0000-0000-0000-0000000237d5",
                    "00000000-0000-0000-0000-0000000237D5"
                ]
            );
        }
        // A UUID with no letter has one hyphenated form.
        assert_eq!(
            subject_forms("Identity.UUID", "00000000-0000-0000-0000-000000000000"),
            ["00000000-0000-0000-0000-000000000000", "0"]
        );
        // A string field's text is itself alone.
        assert_eq!(subject_forms("string", BASE62_FORM), [BASE62_FORM]);
        assert_eq!(subject_forms("string", "bob"), ["bob"]);
    }

    #[test]
    fn a_hyphenated_uuid_is_kept_in_base62_and_any_other_text_as_it_is() {
        assert_eq!(canonical_subject(HYPHENATED), BASE62_FORM);
        assert_eq!(canonical_subject(&HYPHENATED.to_uppercase()), BASE62_FORM);
        // Base62 is kept, and so is a text that only looks like one.
        assert_eq!(canonical_subject(BASE62_FORM), BASE62_FORM);
        assert_eq!(canonical_subject("bob"), "bob");
        // 7 and 07 stay apart, as they would not by value.
        assert_eq!(canonical_subject("7"), "7");
        assert_eq!(canonical_subject("07"), "07");
        assert_ne!(canonical_subject("7"), canonical_subject("07"));
        // A hyphenated form with a letter out of place is no UUID.
        let broken = "0f8fad5b-d9cb-469f-a165-70867728950g";
        assert_eq!(canonical_subject(broken), broken);
        assert_eq!(canonical_subject(""), "");
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
