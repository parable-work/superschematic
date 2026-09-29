// The engine's named errors and their stable codes, which every language's
// engine shares and the scenario files name (runtime/versiongraph/README.md,
// "Engine and storage").

import type { Finding } from "./contract.js";

/** The stable codes of the engine's named errors. */
export type EngineErrorCode =
  | "not_found"
  | "version_conflict"
  | "name_taken"
  | "no_actor"
  | "ref_sealed"
  | "nothing_to_commit"
  | "walk_ceiling"
  | "schema_epoch"
  | "entity_not_found"
  | "history_missing"
  | "invalid_tree"
  | "root_mismatch"
  | "merge_into_itself"
  | "primary_merge_only"
  | "not_tagged"
  | "no_parent";

/** An error the engine names. `code` is stable across languages. */
export class EngineError extends Error {
  readonly code: EngineErrorCode;

  constructor(code: EngineErrorCode, message: string) {
    super(message);
    this.name = "EngineError";
    this.code = code;
  }
}

function named(code: EngineErrorCode, name: string, defaultMessage: string) {
  return class extends EngineError {
    constructor(message = defaultMessage) {
      super(code, message);
      this.name = name;
    }
  };
}

/** A write with no actor to record. */
export class NoActorError extends named("no_actor", "NoActorError", "the write has no actor") {}

/** A write through a sealed ref. */
export class RefSealedError extends named("ref_sealed", "RefSealedError", "the ref is sealed") {}

/** Commit of a ref that composes to the tree of its last commit (or of its base). */
export class NothingToCommitError extends named("nothing_to_commit", "NothingToCommitError", "nothing to commit") {}

/** Reading a commit's tree would walk more parent commits than the engine's walk ceiling. */
export class WalkCeilingError extends named("walk_ceiling", "WalkCeilingError", "the commit walk passed its ceiling") {}

/** A commit written at a newer schema epoch than the engine's. */
export class SchemaEpochError extends named(
  "schema_epoch",
  "SchemaEpochError",
  "the commit is from a newer schema epoch",
) {}

/** An edit that deletes an entity the ref does not hold, or unsets an override the ref does not have. */
export class EntityNotFoundError extends named("entity_not_found", "EntityNotFoundError", "entity not found on the ref") {}

/** A row version a commit names is no longer in its table's history. */
export class HistoryMissingError extends named(
  "history_missing",
  "HistoryMissingError",
  "a row version a commit names is missing from history",
) {}

/** Two refs, or a ref and a commit, of one operation belong to different roots. */
export class RootMismatchError extends named(
  "root_mismatch",
  "RootMismatchError",
  "the ref and the commit or ref it names have different roots",
) {}

/** A merge whose source is its target. */
export class MergeIntoItselfError extends named(
  "merge_into_itself",
  "MergeIntoItselfError",
  "a ref cannot be merged into itself",
) {}

/** A save, commit, seal or revert on a primary line, which takes writes only from a merge. */
export class PrimaryMergeOnlyError extends named(
  "primary_merge_only",
  "PrimaryMergeOnlyError",
  "a primary line takes writes only from a merge",
) {}

/** A release of a commit that is not tagged. */
export class NotTaggedError extends named("not_tagged", "NotTaggedError", "the commit is not tagged") {}

/** A rebase of a primary line, which has no parent to rebase onto. */
export class NoParentError extends named(
  "no_parent",
  "NoParentError",
  "a primary line has no parent to rebase onto",
) {}

/** A commit whose tree breaks the graph's rules: what the core's validate found. */
export class InvalidTreeError extends EngineError {
  readonly findings: Finding[];

  constructor(findings: Finding[]) {
    super(
      "invalid_tree",
      "the tree breaks the graph's rules: " + findings.map((f) => `${f.kind}: ${f.message}`).join("; "),
    );
    this.name = "InvalidTreeError";
    this.findings = findings;
  }
}
