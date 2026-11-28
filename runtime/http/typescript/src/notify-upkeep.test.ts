import { describe, expect, test } from 'bun:test';
import { createLogger } from './logger';
import { HEALTH_CHECK_EVERY_MS, notifierUpkeep } from './notify-upkeep';

describe('notifierUpkeep', () => {
  test('asks every adapter on its schedule, before the prune, and never lets a failed check stop the prune', async () => {
    let clock = 1_000_000;
    let checks = 0;
    let fail = false;
    const pruned: number[] = [];
    const lines: string[] = [];
    const upkeep = notifierUpkeep(
      {
        async checkHealth() {
          checks++;
          if (fail) throw new Error('the inbox did not answer');
          return [
            { slot: 'SMS', kind: 'phone', ok: false, detail: 'relay unreachable' },
            { slot: 'MAIL', kind: 'email', ok: true },
          ];
        },
        async prune(limit) {
          pruned.push(limit);
          return 2;
        },
      },
      { now: () => clock, log: createLogger({}, { write: line => lines.push(line) }) }
    );
    // The first call checks; the batches of the same prune do not.
    expect(await upkeep(1000)).toBe(2);
    expect(await upkeep(1000)).toBe(2);
    expect(checks).toBe(1);
    expect(lines.some(line => line.includes('channels down') && line.includes('SMS') && !line.includes('MAIL'))).toBe(true);
    clock += HEALTH_CHECK_EVERY_MS - 1;
    await upkeep(1000);
    expect(checks).toBe(1);
    clock += 1;
    fail = true;
    expect(await upkeep(1000)).toBe(2);
    expect(checks).toBe(2);
    expect(lines.some(line => line.includes('the channels health check failed'))).toBe(true);
    expect(pruned).toEqual([1000, 1000, 1000, 1000]);
  });
});
