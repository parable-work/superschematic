/*
Reactions' guidance: its rules, each what sets it off and the move it
makes, which the runner makes after a change commits. It adds no
operation, so it gives a summary, and tells a caller of transition and
of the writes that can set a rule off that status may move again on its
own: for a changes rule, a create and an update when its field is an own
field, and the writing operations of the behavior whose field it is.
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
  if ('changes' in on) {
    const { field, to, from } = on.changes;
    return `when ${field} changes${from === undefined ? '' : ` from ${JSON.stringify(from)}`}${to === undefined ? '' : ` to ${JSON.stringify(to)}`}`;
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

// moving lists the operations of the type that can move a changes rule's
// field: a create and an update for an own field, the writing operations
// of the behavior whose field it is.
function moving(config: ReactionsConfig, target: DescribeTarget): string[] {
  const names = new Set<string>();
  for (const rule of config.rules) {
    if (!('changes' in rule.when)) {
      continue;
    }
    const behavior = config.fields[rule.when.changes.field]?.behavior;
    for (const operation of target.operations) {
      if (behavior === undefined ? operation.name === 'create' || operation.name === 'update' : operation.behavior === behavior && operation.writes) {
        names.add(operation.name);
      }
    }
  }
  return [...names];
}

export function reactionsGuidance(config: ReactionsConfig, target: DescribeTarget): BehaviorGuidance {
  const rules = config.rules.map((rule) => `${when(rule)}, it moves ${then(rule)}`);
  const success = `Reactions' rules on ${target.type} may move it, or the instances it links to, again after the commit.`;
  const operations = ['transition', ...moving(config, target).filter((name) => name !== 'transition')];
  return {
    summary: `After a change commits, the runner moves statuses by rules, through transition and its guards: ${rules.join('; ')}. A move a guard vetoes, or that no transition allows, is left undone.`,
    operations: Object.fromEntries(operations.map((name) => [name, { success }])),
  };
}
