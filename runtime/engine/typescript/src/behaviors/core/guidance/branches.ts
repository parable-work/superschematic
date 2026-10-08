/*
Branches' guidance: the kinds of rows an instance's version graph holds,
its primary line, and for each operation which refs it writes through
and which of the version graph's refusals it can meet. Every write names
the ref's version, so a stale one is version_conflict: read the ref
again.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError } from '../../behavior.js';
import type { BranchesConfig } from '../branches.js';
import { duration, list, sentences } from './text.js';

const CORRECTIONS: Readonly<Record<string, string>> = {
  version_conflict: 'Read the ref with refs (or the release log with releases) and try again at its version.',
  name_taken: 'Pick another name, or discard the live ref that has it.',
  ref_sealed: 'Branch a new draft from the sealed one and write there.',
  primary_merge_only: 'Write on a draft, then merge it into the primary line.',
  nothing_to_commit: 'None: the draft has nothing new; save rows first.',
  entity_not_found: "Read the draft with compose and name only entities it holds.",
  invalid_tree: 'Fix the rows details.findings names, then try again.',
  merge_into_itself: 'Name two different refs.',
  no_parent: 'None: a primary line has no parent; rebase a draft.',
  not_tagged: 'Commit with a tag, then release that commit.',
  walk_ceiling: 'Read a commit nearer to a snapshot, or wait for the sweep to write one.',
  primary_line: 'None: the primary line is not discarded.',
};

function errors(...codes: string[]): GuidanceError[] {
  return codes.map((code) => ({ code, commonCorrection: CORRECTIONS[code] }));
}

const PAGE = 'Pass next as cursor for the page after, until it is null.';

export function branchesGuidance(config: BranchesConfig, target: DescribeTarget): BehaviorGuidance {
  const kinds = Object.keys(config.kinds).map((name) => `${name} (${config.kinds[name].type}${config.kinds[name].singleton ? ', one per tree' : ''})`);
  const primary = config.primary;
  return {
    summary: sentences(
      `Each ${target.type} holds a version graph of rows of the kinds ${list(kinds)}, on its primary line, ${primary}, and drafts of it.`,
      'Work happens on a draft at its version and merges into the primary line; a tagged commit can be released, and behaviors.Branches.release holds the number of the latest release.',
      config.sweep === undefined
        ? undefined
        : `Every ${duration(config.sweep.intervalMs)} the sweep${config.sweep.abandonAfter === undefined ? '' : ` discards drafts idle for ${duration(config.sweep.abandonAfter)} and`} prunes history.`
    ),
    operations: {
      branch: {
        useWhen: `Use to start a draft from a ref's head, or without fromRef from ${primary}.`,
        success: 'Returns the draft, with its id and the version its next write names.',
        errors: errors('name_taken'),
      },
      save: {
        useWhen: `Use to write rows on a draft at its version: by kind (${list(Object.keys(config.kinds))}), upsert rows, delete entities or unset the draft's own rows.`,
        doNotUseWhen: `Do not use on ${primary}; it takes writes only from merge.`,
        success: 'Returns the draft at its new version and the rows stored.',
        errors: errors('version_conflict', 'ref_sealed', 'primary_merge_only', 'entity_not_found'),
      },
      commit: {
        useWhen: 'Use to commit what a draft at its version composes to; a tag makes the commit one a release can name.',
        success: 'Returns the draft at its new version and the commit.',
        errors: errors('version_conflict', 'ref_sealed', 'primary_merge_only', 'nothing_to_commit', 'invalid_tree'),
      },
      seal: {
        useWhen: 'Use to commit a draft at its version, when it has changes, and close it to writes.',
        success: 'Returns the draft and the commit, null when there was nothing to commit.',
        errors: errors('version_conflict', 'ref_sealed', 'primary_merge_only', 'invalid_tree'),
      },
      merge: {
        useWhen: `Use to merge a ref's head into another ref, ${primary} among them, at the target's version.`,
        success: 'Returns the target, the commit and the conflicts; with conflicts left it writes nothing, so resolve them and merge again.',
        errors: errors('version_conflict', 'ref_sealed', 'merge_into_itself', 'invalid_tree'),
      },
      rebase: {
        useWhen: "Use to bring a draft at its version up to its parent's head.",
        success: 'Returns the draft, the commit and the conflicts; with conflicts left it writes nothing.',
        errors: errors('version_conflict', 'ref_sealed', 'no_parent', 'invalid_tree'),
      },
      revert: {
        useWhen: "Use to make a draft at its version compose to an earlier commit's tree, as a new commit.",
        success: 'Returns the draft and the commit.',
        errors: errors('version_conflict', 'ref_sealed', 'primary_merge_only', 'invalid_tree'),
      },
      releaseCommit: {
        useWhen: "Use to release a tagged commit: it points the release pointer at it, at the pointer's version, which behaviors.Branches.release holds; 0 for the first release.",
        success: 'Returns the commit and the release pointer at its new version.',
        errors: errors('version_conflict', 'not_tagged'),
      },
      discard: {
        useWhen: 'Use to drop a draft at its version, which frees its name.',
        doNotUseWhen: `Do not use on ${primary}, the primary line.`,
        success: 'Returns the draft, discarded.',
        errors: errors('version_conflict', 'primary_line'),
      },
      refs: { useWhen: `Use to list the instance's live refs, ${primary} first, with each one's version.`, success: `Returns items and next. ${PAGE}` },
      releases: { useWhen: "Use to read the instance's release log, oldest first.", success: `Returns items and next. ${PAGE}` },
      compose: { useWhen: "Use to read a ref's tree, its uncommitted rows included.", success: 'Returns the tree, its content hash and any findings.', errors: errors('walk_ceiling') },
      materialize: { useWhen: "Use to read a commit's tree.", success: 'Returns the tree, its content hash and any findings.', errors: errors('walk_ceiling') },
      released: {
        useWhen: 'Use to read what is released: the tree of the commit the release pointer names.',
        success: 'Returns the release and its tree; an instance never released is not_found.',
        errors: errors('walk_ceiling'),
      },
      diff: { useWhen: 'Use to list the entities two commits of the instance differ on.', success: 'Returns the changes.', errors: errors('walk_ceiling') },
      history: { useWhen: 'Use to list the commits a ref wrote, newest first.', success: 'Returns the commits.' },
    },
  };
}
