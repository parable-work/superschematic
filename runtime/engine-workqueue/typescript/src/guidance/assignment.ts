/*
Assignment's guidance: who may assign whom, that an assigned instance's
lease and claim are its assignee's alone, which it adds to Lease's
acquire and Queue's claim when the type composes them, and the filter a
list takes on the assignee.
*/

import type { BehaviorGuidance, DescribeTarget, OperationGuidance } from '@superschematic/engine';

import type { AssignmentConfig } from '../assignment.js';
import { sentences } from './text.js';

const TAKEN: OperationGuidance = {
  doNotUseWhen: 'Do not use on an instance assigned to another principal.',
  errors: [{ code: 'assigned_to_another', commonCorrection: 'Take work assigned to you or to no one, or have it unassigned first.' }],
};

export function assignmentGuidance(config: AssignmentConfig, target: DescribeTarget): BehaviorGuidance {
  const permission = config.permission;
  const names = new Set(target.operations.map((operation) => operation.name));
  const notConfigured = { code: 'not_configured', commonCorrection: 'None: only the assignee or an unassigned caller acts on its own assignment here.' };
  return {
    summary: sentences(
      `Each ${target.type} is assigned to one principal or none; while it is assigned, only its assignee acquires its lease or claims it.`,
      permission === undefined
        ? 'A principal assigns an unassigned instance to itself and unassigns itself; the config names no permission for anything else.'
        : `A principal assigns an unassigned instance to itself and unassigns itself; assigning another principal, reassigning and unassigning another need permission ${permission}.`
    ),
    operations: {
      assign: {
        useWhen:
          permission === undefined
            ? 'Use to assign an unassigned instance to yourself.'
            : `Use to assign an unassigned instance to yourself, or, with permission ${permission}, any instance to another principal.`,
        success: 'Returns the assignee, when and by whom it was assigned.',
        errors: [
          { code: 'already_assigned', commonCorrection: 'None: the instance is already assigned to that principal.' },
          ...(permission === undefined ? [notConfigured] : []),
        ],
      },
      unassign: {
        useWhen:
          permission === undefined
            ? 'Use as the assignee to give the assignment up.'
            : `Use as the assignee to give the assignment up, or with permission ${permission} to unassign another principal.`,
        success: 'Returns the assignee it had.',
        errors: [
          { code: 'not_assigned', commonCorrection: 'None: the instance is not assigned.' },
          ...(permission === undefined ? [notConfigured] : []),
        ],
      },
      list: {
        useWhen: `where: { assignee: <subject> } lists the ${target.type} instances assigned to a principal, assignee: null the unassigned ones, and [null, <subject>] both.`,
      },
      ...(names.has('acquire') ? { acquire: TAKEN } : {}),
      ...(names.has('claim') ? { claim: TAKEN } : {}),
    },
  };
}
