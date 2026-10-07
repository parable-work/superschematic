/*
Lease's guidance: how long a lease lasts, how often its holder renews
it, how long it may be held, what an expiry does to the status, the
token every write under it presents, the directives the runner sends
when a link's target moves on (directOn), and the filter a list takes on
the holder. Lease's guard refuses the writes of
other behaviors and the instance's updates and deletes while another
principal holds the lease, so it adds its vetoes to each of those
operations' guidance too, but for the ones its config exempts and
Queue's refresh, which it lets through.
*/

import type { BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance } from '@superschematic/engine';

import type { LeaseConfig, LeaseTransition } from '../lease.js';
import { duration, list, sentences } from './text.js';

const TOKEN = 'preconditions {"Lease": {"token": n}}';

const CORRECTIONS: Readonly<Record<string, string>> = {
  held_by_another: 'Wait until expiresAt in details, or work on another instance.',
  held_by_caller: 'Keep the lease you hold: renew it with heartbeat.',
  not_leased: 'Acquire the lease (or claim the instance) first.',
  not_holder: 'Only the holder may do this; acquire the lease once it is free.',
  lapsed: 'Stop working on the instance: the lease has lapsed. Acquire it again once it has expired.',
  token_stale: 'Stop: a newer lease replaced yours. Read the lease field, and acquire again only if it is free.',
  token_required: `Present the lease's token as ${TOKEN}.`,
  max_expiries: 'A caller with the override permission resets the count with resetExpiries.',
  hold_limit_fixed: 'Change the field once the lease has ended, or with the override permission.',
  not_configured: "None: the type's Lease config names no permission that allows it.",
};

function errors(...codes: string[]): GuidanceError[] {
  return codes.map((code) => ({ code, commonCorrection: CORRECTIONS[code] }));
}

// moves says a status move a Lease config makes.
function moves(rule: LeaseTransition): string {
  return `moves status from ${list(rule.from, 'or')} to ${rule.transition}`;
}

