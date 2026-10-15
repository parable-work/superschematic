//! Branches' guidance (`guidance/branches.ts`): the kinds of rows an
//! instance's version graph holds, its primary line, and for each operation
//! which refs it writes through and which of the version graph's refusals
//! it can meet. Every write names the ref's version, so a stale one is
//! `version_conflict`: read the ref again.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::{duration, list, refusal, said, sentences};
use crate::branches::BranchesConfig;

// The correction for each refusal an operation lists.
fn correction(code: &str) -> &'static str {
    match code {
        "version_conflict" => {
            "Read the ref with refs (or the release log with releases) and try again at its version."
        }
        "name_taken" => "Pick another name, or discard the live ref that has it.",
        "ref_sealed" => "Branch a new draft from the sealed one and write there.",
        "primary_merge_only" => "Write on a draft, then merge it into the primary line.",
        "nothing_to_commit" => "None: the draft has nothing new; save rows first.",
        "entity_not_found" => "Read the draft with compose and name only entities it holds.",
        "invalid_tree" => "Fix the rows details.findings names, then try again.",
        "merge_into_itself" => "Name two different refs.",
        "no_parent" => "None: a primary line has no parent; rebase a draft.",
        "not_tagged" => "Commit with a tag, then release that commit.",
        "walk_ceiling" => "Read a commit nearer to a snapshot, or wait for the sweep to write one.",
        "primary_line" => "None: the primary line is not discarded.",
        _ => "",
    }
}

fn errors(codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| refusal(code, None, correction(code)))
        .collect()
}

const PAGE: &str = "Pass next as cursor for the page after, until it is null.";

pub(crate) fn guidance(config: &BranchesConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let kinds: Vec<String> = config
        .kinds
        .iter()
        .map(|(name, kind)| {
            format!(
                "{name} ({}{})",
                kind.type_name,
                if kind.singleton { ", one per tree" } else { "" }
            )
        })
        .collect();
    let names: Vec<&str> = config.kinds.keys().map(String::as_str).collect();
    let primary = config.primary.as_str();
    let text = |text: &str| Some(text.to_owned());
    let operation = |name: &str, guidance: OperationGuidance| (name.to_owned(), guidance);
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Each {} holds a version graph of rows of the kinds {}, on its primary line, {primary}, and drafts of it.",
                target.type_name,
                list(&kinds, "and")
            )),
            text("Work happens on a draft at its version and merges into the primary line; a tagged commit can be released, and behaviors.Branches.release holds the number of the latest release."),
            config.sweep.map(|sweep| {
                format!(
                    "Every {} the sweep{} prunes history.",
                    duration(sweep.interval_ms),
                    sweep.abandon_after.map_or(String::new(), |idle| format!(
                        " discards drafts idle for {} and",
                        duration(idle)
                    ))
                )
            }),
        ]),
        operations: vec![
            operation(
                "branch",
                said(
                    Some(format!("Use to start a draft from a ref's head, or without fromRef from {primary}.")),
                    None,
                    text("Returns the draft, with its id and the version its next write names."),
                    errors(&["name_taken"]),
                ),
            ),
            operation(
                "save",
                said(
                    Some(format!(
                        "Use to write rows on a draft at its version: by kind ({}), upsert rows, delete entities or unset the draft's own rows.",
                        list(&names, "and")
                    )),
                    Some(format!("Do not use on {primary}; it takes writes only from merge.")),
                    text("Returns the draft at its new version and the rows stored."),
                    errors(&["version_conflict", "ref_sealed", "primary_merge_only", "entity_not_found"]),
                ),
            ),
            operation(
                "commit",
                said(
                    text("Use to commit what a draft at its version composes to; a tag makes the commit one a release can name."),
                    None,
                    text("Returns the draft at its new version and the commit."),
                    errors(&["version_conflict", "ref_sealed", "primary_merge_only", "nothing_to_commit", "invalid_tree"]),
                ),
            ),
            operation(
                "seal",
                said(
                    text("Use to commit a draft at its version, when it has changes, and close it to writes."),
                    None,
                    text("Returns the draft and the commit, null when there was nothing to commit."),
                    errors(&["version_conflict", "ref_sealed", "primary_merge_only", "invalid_tree"]),
                ),
            ),
            operation(
                "merge",
                said(
                    Some(format!("Use to merge a ref's head into another ref, {primary} among them, at the target's version.")),
                    None,
                    text("Returns the target, the commit and the conflicts; with conflicts left it writes nothing, so resolve them and merge again."),
                    errors(&["version_conflict", "ref_sealed", "merge_into_itself", "invalid_tree"]),
                ),
            ),
            operation(
                "rebase",
                said(
                    text("Use to bring a draft at its version up to its parent's head."),
                    None,
                    text("Returns the draft, the commit and the conflicts; with conflicts left it writes nothing."),
                    errors(&["version_conflict", "ref_sealed", "no_parent", "invalid_tree"]),
                ),
            ),
            operation(
                "revert",
                said(
                    text("Use to make a draft at its version compose to an earlier commit's tree, as a new commit."),
                    None,
                    text("Returns the draft and the commit."),
                    errors(&["version_conflict", "ref_sealed", "primary_merge_only", "invalid_tree"]),
                ),
            ),
            operation(
                "releaseCommit",
                said(
                    text("Use to release a tagged commit: it points the release pointer at it, at the pointer's version, which behaviors.Branches.release holds; 0 for the first release."),
                    None,
                    text("Returns the commit and the release pointer at its new version."),
                    errors(&["version_conflict", "not_tagged"]),
                ),
            ),
            operation(
                "discard",
                said(
                    text("Use to drop a draft at its version, which frees its name."),
                    Some(format!("Do not use on {primary}, the primary line.")),
                    text("Returns the draft, discarded."),
                    errors(&["version_conflict", "primary_line"]),
                ),
            ),
            operation(
                "refs",
                said(
                    Some(format!("Use to list the instance's live refs, {primary} first, with each one's version.")),
                    None,
                    Some(format!("Returns items and next. {PAGE}")),
                    Vec::new(),
                ),
            ),
            operation(
                "releases",
                said(
                    text("Use to read the instance's release log, oldest first."),
                    None,
                    Some(format!("Returns items and next. {PAGE}")),
                    Vec::new(),
                ),
            ),
            operation(
                "compose",
                said(
                    text("Use to read a ref's tree, its uncommitted rows included."),
                    None,
                    text("Returns the tree, its content hash and any findings."),
                    errors(&["walk_ceiling"]),
                ),
            ),
            operation(
                "materialize",
                said(
                    text("Use to read a commit's tree."),
                    None,
                    text("Returns the tree, its content hash and any findings."),
                    errors(&["walk_ceiling"]),
                ),
            ),
            operation(
                "released",
                said(
                    text("Use to read what is released: the tree of the commit the release pointer names."),
                    None,
                    text("Returns the release and its tree; an instance never released is not_found."),
                    errors(&["walk_ceiling"]),
                ),
            ),
            operation(
                "diff",
                said(
                    text("Use to list the entities two commits of the instance differ on."),
                    None,
                    text("Returns the changes."),
                    errors(&["walk_ceiling"]),
                ),
            ),
            operation(
                "history",
                said(
                    text("Use to list the commits a ref wrote, newest first."),
                    None,
                    text("Returns the commits."),
                    Vec::new(),
                ),
            ),
        ],
    }
}
