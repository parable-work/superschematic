//! The check a generated handler makes on the request path before it trusts
//! the path parameters axum captured from it.

/// Reports whether every `%` in `path` starts an escape of two hex digits
/// and the escapes decode to UTF-8, that is whether each path parameter
/// captured from it was decoded exactly once. axum decodes a capture once,
/// but keeps an escape it cannot decode (`%ZZ`) as text, so without this
/// check `%ZZ` and `%25ZZ` would reach the implementation as the same value.
pub fn path_is_percent_encoded(path: &str) -> bool {
    let bytes = path.as_bytes();
    let mut decoded = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] != b'%' {
            decoded.push(bytes[i]);
            i += 1;
            continue;
        }
        let high = bytes.get(i + 1).copied().and_then(hex_digit);
        let low = bytes.get(i + 2).copied().and_then(hex_digit);
        match (high, low) {
            (Some(high), Some(low)) => decoded.push(high << 4 | low),
            _ => return false,
        }
        i += 3;
    }
    std::str::from_utf8(&decoded).is_ok()
}

fn hex_digit(byte: u8) -> Option<u8> {
    char::from(byte)
        .to_digit(16)
        .and_then(|digit| u8::try_from(digit).ok())
}

#[cfg(test)]
mod tests {
    use super::path_is_percent_encoded;

    #[test]
    fn a_path_whose_escapes_decode_to_utf8_is_percent_encoded() {
        for path in [
            "/api/items/plain",
            "/api/items/%25",
            "/api/items/a%2525b",
            "/api/items/x%2541y",
            "/api/items/caf%C3%A9",
            "/api/items/caf%c3%a9",
            "/api/items/a%2Fb",
        ] {
            assert!(path_is_percent_encoded(path), "{path}");
        }
    }

    #[test]
    fn a_bad_escape_or_bytes_that_are_not_utf8_are_not() {
        for path in [
            "/api/items/%",
            "/api/items/100%",
            "/api/items/%ZZ",
            "/api/items/a%2",
            "/api/items/%+1",
            "/api/items/%E9",
            "/api/items/%C3%28",
        ] {
            assert!(!path_is_percent_encoded(path), "{path}");
        }
    }
}
