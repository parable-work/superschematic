//! Currencies (D70): a config that names a currency as a literal gives an
//! ISO 4217 alphabetic code, which the declarations' configSchemas hold to
//! `^[A-Z]{3}$` and the engine checks against the active codes of ISO 4217
//! List One, the table superscalar's `Finance.Currency` keeps
//! (`superscalar::scalars::finance_currency`), so the engine and every
//! generated validator take the same codes.
//!
//! The table reaches the crate through superscalar's core by path, from
//! the checkout `superscalar.pin` names. An own field that holds a
//! currency is a `Finance.Currency`, which the engine's validation checks
//! with the rest of the instance.

use superscalar::scalars::finance_currency;

/// Whether a code has the shape of an ISO 4217 alphabetic code: three
/// capital ASCII letters.
pub fn is_code_shaped(code: &str) -> bool {
    code.len() == 3 && code.bytes().all(|byte| byte.is_ascii_uppercase())
}

/// Whether a code is an active ISO 4217 currency, as `Finance.Currency`
/// takes one.
pub fn is_currency(code: &str) -> bool {
    finance_currency::lookup(code).is_some()
}

/// The minor-unit exponent of a currency: an amount in minor units over
/// `10^minor_units` is the amount in major units (GBP 2, JPY 0, BHD 3).
/// `None` for a code ISO 4217 gives none, or does not list.
pub fn minor_units(code: &str) -> Option<u8> {
    finance_currency::minor_units(code)
}

/// Why a literal currency in a config is refused, at `at`; `None` for an
/// active code.
pub fn refusal(at: &str, code: &str) -> Option<String> {
    if is_currency(code) {
        return None;
    }
    Some(if is_code_shaped(code) {
        format!("{at} \"{code}\" is not an active ISO 4217 currency code")
    } else {
        format!("{at} \"{code}\" is not an ISO 4217 currency code: three capital letters")
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_code_is_three_capital_letters() {
        assert!(is_code_shaped("GBP"));
        for code in ["gbp", "GB", "GBPS", "G1P", ""] {
            assert!(!is_code_shaped(code), "{code}");
            assert!(!is_currency(code), "{code}");
            assert!(refusal("currency", code)
                .expect("refused")
                .ends_with("three capital letters"));
        }
        for code in ["GBP", "USD", "JPY", "EUR"] {
            assert!(is_currency(code), "{code}");
            assert_eq!(refusal("currency", code), None);
        }
    }

    // A code of the right shape that ISO 4217 does not list is refused,
    // and each code has its exponent.
    #[test]
    fn the_table_holds_the_active_codes() {
        for code in ["ABC", "XYZ"] {
            assert!(is_code_shaped(code), "{code}");
            assert!(!is_currency(code), "{code}");
        }
        assert_eq!(
            refusal("price.currency", "ABC").as_deref(),
            Some("price.currency \"ABC\" is not an active ISO 4217 currency code")
        );
        assert_eq!(minor_units("GBP"), Some(2));
        assert_eq!(minor_units("JPY"), Some(0));
        assert_eq!(minor_units("BHD"), Some(3));
        assert_eq!(minor_units("ABC"), None);
    }
}
