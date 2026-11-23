//! Comments' configSchema and its operations' paramsSchemas, held in code
//! in the words the engine gave them (D58): a config's in the schema
//! loader's (`unknown key "x"`, `must be one of ...`), an operation's
//! parameters in the behavior registry's (`must NOT have additional
//! properties: x`). A block is checked against each kind's schema of the
//! paramsSchema's `oneOf`, as ajv checks one: a block that matches none
//! fails with every kind's issues and the `oneOf`'s own.

use serde_json::{Map, Value};

use crate::schema::{self, Issues};

// The configSchema's bounds.
const MAX_BODY: i64 = 40_000;
const MAX_SAFE: i64 = 9_007_199_254_740_991;

/// The block kinds a config's `blocks` lists, in the schema's order.
const BLOCK_KINDS: [&str; 4] = ["ref", "action", "code", "attachment"];
/// An action block's states.
const ACTION_STATES: [&str; 3] = ["running", "succeeded", "failed"];
/// The URI schemes an attachment never takes.
const REFUSED_SCHEMES: [&str; 6] = ["javascript", "vbscript", "data", "file", "blob", "about"];

const CONFIG_KEYS: [&str; 14] = [
    "anchorPermission",
    "attachments",
    "blocks",
    "composeTimeoutMs",
    "deliverPermission",
    "deliveryRetentionMs",
    "editWindowMs",
    "maxAttempts",
    "maxBodyLength",
    "maxDrafts",
    "moderatePermission",
    "pinPermission",
    "resolvePermission",
    "retryBackoffMs",
];

// The patterns the schemas name, as their messages print them.
const HAS_TEXT: &str = "\\S";
const NO_SPACE: &str = "^\\S+$";
const ID_PATTERN: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$";
const SCHEMA_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]{0,127}$";
const PATH_PATTERN: &str = "^[^\\x00-\\x1F\\x7F]+$";
const WORD_PATTERN: &str = "^[^\\x00-\\x20\\x7F]+$";
const MEDIA_PATTERN: &str = "^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$";
const MEDIA_RANGE_PATTERN: &str =
    "^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/([a-z0-9][a-z0-9!#$&^_.+-]{0,126}|\\*)$";
const NAME_PATTERN: &str =
    "^[^/\\\\\\x00-\\x1F\\x7F]*[^/\\\\\\s\\x00-\\x1F\\x7F][^/\\\\\\x00-\\x1F\\x7F]*$";
const SHA_PATTERN: &str = "^[0-9a-f]{64}$";
const URI_PATTERN: &str = "^[a-z][a-z0-9+.-]{0,31}:[^\\x00-\\x20\\x7F]+$";
const SCHEME_PATTERN: &str = "^[a-z][a-z0-9+.-]{0,31}$";

/// What a config breaks of the configSchema.
pub(super) fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues, true);
    for key in [
        "anchorPermission",
        "deliverPermission",
        "moderatePermission",
        "pinPermission",
        "resolvePermission",
    ] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    for (key, minimum, maximum) in [
        ("composeTimeoutMs", 5_000, Some(3_600_000)),
        ("deliveryRetentionMs", 86_400_000, Some(31_536_000_000)),
        ("editWindowMs", 1_000, None),
        ("maxAttempts", 1, Some(20)),
        ("maxBodyLength", 1, Some(MAX_BODY)),
        ("maxDrafts", 1, Some(50)),
        ("retryBackoffMs", 1_000, Some(3_600_000)),
    ] {
        if let Some(value) = config.get(key) {
            schema::integer_issues(
                &schema::member("", key),
                value,
                Some(minimum),
                maximum,
                &mut issues,
            );
        }
    }
    if let Some(blocks) = config.get("blocks") {
        match blocks.as_array() {
            None => issues.push("/blocks", "type", "must be array"),
            Some(kinds) => {
                unique("/blocks", kinds, &mut issues);
                for (index, kind) in kinds.iter().enumerate() {
                    if !kind
                        .as_str()
                        .is_some_and(|kind| BLOCK_KINDS.contains(&kind))
                    {
                        issues.push(
                            format!("/blocks/{index}"),
                            "enum",
                            format!(
                                "must be one of {}",
                                BLOCK_KINDS.map(schema::quoted).join(", ")
                            ),
                        );
                    }
                }
            }
        }
    }
    if let Some(attachments) = config.get("attachments") {
        attachments_issues(attachments, &mut issues);
    }
    issues
}

