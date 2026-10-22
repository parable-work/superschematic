// Holds beside the core's Timers (D70, D56): a hold expires through a
// deadline the schema writes, which the runner fires, and whose move into
// a releaseOn state lets go of what the checkout held; a second claim then
// takes it. Real SQLite, the engine's Rust behaviors and its runner, a
// clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { HoldsRecord, StockRecord } from '../dist/index.js';
import { Clock, alice, checkout, checkoutHolds, cleanup, inventory, openTestEngine, principal, publish } from './helpers.ts';

afterEach(cleanup);

const MINUTE = 60_000;
const stocker = principal('sam', ['stock.adjust']);
const runner = principal('runner');

describe('Holds with Timers', () => {
  test('a hold expires through its deadline, which the runner fires, and what it held is free again', () => {
    const clock = new Clock();
    const engine = openTestEngine({ clock: clock.now, runner: { principal: runner } });
    publish(engine, inventory({ permission: 'stock.adjust' }));
    publish(
      engine,
      checkout(checkoutHolds, [
        { name: 'Timers', config: { deadlines: { holdExpiry: { start: { enters: 'held' }, afterMs: 15 * MINUTE, then: { transition: 'expired' } } } } },
      ])
    );
    engine.runner.runDue();
    engine.instances.create(alice, 'Inventory', { title: 'Anvil' }, { id: 'anvil' });
    engine.instances.invoke(stocker, 'Inventory', 'anvil', 'receiveStock', { quantity: 3, reason: 'delivery' });
    const stockOf = () => engine.instances.get(alice, 'Inventory', 'anvil')?.behaviors.Stock as unknown as StockRecord;
    const read = (id: string) => engine.instances.get(alice, 'Checkout', id);
    engine.instances.create(alice, 'Checkout', { title: 'c1', lines: [{ inventoryId: 'anvil', quantity: 3 }] }, { id: 'c1' });
    engine.instances.invoke(alice, 'Checkout', 'c1', 'transition', { to: 'held' });
    assert.equal(stockOf().available, 0);

    clock.advance(15 * MINUTE - 1);
    engine.runner.runDue();
    assert.equal(read('c1')?.behaviors.Workflow?.status, 'held');
    clock.advance(5_000);
    engine.runner.runDue();
    assert.equal(read('c1')?.behaviors.Workflow?.status, 'expired');
    assert.deepEqual(
      (read('c1')?.behaviors.Holds as unknown as HoldsRecord).items.map((item) => item.state),
      ['released']
    );
    assert.equal(stockOf().available, 3);

    engine.instances.create(alice, 'Checkout', { title: 'c2', lines: [{ inventoryId: 'anvil', quantity: 3 }] }, { id: 'c2' });
    engine.instances.invoke(alice, 'Checkout', 'c2', 'transition', { to: 'held' });
    assert.equal(stockOf().held, 3);
  });
});