export function leaseGuidance(config: LeaseConfig, target: DescribeTarget): BehaviorGuidance {
  const override = config.overridePermission;
  const hold =
    config.maxHoldField !== undefined
      ? `at most the instance's ${config.maxHoldField} milliseconds${config.maxHoldMs === undefined ? '' : `, else ${duration(config.maxHoldMs)}`}`
      : config.maxHoldMs !== undefined
        ? `at most ${duration(config.maxHoldMs)}`
        : undefined;
  const fenced: OperationGuidance = {
    doNotUseWhen: config.requireToken
      ? `Do not write under another principal's active lease; the holder presents its token as ${TOKEN}.`
      : `Do not write under another principal's active lease; the holder may present its token as ${TOKEN}, which a newer lease makes stale.`,
    errors: errors('held_by_another', 'token_stale', 'lapsed', ...(config.requireToken ? ['token_required'] : [])),
  };
  // The writes Lease's guard holds while a lease is active: updates,
  // deletes and other behaviors' writing instance operations, but the
  // exempt ones and Queue's refresh.
  const others: Record<string, OperationGuidance> = {};
  for (const operation of target.operations) {
    if (operation.behavior === 'Lease' || !operation.writes || operation.scope === 'schema' || operation.name === 'create') {
      continue;
    }
    if (operation.behavior !== undefined && (config.exempt.includes(`${operation.behavior}.${operation.name}`) || (operation.behavior === 'Queue' && operation.name === 'refresh'))) {
      continue;
    }
    others[operation.name] =
      operation.name === 'update' && config.maxHoldField !== undefined
        ? {
            doNotUseWhen: sentences(fenced.doNotUseWhen, `${config.maxHoldField} does not change while a lease is held.`),
            errors: [...(fenced.errors ?? []), ...errors('hold_limit_fixed')],
          }
        : fenced;
  }
  const moved = config.directOn.map(
    (rule) => `directive ${rule.name} when the ${rule.link} link's target gains ${rule.pinned ? 'a revision past the pinned one' : 'a revision'} or a release`
  );
  return {
    summary: sentences(
      `A lease gives one principal the ${target.type} for ${duration(config.ttlMs)} at a time, renewed by heartbeat at least every ${duration(config.heartbeatMs)}${hold === undefined ? '' : `, and held ${hold}`}.`,
      `The runner expires a lapsed lease within ${duration(config.sweepMs)}${config.onExpiry === undefined ? '' : `; an expiry ${moves(config.onExpiry)}`}.`,
      config.maxExpiries === undefined
        ? undefined
        : `After ${config.maxExpiries} expiries it is not leased again${config.escalate === undefined ? '' : `, and that expiry ${moves(config.escalate)} instead`}.`,
      `While a lease is active only its holder writes${config.exempt.length > 0 ? `, but for ${list(config.exempt)}` : ''}; each acquire gives a new token, which the holder presents as ${TOKEN}${config.requireToken ? ' on every write' : ''}.`,
      moved.length === 0
        ? undefined
        : `The runner sends the holder of an active lease ${list(moved)}; an instance with no lease held hears nothing, and its next holder nothing either.`
    ),
    operations: {
      ...others,
      list: {
        useWhen: `where: { "lease.holder": <subject> } lists the ${target.type} instances whose lease a principal holds, a lapsed one until the runner expires it, and "lease.holder": null the free ones.`,
      },
      acquire: {
        useWhen: sentences(
          `Use to take the lease, for ${duration(config.ttlMs)} or a shorter ttlMs, before working on the instance alone.`,
          config.acquirePermission === undefined ? undefined : `It needs permission ${config.acquirePermission}, else the call is forbidden.`
        ),
        success: `Returns token, expiresAt and heartbeatMs: heartbeat at least every heartbeatMs and present the token as ${TOKEN} on each write.`,
        errors: errors('held_by_another', 'held_by_caller', 'token_stale', ...(config.maxExpiries === undefined ? [] : ['max_expiries'])),
      },
      heartbeat: {
        useWhen: `Use as the holder at least every ${duration(config.heartbeatMs)}, presenting the token, to keep the lease; acknowledge takes the ids of directives handled.`,
        success: sentences(
          'Returns the new expiresAt and the directives not yet acknowledged, oldest first.',
          moved.length === 0
            ? undefined
            : `A directive the runner sends when a link's target moves on (${list(config.directOn.map((rule) => rule.name), 'or')}) carries data.revised: the link, the target, and its revision or the release's commit.`
        ),
        errors: errors('not_leased', 'not_holder', 'token_required', 'token_stale', 'lapsed'),
      },
      release: {
        useWhen: sentences(
          'Use as the holder, presenting the token, when done with the instance or handing it back.',
          `abandon true gives it up as failed, which counts as an expiry${config.onExpiry === undefined ? '' : ` and ${moves(config.onExpiry)}`}.`
        ),
        success: 'Returns {}; the lease is free and its token stale.',
        errors: errors('not_leased', 'not_holder', 'token_required', 'token_stale', 'lapsed'),
      },
      expire: {
        useWhen: sentences(
          `Use to apply the expiry of a lapsed lease; the runner's sweep does so within ${duration(config.sweepMs)}.`,
          override === undefined ? undefined : `With holder, it expires that principal's lease at once, and needs permission ${override}.`
        ),
        success: 'Returns expired and its reason: ttl, maxHold or holder.',
      },
      direct: {
        useWhen:
          config.directPermission === undefined && override === undefined
            ? undefined
            : `Use to send a message to the holder of the active lease, which its next heartbeat returns; it needs permission ${config.directPermission ?? override}.`,
        doNotUseWhen:
          config.directPermission === undefined && override === undefined ? 'Do not use: the config names no permission that sends directives.' : undefined,
        success: 'Returns the directive id, and created false when one with its dedupeKey stands.',
        errors: errors('not_leased', 'lapsed', ...(config.directPermission === undefined && override === undefined ? ['not_configured'] : [])),
      },
      acknowledge: {
        useWhen: "Use as the holder, presenting the token, to stop directives' delivery; heartbeat's acknowledge does the same in the heartbeat's write.",
        success: 'Returns {}.',
        errors: errors('not_leased', 'not_holder', 'token_required', 'token_stale', 'lapsed'),
      },
      resetExpiries:
        override === undefined
          ? { doNotUseWhen: 'Do not use: the config names no overridePermission, which it needs.', errors: errors('not_configured') }
          : {
              useWhen: `Use to let an instance that reached its expiries be leased again; it needs permission ${override}.`,
              success: 'Returns the expiries it cleared.',
            },
      expireHolder:
        override === undefined
          ? { doNotUseWhen: 'Do not use: the config names no overridePermission, which it needs.' }
          : {
              useWhen: `Use when a worker is gone: it expires every lease its principal holds on the schema; it needs permission ${override}. notRenewedAfter spares a lease renewed since then.`,
              success: 'Returns how many it expired, by reason, and their ids.',
            },
    },
  };
}
