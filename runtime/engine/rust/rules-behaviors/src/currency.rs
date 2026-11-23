//! Currencies (D70): a config that names a currency as a literal gives an
//! ISO 4217 alphabetic code, which the declarations' configSchemas hold to
//! `^[A-Z]{3}$` and the behaviors check against the active codes of ISO
//! 4217 List One, the table superscalar's `Finance.Currency` keeps, so the
//! core and every generated validator take the same codes. The core links
//! no superscalar (it builds for wasm32 with serde alone), so the table is
//! copied here, and a test holds it to superscalar's at the pin
//! `superscalar.pin` names.
//!
//! An own field that holds a currency is a `Finance.Currency`, which a
//! table's validators check with the rest of the row.

/// The active codes of ISO 4217 List One, each with its minor-unit exponent
/// (`None` where the list says N.A.), sorted by code: superscalar's
/// `finance_currency::CURRENCIES`.
#[rustfmt::skip]
pub const CURRENCIES: &[(&str, Option<u8>)] = &[
    ("AED",Some(2)), ("AFN",Some(2)), ("ALL",Some(2)), ("AMD",Some(2)), ("AOA",Some(2)),
    ("ARS",Some(2)), ("AUD",Some(2)), ("AWG",Some(2)), ("AZN",Some(2)), ("BAM",Some(2)),
    ("BBD",Some(2)), ("BDT",Some(2)), ("BHD",Some(3)), ("BIF",Some(0)), ("BMD",Some(2)),
    ("BND",Some(2)), ("BOB",Some(2)), ("BOV",Some(2)), ("BRL",Some(2)), ("BSD",Some(2)),
    ("BTN",Some(2)), ("BWP",Some(2)), ("BYN",Some(2)), ("BZD",Some(2)), ("CAD",Some(2)),
    ("CDF",Some(2)), ("CHE",Some(2)), ("CHF",Some(2)), ("CHW",Some(2)), ("CLF",Some(4)),
    ("CLP",Some(0)), ("CNY",Some(2)), ("COP",Some(2)), ("COU",Some(2)), ("CRC",Some(2)),
    ("CUP",Some(2)), ("CVE",Some(2)), ("CZK",Some(2)), ("DJF",Some(0)), ("DKK",Some(2)),
    ("DOP",Some(2)), ("DZD",Some(2)), ("EGP",Some(2)), ("ERN",Some(2)), ("ETB",Some(2)),
    ("EUR",Some(2)), ("FJD",Some(2)), ("FKP",Some(2)), ("GBP",Some(2)), ("GEL",Some(2)),
    ("GHS",Some(2)), ("GIP",Some(2)), ("GMD",Some(2)), ("GNF",Some(0)), ("GTQ",Some(2)),
    ("GYD",Some(2)), ("HKD",Some(2)), ("HNL",Some(2)), ("HTG",Some(2)), ("HUF",Some(2)),
    ("IDR",Some(2)), ("ILS",Some(2)), ("INR",Some(2)), ("IQD",Some(3)), ("IRR",Some(2)),
    ("ISK",Some(0)), ("JMD",Some(2)), ("JOD",Some(3)), ("JPY",Some(0)), ("KES",Some(2)),
    ("KGS",Some(2)), ("KHR",Some(2)), ("KMF",Some(0)), ("KPW",Some(2)), ("KRW",Some(0)),
    ("KWD",Some(3)), ("KYD",Some(2)), ("KZT",Some(2)), ("LAK",Some(2)), ("LBP",Some(2)),
    ("LKR",Some(2)), ("LRD",Some(2)), ("LSL",Some(2)), ("LYD",Some(3)), ("MAD",Some(2)),
    ("MDL",Some(2)), ("MGA",Some(2)), ("MKD",Some(2)), ("MMK",Some(2)), ("MNT",Some(2)),
    ("MOP",Some(2)), ("MRU",Some(2)), ("MUR",Some(2)), ("MVR",Some(2)), ("MWK",Some(2)),
    ("MXN",Some(2)), ("MXV",Some(2)), ("MYR",Some(2)), ("MZN",Some(2)), ("NAD",Some(2)),
    ("NGN",Some(2)), ("NIO",Some(2)), ("NOK",Some(2)), ("NPR",Some(2)), ("NZD",Some(2)),
    ("OMR",Some(3)), ("PAB",Some(2)), ("PEN",Some(2)), ("PGK",Some(2)), ("PHP",Some(2)),
    ("PKR",Some(2)), ("PLN",Some(2)), ("PYG",Some(0)), ("QAR",Some(2)), ("RON",Some(2)),
    ("RSD",Some(2)), ("RUB",Some(2)), ("RWF",Some(0)), ("SAR",Some(2)), ("SBD",Some(2)),
    ("SCR",Some(2)), ("SDG",Some(2)), ("SEK",Some(2)), ("SGD",Some(2)), ("SHP",Some(2)),
    ("SLE",Some(2)), ("SOS",Some(2)), ("SRD",Some(2)), ("SSP",Some(2)), ("STN",Some(2)),
    ("SVC",Some(2)), ("SYP",Some(2)), ("SZL",Some(2)), ("THB",Some(2)), ("TJS",Some(2)),
    ("TMT",Some(2)), ("TND",Some(3)), ("TOP",Some(2)), ("TRY",Some(2)), ("TTD",Some(2)),
    ("TWD",Some(2)), ("TZS",Some(2)), ("UAH",Some(2)), ("UGX",Some(0)), ("USD",Some(2)),
    ("USN",Some(2)), ("UYI",Some(0)), ("UYU",Some(2)), ("UYW",Some(4)), ("UZS",Some(2)),
    ("VED",Some(2)), ("VES",Some(2)), ("VND",Some(0)), ("VUV",Some(0)), ("WST",Some(2)),
    ("XAD",Some(2)), ("XAF",Some(0)), ("XAG",None), ("XAU",None), ("XBA",None), ("XBB",None),
    ("XBC",None), ("XBD",None), ("XCD",Some(2)), ("XCG",Some(2)), ("XDR",None), ("XOF",Some(0)),
    ("XPD",None), ("XPF",Some(0)), ("XPT",None), ("XSU",None), ("XTS",None), ("XUA",None),
    ("XXX",None), ("YER",Some(2)), ("ZAR",Some(2)), ("ZMW",Some(2)), ("ZWG",Some(2)),
];