// attachments_issues lists what the config's attachments break of
// `{ maxBytes?, mediaTypes?, schemes? }`.
fn attachments_issues(attachments: &Value, issues: &mut Issues) {
    let at = "/attachments";
    let Some(members) = attachments.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(
        at,
        members,
        &["maxBytes", "mediaTypes", "schemes"],
        issues,
        true,
    );
    if let Some(bytes) = members.get("maxBytes") {
        schema::integer_issues(
            "/attachments/maxBytes",
            bytes,
            Some(1),
            Some(MAX_SAFE),
            issues,
        );
    }
    for (key, pattern, holds) in [
        (
            "mediaTypes",
            MEDIA_RANGE_PATTERN,
            is_media_range as fn(&str) -> bool,
        ),
        ("schemes", SCHEME_PATTERN, is_scheme as fn(&str) -> bool),
    ] {
        let Some(list) = members.get(key) else {
            continue;
        };
        let at = format!("/attachments/{key}");
        let Some(items) = list.as_array() else {
            issues.push(at, "type", "must be array");
            continue;
        };
        if items.is_empty() {
            issues.push(at.clone(), "minItems", "must NOT have fewer than 1 items");
        }
        unique(&at, items, issues);
        for (index, item) in items.iter().enumerate() {
            let at = format!("{at}/{index}");
            let Some(text) = item.as_str() else {
                issues.push(at, "type", "must be string");
                continue;
            };
            if key == "mediaTypes" && schema::length(text) > 255 {
                issues.push(
                    at.clone(),
                    "maxLength",
                    "must NOT have more than 255 characters",
                );
            }
            if !holds(text) {
                issues.push(
                    at.clone(),
                    "pattern",
                    format!("must match pattern \"{pattern}\""),
                );
            }
            if key == "schemes" && REFUSED_SCHEMES.contains(&text) {
                issues.push(at, "not", "must NOT be valid");
            }
        }
    }
}

/// What a signal's data breaks of the signal's paramsSchema (D77's
/// primitive 2): `typing`'s `{ thread? }`, `thread` an integer from 1, and
/// `viewing`'s `{}`. `draft` is appendDraft's own, which holds its data.
pub(super) fn signal_issues(name: &str, data: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = data.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = match name {
        "typing" => &["thread"],
        "viewing" => &[],
        _ => return issues,
    };
    unknown_keys("", given, keys, &mut issues, false);
    if let Some(thread) = given.get("thread") {
        schema::integer_issues("/thread", thread, Some(1), None, &mut issues);
    }
    issues
}

