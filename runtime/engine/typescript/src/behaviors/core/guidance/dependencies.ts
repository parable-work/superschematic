/*
Dependencies' guidance: which schemas a blocker comes from, which
Workflow moves wait on blockers and which outcomes finish one, and the
narrowed create parameters: the blocker schemas the config allows, and a
blocker's schema required when the type's own schema is not among them.
*/

import type { BehaviorGuidance, DescribeTarget } from '../../behavior.js';
import type { JSONSchema } from '../../declaration.js';
import type { DependenciesConfig } from '../dependencies.js';
import { list, sentences } from './text.js';

// from says where a blocker comes from, and what an absent schema means.
function from(config: DependenciesConfig, target: DescribeTarget): string {
  const own = config.schemas.includes(target.schema);
  return own
    ? `an instance of ${list(config.schemas, 'or')} (the instance's own schema when schema is absent)`
    : `an instance of ${list(config.schemas, 'or')}, named by schema`;
}

export function dependenciesGuidance(config: DependenciesConfig, target: DescribeTarget): BehaviorGuidance {
  const gated = config.gatedStates;
  const finishes = `a terminal state of its own Workflow whose outcome is ${list(config.satisfiedBy, 'or')}`;
  const edgeErrors = [
    { code: 'already_blocking', commonCorrection: 'Leave it: that instance already blocks this one.' },
    { code: 'cycle', commonCorrection: 'Remove the edge that makes the other instance wait on this one first, or leave this edge out.' },
    ...(config.terminalGatedStates.length > 0
      ? [{ code: 'gated', commonCorrection: 'Add only finished blockers to an instance in a gated state no transition leaves, or none.' }]
      : []),
  ];
  return {
    summary: sentences(
      `Each ${target.type} waits on blockers, each ${from(config, target)}.`,
      gated.length > 0
        ? `A transition into ${list(gated, 'or')} waits until every blocker has finished: its status is ${finishes}. blocked says whether one is open.`
        : 'No state is gated, so blockers hold up no transition; blocked says whether one is open.'
    ),
    operations: {
      addBlocker: {
        useWhen: sentences(
          `Use to make ${from(config, target)} block this one.`,
          gated.length > 0 ? `It holds up the transition into ${list(gated, 'or')} until it finishes.` : undefined
        ),
        doNotUseWhen: 'Do not use at create; give the blockers in behaviors.Dependencies.blockers instead.',
        success: 'Returns the blocker, with its status and whether it is open; blocked is true while one is.',
        errors: edgeErrors,
      },
      removeBlocker: {
        useWhen: 'Use to stop an instance blocking this one, as when a blocker failed or was cancelled and the work goes on without it.',
        success: 'Returns the blocker it removed; blocked is false once no open blocker is left.',
      },
      listBlockers: {
        useWhen: "Use to read the instance's blockers, each with its status and whether it is open.",
        success: 'Returns items and next; pass next as cursor for the page after, until it is null.',
      },
      listDependents: {
        useWhen: 'Use to read the instances this one blocks, in schemas the caller may read.',
        success: 'Returns items and next; pass next as cursor for the page after, until it is null.',
      },
      create: {
        useWhen: `behaviors.Dependencies.blockers gives the instance its blockers from the create, at most 500, each ${from(config, target)}.`,
        errors: edgeErrors,
      },
      ...(gated.length > 0
        ? {
            transition: {
              doNotUseWhen: `Do not move into ${list(gated, 'or')} while blocked is true.`,
              errors: [
                {
                  code: 'blocked',
                  description: `A move into ${list(gated, 'or')} while a blocker is open; details.details.blockers lists the open ones.`,
                  commonCorrection: 'Wait for the open blockers to finish, or remove the ones the work goes on without, then try again.',
                },
              ],
            },
          }
        : {}),
    },
  };
}

/**
 * dependenciesCreateParams is Dependencies' create parameters under its
 * config: each blocker's schema one the config lists, and required when
 * the type's own schema is not among them, which an absent schema names.
 */
export function dependenciesCreateParams(config: DependenciesConfig, target: DescribeTarget): JSONSchema {
  const own = config.schemas.includes(target.schema);
  return {
    type: 'object',
    additionalProperties: false,
    properties: {
      blockers: {
        description: `The instance's blockers from its create, each held to addBlocker's rules: an instance of ${list(config.schemas, 'or')}.`,
        type: 'array',
        maxItems: 500,
        items: {
          type: 'object',
          additionalProperties: false,
          required: own ? ['id'] : ['id', 'schema'],
          properties: {
            schema: {
              description: own ? `The blocker's schema; ${target.schema} when absent.` : "The blocker's schema.",
              type: 'string',
              enum: [...config.schemas],
            },
            id: { description: "The blocker's id.", type: 'string', pattern: '^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$' },
          },
        },
      },
    },
  };
}