/// Whether a code has the shape of an ISO 4217 alphabetic code: three
/// capital ASCII letters.
pub fn is_code_shaped(code: &str) -> bool {
    code.len() == 3 && code.bytes().all(|byte| byte.is_ascii_uppercase())
}

/// Whether a code is an active ISO 4217 currency, as `Finance.Currency`
/// takes one.
pub fn is_currency(code: &str) -> bool {
    CURRENCIES
        .binary_search_by(|(known, _)| known.cmp(&code))
        .is_ok()
}

/// The minor-unit exponent of a currency: an amount in minor units over
/// `10^minor_units` is the amount in major units (GBP 2, JPY 0, BHD 3).
/// `None` for a code ISO 4217 gives none, or does not list.
pub fn minor_units(code: &str) -> Option<u8> {
    CURRENCIES
        .binary_search_by(|(known, _)| known.cmp(&code))
        .ok()
        .and_then(|at| CURRENCIES[at].1)
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
    fn the_table_is_superscalars() {
        let theirs: Vec<(&str, Option<u8>)> = superscalar::scalars::finance_currency::CURRENCIES
            .iter()
            .map(|currency| (currency.code, currency.minor_units))
            .collect();
        assert_eq!(CURRENCIES, theirs.as_slice());
    }

    #[test]
    fn a_code_is_an_active_one_of_three_capital_letters() {
        for code in ["gbp", "GB", "GBPS", "G1P", ""] {
            assert!(!is_currency(code), "{code}");
            assert!(refusal("currency", code)
                .expect("refused")
                .ends_with("three capital letters"));
        }
        for code in ["GBP", "USD", "JPY", "EUR"] {
            assert!(is_currency(code), "{code}");
            assert_eq!(refusal("currency", code), None);
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