/// What an operation's parameters break of its paramsSchema.
pub(super) fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let (keys, required): (&[&str], &[&str]) = match operation {
        "post" => (
            &["blocks", "body", "clientId", "mentions", "quotes", "thread"],
            &["body"],
        ),
        "edit" => (&["blocks", "body", "mentions", "message"], &["message"]),
        "deleteMessage" | "purge" | "pin" | "unpin" | "getMessage" => (&["message"], &["message"]),
        "react" | "unreact" => (&["emoji", "message"], &["message", "emoji"]),
        "resolve" | "reopen" => (&["thread"], &["thread"]),
        "listMessages" => (&["after", "around", "before", "limit", "thread"], &[]),
        "listEdits" => (&["cursor", "limit", "message"], &["message"]),
        "listReactions" => (
            &["cursor", "emoji", "limit", "message"],
            &["message", "emoji"],
        ),
        "listPinned" => (&["cursor", "limit"], &[]),
        "begin" => (&["clientId", "quotes", "thread"], &[]),
        "appendDraft" => (&["message", "text"], &["message", "text"]),
        "finish" => (&["blocks", "body", "mentions", "message"], &["message"]),
        "interrupt" => (&["message"], &["message"]),
        "markRead" => (&["thread", "upTo"], &["upTo"]),
        "follow" => (&["on", "thread"], &["thread", "on"]),
        _ => return issues,
    };
    unknown_keys("", given, keys, &mut issues, false);
    for member in required {
        if !given.contains_key(*member) {
            issues.push(
                "",
                "required",
                format!("must have required property '{member}'"),
            );
        }
    }
    for member in [
        "message", "thread", "quotes", "after", "around", "before", "upTo",
    ] {
        if let Some(value) = given.get(member) {
            schema::integer_issues(&format!("/{member}"), value, Some(1), None, &mut issues);
        }
    }
    if given.get("on").is_some_and(|on| !on.is_boolean()) {
        issues.push("/on", "type", "must be boolean");
    }
    if let Some(text) = given.get("text") {
        string_issues("/text", text, None, Some(4000), None, &mut issues);
    }
    if let Some(limit) = given.get("limit") {
        let maximum = match operation {
            "listEdits" | "listReactions" => 500,
            _ => 100,
        };
        schema::integer_issues("/limit", limit, Some(1), Some(maximum), &mut issues);
    }
    if given
        .get("cursor")
        .is_some_and(|cursor| !cursor.is_string())
    {
        issues.push("/cursor", "type", "must be string");
    }
    if let Some(body) = given.get("body") {
        string_issues(
            "/body",
            body,
            Some(1),
            Some(MAX_BODY as usize),
            Some((HAS_TEXT, has_text)),
            &mut issues,
        );
    }
    if let Some(client) = given.get("clientId") {
        string_issues("/clientId", client, Some(1), Some(128), None, &mut issues);
    }
    if let Some(emoji) = given.get("emoji") {
        string_issues(
            "/emoji",
            emoji,
            Some(1),
            Some(32),
            Some((NO_SPACE, no_space)),
            &mut issues,
        );
    }
    if let Some(mentions) = given.get("mentions") {
        match mentions.as_array() {
            None => issues.push("/mentions", "type", "must be array"),
            Some(items) => {
                if items.len() > 50 {
                    issues.push("/mentions", "maxItems", "must NOT have more than 50 items");
                }
                unique("/mentions", items, &mut issues);
                for (index, item) in items.iter().enumerate() {
                    string_issues(
                        &format!("/mentions/{index}"),
                        item,
                        Some(1),
                        Some(256),
                        Some((NO_SPACE, no_space)),
                        &mut issues,
                    );
                }
            }
        }
    }
    if let Some(blocks) = given.get("blocks") {
        match blocks.as_array() {
            None => issues.push("/blocks", "type", "must be array"),
            Some(items) => {
                if items.len() > 20 {
                    issues.push("/blocks", "maxItems", "must NOT have more than 20 items");
                }
                for (index, block) in items.iter().enumerate() {
                    block_issues(&format!("/blocks/{index}"), block, &mut issues);
                }
            }
        }
    }
    issues
}

// unknown_keys lists the members an object's schema does not name: in the
// schema loader's words for a config, the registry's for parameters.
fn unknown_keys(
    at: &str,
    members: &Map<String, Value>,
    known: &[&str],
    issues: &mut Issues,
    config: bool,
) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        let message = if config {
            format!("unknown key {}", schema::quoted(key))
        } else {
            format!("must NOT have additional properties: {key}")
        };
        issues.push(at, "additionalProperties", message);
    }
}

