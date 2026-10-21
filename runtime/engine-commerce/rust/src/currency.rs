//! Currencies (D70): a config that names a currency as a literal gives an
//! ISO 4217 alphabetic code, which the declarations' configSchemas hold to
//! `^[A-Z]{3}$` and the engine checks against the active codes of ISO 4217
//! List One, the table superscalar's `Finance.Currency` keeps
//! (`superscalar::scalars::finance_currency`), so the engine and every
//! generated validator take the same codes.
//!
//! The table reaches the crate through superscalar's core by path, from
//! the checkout `superscalar.pin` names. A pin that predates the scalar
//! has no table: `build.rs` turns on the cfg `superscalar_currency` only
//! when the checkout carries it, and until then [`is_currency`] checks the
//! pattern alone ([`CHECKS_TABLE`] is false). An own field that holds a
//! currency is a `Finance.Currency`, which the engine's validation checks
//! with the rest of the instance.

/// Whether [`is_currency`] checks a code against ISO 4217's table, or, at
/// a superscalar pin without `Finance.Currency`, its pattern alone.
pub const CHECKS_TABLE: bool = cfg!(superscalar_currency);

/// Whether a code has the shape of an ISO 4217 alphabetic code: three
/// capital ASCII letters.
pub fn is_code_shaped(code: &str) -> bool {
    code.len() == 3 && code.bytes().all(|byte| byte.is_ascii_uppercase())
}

/// Whether a code is an active ISO 4217 currency, as `Finance.Currency`
/// takes one.
#[cfg(superscalar_currency)]
pub fn is_currency(code: &str) -> bool {
    superscalar::scalars::finance_currency::lookup(code).is_some()
}

/// Whether a code is an active ISO 4217 currency, as `Finance.Currency`
/// takes one.
///
/// FALLBACK: the superscalar pin predates `Finance.Currency`, so this
/// checks the code's pattern alone, as the configSchema does; a code of
/// the right shape that ISO 4217 does not list (`ABC`) passes. `build.rs`
/// turns the table's check on once the pin carries the scalar.
#[cfg(not(superscalar_currency))]
pub fn is_currency(code: &str) -> bool {
    is_code_shaped(code)
}

/// The minor-unit exponent of a currency: an amount in minor units over
/// `10^minor_units` is the amount in major units (GBP 2, JPY 0, BHD 3).
/// `None` for a code ISO 4217 gives none, or does not list; always `None`
/// at a superscalar pin without the table.
#[cfg(superscalar_currency)]
pub fn minor_units(code: &str) -> Option<u8> {
    superscalar::scalars::finance_currency::minor_units(code)
}

/// The minor-unit exponent of a currency; `None` at this superscalar pin,
/// which carries no table (FALLBACK, see [`is_currency`]).
#[cfg(not(superscalar_currency))]
pub fn minor_units(_code: &str) -> Option<u8> {
    None
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

    // With superscalar's table: a code of the right shape that ISO 4217
    // does not list is refused, and each code has its exponent.
    #[cfg(superscalar_currency)]
    #[test]
    fn the_table_holds_the_active_codes() {
        assert!(!is_currency("ABC"));
        assert_eq!(
            refusal("price.currency", "ABC").as_deref(),
            Some("price.currency \"ABC\" is not an active ISO 4217 currency code")
        );
        assert_eq!(minor_units("GBP"), Some(2));
        assert_eq!(minor_units("JPY"), Some(0));
        assert_eq!(minor_units("BHD"), Some(3));
        assert_eq!(minor_units("ABC"), None);
    }

    // FALLBACK: without the table, the pattern alone.
    #[cfg(not(superscalar_currency))]
    #[test]
    fn without_the_table_the_pattern_alone() {
        assert!(is_currency("ABC"));
        assert_eq!(minor_units("GBP"), None);
    }
}
