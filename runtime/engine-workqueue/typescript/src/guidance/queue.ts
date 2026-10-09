/*
Queue's guidance: which states an instance is claimed from and into,
how claimNext orders and filters the work, and what keeps an instance
out of it, each from the config and the behaviors the type composes. It
adds claim_required to Lease's acquire, which a claim takes instead.
*/

import { linkPin, type BehaviorGuidance, type DescribeTarget } from '@superschematic/engine';

import type { QueueConfig } from '../queue.js';
import { list, sentences } from './text.js';

const TOKEN = 'preconditions {"Lease": {"token": n}}';

// pinned says what the links excludeStale names pin, read from the type's
// Links config: a revision, a release, or either.
function pinned(config: QueueConfig, target: DescribeTarget): { one: string; newest: string } {
  const links = (target.configs.Links as { links?: Readonly<Record<string, unknown>> } | undefined)?.links ?? {};
  const pins = new Set(config.excludeStale.map((name) => (Object.prototype.hasOwnProperty.call(links, name) ? linkPin(links[name]) : undefined)));
  if (pins.has('release')) {
    return pins.has('revision') ? { one: 'a revision or a release', newest: 'revision or release' } : { one: 'a release', newest: 'release' };
  }
  return { one: 'a revision', newest: 'revision' };
}

export function queueGuidance(config: QueueConfig, target: DescribeTarget): BehaviorGuidance {
  const from = list(config.claim.from, 'or');
  const pin = pinned(config, target);
  const out = [
    config.dependencies ? 'blocked' : undefined,
    config.retries ? 'exhausted (Retries)' : undefined,
    config.budget ? 'over its budget, here or in an enclosing scope (Budget)' : undefined,
    config.excludeStale.length > 0 ? `pinned through ${list(config.excludeStale, 'or')} to ${pin.one} its target has moved past` : undefined,
    config.maxExpiries === undefined ? undefined : `at ${config.maxExpiries} lease expiries`,
    config.assignment ? 'assigned to another principal' : undefined,
  ].filter((part): part is string => part !== undefined);
  const order = `${config.priorityField === undefined ? '' : `highest ${config.priorityField} first, then `}oldest first`;
  return {
    summary: sentences(
      `A claim takes an instance in ${from}: its lease, ${config.budget ? 'its budget reservation, ' : ''}and a move to ${config.claim.to}, in one write.`,
      `claimNext claims the first the caller can, ${order}${config.match.length > 0 ? `, matching ${list(config.match)}` : ''}, trying at most ${config.maxCandidates}.`,
      out.length > 0 ? `An instance is not claimed while it is ${list(out, 'or')}.` : undefined
    ),
    operations: {
      claim: {
        useWhen: `Use to claim this ${target.type} when you know its id: it must be in ${from}.`,
        doNotUseWhen: 'Do not use to find work; call claimNext.',
        success: `Returns id, token, expiresAt and heartbeatMs; the status is ${config.claim.to}. Present the token as ${TOKEN} on each write and heartbeat at least every heartbeatMs.`,
        errors: [
          { code: 'not_claimable', commonCorrection: `None now: only an instance in ${from} is claimed. Call claimNext for work that is.` },
          ...(config.dependencies ? [{ code: 'blocked', commonCorrection: 'Wait for its blockers to finish, or take other work with claimNext.' }] : []),
          ...(config.excludeStale.length > 0
            ? [{ code: 'stale_link', commonCorrection: `Link ${list(config.excludeStale, 'or')} to its target's newest ${pin.newest} first, or take other work.` }]
            : []),
        ],
      },
      claimNext: {
        useWhen: sentences(
          `Use as a worker to take the next ${target.type} to work on, ${order}.`,
          config.match.length > 0 ? `match filters on ${list(config.match)}, each a value or a list of values.` : undefined,
          config.assignment ? 'assignedOnly takes only work assigned to you.' : undefined
        ),
        doNotUseWhen: 'Do not use to count the work waiting; call countClaimable.',
        success: `Returns claimed: id, token, expiresAt and heartbeatMs, or null when nothing is claimable now. Present the token as ${TOKEN} on each write.`,
      },
      countClaimable: {
        useWhen: 'Use to count the work claimNext would try for you, a signal to scale workers on; it claims nothing.',
        success: 'Returns count.',
      },
      refresh: {
        useWhen: "Use only to recompute Queue's copies of the instance's facts; Queue invokes it itself when a blocker, a budget scope or a pinned link's target changes.",
        success: 'Returns {}.',
      },
      acquire: {
        doNotUseWhen: `Do not use: on ${target.type} the lease is taken by claim or claimNext.`,
        errors: [{ code: 'claim_required', commonCorrection: 'Call claim, or claimNext.' }],
      },
    },
  };
}
