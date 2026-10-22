// Whether the superscalar checkout this repository builds against
// (third_party/superscalar, at superscalar.pin) carries Finance.Currency,
// the scalar of ISO 4217's active codes (D70): with it, the cfg
// `superscalar_currency` turns on, and `currency` checks a config's code
// against the core's table; without it, `currency` checks the code's
// pattern alone. A cargo feature would be turned on by `--all-features`,
// which the engine's CI builds with, and fail at a pin without the scalar;
// this turns on by itself once the pin carries it.
use std::path::Path;

fn main() {
    let manifest = std::env::var("CARGO_MANIFEST_DIR").expect("cargo sets CARGO_MANIFEST_DIR");
    let scalars =
        Path::new(&manifest).join("../../../third_party/superscalar/crates/core/src/scalars");
    println!("cargo::rustc-check-cfg=cfg(superscalar_currency)");
    println!("cargo::rerun-if-changed={}", scalars.display());
    if scalars.join("finance_currency.rs").is_file() {
        println!("cargo::rustc-cfg=superscalar_currency");
    }
}
