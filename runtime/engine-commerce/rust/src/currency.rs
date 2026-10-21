//! Currency codes (D70): whatever holds an amount names its currency, an
//! ISO 4217 code that superscalar's `Finance.Currency` scalar holds, the
//! active List One codes. A config's literal code (`"GBP"`) and a code a
//! provider's adapter reports are checked here, through the core's own
//! check of the scalar, so the engine and every binding read one list.
//!
//! The check is made at run time, through superscalar's built-in registry,
//! rather than at build time: a superscalar without `Finance.Currency` (a
//! pin before the scalar's commit) still builds, and then holds a code to
//! its shape alone, three ASCII uppercase letters, as the Go binding's test
//! skips until the pin carries it. [`listed`] says which.

use superscalar::Registry;

/// The scalar that holds a currency code.
pub const SCALAR: &str = "Finance.Currency";

/// Whether the linked superscalar has `Finance.Currency`, so [`check`]
/// holds a code to the active ISO 4217 list and not to its shape alone.
pub fn listed() -> bool {
    Registry::builtin().scalar(SCALAR).is_some()
}

/// Whether a code has a currency code's shape: three ASCII uppercase
/// letters.
pub fn shaped(code: &str) -> bool {
    code.len() == 3 && code.bytes().all(|byte| byte.is_ascii_uppercase())
}

/// Checks a currency code: its shape, and, when superscalar has the scalar
/// ([`listed`]), that it is an active ISO 4217 code. Refused with why.
pub fn check(code: &str) -> Result<(), String> {
    if !shaped(code) {
        return Err(format!(
            "{code:?} is not an ISO 4217 code: three uppercase letters"
        ));
    }
    let registry = Registry::builtin();
    match registry.scalar(SCALAR) {
        Some(scalar) => scalar
            .validate(registry, code)
            .map_err(|error| format!("{code} is not an active ISO 4217 currency: {error}")),
        None => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_code_is_three_uppercase_letters() {
        for code in ["GBP", "EUR", "USD", "JPY"] {
            assert!(check(code).is_ok(), "{code}");
        }
        for code in ["gbp", "Gbp", "GB", "GBPP", "", "G1P", "ÉUR"] {
            assert!(check(code).is_err(), "{code:?}");
        }
    }

    // Membership is the scalar's: it runs where the linked superscalar has
    // Finance.Currency.
    #[test]
    fn a_code_no_active_currency_has_is_refused_when_the_list_is_linked() {
        if !listed() {
            eprintln!("the linked superscalar has no {SCALAR}; membership waits for the pin");
            return;
        }
        for code in ["ABC", "DEM", "HRK", "XYZ"] {
            assert!(check(code).is_err(), "{code}");
        }
        for code in ["GBP", "BHD", "XAU", "CHE"] {
            assert!(check(code).is_ok(), "{code}");
        }
    }
}
