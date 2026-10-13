/*
Approvals' guidance: which moves of the type's Workflow wait for how many
sign-offs by callers with which permission, whether the creator may sign,
and what consumes a sign-off, so a caller signs before the move and again
after a change; Workflow's transition and update hear the same.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError } from '../../behavior.js';
import type { ApprovalsConfig } from '../approvals.js';
import { capital, list, plural, sentences } from './text.js';

export function approvalsGuidance(config: ApprovalsConfig, target: DescribeTarget): BehaviorGuidance {
  const states = Object.keys(config.gates);
  const needs = states.map((state) => {
    const gate = config.gates[state];
    return `a move into ${state}${gate.from === undefined ? '' : ` from ${list(gate.from, 'or')}`} waits for ${plural(gate.count, 'sign-off')} by callers with permission ${gate.permission}`;
  });
  const excluding = states.filter((state) => config.gates[state].excludeCreator);
  const page = 'Returns items and next; pass next as cursor for the page after, until it is null.';
  const noGate: GuidanceError = { code: 'no_gate', commonCorrection: `Name a gated state: ${list(states, 'or')}.` };
  return {
    summary: sentences(
      `${capital(needs.join('; '))}.`,
      excluding.length === states.length
        ? `The principal who created a ${target.type} cannot sign off on it.`
        : excluding.length > 0
          ? `The principal who created a ${target.type} cannot sign off on it for ${list(excluding, 'or')}.`
          : undefined,
      'behaviors.Approvals.gates lists the sign-offs that count, by state.',
      config.resetOnChange
        ? 'A change of the own fields consumes every sign-off, and entering a gated state consumes its own, so each is signed again after.'
        : 'Entering a gated state consumes its sign-offs, so a return to it is signed again; a change of the own fields keeps them.'
    ),
    operations: {
      signOff: {
        useWhen: `Use to sign off for ${list(states, 'or')}, as the caller; it needs the gate's permission, else the call is forbidden.`,
        doNotUseWhen: 'Do not use while the instance is in the state, or to sign twice.',
        success: 'Returns the sign-off, the subjects whose sign-offs for the state count (signedBy) and the count the gate needs.',
        errors: [
          noGate,
          { code: 'in_state', commonCorrection: 'Wait until the instance has left the state; a sign-off counts toward the next move into it.' },
          ...(excluding.length > 0
            ? [{ code: 'creator_cannot_sign', commonCorrection: 'Ask another principal with the permission to sign off.' }]
            : []),
          { code: 'already_signed', commonCorrection: 'Leave it: the caller has signed off for the state already.' },
        ],
      },
      withdrawSignOff: {
        useWhen: "Use to take back the caller's own sign-off for a gated state before the move it waits for.",
        success: 'Returns the sign-off withdrawn; it no longer counts.',
        errors: [noGate, { code: 'not_signed', commonCorrection: 'None: the caller has no sign-off for the state that counts.' }],
      },
      listSignOffs: {
        useWhen: "Use to read the instance's sign-offs, oldest first, the consumed ones with consumedAt; with state, only those for it.",
        doNotUseWhen: 'Do not page through them for the ones that count; read behaviors.Approvals.gates.',
        success: page,
      },
      transition: {
        doNotUseWhen: `Read behaviors.Approvals.gates before a move: ${needs.join('; ')}.`,
        errors: [
          {
            code: 'approvals_missing',
            description: 'A move into a gated state with fewer sign-offs than its count; details.details gives the state, the count it requires and the sign-offs it has.',
            commonCorrection: "Ask callers with the gate's permission to sign off, then try again.",
          },
        ],
      },
      ...(config.resetOnChange ? { update: { success: 'A change of the own fields consumes every sign-off: each gate is signed again before its move.' } } : {}),
    },
  };
}
