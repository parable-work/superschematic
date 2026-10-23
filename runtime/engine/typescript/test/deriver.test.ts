// runDeriver (D74): the loop that pulls a schema's stale Derived fields,
// hands each to the project's answer and settles the answers; an answer
// that throws settles as a failure; a commit wakes it; its signal stops it.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { inProcess, runDeriver, type DerivationItem, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument } from './helpers.ts';

afterEach(cleanup);

const deriver: Principal = { subject: 'deriver', permissions: ['tickets.derive'] };

function ticketSchema(): Record<string, unknown> {
  const document = schemaDocument('Ticket', [
    { name: 'subject', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
    { name: 'category', typeRef: { name: 'string' } },
    { name: 'summary', typeRef: { name: 'string' } },
  ]) as { types: { Ticket: Record<string, unknown> } };
  document.types.Ticket.behaviors = [
    {
      name: 'Derived',
      config: {
        fields: {
          category: { from: ['subject', 'body'], instruction: 'Classify the request.', model: 'standard' },
          summary: { from: ['body'], instruction: 'Summarize it.', model: 'simple' },
        },
        permission: 'tickets.derive',
        maxAttempts: 1,
      },
    },
  ];
  return document;
}

// until polls a condition while the loop runs on the event loop.
async function until(check: () => boolean, ms = 3000): Promise<void> {
  const end = Date.now() + ms;
  while (!check()) {
    if (Date.now() > end) {
      throw new Error('timed out');
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

for (const driver of drivers) {
  function opened(): Engine {
    const engine = openTestEngine({ driver });
    engine.schemas.define(alice, ticketSchema());
    engine.schemas.publish(alice, 'Ticket');
    return engine;
  }

  describe(`runDeriver (${driver})`, () => {
    test('fills the stale fields, settles a throw as a failure, wakes on a commit and stops on its signal', async () => {
      const engine = opened();
      engine.instances.create(alice, 'Ticket', { subject: 'Card declined', body: 'Refused at checkout.' }, { id: 't1' });
      const seen: Array<[string, string]> = [];
      const controller = new AbortController();
      const running = runDeriver({
        source: inProcess(engine, deriver),
        schemas: ['Ticket'],
        idleMs: 60_000,
        signal: controller.signal,
        answer: (schema, item: DerivationItem) => {
          seen.push([schema, `${item.id}.${item.field}`]);
          if (item.id === 't2' && item.field === 'summary') {
            throw new Error('the model refused');
          }
          return item.field === 'category' ? { value: 'billing', confidence: 0.9 } : { value: `About: ${String(item.inputs.body)}` };
        },
      });
      await until(() => engine.instances.get(alice, 'Ticket', 't1')?.data.summary !== undefined);
      assert.equal(engine.instances.get(alice, 'Ticket', 't1')?.data.category, 'billing');

      // A create commits, which wakes the loop well before its idle wait.
      engine.instances.create(alice, 'Ticket', { subject: 'Login', body: 'Locked out.' }, { id: 't2' });
      await until(() => engine.instances.get(alice, 'Ticket', 't2')?.data.category !== undefined);
      await until(() => (engine.instances.invokeSchema(deriver, 'Ticket', 'failedDerivations', {}) as { items: unknown[] }).items.length === 1);
      assert.deepEqual((engine.instances.invokeSchema(deriver, 'Ticket', 'failedDerivations', {}) as { items: unknown[] }).items, [
        { id: 't2', field: 'summary', attempts: 1, failure: { class: 'thrown', message: 'the model refused' } },
      ]);
      controller.abort();
      await running;
      assert.deepEqual(seen.map(([, key]) => key).sort(), ['t1.category', 't1.summary', 't2.category', 't2.summary']);
    });

    test('waits out a schema not yet published, and fills its fields once it is', async () => {
      const engine = openTestEngine({ driver });
      const retried: string[] = [];
      const controller = new AbortController();
      const running = runDeriver({
        source: inProcess(engine, deriver),
        schemas: ['Ticket'],
        idleMs: 60_000,
        signal: controller.signal,
        onRetry: (error) => retried.push(String((error as { code?: string }).code)),
        answer: (_schema, item) => ({ value: item.field === 'category' ? 'billing' : 'A summary.' }),
      });
      await until(() => retried.length > 0);
      engine.schemas.define(alice, ticketSchema());
      engine.schemas.publish(alice, 'Ticket');
      engine.instances.create(alice, 'Ticket', { subject: 'Card declined', body: 'Refused.' }, { id: 't1' });
      await until(() => engine.instances.get(alice, 'Ticket', 't1')?.data.summary !== undefined, 5000);
      controller.abort();
      await running;
      assert.equal(retried[0], 'not_found');
    });
  });
}
