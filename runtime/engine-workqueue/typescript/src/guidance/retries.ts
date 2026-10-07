/*
Retries' guidance: the failure classes and their caps, the total, what
exhaustion does, and the refusals its guard adds once an instance is
exhausted (Workflow's transition, Lease's acquire, Queue's claim) and to
updates of its limits field.
*/

import type { BehaviorGuidance, DescribeTarget, OperationGuidance } from '@superschematic/engine';

import type { RetriesConfig } from '../retries.js';
import { list, sentences } from './text.js';

export function retriesGuidance(config: RetriesConfig, target: DescribeTarget): BehaviorGuidance {
  const classes = Object.keys(config.classes).map((name) => {
    const spec = config.classes[name];
    return spec === 'terminal' ? `${name} (terminal: one failure exhausts)` : `${name} (${spec.attempts} attempt${spec.attempts === 1 ? '' : 's'})`;
  });
  const operations = new Set(target.operations.map((operation) => operation.name));
  const exhausted: OperationGuidance = {
    doNotUseWhen: 'Do not use once retries.exhausted is true.',
    errors: [{ code: 'exhausted', commonCorrection: 'None: the instance takes no more attempts; create new work instead.' }],
  };
  const limits = config.limitsField;
  return {
    summary: sentences(
      `Failure classes: ${list(classes)}; at most ${config.totalAttempts} counted failure${config.totalAttempts === 1 ? '' : 's'} in all${limits === undefined ? '' : `, or the caps the instance's ${limits} holds`}.`,
      `Once exhausted, status moves from ${list(config.from, 'or')} to ${config.exhaustedState}, and the ${target.type} takes no more attempts, moves only to ${config.exhaustedState}, and is not acquired or claimed.`,
      config.stuckAfter === undefined ? undefined : `${config.stuckAfter} failures in a row with the same signature exhaust it as stuck.`,
      config.resultField === undefined ? undefined : `A kept attempt's result is written to ${config.resultField}${config.keepBest === undefined ? '' : `; a failure's is kept when its score beats the best by ${config.keepBest.minDelta}`}.`
    ),
    operations: {
      recordAttempt: {
        useWhen: sentences(
          `Use after each attempt: without failure it is a success; with failure, one of ${list(Object.keys(config.classes), 'or')}.`,
          config.permission === undefined ? undefined : `It needs permission ${config.permission}.`
        ),
        success: "Returns the counts, whether the instance is exhausted or stuck, and the failure class's hint for the next attempt.",
        errors: exhausted.errors,
      },
      transition: {
        doNotUseWhen: `Do not move an exhausted instance anywhere but ${config.exhaustedState}.`,
        errors: exhausted.errors,
      },
      list: {
        useWhen: `where: { "retries.exhausted": true } lists the ${target.type} instances whose retries are exhausted, and false the ones that may run again.`,
      },
      ...(operations.has('acquire') ? { acquire: exhausted } : {}),
      ...(operations.has('claim') ? { claim: exhausted } : {}),
      ...(limits === undefined
        ? {}
        : {
            update: {
              doNotUseWhen: `Do not change ${limits}${config.limitsPermission === undefined ? '' : ` without permission ${config.limitsPermission}`}${config.leased ? ', nor as the holder of its lease' : ''}.`,
              errors: [
                ...(config.leased ? [{ code: 'limits_fixed', commonCorrection: 'None: a worker never raises its own caps; an operator does.' }] : []),
                ...(config.limitsPermission === undefined ? [{ code: 'not_configured', commonCorrection: `None: ${limits} does not change once the instance exists.` }] : []),
              ],
            },
          }),
    },
  };
}
