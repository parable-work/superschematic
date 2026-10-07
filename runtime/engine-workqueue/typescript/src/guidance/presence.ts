/*
Presence's guidance: who beats an instance and how often, what a miss
does to the status and to the principal's leases, and that the field
naming the principal does not change once set.
*/

import type { BehaviorGuidance, DescribeTarget } from '@superschematic/engine';

import type { PresenceConfig, PresenceTransition } from '../presence.js';
import { duration, list, sentences } from './text.js';

function moves(rule: PresenceTransition): string {
  return `moves status from ${list(rule.from, 'or')} to ${rule.transition}`;
}

export function presenceGuidance(config: PresenceConfig, target: DescribeTarget): BehaviorGuidance {
  const field = config.principalField;
  return {
    summary: sentences(
      `Each ${target.type} stands for the principal ${field} names, which beats it at least every ${duration(config.ttlMs)}; the runner misses one past its deadline within ${duration(config.sweepMs)}.`,
      config.onMissed === undefined ? undefined : `A miss ${moves(config.onMissed)}.`,
      config.releaseLeases.length > 0
        ? `A miss expires the principal's leases on ${list(config.releaseLeases)}, but those renewed since its last beat.`
        : undefined,
      config.onBeat === undefined ? undefined : `A beat ${moves(config.onBeat)}.`
    ),
    operations: {
      beat: {
        useWhen: `Use as the principal ${field} names, at least every ${duration(config.ttlMs)}, to show it is alive.`,
        success: 'Returns the new deadline; a missed instance is present again.',
        errors: [
          { code: 'not_principal', commonCorrection: `Beat the instance whose ${field} names you.` },
          { code: 'no_principal', commonCorrection: `Set ${field} to the principal's subject first.` },
        ],
      },
      miss: {
        useWhen: `Use to apply the miss of an instance past its deadline; the runner's sweep does so within ${duration(config.sweepMs)}.`,
        success: 'Returns missed, false for an instance not past its deadline, and the leases it released, by schema.',
      },
      update: {
        doNotUseWhen: `Do not change ${field} once it holds a principal.`,
        errors: [{ code: 'principal_fixed', commonCorrection: 'Create a new instance for another principal.' }],
      },
    },
  };
}
