/*
Workflow's guidance: the states, where an instance starts, which moves
leave each state and which of them need a permission, and the outcome of
each terminal state, so a caller of transition knows which `to` it may
name from where the instance is.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { WorkflowConfig } from '../workflow.js';
import { capital, list, sentences } from './text.js';

/** The moves that leave one state, in the config's order. */
function moves(config: WorkflowConfig, from: string): WorkflowConfig['transitions'] {
  return config.transitions.filter((transition) => transition.from === from);
}

export function workflowGuidance(config: WorkflowConfig, target: DescribeTarget): BehaviorGuidance {
  const terminal = config.states.filter((state) => moves(config, state).length === 0);
  const leaving = config.states.filter((state) => moves(config, state).length > 0);
  const outcomes = terminal.map((state) => `${state} (${config.outcomes[state] ?? 'success'})`);
  const routes = leaving.map((state) => `from ${state} to ${list(moves(config, state).map((move) => move.to), 'or')}`);
  // The moves each permission guards, by permission in the config's order.
  const guarded = new Map<string, string[]>();
  for (const move of config.transitions) {
    if (move.permission !== undefined) {
      guarded.set(move.permission, [...(guarded.get(move.permission) ?? []), `${move.from} to ${move.to}`]);
    }
  }
  const permissions = [...guarded].map(([permission, guardedMoves]) =>
    guardedMoves.length === 1 ? `the move ${guardedMoves[0]} needs permission ${permission}` : `the moves ${list(guardedMoves)} need permission ${permission}`
  );
  return {
    summary: sentences(
      `Its status is one of ${list(config.states)}; a new ${target.type} starts in ${config.initial}, and status moves only by transition.`,
      terminal.length > 0
        ? `Terminal, with their outcomes: ${list(outcomes)}.`
        : 'No state is terminal: a transition leaves every state.'
    ),
    operations: {
      transition: {
        useWhen: sentences(
          leaving.length > 0 ? `Use to move status ${routes.join('; ')}.` : undefined,
          permissions.length > 0 ? `${capital(permissions.join('; '))}; without it the call is forbidden.` : undefined
        ),
        doNotUseWhen: sentences(
          terminal.length > 0 ? `Do not use from ${list(terminal, 'or')}: no transition leaves a terminal state.` : undefined,
          'Do not use to stay where the instance is.'
        ),
        success: 'Returns from and to; status holds to from then on.',
        errors: [
          {
            code: 'transition_not_allowed',
            description: "No transition leads from the instance's status to the state named; details.allowed lists the states it can move to.",
            commonCorrection: 'Read status, then name a state details.allowed lists, or move through one of them first.',
          },
          { code: 'already_in_state', commonCorrection: 'Read status: the instance is already there, so nothing needs to move.' },
          ...(terminal.length > 0
            ? [{ code: 'terminal_state', commonCorrection: 'None: the work has ended. Create a new instance to start again.' }]
            : []),
          { code: 'no_status', commonCorrection: 'None: an instance without a status cannot move.' },
        ],
      },
      create: { success: `A new instance's status is ${config.initial}.` },
      list: {
        useWhen: `where: { "Workflow.status": <state> } lists the instances in one of ${list(config.states, 'or')}, and a list of states the instances in any of them.`,
      },
      update: { doNotUseWhen: "Do not use to move Workflow's status, which behaviors.Workflow holds; call transition." },
    },
  };
}
