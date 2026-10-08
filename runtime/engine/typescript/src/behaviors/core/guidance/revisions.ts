/*
Revisions' guidance: every change of the own fields records a revision,
which getRevision reads by number, and with a review step a change may be
proposed, with evidence, and settled by a caller with the review
permission, while pendingProposals counts the ones waiting. Without one,
the review operations are refused, which their guidance says.
*/

import type { BehaviorGuidance, DescribeTarget, OperationGuidance } from '../../behavior.js';
import type { RevisionsConfig } from '../revisions.js';

const NO_REVIEW = {
  code: 'no_review',
  commonCorrection: 'None on this type: change the instance with update instead.',
};

export function revisionsGuidance(config: RevisionsConfig, target: DescribeTarget): BehaviorGuidance {
  const review = config.review;
  const page = 'Returns items and next; pass next as cursor for the page after, until it is null.';
  const refused = (what: string): OperationGuidance => ({
    doNotUseWhen: `Do not use: ${target.type} has no review step, so ${what} is refused (no_review).`,
    errors: [NO_REVIEW],
  });
  const pending = {
    code: 'not_pending',
    commonCorrection: 'Read the proposal with listProposals: someone has already settled it.',
  };
  return {
    summary:
      review === undefined
        ? `Every change of the own fields of a ${target.type} records a numbered revision of them; behaviors.Revisions.revision holds the latest. There is no review step.`
        : `Every change of the own fields of a ${target.type} records a numbered revision of them; behaviors.Revisions.revision holds the latest. A change may also be proposed, and a caller with permission ${review.permission} approves or rejects it; behaviors.Revisions.pendingProposals counts the proposals waiting.`,
    operations: {
      listRevisions: {
        useWhen: "Use to read the instance's revisions, oldest first: each its number and the own fields as that change left them.",
        doNotUseWhen: 'Do not page through them for one revision; call getRevision with its number.',
        success: page,
      },
      getRevision: {
        useWhen: 'Use to read one revision by its number, the one a pinned link or a proposal names say: the own fields as that change left them.',
        success: 'Returns the revision; a number the instance has not reached is not_found.',
      },
      propose:
        review === undefined
          ? refused('a proposal')
          : {
              useWhen: `Use to propose a change of the own fields, as a JSON merge patch, for a caller with permission ${review.permission} to approve. evidence cites instances the proposer may read, each optionally at a revision it has had.`,
              doNotUseWhen: 'Do not use to change the instance at once; call update.',
              success: 'Returns the pending proposal with its id and its evidence; the instance does not change until it is approved.',
            },
      approve:
        review === undefined
          ? refused('an approval')
          : {
              useWhen: `Use to apply a pending proposal; it needs permission ${review.permission}, else the call is forbidden.`,
              success: 'Returns the proposal, approved, with the revision its patch made.',
              errors: [pending],
            },
      reject:
        review === undefined
          ? refused('a rejection')
          : {
              useWhen: `Use to turn a pending proposal down, with an optional reason; it needs permission ${review.permission}, else the call is forbidden.`,
              success: 'Returns the proposal, rejected; the instance does not change.',
              errors: [pending],
            },
      listProposals:
        review === undefined
          ? refused('listing proposals')
          : {
              useWhen: "Use to read the instance's proposals, oldest first, each with the evidence it cites; with state, only the pending, approved or rejected ones.",
              success: page,
            },
      update: { success: 'A change of the own fields records the next revision.' },
    },
  };
}
