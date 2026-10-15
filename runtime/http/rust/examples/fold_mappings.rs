//! Prints every character `search::fold` changes, one `XXXX YYYY` line a
//! character, its code point and its fold's in hex, after a first line
//! naming the Unicode version of this toolchain's `char`: Rust's simple
//! lowercase mappings. The Go runtime's `search` package reads them to
//! write the mappings Go's `unicode` package lacks (`go generate` in
//! `runtime/http/go/search`, which runs this).

use superschematic_http_runtime::search::fold;

fn main() {
    let (major, minor, update) = char::UNICODE_VERSION;
    println!("unicode {major}.{minor}.{update}");
    for c in (0..=char::MAX as u32).filter_map(char::from_u32) {
        let folded = fold(c.encode_utf8(&mut [0; 4]));
        let lower = folded.chars().next().unwrap_or(c);
        if lower != c {
            println!("{:04X} {:04X}", c as u32, lower as u32);
        }
    }
}
