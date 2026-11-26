// The implementation of smoke-api, which scripts/smoke.sh checks through
// the site's origin on every target (D97): line items written and read
// through the repositories, smoke-db's declared query, nearest by cosine
// over the hnsw index, an enqueue in a transaction that the worker
// DrainLineItems drains, at once or five seconds on, and the rows three
// behavior workers move (D151): a follow-up its Timers deadline makes due,
// a check-in whose Schedules run the worker hands to checkInWakeUpsSchedulesRun,
// and an alert its Reactions raises when its level changes; and a line
// item's files (D154), uploaded straight to the bucket through a signed
// URL, checked on completion, filed onto another line item with no second
// upload, which then lists the first among its backlinks.

import { notFound } from '@superschematic/http-runtime';
import { enqueue, type Attachment, type CoreCaller } from '@superschematic/http-runtime/postgres';
import type { BehaviorsImplementation, Constructor, Deps, JobsConstructor, LineItemsImplementation, WorkersConstructor } from '@smoke/smoke-api-api';
import type { AlertRow, CheckInRow, FollowUpRow, LineItemRow } from '@smoke/smoke-db-types/orm';
import { drainLineItemQueue } from '@smoke/smoke-db-types/queues';
import { embed } from './embed.ts';

export const create: Constructor = deps => ({
  lineItems: lineItemsImplementation(deps),
  behaviors: behaviorsImplementation(deps),
});

/** How long a follow-up's deadline and a check-in's wake-up wait. */
const WAKE_AFTER_MS = 5_000;

/** The caller a check-in's schedule is set as: the route has checked smoke.write. */
const scheduler: CoreCaller = { subject: 'smoke-api/Behaviors', permissions: ['smoke.write'] };

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
  // A check-in's wake-up ran: record when, in the transaction the event's
  // completion commits in, and report the run told.
  // A file's object, released: deleted once no file names it.
  async releaseFiles(message) {
    const { deleted } = await deps.orm.asSystem().attachments.release(message, { bucket: deps.smokeFiles });
    deps.logger.info('released', { object: message.objectKey, deleted });
  },
  async checkInWakeUpsSchedulesRun(event, tx) {
    await tx.checkIn.updateOne(event.key, { ranAt: new Date(Number(event.payload.firedAt)) });
    deps.logger.info('checked in', { checkIn: event.key, run: event.payload.name });
  },
});

function behaviorsImplementation(deps: Deps): BehaviorsImplementation {
  const followUp = (row: FollowUpRow) => ({ id: row.id, title: row.title, status: row.status });
  const checkIn = (row: CheckInRow) => ({ id: row.id, title: row.title, ranAt: row.ranAt });
  const alert = (row: AlertRow) => ({ id: row.id, level: row.level, status: row.status });
  const found = <T>(row: T | null | undefined, what: string): T => {
    if (!row) throw notFound(`no ${what}`);
    return row;
  };
  return {
    async createFollowUp({ input }) {
      return followUp(await deps.orm.followUp.createOne({ title: input.title }));
    },
    async getFollowUp({ id }) {
      return followUp(found(await deps.orm.followUp.findOne({ id: { eq: id } }), `follow-up ${id}`));
    },
    async createCheckIn({ input }) {
      const row = await deps.orm.checkIn.createOne({ title: input.title });
      await deps.orm.checkIn.schedules.setSchedule(row.id, { name: 'soon', after: WAKE_AFTER_MS }, { caller: scheduler });
      return checkIn(row);
    },
    async getCheckIn({ id }) {
      return checkIn(found(await deps.orm.checkIn.findOne({ id: { eq: id } }), `check-in ${id}`));
    },
    async createAlert() {
      return alert(await deps.orm.alert.createOne({ level: 'low' }));
    },
    async raiseAlert({ id }) {
      found(await deps.orm.alert.findOne({ id: { eq: id } }), `alert ${id}`);
      await deps.orm.alert.updateOne(id, { level: 'high' });
      return alert(found(await deps.orm.alert.findOne({ id: { eq: id } }), `alert ${id}`));
    },
    async getAlert({ id }) {
      return alert(found(await deps.orm.alert.findOne({ id: { eq: id } }), `alert ${id}`));
    },
  };
}

function view(row: LineItemRow) {
  return { id: row.id, name: row.name, amount: row.amount, drainedAt: row.drainedAt };
}

function fileView(file: Attachment) {
  return { id: file.id, lineItemId: file.key, name: file.name, size: file.size, contentHash: file.contentHash, objectKey: file.objectKey, state: file.state };
}

function lineItemsImplementation(deps: Deps): LineItemsImplementation {
  const bucket = deps.smokeFiles;
  return {
    async beginFile({ id, input }) {
      const { attachment, upload } = await deps.orm.attachments.begin(
        { to: { table: 'LineItem', key: id }, name: input.name, mediaType: input.mediaType, size: input.size, sha256: input.sha256 },
        { bucket, expiresInSeconds: 600 }
      );
      return { fileId: attachment.id, url: upload.url, contentType: input.mediaType };
    },
    async completeFile({ fileId }) {
      return fileView(await deps.orm.attachments.complete({ table: 'LineItem', id: fileId }, { bucket }));
    },
    async fileFile({ fileId, input }) {
      return fileView(await deps.orm.attachments.file({ table: 'LineItem', id: fileId, to: { table: 'LineItem', key: input.lineItemId } }));
    },
    async backlinks({ id }) {
      const { items } = await deps.orm.references.referencedBy({ table: 'LineItem', key: id });
      return items.map(item => ({ lineItemId: item.key, role: item.role ?? '' }));
    },
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
    async requestDelayedDrain({ id }) {
      const row = await deps.orm.lineItem.findOne({ id: { eq: id } });
      if (!row) throw notFound(`no line item ${id}`);
      await deps.orm.transaction(tx => enqueue(tx, drainLineItemQueue, { lineItemId: row.id }, { delaySeconds: WAKE_AFTER_MS / 1000 }));
      return { id: row.id, enqueued: true };
    },
  };
}
