import { createLogger, type Logger } from './logger.js';
import type { Notifier } from './notify.js';

/*
The notifier's upkeep on a schedule (D153, D159, #702): what the dispatch
queue's worker runs beside its queue's prune, as it starts and every five
minutes in a process, and at each firing of its Cron Trigger on Workers.
It asks every channel adapter's health on its own, not only before a send,
so a channel that is down raises channel.down, which the routes send on the
others, while nothing is being sent; and it deletes the deliveries past
their retention.
*/

/** How often the upkeep asks every channel adapter's health: every five minutes unless set. */
export const HEALTH_CHECK_EVERY_MS = 300_000;

/** What notifierUpkeep reads. */
export interface NotifierUpkeepOptions {
  /** How often to ask every adapter's health: HEALTH_CHECK_EVERY_MS unless set. */
  readonly healthEveryMs?: number;
  readonly log?: Logger;
  /** The clock, for a test; Date.now unless set. */
  readonly now?: () => number;
}

/**
 * The prune a worker of the notifier's dispatch queue runs beside its
 * queue's: each call deletes up to limit deliveries past their retention,
 * and the first call, and the first after healthEveryMs, asks every
 * adapter's health first (notifier.checkHealth), which raises channel.down
 * for each that fails. A health check that throws is logged and never
 * stops the prune.
 */
export function notifierUpkeep(notifier: Pick<Notifier, 'checkHealth' | 'prune'>, options: NotifierUpkeepOptions = {}): (limit: number) => Promise<number> {
  const every = options.healthEveryMs !== undefined && options.healthEveryMs > 0 ? options.healthEveryMs : HEALTH_CHECK_EVERY_MS;
  const now = options.now ?? Date.now;
  const log = options.log ?? createLogger({ component: 'notifier' });
  let checkedAt: number | undefined;
  return async limit => {
    const at = now();
    if (checkedAt === undefined || at - checkedAt >= every) {
      checkedAt = at;
      try {
        const down = (await notifier.checkHealth()).filter(channel => !channel.ok);
        if (down.length > 0) log.warn('channels down', { slots: down.map(channel => channel.slot) });
      } catch (error) {
        log.warn('the channels health check failed', { error });
      }
    }
    return notifier.prune(limit);
  };
}
