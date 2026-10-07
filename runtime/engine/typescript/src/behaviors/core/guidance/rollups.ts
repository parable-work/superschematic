/*
Rollups' guidance: what each rollup computes and over which link, and
which Workflow moves wait for an all or any rollup to hold.
*/

import type { BehaviorGuidance } from '../../behavior.js';
import type { RollupSpec, RollupsConfig } from '../rollups.js';
import { capital, list, sentences } from './text.js';

// computes says what one rollup's value is.
function computes(spec: RollupSpec): string {
  const over = `the ${spec.schema} instances whose ${spec.link} points here`;
  const terminal = spec.outcomes === undefined ? 'a terminal state' : `a terminal state with outcome ${list(spec.outcomes, 'or')}`;
  switch (spec.function) {
    case 'count':
      return `how many of ${over} there are`;
    case 'countBy':
      return `how many of ${over} hold each value of ${String(spec.field)}`;
    case 'sum':
    case 'min':
    case 'max':
      return `the ${spec.function} of ${String(spec.field)} over ${over}`;
    case 'latest':
      return `the ${String(spec.field)} of the one of ${over} created last`;
    case 'all':
      return `whether every one of ${over} is in ${terminal}`;
    case 'any':
      return `whether one of ${over} is in ${terminal}`;
  }
}

export function rollupsGuidance(config: RollupsConfig): BehaviorGuidance {
  const names = Object.keys(config.rollups);
  const gates = names.filter((name) => config.rollups[name].gatedStates.length > 0);
  const gating = gates.map((name) => `a move into ${list(config.rollups[name].gatedStates, 'or')} waits until ${name} holds`);
  return {
    summary: sentences(
      `The rollups field holds values computed from linked instances at each read: ${names.map((name) => `${name}, ${computes(config.rollups[name])}`).join('; ')}.`,
      'A rollup over more than 500 linked instances reads {"over": true}.',
      gates.length > 0 ? `${capital(gating.join('; '))}.` : undefined
    ),
    operations:
      gates.length > 0
        ? {
            transition: {
              doNotUseWhen: `Read rollups before a move: ${gating.join('; ')}.`,
              errors: [
                {
                  code: 'not_held',
                  description: 'A move into a state a rollup gates while the rollup does not hold; details names the rollup and counts the linked instances it read and counted.',
                  commonCorrection: 'Read rollups, and finish the linked work the rollup waits on, then try again.',
                },
              ],
            },
          }
        : {},
  };
}
