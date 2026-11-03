// The implementation of smoke-api, which scripts/smoke.sh checks through
// the site's origin on every target (D97): line items written and read
// through the repositories, smoke-db's declared query, nearest by cosine
// over the hnsw index, and an enqueue in a transaction that the worker
// DrainLineItems drains.

import { notFound } from '@superschematic/http-runtime';
import { enqueue } from '@superschematic/http-runtime/postgres';
import type { Constructor, Deps, JobsConstructor, LineItemsImplementation, WorkersConstructor } from '@smoke/smoke-api-api';
import type { LineItemRow } from '@smoke/smoke-db-types/orm';
import { drainLineItemQueue } from '@smoke/smoke-db-types/queues';
import { embed } from './embed.ts';

export const create: Constructor = deps => ({
  lineItems: lineItemsImplementation(deps),
});

// The job stands in for a finances API's sync of every linked account; it
// is here for its name, which gcp shortens (D97), and logs a line.
export const createJobs: JobsConstructor = deps => ({
  async syncAllLinkedAccounts() {
    deps.logger.info('nothing to sync');
  },
});

// The worker drains a line item as the system actor (D87): one claim per
// line item at a time, since the queue serializes on it.
export const createWorkers: WorkersConstructor = deps => ({
  async drainLineItems(message) {
    const orm = deps.orm.asSystem();
    const item = await orm.lineItem.findOne({ id: { eq: message.lineItemId } });
    if (!item) return;
    await orm.lineItem.updateOne(item.id, { drainedAt: new Date() });
    deps.logger.info('drained', { lineItem: item.id });
  },
});

function view(row: LineItemRow) {
  return { id: row.id, name: row.name, amount: row.amount, drainedAt: row.drainedAt };
}

function lineItemsImplementation(deps: Deps): LineItemsImplementation {
  return {
    async createLineItem({ input }) {
      return view(await deps.orm.lineItem.createOne({ name: input.name, amount: input.amount, embedding: embed(input.name) }));
    },
    async getLineItem({ id }) {
      const row = await deps.orm.lineItem.findOne({ id: { eq: id } });
      if (!row) throw notFound(`no line item ${id}`);
      return view(row);
    },
    async totals({ prefix }) {
      const [row] = await deps.orm.queries.lineItemTotals({ prefix });
      return row ?? { lines: 0, spent: 0, earned: 0 };
    },
    async nearest({ name, k }) {
      const near = await deps.orm.lineItem.nearest('embedding', embed(name), { k: Math.min(Math.max(k, 1), 20), metric: 'cosine' });
      return near.map(n => ({ id: n.row.id, name: n.row.name, distance: n.distance }));
    },
    async requestDrain({ id }) {
      const row = await deps.orm.lineItem.findOne({ id: { eq: id } });
      if (!row) throw notFound(`no line item ${id}`);
      await deps.orm.transaction(tx => enqueue(tx, drainLineItemQueue, { lineItemId: row.id }));
      return { id: row.id, enqueued: true };
    },
  };
}
