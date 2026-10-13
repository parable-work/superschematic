/*
StateRules' guidance: what each state requires and freezes, who may
change a frozen field, and what a create, a transition, an update and a
Revisions approval meet under the rules.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance } from '../../behavior.js';
import type { StateRule, StateRulesConfig } from '../staterules.js';
import { capital, list, sentences } from './text.js';

// ruleText says one state's rules as a sentence.
function ruleText(state: string, rule: StateRule): string {
  const parts: string[] = [];
  if (rule.required.length > 0) {
    parts.push(`entering ${state} needs ${list(rule.required)}, which ${rule.required.length === 1 ? 'stays' : 'stay'} set while it is ${state}`);
  }
  if (rule.frozenAll) {
    parts.push(`no field changes while it is ${state}`);
  } else if (rule.frozen.length > 0) {
    parts.push(`${list(rule.frozen)} ${rule.frozen.length === 1 ? 'does' : 'do'} not change while it is ${state}`);
  }
  return `${capital(parts.join('; '))}.`;
}

export function stateRulesGuidance(config: StateRulesConfig, target: DescribeTarget): BehaviorGuidance {
  const rules = Object.entries(config.states);
  const entering = rules.filter(([, rule]) => rule.required.length > 0);
  const freezing = rules.filter(([, rule]) => rule.frozen.length > 0);
  const initial = config.initial === undefined ? undefined : rules.find(([state]) => state === config.initial)?.[1];
  const createRequires = initial !== undefined && initial.required.length > 0 ? initial.required : [];
  const who =
    config.permission === undefined ? 'Nobody changes a frozen field.' : `Only a caller with permission ${config.permission} changes a frozen field.`;
  const missing: GuidanceError = {
    code: 'missing_fields',
    description: `A move into ${list(entering.map(([state]) => state), 'or')} while a field it requires is absent or null; details.details.fields lists them.`,
    commonCorrection: 'Update the instance to give the fields the state requires, then move it.',
  };
  const updateErrors: GuidanceError[] = [
    ...(entering.length > 0
      ? [
          {
            code: 'required_fields',
            description: "An update that clears a field the instance's state requires; details.details.fields lists them.",
            commonCorrection: 'Give the field a value instead of clearing it, or move the instance to a state that does not require it first.',
          },
        ]
      : []),
    ...(freezing.length > 0
      ? [
          {
            code: 'frozen_fields',
            description: "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            commonCorrection:
              config.permission === undefined
                ? 'Leave the field as it is, or move the instance to a state that does not freeze it first.'
                : `Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission ${config.permission}.`,
          },
        ]
      : []),
  ];
  // What an update leaves alone in each state: a field it requires, one
  // it freezes, or one it does both to, named once.
  const updateRules = rules.map(([state, rule]) => {
    const same = !rule.frozenAll && rule.required.length === rule.frozen.length && rule.required.every((field) => rule.frozen.includes(field));
    const parts = same
      ? [`change or clear ${list(rule.required, 'or')}`]
      : [
          rule.required.length > 0 ? `clear ${list(rule.required, 'or')}` : undefined,
          rule.frozen.length > 0 ? `change ${rule.frozenAll ? 'any field' : list(rule.frozen, 'or')}` : undefined,
        ].filter((part): part is string => part !== undefined);
    return `while it is ${state}, do not ${parts.join(' or ')}`;
  });
  const permitted =
    config.permission === undefined || freezing.length === 0
      ? ''
      : ` (a caller with permission ${config.permission} may change a frozen field${entering.length > 0 ? ', but not clear a required one' : ''})`;
  const approves = target.operations.some((operation) => operation.name === 'approve' && operation.behavior === 'Revisions');
  const operations: Record<string, OperationGuidance> = {
    update: {
      doNotUseWhen: `${capital(updateRules.join('; '))}${permitted}.`,
      errors: updateErrors,
    },
    ...(entering.length > 0
      ? {
          transition: {
            doNotUseWhen: `Do not move into ${entering.map(([state, rule]) => `${state} before ${list(rule.required)} ${rule.required.length === 1 ? 'holds a value' : 'hold values'}`).join(', or ')}.`,
            errors: [missing],
          },
        }
      : {}),
    ...(createRequires.length > 0
      ? {
          create: {
            useWhen: `Give ${list(createRequires)} at create: ${config.initial}, the state a new ${target.type} starts in, requires ${createRequires.length === 1 ? 'it' : 'them'}, and a create without ${createRequires.length === 1 ? 'it' : 'them'} is refused as invalid_instance, with rule required at the field.`,
          },
        }
      : {}),
    ...(approves
      ? {
          approve: {
            doNotUseWhen: "Do not approve a proposal whose patch breaks the rules of the instance's state: propose accepts it, and approve is refused.",
            errors: updateErrors,
          },
        }
      : {}),
  };
  return {
    summary: sentences(
      ...rules.map(([state, rule]) => ruleText(state, rule)),
      freezing.length > 0 ? who : undefined,
      'A field is held when it is neither absent nor null.'
    ),
    operations,
  };
}
