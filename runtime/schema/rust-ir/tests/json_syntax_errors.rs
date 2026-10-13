//! The schema-file reader words a JSON syntax error as Node's `JSON.parse`
//! does. The TypeScript loader passes `JSON.parse`'s message on as the
//! payload's one issue, and the TypeScript engine's corpus is a Node 24
//! run, so the Rust engine (D58) must give the same message for the same
//! text.
//!
//! Every expected message here is Node 24's `JSON.parse` message (Node
//! 24.15.0, V8 13.6) for its input, hard-coded. To re-derive one, run the
//! input through Node 24:
//!
//! ```sh
//! node -e 'try { JSON.parse(process.argv[1]); console.log("accepted") }
//!   catch (e) { console.log(JSON.stringify(e.message)) }' "$input"
//! ```
//!
//! or, for an input a shell argument cannot carry (a NUL, a lone `\r`),
//! read it from a file of JSON strings and print `JSON.stringify` of each
//! message. A position and a column count UTF-16 code units, as a
//! JavaScript string's length does.

use superschematic_schema_ir::schemafile::{canonical_json, load_schema_file};

/// Inputs and Node 24's `JSON.parse` message for each.
const CASES: &[(&str, &str)] = &[
    // The end of the text where a value, or the rest of an escape,
    // belongs.
    ("", "Unexpected end of JSON input"),
    (" \n\t\r", "Unexpected end of JSON input"),
    ("[", "Unexpected end of JSON input"),
    ("[1,", "Unexpected end of JSON input"),
    ("{\"a\":", "Unexpected end of JSON input"),
    ("tru", "Unexpected end of JSON input"),
    ("\"\\", "Unexpected end of JSON input"),
    // An object's first key.
    (
        "{",
        "Expected property name or '}' in JSON at position 1 (line 1 column 2)",
    ),
    (
        "{]",
        "Expected property name or '}' in JSON at position 1 (line 1 column 2)",
    ),
    (
        "{ a: 1}",
        "Expected property name or '}' in JSON at position 2 (line 1 column 3)",
    ),
    (
        "{'a': 1}",
        "Expected property name or '}' in JSON at position 1 (line 1 column 2)",
    ),
    // A key after a comma.
    (
        "{\"a\": 1,}",
        "Expected double-quoted property name in JSON at position 8 (line 1 column 9)",
    ),
    (
        "{\"a\": 1,",
        "Expected double-quoted property name in JSON at position 8 (line 1 column 9)",
    ),
    (
        "{\"name\": \"Item\", }",
        "Expected double-quoted property name in JSON at position 17 (line 1 column 18)",
    ),
    // The colon after a key.
    (
        "{\"a\" 1}",
        "Expected ':' after property name in JSON at position 5 (line 1 column 6)",
    ),
    (
        "{\"a\"",
        "Expected ':' after property name in JSON at position 4 (line 1 column 5)",
    ),
    // A comma or the end of an object.
    (
        "{\"a\": 1",
        "Expected ',' or '}' after property value in JSON at position 7 (line 1 column 8)",
    ),
    (
        "{\"a\": 1 \"b\": 2}",
        "Expected ',' or '}' after property value in JSON at position 8 (line 1 column 9)",
    ),
    (
        "{\"a\": 1]",
        "Expected ',' or '}' after property value in JSON at position 7 (line 1 column 8)",
    ),
    // A comma or the end of an array.
    (
        "[1",
        "Expected ',' or ']' after array element in JSON at position 2 (line 1 column 3)",
    ),
    (
        "[1 2]",
        "Expected ',' or ']' after array element in JSON at position 3 (line 1 column 4)",
    ),
    (
        "[1}",
        "Expected ',' or ']' after array element in JSON at position 2 (line 1 column 3)",
    ),
    // Any other token where a value belongs, or in a literal, quoted with
    // the whole text when it is shorter than 21 UTF-16 code units. A
    // character up to U+00FF after a backslash is a bad escape; one past
    // it is a token.
    ("[1,]", "Unexpected token ']', \"[1,]\" is not valid JSON"),
    ("[,1]", "Unexpected token ',', \"[,1]\" is not valid JSON"),
    (
        "{\"a\": :1}",
        "Unexpected token ':', \"{\"a\": :1}\" is not valid JSON",
    ),
    (
        "{\"a\": }",
        "Unexpected token '}', \"{\"a\": }\" is not valid JSON",
    ),
    (".5", "Unexpected token '.', \".5\" is not valid JSON"),
    ("+1", "Unexpected token '+', \"+1\" is not valid JSON"),
    ("'a'", "Unexpected token ''', \"'a'\" is not valid JSON"),
    ("trux", "Unexpected token 'x', \"trux\" is not valid JSON"),
    ("tr ue", "Unexpected token ' ', \"tr ue\" is not valid JSON"),
    (
        "t\nrue",
        "Unexpected token '\n', \"t\nrue\" is not valid JSON",
    ),
    ("é", "Unexpected token 'é', \"é\" is not valid JSON"),
    ("ā", "Unexpected token 'ā', \"ā\" is not valid JSON"),
    (
        "\u{feff}{}",
        "Unexpected token '\u{feff}', \"\u{feff}{}\" is not valid JSON",
    ),
    (
        "\"\\ā\"",
        "Unexpected token 'ā', \"\"\\ā\"\" is not valid JSON",
    ),
    (" NaN", "Unexpected token 'N', \" NaN\" is not valid JSON"),
    // The four texts V8 names whole: what JSON.parse gets from `NaN`,
    // `Infinity`, `undefined` and `{}`.
    ("NaN", "\"NaN\" is not valid JSON"),
    ("Infinity", "\"Infinity\" is not valid JSON"),
    ("undefined", "\"undefined\" is not valid JSON"),
    ("[object Object]", "\"[object Object]\" is not valid JSON"),
    // From 21 UTF-16 code units, ten units either side of the token, `...`
    // marking each side that is not the text's start or end by that rule
    // (at position 10 the context starts at 0 and still has `...`).
    (
        "x                   ",
        "Unexpected token 'x', \"x                   \" is not valid JSON",
    ),
    (
        "x                    ",
        "Unexpected token 'x', \"x         \"... is not valid JSON",
    ),
    (
        "[1,2,3,4,5,6,7,8,9,]",
        "Unexpected token ']', \"[1,2,3,4,5,6,7,8,9,]\" is not valid JSON",
    ),
    (
        "[1,2,3,4,5,6,7,8,9,1,]",
        "Unexpected token ']', ...\"6,7,8,9,1,]\" is not valid JSON",
    ),
    (
        "[1,1,1,1,]                              ",
        "Unexpected token ']', \"[1,1,1,1,]         \"... is not valid JSON",
    ),
    (
        "[1,1,1,1, ]                              ",
        "Unexpected token ']', ...\"[1,1,1,1, ]         \"... is not valid JSON",
    ),
    (
        "               @               ",
        "Unexpected token '@', ...\"          @         \"... is not valid JSON",
    ),
    (
        "                    @          ",
        "Unexpected token '@', ...\"          @         \"... is not valid JSON",
    ),
    (
        "                    @         ",
        "Unexpected token '@', ...\"          @         \" is not valid JSON",
    ),
    (
        "{\"key\": tru, \"other\": 12345678}",
        "Unexpected token ',', ...\"\"key\": tru, \"other\":\"... is not valid JSON",
    ),
    // 21 UTF-16 code units in 20 characters, and 20 in 19.
    (
        "[\"😀aaaaaaaaaaaaa\", ]",
        "Unexpected token ']', ...\"aaaaaaa\", ]\" is not valid JSON",
    ),
    (
        "[\"😀aaaaaaaaaaaa\", ]",
        "Unexpected token ']', \"[\"😀aaaaaaaaaaaa\", ]\" is not valid JSON",
    ),
    // A leading zero, or a digit or `-` in a literal.
    (
        "01",
        "Unexpected number in JSON at position 1 (line 1 column 2)",
    ),
    (
        "-01",
        "Unexpected number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "[01]",
        "Unexpected number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "tr1",
        "Unexpected number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "tr-",
        "Unexpected number in JSON at position 2 (line 1 column 3)",
    ),
    // A `"` in a literal.
    (
        "tr\"",
        "Unexpected string in JSON at position 2 (line 1 column 3)",
    ),
    // A minus sign without a digit.
    (
        "-",
        "No number after minus sign in JSON at position 1 (line 1 column 2)",
    ),
    (
        "-a",
        "No number after minus sign in JSON at position 1 (line 1 column 2)",
    ),
    (
        "-.5",
        "No number after minus sign in JSON at position 1 (line 1 column 2)",
    ),
    (
        "[-]",
        "No number after minus sign in JSON at position 2 (line 1 column 3)",
    ),
    // A decimal point without a digit.
    (
        "1.",
        "Unterminated fractional number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "1.e5",
        "Unterminated fractional number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "-0.",
        "Unterminated fractional number in JSON at position 3 (line 1 column 4)",
    ),
    // An exponent without a digit.
    (
        "1e",
        "Exponent part is missing a number in JSON at position 2 (line 1 column 3)",
    ),
    (
        "1E+",
        "Exponent part is missing a number in JSON at position 3 (line 1 column 4)",
    ),
    (
        "0.0e-x",
        "Exponent part is missing a number in JSON at position 5 (line 1 column 6)",
    ),
    // The end of the text in a string.
    (
        "\"",
        "Unterminated string in JSON at position 1 (line 1 column 2)",
    ),
    (
        "\"abc",
        "Unterminated string in JSON at position 4 (line 1 column 5)",
    ),
    (
        "\"\\\"",
        "Unterminated string in JSON at position 3 (line 1 column 4)",
    ),
    (
        "{\"ab",
        "Unterminated string in JSON at position 4 (line 1 column 5)",
    ),
    (
        "\"😀",
        "Unterminated string in JSON at position 3 (line 1 column 4)",
    ),
    // A control character in a string.
    (
        "\"a\nb\"",
        "Bad control character in string literal in JSON at position 2 (line 1 column 3)",
    ),
    (
        "\"\t\"",
        "Bad control character in string literal in JSON at position 1 (line 1 column 2)",
    ),
    (
        "\"\u{0}\"",
        "Bad control character in string literal in JSON at position 1 (line 1 column 2)",
    ),
    (
        "{\"a\u{1f}b\": 1}",
        "Bad control character in string literal in JSON at position 3 (line 1 column 4)",
    ),
    // An escape JSON does not have, by a character up to U+00FF.
    (
        "\"\\x\"",
        "Bad escaped character in JSON at position 2 (line 1 column 3)",
    ),
    (
        "\"\\é\"",
        "Bad escaped character in JSON at position 2 (line 1 column 3)",
    ),
    (
        "{\"\\x\": 1}",
        "Bad escaped character in JSON at position 3 (line 1 column 4)",
    ),
    // A `\u` escape without four hex digits.
    (
        "\"\\u12g4\"",
        "Bad Unicode escape in JSON at position 5 (line 1 column 6)",
    ),
    (
        "\"\\u12",
        "Bad Unicode escape in JSON at position 5 (line 1 column 6)",
    ),
    (
        "\"\\u\"",
        "Bad Unicode escape in JSON at position 3 (line 1 column 4)",
    ),
    (
        "\"\\uD800\\u12\"",
        "Bad Unicode escape in JSON at position 11 (line 1 column 12)",
    ),
    // Anything after the value.
    (
        "{} {}",
        "Unexpected non-whitespace character after JSON at position 3 (line 1 column 4)",
    ),
    (
        "1 2",
        "Unexpected non-whitespace character after JSON at position 2 (line 1 column 3)",
    ),
    (
        "0x10",
        "Unexpected non-whitespace character after JSON at position 1 (line 1 column 2)",
    ),
    (
        "1.5.5",
        "Unexpected non-whitespace character after JSON at position 3 (line 1 column 4)",
    ),
    (
        "nullx",
        "Unexpected non-whitespace character after JSON at position 4 (line 1 column 5)",
    ),
    (
        "\"a\" 1",
        "Unexpected non-whitespace character after JSON at position 4 (line 1 column 5)",
    ),
    (
        "[1]  ,",
        "Unexpected non-whitespace character after JSON at position 5 (line 1 column 6)",
    ),
    // The position and column in UTF-16 code units; lines break at `\n`,
    // `\r` and `\r\n`, and `\n\r` is two breaks.
    (
        "{\n\"a\": 1,\n}",
        "Expected double-quoted property name in JSON at position 10 (line 3 column 1)",
    ),
    (
        "{\r\n\"a\": 1,\r\n}",
        "Expected double-quoted property name in JSON at position 12 (line 3 column 1)",
    ),
    (
        "{\r\"a\": 1,\r}",
        "Expected double-quoted property name in JSON at position 10 (line 3 column 1)",
    ),
    (
        "{\n\r\"a\": 1,}",
        "Expected double-quoted property name in JSON at position 10 (line 3 column 8)",
    ),
    (
        "{\"a\": 1,\n  \"b\" 2}",
        "Expected ':' after property name in JSON at position 15 (line 2 column 7)",
    ),
    (
        "{\"é\": 1,}",
        "Expected double-quoted property name in JSON at position 8 (line 1 column 9)",
    ),
    (
        "{\"ā\": 1,}",
        "Expected double-quoted property name in JSON at position 8 (line 1 column 9)",
    ),
    (
        "{\"😀\": 1,}",
        "Expected double-quoted property name in JSON at position 9 (line 1 column 10)",
    ),
    (
        "\"😀\\u12x\"",
        "Bad Unicode escape in JSON at position 7 (line 1 column 8)",
    ),
];