// unique lists a list's first pair of equal items, as ajv names it.
fn unique(at: &str, items: &[Value], issues: &mut Issues) {
    if let Some((j, i)) = schema::duplicate_value(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// A pattern a string is held to: as its message prints it, and whether a
// string matches it.
type Pattern = (&'static str, fn(&str) -> bool);

// string_issues lists what a value breaks of `{ type: string, minLength?,
// maxLength?, pattern? }`; a value that is no string fails its type alone.
fn string_issues(
    at: &str,
    value: &Value,
    min: Option<usize>,
    max: Option<usize>,
    pattern: Option<Pattern>,
    issues: &mut Issues,
) {
    let Some(text) = value.as_str() else {
        issues.push(at, "type", "must be string");
        return;
    };
    let length = schema::length(text);
    if let Some(min) = min.filter(|min| length < *min) {
        issues.push(
            at,
            "minLength",
            format!("must NOT have fewer than {min} characters"),
        );
    }
    if let Some(max) = max.filter(|max| length > *max) {
        issues.push(
            at,
            "maxLength",
            format!("must NOT have more than {max} characters"),
        );
    }
    if let Some((printed, holds)) = pattern {
        if !holds(text) {
            issues.push(at, "pattern", format!("must match pattern \"{printed}\""));
        }
    }
}

// block_issues lists what a block breaks of the paramsSchema's items: an
// object with a kind, matching exactly one kind's schema. One that matches
// none has every kind's issues and the oneOf's.
fn block_issues(at: &str, block: &Value, issues: &mut Issues) {
    let mut found = Issues::default();
    match block.as_object() {
        None => found.push(at, "type", "must be object"),
        Some(members) if !members.contains_key("kind") => {
            found.push(at, "required", "must have required property 'kind'");
        }
        Some(_) => {}
    }
    let kinds: [fn(&str, &Value) -> Issues; 4] =
        [ref_issues, action_issues, code_issues, attachment_issues];
    let branches: Vec<Issues> = kinds.iter().map(|kind| kind(at, block)).collect();
    let matched = branches.iter().filter(|branch| branch.is_empty()).count();
    if matched == 1 && found.is_empty() {
        return;
    }
    issues.extend(found);
    if matched == 0 {
        for branch in branches {
            issues.extend(branch);
        }
    }
    if matched != 1 {
        issues.push(at, "oneOf", "must match exactly one schema in oneOf");
    }
}

// branch_issues lists what a block breaks of one kind's object schema: its
// type, its members, its required members and its kind's const.
fn branch_issues<'b>(
    at: &str,
    block: &'b Value,
    kind: &str,
    members: &[&str],
    required: &[&str],
    issues: &mut Issues,
) -> Option<&'b Map<String, Value>> {
    let Some(given) = block.as_object() else {
        issues.push(at, "type", "must be object");
        return None;
    };
    unknown_keys(at, given, members, issues, false);
    for member in required {
        if !given.contains_key(*member) {
            issues.push(
                at,
                "required",
                format!("must have required property '{member}'"),
            );
        }
    }
    if given.get("kind").is_some_and(|given| given != kind) {
        issues.push(format!("{at}/kind"), "const", "must be equal to constant");
    }
    Some(given)
}

fn ref_issues(at: &str, block: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = branch_issues(
        at,
        block,
        "ref",
        &["id", "kind", "schema"],
        &["kind", "schema", "id"],
        &mut issues,
    ) else {
        return issues;
    };
    if let Some(id) = given.get("id") {
        string_issues(
            &format!("{at}/id"),
            id,
            None,
            None,
            Some((ID_PATTERN, is_id)),
            &mut issues,
        );
    }
    if let Some(schema) = given.get("schema") {
        string_issues(
            &format!("{at}/schema"),
            schema,
            None,
            None,
            Some((SCHEMA_PATTERN, is_schema_name)),
            &mut issues,
        );
    }
    issues
}

fn action_issues(at: &str, block: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = branch_issues(
        at,
        block,
        "action",
        &["data", "kind", "status", "title"],
        &["kind", "title", "status"],
        &mut issues,
    ) else {
        return issues;
    };
    if given.get("data").is_some_and(|data| !data.is_object()) {
        issues.push(format!("{at}/data"), "type", "must be object");
    }
    if let Some(status) = given.get("status") {
        if !status
            .as_str()
            .is_some_and(|status| ACTION_STATES.contains(&status))
        {
            issues.push(
                format!("{at}/status"),
                "enum",
                format!(
                    "must be equal to one of the allowed values: {}",
                    ACTION_STATES.map(schema::quoted).join(", ")
                ),
            );
        }
    }
    if let Some(title) = given.get("title") {
        string_issues(
            &format!("{at}/title"),
            title,
            Some(1),
            Some(200),
            Some((HAS_TEXT, has_text)),
            &mut issues,
        );
    }
    issues
}

fn code_issues(at: &str, block: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = branch_issues(
        at,
        block,
        "code",
        &["kind", "lines", "path", "repo", "revision", "snippet"],
        &["kind", "repo", "path", "revision", "lines", "snippet"],
        &mut issues,
    ) else {
        return issues;
    };
    if let Some(lines) = given.get("lines") {
        let at = format!("{at}/lines");
        match lines.as_array() {
            None => issues.push(at, "type", "must be array"),
            Some(items) => {
                if items.len() < 2 {
                    issues.push(at.clone(), "minItems", "must NOT have fewer than 2 items");
                }
                if items.len() > 2 {
                    issues.push(at.clone(), "maxItems", "must NOT have more than 2 items");
                }
                for (index, line) in items.iter().enumerate() {
                    schema::integer_issues(
                        &format!("{at}/{index}"),
                        line,
                        Some(1),
                        Some(2_147_483_647),
                        &mut issues,
                    );
                }
            }
        }
    }
    for (member, max, pattern) in [
        (
            "path",
            1024,
            (PATH_PATTERN, is_path_text as fn(&str) -> bool),
        ),
        ("repo", 512, (WORD_PATTERN, is_word as fn(&str) -> bool)),
        ("revision", 256, (WORD_PATTERN, is_word as fn(&str) -> bool)),
    ] {
        if let Some(value) = given.get(member) {
            string_issues(
                &format!("{at}/{member}"),
                value,
                Some(1),
                Some(max),
                Some(pattern),
                &mut issues,
            );
        }
    }
    if let Some(snippet) = given.get("snippet") {
        string_issues(
            &format!("{at}/snippet"),
            snippet,
            None,
            Some(8000),
            None,
            &mut issues,
        );
    }
    issues
}

fn attachment_issues(at: &str, block: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = branch_issues(
        at,
        block,
        "attachment",
        &["bytes", "kind", "mediaType", "name", "sha256", "uri"],
        &["kind", "uri", "name", "mediaType", "bytes", "sha256"],
        &mut issues,
    ) else {
        return issues;
    };
    if let Some(bytes) = given.get("bytes") {
        schema::integer_issues(
            &format!("{at}/bytes"),
            bytes,
            Some(0),
            Some(MAX_SAFE),
            &mut issues,
        );
    }
    let strings: [(&str, Option<usize>, Option<usize>, Pattern); 4] = [
        ("mediaType", None, Some(255), (MEDIA_PATTERN, is_media_type)),
        ("name", Some(1), Some(255), (NAME_PATTERN, is_file_name)),
        ("sha256", None, None, (SHA_PATTERN, is_sha256)),
        ("uri", Some(3), Some(2048), (URI_PATTERN, is_uri)),
    ];
    for (member, min, max, pattern) in strings {
        if let Some(value) = given.get(member) {
            string_issues(
                &format!("{at}/{member}"),
                value,
                min,
                max,
                Some(pattern),
                &mut issues,
            );
        }
    }
    issues
}

// ---- the patterns, each as its regex holds a string ----

/// `\S`: the text holds a code point that is not white space.
pub(super) fn has_text(text: &str) -> bool {
    text.chars().any(|c| !c.is_whitespace())
}

// `^\S+$`: at least one code point, none of them white space.
fn no_space(text: &str) -> bool {
    !text.is_empty() && !text.chars().any(char::is_whitespace)
}

// A first character `first` holds, then at most `rest_max` more that
// `rest` holds.
fn led(text: &str, first: fn(char) -> bool, rest: fn(char) -> bool, rest_max: usize) -> bool {
    let mut chars = text.chars();
    chars.next().is_some_and(first) && chars.clone().count() <= rest_max && chars.all(rest)
}

fn is_id(text: &str) -> bool {
    led(
        text,
        |c| c.is_ascii_alphanumeric(),
        |c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-'),
        255,
    )
}

fn is_schema_name(text: &str) -> bool {
    led(
        text,
        |c| c.is_ascii_alphabetic(),
        |c| c.is_ascii_alphanumeric() || matches!(c, '_' | '-'),
        127,
    )
}

fn is_control(c: char) -> bool {
    c <= '\u{1F}' || c == '\u{7F}'
}

fn is_path_text(text: &str) -> bool {
    !text.is_empty() && !text.chars().any(is_control)
}

fn is_word(text: &str) -> bool {
    !text.is_empty() && !text.chars().any(|c| c <= ' ' || c == '\u{7F}')
}

fn is_media_part(part: &str) -> bool {
    led(
        part,
        |c| c.is_ascii_lowercase() || c.is_ascii_digit(),
        |c| {
            c.is_ascii_lowercase()
                || c.is_ascii_digit()
                || matches!(c, '!' | '#' | '$' | '&' | '^' | '_' | '.' | '+' | '-')
        },
        126,
    )
}

fn is_media_type(text: &str) -> bool {
    text.split_once('/')
        .is_some_and(|(kind, sub)| is_media_part(kind) && is_media_part(sub))
}

fn is_media_range(text: &str) -> bool {
    text.split_once('/')
        .is_some_and(|(kind, sub)| is_media_part(kind) && (sub == "*" || is_media_part(sub)))
}

fn is_file_name(text: &str) -> bool {
    let allowed = |c: char| c != '/' && c != '\\' && !is_control(c);
    text.chars().all(allowed) && text.chars().any(|c| !c.is_whitespace())
}

fn is_sha256(text: &str) -> bool {
    text.len() == 64
        && text
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn is_scheme(text: &str) -> bool {
    led(
        text,
        |c| c.is_ascii_lowercase(),
        |c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '+' | '.' | '-'),
        31,
    )
}

fn is_uri(text: &str) -> bool {
    text.split_once(':')
        .is_some_and(|(scheme, rest)| is_scheme(scheme) && is_word(rest))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // found lists the issues as `<pointer>: <message>`, in order.
    fn found(issues: Issues) -> Vec<String> {
        issues
            .into_issues()
            .into_iter()
            .map(|issue| format!("{}: {}", issue.path, issue.message))
            .collect()
    }

    #[test]
    fn the_patterns_hold_strings_as_their_regexes_do() {
        assert!(has_text(" a ") && !has_text(" \t\n") && !has_text(""));
        assert!(no_space("role:leads") && !no_space("a b") && !no_space(""));
        assert!(is_id("r1") && is_id("a.b:c-d_e") && !is_id("-r1") && !is_id(""));
        assert!(is_schema_name("Review") && !is_schema_name("1Review"));
        assert!(is_media_type("image/png") && !is_media_type("image/*"));
        assert!(is_media_range("image/*") && is_media_range("text/plain"));
        assert!(is_file_name("a b.txt") && !is_file_name("  ") && !is_file_name("a/b"));
        assert!(is_uri("https://x") && is_uri("bucket:files/7c1e") && !is_uri("https:"));
        assert!(is_sha256(&"a".repeat(64)) && !is_sha256(&"A".repeat(64)));
    }

    #[test]
    fn a_block_of_a_kind_meets_that_kinds_schema() {
        let mut issues = Issues::default();
        block_issues(
            "/blocks/0",
            &json!({"kind": "ref", "schema": "Review", "id": "r1"}),
            &mut issues,
        );
        assert!(issues.is_empty());
        let mut issues = Issues::default();
        block_issues(
            "/blocks/0",
            &json!({"kind": "ref", "schema": "Review"}),
            &mut issues,
        );
        let listed = found(issues);
        assert!(listed.contains(&"/blocks/0: must have required property 'id'".to_owned()));
        assert!(listed.contains(&"/blocks/0: must match exactly one schema in oneOf".to_owned()));
    }

    #[test]
    fn a_posts_parameters_meet_its_schema() {
        assert_eq!(
            found(params_issues("post", &json!({"body": "", "replyTo": 1}))),
            [
                ": must NOT have additional properties: replyTo",
                "/body: must NOT have fewer than 1 characters",
                "/body: must match pattern \"\\S\"",
            ]
        );
        assert_eq!(
            found(params_issues(
                "post",
                &json!({"body": "x", "mentions": ["a", "a"]})
            )),
            ["/mentions: must NOT have duplicate items (items ## 1 and 0 are identical)"]
        );
        assert_eq!(
            found(params_issues("post", &json!({"body": "x", "thread": 0}))),
            ["/thread: must be >= 1"]
        );
    }
}
