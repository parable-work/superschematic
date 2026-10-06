/*
Reactions' guidance: its rules, each what sets it off and the move it
makes, which the runner makes after a change commits. It adds no
operation, so it gives a summary, and tells a caller of transition and
of the writes that can set a rule off that status may move again on its
own.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { ReactionsConfig, ReactionsRule } from '../reactions.js';
import { list } from './text.js';

// when says what sets one rule off.
function when(rule: ReactionsRule): string {
  const on = rule.when;
  if ('enters' in on) {
    return `when it enters ${on.enters}`;
  }
  if ('allTerminal' in on) {
    const outcomes = on.allTerminal.outcomes === undefined ? '' : ` with outcome ${list(on.allTerminal.outcomes, 'or')}`;
    return `when every ${on.allTerminal.schema} linking here through ${on.allTerminal.link} is in a terminal state${outcomes}`;
  }
  if ('anyTerminal' in on) {
    return `when a ${on.anyTerminal.schema} linking here through ${on.anyTerminal.link} ends with outcome ${list(on.anyTerminal.outcomes, 'or')}`;
  }
  if ('holds' in on) {
    return `when rollup ${on.holds} comes to hold`;
  }
  return `when the target of ${on.revised.link} gains a revision or a release`;
}

// then says the move one rule makes.
function then(rule: ReactionsRule): string {
  return rule.then.link === undefined ? `to ${rule.then.transition}` : `the instance its ${rule.then.link} points to, to ${rule.then.transition}`;
}

export function reactionsGuidance(config: ReactionsConfig, target: DescribeTarget): BehaviorGuidance {
  const rules = config.rules.map((rule) => `${when(rule)}, it moves ${then(rule)}`);
  return {
    summary: `After a change commits, the runner moves statuses by rules, through transition and its guards: ${rules.join('; ')}. A move a guard vetoes, or that no transition allows, is left undone.`,
    operations: {
      transition: {
        success: `Reactions' rules on ${target.type} may move it, or the instances it links to, again after the commit.`,
      },
    },
  };
}