/// Inputs where V8's message holds half a surrogate pair, which no Rust
/// string holds: the token is one UTF-16 code unit, the high half of a
/// character past U+FFFF, and the quoted context can cut a pair. The
/// reader writes U+FFFD there; the rest is Node 24's message (it prints
/// the half as `\ud83d` or `\ude00` under `JSON.stringify`).
const HALF_PAIRS: &[(&str, &str)] = &[
    (
        "😀",
        "Unexpected token '\u{fffd}', \"😀\" is not valid JSON",
    ),
    (
        "\"\\😀\"",
        "Unexpected token '\u{fffd}', \"\"\\😀\"\" is not valid JSON",
    ),
    (
        "[1,]aaaaaaaa😀aaaaaaaaaaaaaaaaaaaa",
        "Unexpected token ']', \"[1,]aaaaaaaa\u{fffd}\"... is not valid JSON",
    ),
    (
        "[\"😀aaaaaaa\",]                    ",
        "Unexpected token ']', ...\"\u{fffd}aaaaaaa\",]         \"... is not valid JSON",
    ),
];

#[test]
fn a_syntax_error_has_json_parses_message() {
    let mut failures = Vec::new();
    for (input, want) in CASES.iter().chain(HALF_PAIRS) {
        match canonical_json(input) {
            Ok(output) => failures.push(format!("{input:?}: accepted as {output}")),
            Err(error) if error.message() == *want => {}
            Err(error) => failures.push(format!(
                "{input:?}:\n  got  {:?}\n  want {want:?}",
                error.message()
            )),
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn the_loader_refuses_a_syntax_error_with_json_parses_message() {
    for (input, want) in CASES.iter().chain(HALF_PAIRS) {
        let error = load_schema_file(input, "item.schema.json").unwrap_err();
        assert_eq!(error.source(), "item.schema.json", "{input:?}");
        assert_eq!(error.issues().len(), 1, "{input:?}");
        assert_eq!(error.issues()[0].path, "", "{input:?}");
        assert_eq!(error.issues()[0].message, *want, "{input:?}");
    }
}

/// V8 nests without bound; the reader stops past 1000 levels, in its own
/// words at V8's position convention.
#[test]
fn nesting_past_the_bound_is_refused() {
    let error = canonical_json(&"[".repeat(1002)).unwrap_err();
    assert_eq!(
        error.message(),
        "JSON nests more than 1000 levels deep at position 1001 (line 1 column 1002)"
    );
    let nested = format!("{}{}", "[".repeat(1000), "]".repeat(1000));
    assert!(canonical_json(&nested).is_ok());
}
