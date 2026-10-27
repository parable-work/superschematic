//! Comments' blocks beyond the paramsSchema (`comments-contract.md`,
//! "Blocks", "Batch 5: blocks"): each kind's own checks, which refuse as
//! `invalid_argument` at the block's pointer (an action's `data`, a code
//! block's `lines` then `path`, an attachment's `uri`); the config's
//! checks of an attachment, `attachment_not_allowed`; and an edit's code
//! blocks, which keep the moves of the block they give back unchanged.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::Result;

use super::checks::{blocks_of, integer, invalid, veto, MAX_ACTION_DATA};
use super::CommentsConfig;

/// The members moveAnchor adds to a code block, in the order it writes
/// them.
pub(super) const MOVE_MEMBERS: [&str; 5] = ["original", "moves", "movedAt", "movedBy", "movedFor"];

/// The members of a code block an edit compares to keep its moves.
const CODE_MEMBERS: [&str; 5] = ["repo", "path", "revision", "lines", "snippet"];

/// A block's kind.
pub(super) fn kind_of(block: &Value) -> &str {
    block
        .get("kind")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// Each block's own checks, the first block by index that fails them
/// refused: an action's `data` at most 8192 bytes of compact JSON; a code
/// block's first line at most its last, then its path relative; an https
/// attachment's `uri` with a host.
pub(super) fn check_block_shapes(operation: &str, blocks: Option<&Value>) -> Result<()> {
    for (index, block) in blocks_of(blocks).iter().enumerate() {
        match kind_of(block) {
            "action" => {
                if let Some(data) = block.get("data") {
                    let size = serde_json::to_vec(data)
                        .map(|bytes| bytes.len())
                        .unwrap_or(0);
                    if size > MAX_ACTION_DATA {
                        return Err(invalid(
                            operation,
                            format!("/blocks/{index}/data"),
                            format!("is more than {MAX_ACTION_DATA} bytes of JSON"),
                        ));
                    }
                }
            }
            "code" => {
                if !lines_in_order(block.get("lines")) {
                    return Err(invalid(
                        operation,
                        format!("/blocks/{index}/lines"),
                        "must have its first line at most its last",
                    ));
                }
                let path = block
                    .get("path")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if !relative_path(path) {
                    return Err(invalid(
                        operation,
                        format!("/blocks/{index}/path"),
                        "must be a relative path: no leading or trailing /, no \\, and no empty, . or .. part",
                    ));
                }
            }
            "attachment" => {
                let uri = block
                    .get("uri")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if scheme_of(uri) == "https" && !https_with_host(uri) {
                    return Err(invalid(
                        operation,
                        format!("/blocks/{index}/uri"),
                        "must be an https URL with a host",
                    ));
                }
            }
            _ => {}
        }
    }
    Ok(())
}

/// Whether `lines`, two integers, has its first at most its last.
pub(super) fn lines_in_order(lines: Option<&Value>) -> bool {
    let read: Vec<Option<i64>> = lines
        .and_then(Value::as_array)
        .map(|lines| lines.iter().map(|line| integer(Some(line))).collect())
        .unwrap_or_default();
    match read.as_slice() {
        [Some(first), Some(last)] => first <= last,
        _ => true,
    }
}

// relative_path is a path with no leading or trailing /, no \, and no
// empty, . or .. part.
fn relative_path(path: &str) -> bool {
    !path.starts_with('/')
        && !path.ends_with('/')
        && !path.contains('\\')
        && path
            .split('/')
            .all(|part| !matches!(part, "" | "." | ".."))
}

// scheme_of is a uri's text before its first colon.
fn scheme_of(uri: &str) -> &str {
    uri.split_once(':').map_or("", |(scheme, _)| scheme)
}

// https_with_host is `https://` and then a character that is not /, ? or
// #.
fn https_with_host(uri: &str) -> bool {
    uri.strip_prefix("https://")
        .and_then(|rest| rest.chars().next())
        .is_some_and(|first| !matches!(first, '/' | '?' | '#'))
}

/// The config's checks of the attachment blocks a call gives, the first
/// by index that one refuses as `attachment_not_allowed`, its first failing
/// check: the scheme, the media type, the size. An attachment equal, as a
/// JSON value, to one of `before`'s is not held to the config again.
pub(super) fn check_attachments(
    view: &InstanceView<'_, CommentsConfig>,
    operation: &str,
    blocks: &[Value],
    before: &[Value],
) -> Result<()> {
    let config = &view.config().attachments;
    for (index, block) in blocks.iter().enumerate() {
        if kind_of(block) != "attachment" || before.contains(block) {
            continue;
        }
        let uri = block
            .get("uri")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let scheme = scheme_of(uri);
        if !config.schemes.iter().any(|allowed| allowed == scheme) {
            return Err(not_allowed(
                view,
                operation,
                index,
                "uri",
                Value::from(scheme),
                json!(config.schemes),
                format!(
                    "the scheme {scheme} of block {index}'s uri is not one Comments on {} allows ({})",
                    view.schema(),
                    config.schemes.join(", ")
                ),
            ));
        }
        let media_type = block
            .get("mediaType")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if let Some(allowed) = &config.media_types {
            let family = media_type.split('/').next().unwrap_or_default();
            let fits = allowed.iter().any(|one| {
                one == media_type
                    || one
                        .strip_suffix("/*")
                        .is_some_and(|prefix| prefix == family)
            });
            if !fits {
                return Err(not_allowed(
                    view,
                    operation,
                    index,
                    "mediaType",
                    Value::from(media_type),
                    json!(allowed),
                    format!(
                        "block {index}'s media type {media_type} is not one Comments on {} allows ({})",
                        view.schema(),
                        allowed.join(", ")
                    ),
                ));
            }
        }
        if let Some(max) = config.max_bytes {
            let bytes = integer(block.get("bytes")).unwrap_or_default();
            if bytes > max {
                return Err(not_allowed(
                    view,
                    operation,
                    index,
                    "bytes",
                    Value::from(bytes),
                    Value::from(max),
                    format!(
                        "block {index} describes a file of {bytes} bytes, more than the {max} Comments on {} allows",
                        view.schema()
                    ),
                ));
            }
        }
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn not_allowed(
    view: &InstanceView<'_, CommentsConfig>,
    operation: &str,
    block: usize,
    member: &str,
    value: Value,
    allowed: Value,
    reason: String,
) -> superschematic_engine::Error {
    veto(
        view,
        operation,
        "attachment_not_allowed",
        reason,
        json!({ "block": block, "member": member, "value": value, "allowed": allowed }),
    )
}

/// The blocks an edit stores: each code block given at an index whose
/// `repo`, `path`, `revision`, `lines` and `snippet` equal those of a code
/// block at that index before the edit is the block before, its moves
/// kept; every other block as given.
pub(super) fn keep_moves(given: &Value, before: &Value) -> Value {
    let Some(blocks) = given.as_array() else {
        return given.clone();
    };
    let held = before.as_array().map(Vec::as_slice).unwrap_or_default();
    Value::Array(
        blocks
            .iter()
            .enumerate()
            .map(|(index, block)| match held.get(index) {
                Some(old)
                    if kind_of(block) == "code"
                        && kind_of(old) == "code"
                        && CODE_MEMBERS
                            .iter()
                            .all(|member| block.get(member) == old.get(member)) =>
                {
                    old.clone()
                }
                _ => block.clone(),
            })
            .collect(),
    )
}

/// A code block moved to `revision` and `lines`: its members as they were
/// but those two, then `original` (the anchor before its first move),
/// `moves`, `movedAt`, `movedBy` and `movedFor` when the mover acted for
/// someone.
pub(super) fn moved(
    block: &Value,
    revision: &str,
    lines: &Value,
    at: i64,
    by: &str,
    for_subject: Option<&str>,
) -> Value {
    let empty = Map::new();
    let members = block.as_object().unwrap_or(&empty);
    let mut out = Map::new();
    for (name, value) in members {
        if MOVE_MEMBERS.contains(&name.as_str()) {
            continue;
        }
        let value = match name.as_str() {
            "revision" => Value::from(revision),
            "lines" => lines.clone(),
            _ => value.clone(),
        };
        out.insert(name.clone(), value);
    }
    let original = members.get("original").cloned().unwrap_or_else(|| {
        let mut first = Map::new();
        first.insert(
            "revision".into(),
            members.get("revision").cloned().unwrap_or(Value::Null),
        );
        first.insert(
            "lines".into(),
            members.get("lines").cloned().unwrap_or(Value::Null),
        );
        Value::Object(first)
    });
    let moves = integer(members.get("moves")).unwrap_or(0) + 1;
    out.insert("original".into(), original);
    out.insert("moves".into(), Value::from(moves));
    out.insert("movedAt".into(), Value::from(at));
    out.insert("movedBy".into(), Value::from(by));
    if let Some(subject) = for_subject {
        out.insert("movedFor".into(), Value::from(subject));
    }
    Value::Object(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_path_is_relative_with_no_empty_dot_or_dot_dot_part() {
        for path in ["src/orders.rs", "a", "a/b.c/d", ".github/x", "a..b"] {
            assert!(relative_path(path), "{path}");
        }
        for path in ["/a", "a/", "a//b", "./a", "a/../b", "a\\b", ".", ".."] {
            assert!(!relative_path(path), "{path}");
        }
    }

    #[test]
    fn an_https_uri_has_a_host() {
        assert!(https_with_host("https://files.example.com/a.png"));
        for uri in ["https://", "https:///a", "https://?x", "https://#y", "https:a"] {
            assert!(!https_with_host(uri), "{uri}");
        }
        assert_eq!(scheme_of("bucket:uploads/a.png"), "bucket");
    }

    #[test]
    fn a_move_keeps_the_first_anchor_and_counts() {
        let block = json!({"kind": "code", "repo": "r", "path": "p", "revision": "a1", "lines": [1, 2], "snippet": "x"});
        let once = moved(&block, "b2", &json!([3, 4]), 10, "bot", None);
        assert_eq!(
            once,
            json!({"kind": "code", "repo": "r", "path": "p", "revision": "b2", "lines": [3, 4], "snippet": "x",
                   "original": {"revision": "a1", "lines": [1, 2]}, "moves": 1, "movedAt": 10, "movedBy": "bot"})
        );
        let twice = moved(&once, "c3", &json!([5, 6]), 20, "bot", Some("alice"));
        assert_eq!(twice["original"], json!({"revision": "a1", "lines": [1, 2]}));
        assert_eq!(twice["moves"], json!(2));
        assert_eq!(twice["movedFor"], json!("alice"));
        let keys: Vec<&String> = twice.as_object().unwrap().keys().collect();
        assert_eq!(
            keys,
            ["kind", "repo", "path", "revision", "lines", "snippet", "original", "moves", "movedAt", "movedBy", "movedFor"]
        );
    }
}
