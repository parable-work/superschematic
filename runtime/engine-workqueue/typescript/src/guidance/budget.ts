/*
Budget's guidance: each meter's limit, reservation, scope and day, what
a reservation needs and when it is settled, what usage over a limit
does, and the refusals its guard adds to Links' link and unlink of a
scope link, to updates of a limit field, and to a Queue claim, which
reserves.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance } from '@superschematic/engine';

import type { BudgetConfig, BudgetMeter } from '../budget.js';
import { list, sentences } from './text.js';

const CORRECTIONS: Readonly<Record<string, string>> = {
  over_limit: 'Wait for reservations to settle or the day to pass, raise the limit with setLimit, or take other work; details.scope names where it did not fit.',
  not_leased: 'Acquire the lease, or claim the instance, then reserve.',
  scope_moved: 'Settle the reservation held through the old scope first.',
  scope_reserved: 'Settle the reservation held through the link first.',
  below_committed: 'Name a limit at or above what is used and reserved; details.committed says how much.',
  exceeds_reservation: 'Settle or record no more than the instance reserved through this scope.',
  not_configured: 'None: the config names no limitPermission, so limits do not change.',
};

function errors(...codes: string[]): GuidanceError[] {
  return codes.map((code) => ({ code, commonCorrection: CORRECTIONS[code] }));
}

// meter says one meter: its limit, its reservation, its scope and its day.
function meter(name: string, spec: BudgetMeter): string {
  const parts = [
    spec.limit !== undefined || spec.limitField !== undefined
      ? `limit ${[spec.limitField === undefined ? undefined : `the instance's ${spec.limitField}`, spec.limit === undefined ? undefined : String(spec.limit)].filter((part) => part !== undefined).join(', else ')}`
      : 'no limit',
    spec.reserve !== undefined || spec.reserveField !== undefined
      ? `reserving ${[spec.reserveField === undefined ? undefined : `the instance's ${spec.reserveField}`, spec.reserve === undefined ? undefined : String(spec.reserve)].filter((part) => part !== undefined).join(', else ')} at a claim`
      : undefined,
    spec.scope === undefined ? undefined : `drawn from the scope its ${spec.scope} link points at`,
    spec.daily ? 'counted per UTC day' : undefined,
  ].filter((part): part is string => part !== undefined);
  return `${name} (${parts.join('; ')})`;
}

export function budgetGuidance(config: BudgetConfig, target: DescribeTarget): BehaviorGuidance {
  const names = Object.keys(config.meters);
  const scopes = [...new Set(names.map((name) => config.meters[name].scope).filter((scope): scope is string => scope !== undefined))];
  const limitFields = names.map((name) => config.meters[name].limitField).filter((field): field is string => field !== undefined);
  const operations = new Set(target.operations.map((operation) => operation.name));
  const permission = config.limitPermission;
  const scopeSide: OperationGuidance = {
    doNotUseWhen: 'Do not call it yourself: an instance inside this scope calls it on its scope, as its reserve, settle and recordUsage run.',
  };
  const others: Record<string, OperationGuidance> = {};
  if (scopes.length > 0) {
    for (const name of ['link', 'unlink']) {
      if (operations.has(name)) {
        others[name] = { doNotUseWhen: `Do not move ${list(scopes, 'or')} while a reservation is held through it.`, errors: errors('scope_reserved') };
      }
    }
  }
  if (limitFields.length > 0) {
    others.update = {
      doNotUseWhen: `Do not change ${list(limitFields, 'or')}${permission === undefined ? '' : ` without permission ${permission}`} or below what is used and reserved.`,
      errors: errors('below_committed', ...(permission === undefined ? ['not_configured'] : [])),
    };
  }
  if (operations.has('claim')) {
    others.claim = { errors: errors('over_limit', 'scope_moved') };
  }
  return {
    summary: sentences(
      `Meters: ${names.map((name) => meter(name, config.meters[name])).join('; ')}.`,
      `A reservation fits while used plus reserved plus the amount is within the limit, here and in each enclosing scope${config.leased ? '; it is made under the active lease and settled when the lease ends' : '; it lasts until settle'}.`,
      config.onExceeded === undefined && config.escalate === undefined
        ? undefined
        : `Usage over a limit ${list(
            [
              config.onExceeded === undefined ? undefined : `sends directive ${config.onExceeded.direct} to the lease holder`,
              config.escalate === undefined ? undefined : `moves status from ${list(config.escalate.from, 'or')} to ${config.escalate.transition}`,
            ].filter((part): part is string => part !== undefined)
          )}.`
    ),
    operations: {
      ...others,
      reserve: {
        useWhen: sentences(
          `Use to reserve budget before work: with no meter, every meter's configured amount (${list(names)}).`,
          config.leased ? 'It needs the active lease; a claim reserves for you.' : undefined
        ),
        doNotUseWhen: 'Do not use to ask whether it would fit; call checkReserve.',
        success: 'Returns what it reserved, by meter.',
        errors: errors('over_limit', 'scope_moved', ...(config.leased ? ['not_leased'] : [])),
      },
      checkReserve: {
        useWhen: 'Use to ask whether reserve would fit now, here and in every scope, without reserving.',
        success: 'Returns fits, until (the next UTC day when that alone makes it fit, else null) and the scopes it read.',
      },
      recordUsage: {
        useWhen: 'Use to record what the work used, by meter; it is never refused for its amount, and releases the reservation it covers.',
        success: 'Returns used, what it released, and the overruns: each instance up the chain now over its limit.',
      },
      settle: {
        useWhen: `Use to release what the instance's own reservations hold${config.leased ? '; the end of a lease settles them too' : ''}.`,
        success: 'Returns what it released, by meter.',
      },
      setLimit: {
        useWhen:
          permission === undefined
            ? undefined
            : `Use to raise or lower a meter's limit; it needs permission ${permission}, and never goes below what is used and reserved.`,
        doNotUseWhen: permission === undefined ? 'Do not use: the config names no limitPermission, so limits do not change.' : undefined,
        success: 'Returns the meter, its limit and the one before.',
        errors: errors('below_committed', ...(permission === undefined ? ['not_configured'] : [])),
      },
      reserveFor: { ...scopeSide, errors: errors('over_limit') },
      settleFor: { ...scopeSide, errors: errors('exceeds_reservation') },
      recordUsageFor: { ...scopeSide, errors: errors('exceeds_reservation') },
    },
  };
}
